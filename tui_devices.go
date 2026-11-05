package main

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

const (
	deviceHost = iota
	deviceConfirm
	deviceAuto
	deviceFirstResult
)

type deviceDiscoveryMsg struct {
	generation int
	result     commandResult
}
type manualDeviceDiscoveryMsg struct {
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
	host := m.request.host
	if cfg, err := loadConfig(); err == nil {
		if host == "" {
			host = cfg.Device.Host
		}
	} else {
		m.notice = "读取设备默认值失败：" + err.Error()
	}
	if m.request.endpoint != "" {
		if current, err := deviceFromProtocolEndpoint(m.request.endpoint, m.request.protocol); err == nil {
			host = current.Host
		}
	}
	input := textinput.New()
	input.Prompt = ""
	input.SetVirtualCursor(true)
	input.SetValue(host)
	input.Placeholder = "扫描仪 IP，例如 192.168.1.100"
	styles := input.Styles()
	styles.Focused.Prompt, styles.Focused.Placeholder = tuiAccent, tuiMuted
	styles.Cursor.Color = lipgloss.Color("6")
	input.SetStyles(styles)
	m.deviceInputs[deviceHost] = input
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
	ip, err := discoveryIP(strings.TrimSpace(m.deviceInputs[deviceHost].Value()))
	if err != nil {
		m.notice = err.Error()
		return
	}
	m.stopDeviceDiscovery()
	m.autoDiscover, m.discovering, m.devices = false, true, nil
	m.notice = "正在发现 IP " + ip.String() + " 的扫描服务；Esc 返回，不会启动扫描"
	ctx, cancel := context.WithCancel(m.ctx)
	m.discoveryCancel = cancel
	generation := m.discoveryGeneration
	r, find := m.request, m.find
	r.command, r.timeout = "discover", min(r.timeout, 10*time.Second)
	m.task(func() tea.Msg {
		defer cancel()
		filtered := func(ctx context.Context) ([]scannerDevice, []diagnostic, error) {
			return discoverDeviceIP(ctx, ip.String(), find)
		}
		result, _ := executeCommand(ctx, r, filtered, commandHooks{}, io.Discard, io.Discard)
		return manualDeviceDiscoveryMsg{generation: generation, result: result}
	})
}

func (m *humanModel) verifyScannerDevice(selected scannerDevice) {
	protocol := ""
	if m.request.protocolExplicit {
		protocol = m.request.protocol
	}
	e, err := selected.endpointFor(protocol)
	if err != nil {
		m.notice = err.Error()
		return
	}
	device, err := deviceFromProtocolEndpoint(e.Endpoint, e.Protocol)
	if err != nil {
		m.notice = err.Error()
		return
	}
	m.deviceInputs[deviceHost].SetValue(device.Host)
	m.stopDeviceDiscovery()
	m.autoDiscover, m.devices = false, nil
	m.deviceField = min(m.deviceField, deviceAuto)
	m.working, m.state, m.notice = true, "working", ""
	m.validatingEndpoint = e.Endpoint
	m.status = "正在验证 " + protocolLabel(e.Protocol) + " 扫描服务；通过后保存默认设备…"
	r := m.request
	r.command, r.endpoint, r.protocol, r.host = "info", e.Endpoint, e.Protocol, ""
	r.protocolExplicit = true
	r.recordID = selected.RecordID
	m.task(func() tea.Msg {
		result, code := executeCommand(m.ctx, r, nil, commandHooks{}, io.Discard, io.Discard)
		if code == 0 {
			if m.ctx.Err() != nil {
				result.fail("cancelled", m.ctx.Err())
				code = 130
			} else if result.device == nil || result.device.RecordID == "" {
				result.fail("device_store_failed", errRecordNotFound)
				code = 1
			} else if err := saveDefaultRecord(m.ctx, result.device.RecordID); err != nil {
				result.fail("config_save_failed", fmt.Errorf("登记默认设备失败；未启动扫描：%w", err))
				code = 1
			}
		}
		return deviceSavedMsg{endpoint: e.Endpoint, protocol: e.Protocol, result: result, code: code}
	})
}

func (m *humanModel) updateDevices(msg tea.KeyPressMsg) tea.Cmd {
	if m.discovering && !m.autoDiscover && msg.String() != "esc" {
		return nil
	}
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
			m.verifyScannerDevice(m.devices[m.deviceField-deviceFirstResult])
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
	lines := []string{tuiAccent.Render("添加设备（只需 IP；先发现并验证，再保存）")}
	focus := 0
	for field, name := range []string{"IP", "发现并使用设备", "自动发现"} {
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
			status = "未取得可用设备，可输入 IP 或按 r 重试"
		}
		lines = append(lines, tuiAccent.Render(ansi.Truncate(status, width, "…")))
		for i, d := range m.devices {
			prefix := "  "
			if m.deviceField == deviceFirstResult+i {
				prefix, focus = "> ", len(lines)
			}
			label := m.scannerLabel(d.Endpoint, d.Protocol, d.Model, width-2)
			lines = append(lines, prefix+label)
		}
	}
	m.viewport.SetWidth(width)
	m.viewport.SetHeight(max(1, m.height-8))
	m.viewport.FillHeight, m.viewport.SoftWrap = false, true
	m.viewport.SetContent(strings.Join(lines, "\n"))
	m.viewport.EnsureVisible(focus, 0, 0)
	return m.viewport.View()
}
