package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
)

type commandError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type commandResult struct {
	Status     string           `json:"status"`
	Files      []string         `json:"files,omitempty"`
	Pages      int              `json:"pages,omitempty"`
	Originals  []string         `json:"originals,omitempty"`
	Retained   []string         `json:"retained,omitempty"`
	Warnings   []string         `json:"warnings,omitempty"`
	Devices    *[]scannerDevice `json:"devices,omitempty"`
	Info       *info            `json:"info,omitempty"`
	Processing []pageProcessing `json:"processing,omitempty"`
	Error      *commandError    `json:"error,omitempty"`
	Help       string           `json:"help,omitempty"`
}

type pageProcessing struct {
	Page int `json:"page"`
	processingInfo
}

func (r *commandResult) fail(code string, err error) {
	r.Status = "error"
	if len(r.Files) > 0 || len(r.Originals) > 0 || len(r.Retained) > 0 {
		r.Status = "partial"
	}
	r.Error = &commandError{Code: code, Message: err.Error()}
}

// Events describe completed work, not text printed by a particular interface.
type scanEvent struct {
	kind, path, message string
	page                int
	processing          processingInfo
}

type commandHooks struct {
	agent  bool
	choose func(context.Context, []scannerDevice) (string, error)
	wait   func(context.Context) (bool, error)
	event  func(scanEvent)
}

func executeCommand(parent context.Context, r commandRequest, find discoveryFunc, hooks commandHooks, stdout, stderr io.Writer) (result commandResult, code int) {
	result.Status = "ok"
	fail := func(kind string, err error) (commandResult, int) {
		if parent.Err() != nil {
			kind = "cancelled"
		} else if errors.Is(err, context.DeadlineExceeded) {
			kind = "timeout"
		}
		result.fail(kind, err)
		return result, exitCode(parent)
	}
	if r.command == "discover" || r.endpoint == "" {
		if hooks.agent && r.command != "discover" {
			return fail("device_required", errors.New("未指定扫描仪，也没有默认设备；请先执行 goscanner --agent discover，再使用 --host 或 --endpoint"))
		}
		ctx, cancel := context.WithTimeout(parent, r.timeout)
		devices, warnings, err := find(ctx)
		cancel()
		result.Warnings = append(result.Warnings, warnings...)
		for _, warning := range warnings {
			fmt.Fprintf(stderr, "发现警告：%q\n", warning)
		}
		if err != nil {
			return fail("discovery_failed", fmt.Errorf("发现失败：%w", err))
		}
		if !hooks.agent {
			if err := parent.Err(); err != nil {
				return fail("cancelled", err)
			}
			updates := make([]savedDevice, 0, len(devices))
			for _, device := range devices {
				updates = append(updates, savedDevice{scannerDevice: device, Discovered: true})
			}
			if err := rememberDrives(updates); err != nil {
				return fail("device_store_failed", fmt.Errorf("保存设备信息表失败：%w", err))
			}
		}
		if r.command == "discover" {
			if devices == nil {
				devices = []scannerDevice{}
			}
			result.Devices = &devices
			encoder := json.NewEncoder(stdout)
			encoder.SetIndent("", "  ")
			if err := encoder.Encode(devices); err != nil {
				return fail("output_failed", err)
			}
			return result, 0
		}
		if hooks.choose == nil {
			return fail("device_required", errors.New("需要明确选择扫描仪"))
		}
		endpoint, err := hooks.choose(parent, devices)
		if err != nil {
			return fail("device_selection_failed", err)
		}
		selected, err := deviceFromEndpoint(endpoint)
		if err == nil {
			err = saveDefaultDevice(selected)
		}
		if err != nil {
			return fail("config_save_failed", fmt.Errorf("登记默认设备失败；未启动扫描：%w", err))
		}
		r.endpoint = endpoint
		fmt.Fprintln(stderr, "已登记默认设备：", endpoint)
	}
	c, err := newEndpointClient(r.endpoint)
	if err != nil {
		result.fail("invalid_arguments", err)
		return result, 2
	}
	if r.command == "info" {
		ctx, cancel := context.WithTimeout(parent, r.timeout)
		defer cancel()
		cap, err := c.info(ctx)
		if err != nil {
			return fail("info_failed", fmt.Errorf("读取失败：%w", err))
		}
		result.Info = &cap
		if !hooks.agent {
			if err := parent.Err(); err != nil {
				return fail("cancelled", err)
			}
			update := savedDevice{scannerDevice: scannerDevice{Endpoint: c.endpoint, Protocol: "wsd"}, Verified: true, Info: &cap}
			if err := rememberDrives([]savedDevice{update}); err != nil {
				return fail("device_store_failed", fmt.Errorf("保存设备信息表失败：%w", err))
			}
		}
		encoder := json.NewEncoder(stdout)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(cap); err != nil {
			return fail("output_failed", err)
		}
		return result, 0
	}
	onEvent := func(event scanEvent) {
		path := event.path
		if path != "" {
			if absolute, err := filepath.Abs(path); err == nil {
				path = absolute
			}
		}
		switch event.kind {
		case "saved":
			result.Files = append(result.Files, path)
			result.Pages += event.page
		case "original":
			result.Originals = append(result.Originals, path)
		case "retained":
			result.Retained = append(result.Retained, path)
		case "warning":
			result.Warnings = append(result.Warnings, event.message)
		case "processed":
			result.Processing = append(result.Processing, pageProcessing{Page: event.page, processingInfo: event.processing})
		}
		if hooks.event != nil {
			hooks.event(event)
		}
	}
	if err := scanPagesWithEvents(parent, c, r.opts, r.output, r.singlePage, r.timeout, hooks.wait, stdout, stderr, onEvent); err != nil {
		return fail("scan_failed", err)
	}
	return result, 0
}
