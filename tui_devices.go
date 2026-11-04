package main

import (
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

const (
	deviceHost = iota
	devicePort
	devicePath
	deviceConfirm
	deviceAuto
	deviceFirstResult
)

type deviceDiscoveryMsg struct {
	generation int
	result     commandResult
}

func (m *humanModel) stopDeviceDiscovery() {
	if m.discoveryCancel != nil {
		m.discoveryCancel()
		m.discoveryCancel = nil
	}
	m.discoveryGeneration++
	m.discovering = false
}

func (m *humanModel) focusDeviceField(field int) tea.Cmd {
	for i := range m.deviceInputs {
		m.deviceInputs[i].Blur()
	}
	m.deviceField = max(0, min(deviceFirstResult+len(m.devices)-1, field))
	if m.deviceField < len(m.deviceInputs) {
		return m.deviceInputs[m.deviceField].Focus()
	}
	return nil
}

func (m *humanModel) editDeviceForm() tea.Cmd {
	m.stopDeviceDiscovery()
	m.state, m.notice, m.autoDiscover, m.devices, m.warnings = "devices", "", false, nil, nil
	device := defaultConfig().Device
	if cfg, err := loadConfig(); err == nil {
		device = cfg.Device
	} else {
		m.notice = "读取设备默认值失败：" + err.Error()
	}
	if m.request.endpoint != "" {
		if current, err := deviceFromEndpoint(m.request.endpoint); err == nil {
			device = current
		}
	}
	values := []string{device.Host, strconv.Itoa(device.Port), device.Path}
	for i, value := range values {
		input := textinput.New()
		input.Prompt = ""
		input.SetVirtualCursor(true)
		input.SetValue(value)
		styles := input.Styles()
		styles.Focused.Prompt, styles.Focused.Placeholder = tuiAccent, tuiMuted
		styles.Cursor.Color = lipgloss.Color("6")
		input.SetStyles(styles)
		m.deviceInputs[i] = input
	}
	m.deviceInputs[deviceHost].Placeholder = "扫描仪 IP，例如 192.168.1.100"
	m.viewport.GotoTop()
	return m.focusDeviceField(deviceHost)
}

func (m *humanModel) discoverDevices() {
	m.stopDeviceDiscovery()
	m.autoDiscover, m.discovering, m.devices, m.notice = true, true, nil, ""
	m.deviceField = min(m.deviceField, deviceAuto)
	ctx, cancel := context.WithCancel(m.ctx)
	m.discoveryCancel = cancel
	generation := m.discoveryGeneration
	r, find := m.request, m.find
	r.command, r.timeout = "discover", min(r.timeout, 10*time.Second)
	m.task(func() tea.Msg {
		defer cancel()
		result, _ := executeCommand(ctx, r, find, commandHooks{}, io.Discard, io.Discard)
		return deviceDiscoveryMsg{generation: generation, result: result}
	})
}

func (m *humanModel) verifyDevice() {
	port, err := strconv.Atoi(strings.TrimSpace(m.deviceInputs[devicePort].Value()))
	if err != nil {
		m.notice = "端口必须是 1～65535 的整数"
		return
	}
	device := deviceConfig{Host: strings.TrimSpace(m.deviceInputs[deviceHost].Value()), Port: port, Path: strings.TrimSpace(m.deviceInputs[devicePath].Value())}
	endpoint, err := device.endpoint()
	if err != nil {
		m.notice = err.Error()
		return
	}
	m.stopDeviceDiscovery()
	m.autoDiscover, m.devices = false, nil
	m.deviceField = min(m.deviceField, deviceAuto)
	m.working, m.state, m.notice = true, "working", ""
	m.validatingEndpoint = endpoint
	m.status = "正在验证 WSD 扫描服务；通过后保存默认设备…"
	r := m.request
	r.command, r.endpoint = "info", endpoint
	m.task(func() tea.Msg {
		result, code := executeCommand(m.ctx, r, nil, commandHooks{}, io.Discard, io.Discard)
		if code == 0 {
			if m.ctx.Err() != nil {
				result.fail("cancelled", m.ctx.Err())
				code = 130
			} else if err := saveDefaultDevice(device); err != nil {
				result.fail("config_save_failed", fmt.Errorf("登记默认设备失败；未启动扫描：%w", err))
				code = 1
			}
		}
		return deviceSavedMsg{endpoint: endpoint, result: result, code: code}
	})
}

func (m *humanModel) updateDevices(msg tea.KeyPressMsg) tea.Cmd {
	switch msg.String() {
	case "esc":
		m.stopDeviceDiscovery()
		m.autoDiscover = false
		return m.editDevice()
	case "up", "shift+tab":
		return m.focusDeviceField(m.deviceField - 1)
	case "down", "tab":
		return m.focusDeviceField(m.deviceField + 1)
	case "enter":
		switch {
		case m.deviceField < len(m.deviceInputs):
			return m.focusDeviceField(m.deviceField + 1)
		case m.deviceField == deviceConfirm:
			m.verifyDevice()
		case m.deviceField == deviceAuto:
			m.toggleDeviceDiscovery()
		case m.deviceField >= deviceFirstResult:
			device, err := deviceFromEndpoint(m.devices[m.deviceField-deviceFirstResult].Endpoint)
			if err != nil {
				m.notice = err.Error()
				return nil
			}
			for i, value := range []string{device.Host, strconv.Itoa(device.Port), device.Path} {
				m.deviceInputs[i].SetValue(value)
			}
			m.verifyDevice()
		}
		return nil
	case "space":
		if m.deviceField == deviceAuto {
			m.toggleDeviceDiscovery()
			return nil
		}
	case "r":
		if m.deviceField >= deviceConfirm && m.autoDiscover {
			m.discoverDevices()
			return nil
		}
	}
	if m.deviceField < len(m.deviceInputs) {
		var cmd tea.Cmd
		m.deviceInputs[m.deviceField], cmd = m.deviceInputs[m.deviceField].Update(msg)
		return cmd
	}
	return nil
}

func (m *humanModel) toggleDeviceDiscovery() {
	if m.autoDiscover {
		m.stopDeviceDiscovery()
		m.autoDiscover, m.devices, m.notice = false, nil, ""
		m.deviceField = min(m.deviceField, deviceAuto)
	} else {
		m.discoverDevices()
	}
}

func (m *humanModel) devicesView(width int) string {
	lines := []string{tuiAccent.Render("更换设备（先验证，再保存）")}
	focus := 0
	for field, name := range []string{"IP", "端口", "路径", "验证并使用设备", "自动发现"} {
		prefix, label := "  ", name
		if field == m.deviceField {
			prefix, focus = "> ", len(lines)
		}
		label = prefix + lipgloss.NewStyle().Width(18).Render(label)
		if field == m.deviceField {
			label = tuiAccent.Render(label)
		} else {
			label = tuiMuted.Render(label)
		}
		value := ""
		if field < len(m.deviceInputs) {
			m.deviceInputs[field].SetWidth(max(5, width-23))
			value = m.deviceInputs[field].View()
		}
		if field == deviceAuto {
			value = tuiMuted.Render("[ ] 关闭")
			if m.autoDiscover {
				value = tuiSuccess.Render("[x] 开启")
			}
		}
		lines = append(lines, label+"  "+value)
	}
	if m.autoDiscover {
		status := fmt.Sprintf("发现 %d 台设备；选择后按 Enter 验证并使用", len(m.devices))
		if m.discovering {
			status = "正在发现局域网设备…（关闭开关可停止）"
		} else if len(m.devices) == 0 {
			status = "未取得可用设备，可手动输入 IP 或按 r 重试"
		}
		lines = append(lines, tuiAccent.Render(ansi.Truncate(status, width, "…")))
		for i, device := range m.devices {
			prefix := "  "
			if m.deviceField == deviceFirstResult+i {
				prefix, focus = "> ", len(lines)
			}
			line := ansi.Truncate(prefix+terminalText(device.Name+" · "+device.Model+" · "+device.Endpoint), width, "…")
			if m.deviceField == deviceFirstResult+i {
				line = tuiAccent.Render(line)
			}
			lines = append(lines, line)
		}
	}
	m.viewport.SetWidth(width)
	m.viewport.SetHeight(max(1, m.height-8))
	m.viewport.FillHeight, m.viewport.SoftWrap = false, true
	m.viewport.SetContent(strings.Join(lines, "\n"))
	m.viewport.EnsureVisible(focus, 0, 0)
	return m.viewport.View()
}
