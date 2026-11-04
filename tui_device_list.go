package main

import (
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

func (m *humanModel) reloadDrives() {
	m.drives, m.drivesError = loadDrives()
}

func (m *humanModel) deviceLabel(endpoint string, width int) string {
	model := "未知型号"
	for _, device := range m.drives {
		if device.Endpoint == endpoint && device.Model != "" {
			model = device.Model
			break
		}
	}
	ip := endpoint
	if device, err := deviceFromEndpoint(endpoint); err == nil {
		ip = device.Host
	}
	if endpoint == "" {
		return tuiMuted.Render("未选择设备")
	}
	ip = terminalText(ip)
	model = ansi.Truncate(terminalText(model), max(1, width-2-ansi.StringWidth(ip)), "…")
	return tuiModel.Render(model) + "  " + tuiPath.Render(ip)
}

func (m *humanModel) editDevice() tea.Cmd {
	m.stopDeviceDiscovery()
	m.state, m.autoDiscover, m.notice, m.deviceCursor = "device-list", false, "", 0
	m.reloadDrives()
	if m.drivesError != nil {
		m.notice = "读取设备信息表失败：" + m.drivesError.Error()
	}
	for i, device := range m.drives {
		if device.Endpoint == m.request.endpoint {
			m.deviceCursor = i
			break
		}
	}
	m.viewport.GotoTop()
	return nil
}

func (m *humanModel) updateDeviceList(msg tea.KeyPressMsg) tea.Cmd {
	switch msg.String() {
	case "up", "shift+tab":
		m.deviceCursor = max(0, m.deviceCursor-1)
	case "down", "tab":
		m.deviceCursor = min(len(m.drives)+1, m.deviceCursor+1)
	case "enter":
		switch {
		case m.deviceCursor < len(m.drives):
			endpoint := m.drives[m.deviceCursor].Endpoint
			device, err := deviceFromEndpoint(endpoint)
			if err != nil {
				m.notice = err.Error()
				return nil
			}
			m.editDeviceForm()
			for i, value := range []string{device.Host, strconv.Itoa(device.Port), device.Path} {
				m.deviceInputs[i].SetValue(value)
			}
			m.verifyDevice()
		case m.deviceCursor == len(m.drives):
			return m.editDeviceForm()
		default:
			m.toggleDeviceDiscovery()
		}
	case "space":
		if m.deviceCursor == len(m.drives)+1 {
			m.toggleDeviceDiscovery()
		}
	case "r":
		if m.autoDiscover {
			m.discoverDevices()
		}
	case "esc":
		m.stopDeviceDiscovery()
		m.autoDiscover = false
		if m.request.endpoint == "" {
			return tea.Quit
		}
		m.checkDevice()
	}
	return nil
}

func (m *humanModel) deviceListView(width int) string {
	lines := []string{tuiAccent.Render("已保存的设备")}
	if m.drivesError != nil {
		lines = append(lines, tuiError.Render("设备信息表不可用，请检查 drives.json"))
	}
	focus := 0
	if len(m.drives) == 0 {
		lines = append(lines, tuiMuted.Render("暂无已保存设备"))
	}
	for i, device := range m.drives {
		prefix := "  "
		if m.deviceCursor == i {
			prefix, focus = tuiAccent.Render("> "), len(lines)
		}
		lines = append(lines, prefix+m.deviceLabel(device.Endpoint, width-2))
	}
	for i, text := range []string{"手动添加", "自动发现  [ ] 关闭"} {
		if i == 1 && m.autoDiscover {
			text = "自动发现  [x] 开启"
		}
		prefix := "  "
		if m.deviceCursor == len(m.drives)+i {
			prefix, focus = "> ", len(lines)
			text = tuiAccent.Render(prefix + text)
		} else {
			text = prefix + text
		}
		lines = append(lines, text)
	}
	if m.autoDiscover {
		text := "搜索完成；新设备已记录，可选择后验证"
		if m.discovering {
			text = "正在发现局域网设备…"
		}
		lines = append(lines, tuiMuted.Render(ansi.Truncate(text, width, "…")))
	}
	m.viewport.SetWidth(width)
	m.viewport.SetHeight(max(1, m.height-8))
	m.viewport.FillHeight, m.viewport.SoftWrap = false, true
	m.viewport.SetContent(strings.Join(lines, "\n"))
	m.viewport.EnsureVisible(focus, 0, 0)
	return m.viewport.View()
}
