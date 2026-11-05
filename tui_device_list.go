package main

import (
	"net/url"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

func (m *humanModel) reloadDrives() {
	m.drives, m.drivesError = loadDrives()
}

func (m *humanModel) deviceLabel(endpoint string, width int) string {
	model, protocol := "未知型号", m.request.protocol
	for _, device := range m.drives {
		if !deviceHasEndpoint(device.scannerDevice, endpoint) {
			continue
		}
		if device.Model != "" {
			model = device.Model
		}
		for _, e := range scannerEndpoints(device.scannerDevice) {
			if e.Endpoint == endpoint {
				protocol = e.Protocol
				break
			}
		}
		break
	}
	return m.scannerLabel(endpoint, protocol, model, width)
}

func (m *humanModel) scannerLabel(endpoint, protocol, model string, width int) string {
	if model == "" {
		model = "未知型号"
	}
	ip := endpoint
	if u, err := url.Parse(endpoint); err == nil && u.Hostname() != "" {
		ip = u.Hostname()
	}
	if endpoint == "" {
		return tuiMuted.Render("未选择设备")
	}
	ip = terminalText(ip)
	label := protocolLabel(protocol)
	model = ansi.Truncate(terminalText(model), max(1, width-4-ansi.StringWidth(ip)-ansi.StringWidth(label)), "…")
	return tuiModel.Render(model) + "  " + tuiPath.Render(ip) + "  " + tuiProtocol.Render(label)
}

func (m *humanModel) editDevice() tea.Cmd {
	m.stopDeviceDiscovery()
	m.state, m.autoDiscover, m.notice, m.deviceCursor = "device-list", false, "", 0
	if err := ensureStoredRecordIDs(); err != nil {
		m.notice = "设备记录初始化失败：" + err.Error()
	}
	m.reloadDrives()
	if m.drivesError != nil {
		m.notice = "读取设备信息表失败：" + m.drivesError.Error()
	}
	for i, device := range m.drives {
		if recordMatches(device, m.request.recordID) || (m.request.recordID == "" && deviceHasEndpoint(device.scannerDevice, m.request.endpoint)) {
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
			selected := m.drives[m.deviceCursor].scannerDevice
			m.editDeviceForm()
			m.verifyScannerDevice(selected)
		case m.deviceCursor == len(m.drives):
			return m.editDeviceForm()
		default:
			m.toggleDeviceDiscovery()
		}
	case "p":
		if m.deviceCursor >= len(m.drives) {
			m.notice = "请先选中设备，再修改首选协议"
			return nil
		}
		d := m.drives[m.deviceCursor].scannerDevice
		preferred := "escl"
		if protocolName(d.Protocol) == "escl" {
			preferred = "wsd"
		}
		e, err := d.endpointFor(preferred)
		if err != nil {
			m.notice = "该设备只确认支持 " + protocolLabel(d.Protocol) + "，不能切换首选协议"
			return nil
		}
		if err := savePreferredProtocol(d.RecordID, preferred); err != nil {
			m.notice = "保存首选协议失败：" + err.Error()
			return nil
		}
		if (recordMatches(m.drives[m.deviceCursor], m.request.recordID) || (m.request.recordID == "" && deviceHasEndpoint(d, m.request.endpoint))) && !m.request.protocolExplicit {
			m.request.endpoint, m.request.protocol, m.request.host = e.Endpoint, e.Protocol, ""
			m.request.recordID = d.RecordID
			m.capabilities = nil
		}
		m.reloadDrives()
		m.notice = "已保存首选协议：" + protocolLabel(preferred) + "；连接失败不会自动切换协议"
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
		lines = append(lines, prefix+m.scannerLabel(device.Endpoint, device.Protocol, device.Model, width-2))
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
