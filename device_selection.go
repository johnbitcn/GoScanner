//go:build darwin || linux

package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"
)

func chooseDevice(ctx context.Context, devices []scannerDevice, prompt io.Writer, lang language) (string, error) {
	if len(devices) == 0 {
		return "", diagnosticf("No network scanner was discovered. Wake the scanner and retry, or specify its IP with --host.", "未发现网络扫描仪；可唤醒设备后重试，或使用 --host 指定 IP")
	}
	fmt.Fprintln(prompt, lang.text("可用网络扫描设备："))
	for i, device := range devices {
		fmt.Fprintf(prompt, lang.text("%d) 名称 %q；型号 %q；协议 %s；首选 %s；地址 %q\n"), i+1, device.Name, device.Model, scannerProtocolLabel(device), protocolName(device.Protocol), device.Endpoint)
	}
	keys, err := openKeyInput()
	if err != nil {
		return "", diagnosticf("Scanner selection requires an interactive terminal. Specify its IP with --host: %w", "设备选择需要交互终端；请使用 --host 指定 IP：%w", err)
	}
	defer keys.file.Close()
	return readDeviceChoice(ctx, devices, keys.file, prompt, lang)
}

func readDeviceChoice(ctx context.Context, devices []scannerDevice, input *os.File, prompt io.Writer, lang language) (endpoint string, err error) {
	fd := int(input.Fd())
	if err := syscall.SetNonblock(fd, true); err != nil {
		return "", err
	}
	defer func() { err = errors.Join(err, syscall.SetNonblock(fd, false)) }()
	message := lang.text("请输入设备编号并按 Enter 确认，或输入 q 取消。确认后保存默认设备；scan 命令随后开始扫描。")
	fmt.Fprintln(prompt, message)
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	var pending string
	var buffer [256]byte
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		n, err := syscall.Read(fd, buffer[:])
		if err == syscall.EINTR {
			continue
		}
		if err == syscall.EAGAIN || err == syscall.EWOULDBLOCK {
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-ticker.C:
				continue
			}
		}
		if err != nil {
			return "", err
		}
		if n == 0 {
			return "", diagnosticf("Scanner selection input ended. No scanner was selected.", "设备选择输入已结束；未选择设备")
		}
		pending += string(buffer[:n])
		if len(pending) > 4096 {
			return "", diagnosticf("The scanner selection number exceeds the input length limit.", "设备编号输入过长")
		}
		for strings.Contains(pending, "\n") {
			line, rest, _ := strings.Cut(pending, "\n")
			pending = rest
			line = strings.TrimSpace(line)
			if strings.EqualFold(line, "q") {
				return "", diagnosticf("Scanner selection was cancelled.", "已取消设备选择")
			}
			choice, err := strconv.Atoi(line)
			if err == nil && choice >= 1 && choice <= len(devices) {
				return devices[choice-1].Endpoint, ctx.Err()
			}
			fmt.Fprintln(prompt, lang.text("编号无效。"), message)
		}
	}
}
