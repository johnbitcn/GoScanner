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
	cause   error
}

func newCommandError(code string, err error) *commandError {
	return &commandError{Code: code, Message: err.Error(), cause: err}
}

func (e commandError) MarshalJSON() ([]byte, error) {
	message := e.Message
	if e.cause != nil {
		message = agentError(e.cause)
	}
	return json.Marshal(struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}{e.Code, message})
}

type commandResult struct {
	device     *scannerDevice
	Status     string           `json:"status"`
	Files      []string         `json:"files,omitempty"`
	Pages      int              `json:"pages,omitempty"`
	Originals  []string         `json:"originals,omitempty"`
	Retained   []string         `json:"retained,omitempty"`
	Warnings   []diagnostic     `json:"warnings,omitempty"`
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
	r.Error = newCommandError(code, err)
}

// Events describe completed work, not text printed by a particular interface.
type scanEvent struct {
	kind, path string
	warning    diagnostic
	page       int
	processing processingInfo
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
		if errors.Is(err, errRecordNotFound) {
			kind = "device_record_not_found"
		}
		if errors.Is(err, errIdentityConflict) {
			kind = "device_identity_conflict"
		}
		result.fail(kind, err)
		return result, exitCode(parent)
	}
	if r.command != "discover" && (r.endpoint != "" || r.host != "" || r.recordID != "") {
		ctx, cancel := context.WithTimeout(parent, r.timeout)
		resolved, discovered, warnings, err := resolveRequestDevice(ctx, r, find)
		cancel()
		result.Warnings = append(result.Warnings, warnings...)
		for _, warning := range warnings {
			fmt.Fprintf(stderr, r.language.text("发现警告：%q\n"), r.language.error(warning))
		}
		if err != nil {
			return fail("device_resolution_failed", err)
		}
		r = resolved
		if !hooks.agent && len(discovered) > 0 {
			if err := parent.Err(); err != nil {
				return fail("cancelled", err)
			}
			var updates []savedDevice
			for _, d := range discovered {
				updates = append(updates, savedDevice{scannerDevice: d, Discovered: true})
			}
			if err := rememberDrives(updates); err != nil {
				return fail("device_store_failed", err)
			}
		}
	}
	if r.command == "discover" || r.endpoint == "" {
		if hooks.agent && r.command != "discover" {
			return fail("device_required", diagnosticf("No scanner was specified and no default scanner is configured. Run goscanner --agent discover, then specify --host or --endpoint.", "未指定扫描仪，也没有默认设备；请先执行 goscanner --agent discover，再使用 --host 或 --endpoint"))
		}
		ctx, cancel := context.WithTimeout(parent, r.timeout)
		devices, warnings, err := find(ctx)
		cancel()
		result.Warnings = append(result.Warnings, warnings...)
		for _, warning := range warnings {
			fmt.Fprintf(stderr, r.language.text("发现警告：%q\n"), r.language.error(warning))
		}
		if err != nil {
			return fail("discovery_failed", diagnosticf("Scanner discovery failed: %w", "发现失败：%w", err))
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
				return fail("device_store_failed", diagnosticf("Cannot save the scanner registry: %w", "保存设备信息表失败：%w", err))
			}
		}
		saved, err := loadDrives()
		if err != nil {
			return fail("device_store_failed", err)
		}
		devices = applySavedPreferences(devices, saved)
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
			return fail("device_required", diagnosticf("A scanner must be explicitly selected.", "需要明确选择扫描仪"))
		}
		endpoint, err := hooks.choose(parent, devices)
		if err != nil {
			return fail("device_selection_failed", err)
		}
		matched := false
		for _, device := range devices {
			if device.Endpoint != endpoint {
				continue
			}
			protocol := ""
			if r.protocolExplicit {
				protocol = r.protocol
			}
			selected, selectErr := device.endpointFor(protocol)
			if selectErr != nil {
				return fail("device_selection_failed", selectErr)
			}
			copy := device
			r.device = &copy
			r.recordID = device.RecordID
			endpoint, r.protocol = selected.Endpoint, selected.Protocol
			matched = true
			break
		}
		if !matched {
			return fail("device_selection_failed", diagnosticf("The selected scanner is not in the current discovery results.", "选择结果不属于本次发现的设备"))
		}
		r.endpoint = endpoint
		r.registerDefault = true
	}
	c, err := newProtocolClient(r.endpoint, r.protocol)
	if err != nil {
		result.fail("invalid_arguments", err)
		return result, 2
	}
	c.expectedDevice = r.device
	result.device = &scannerDevice{RecordID: r.recordID, Endpoint: c.endpoint, Protocol: protocolName(c.protocol)}
	if r.registerDefault {
		c.beforeScan = func(ctx context.Context, cap info) error {
			d, err := rememberVerifiedDevice(ctx, c, cap)
			if err != nil {
				return err
			}
			result.device.RecordID = d.RecordID
			if err := saveDefaultRecord(ctx, d.RecordID); err != nil {
				return diagnosticf("Cannot register the default scanner. No scan was started: %w", "登记默认设备失败；未启动扫描：%w", err)
			}
			c.beforeScan = nil
			fmt.Fprintln(stderr, r.language.text("已登记默认设备记录："), d.RecordID)
			return nil
		}
	}
	if r.command == "info" {
		ctx, cancel := context.WithTimeout(parent, r.timeout)
		defer cancel()
		cap, err := c.info(ctx)
		if err != nil {
			return fail("info_failed", diagnosticf("Cannot read scanner information: %w", "读取失败：%w", err))
		}
		result.Info = &cap
		if !hooks.agent {
			if err := parent.Err(); err != nil {
				return fail("cancelled", err)
			}
			d, err := rememberVerifiedDevice(ctx, c, cap)
			if err != nil {
				return fail("device_store_failed", diagnosticf("Cannot save the scanner registry: %w", "保存设备信息表失败：%w", err))
			}
			result.device.RecordID = d.RecordID
			if r.registerDefault {
				if err := saveDefaultRecord(ctx, d.RecordID); err != nil {
					return fail("config_save_failed", diagnosticf("Cannot register the default scanner: %w", "登记默认设备失败：%w", err))
				}
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
			result.Warnings = append(result.Warnings, event.warning)
		case "processed":
			result.Processing = append(result.Processing, pageProcessing{Page: event.page, processingInfo: event.processing})
		}
		if hooks.event != nil {
			hooks.event(event)
		}
	}
	if err := scanPagesWithEvents(parent, c, r.opts, r.output, r.singlePage, r.timeout, hooks.wait, stdout, stderr, onEvent, r.language); err != nil {
		return fail("scan_failed", err)
	}
	return result, 0
}
