package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"charm.land/bubbles/v2/textinput"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/term"
)

var (
	tuiAccent   = lipgloss.NewStyle().Foreground(lipgloss.Color("6")).Bold(true)
	tuiSuccess  = lipgloss.NewStyle().Foreground(lipgloss.Color("2"))
	tuiWarning  = lipgloss.NewStyle().Foreground(lipgloss.Color("3"))
	tuiError    = lipgloss.NewStyle().Foreground(lipgloss.Color("1"))
	tuiPath     = lipgloss.NewStyle().Foreground(lipgloss.Color("4"))
	tuiProtocol = lipgloss.NewStyle().Foreground(lipgloss.Color("208"))
	tuiMuted    = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	tuiModel    = lipgloss.NewStyle().Bold(true)
)

func interactiveOutput(output io.Writer) bool {
	file, ok := output.(*os.File)
	return ok && os.Getenv("TERM") != "dumb" && term.IsTerminal(file.Fd()) && term.IsTerminal(os.Stdin.Fd())
}

// Device metadata and errors are untrusted terminal content.
func terminalText(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 32 || (r >= 127 && r <= 159) {
			return ' '
		}
		return r
	}, ansi.Strip(s))
}

type commandDoneMsg struct {
	result    commandResult
	code      int
	discovery bool
	checking  bool
}
type deviceSavedMsg struct {
	endpoint string
	protocol string
	result   commandResult
	code     int
}
type nextPageMsg struct{ reply chan bool }
type cancelMsg struct{}

type humanModel struct {
	ctx                   context.Context
	cancel                context.CancelFunc
	tasks                 *sync.WaitGroup
	request               commandRequest
	find                  discoveryFunc
	events                chan tea.Msg
	stopped               chan struct{}
	state, status, notice string
	working               bool
	checkedAt             time.Time
	capabilities          *info
	devices               []scannerDevice
	drives                []savedDevice
	drivesError           error
	deviceCursor          int
	deviceInputs          [1]textinput.Model
	deviceField           int
	autoDiscover          bool
	discovering           bool
	discoveryCancel       context.CancelFunc
	discoveryGeneration   int
	validatingEndpoint    string
	width, height         int
	page                  int
	latest                string
	warnings              []diagnostic
	reply                 chan bool
	result                commandResult
	lastResult            commandDoneMsg // Read only after tasks.Wait on an abnormal UI exit.
	code                  int
	input                 textinput.Model
	editing               bool
	choices               []settingChoice
	choice, field         int
	draft                 commandRequest
	viewport              viewport.Model
}

func newHumanModel(ctx context.Context, cancel context.CancelFunc, request commandRequest, find discoveryFunc) *humanModel {
	m := &humanModel{ctx: ctx, cancel: cancel, tasks: &sync.WaitGroup{}, request: request, find: find,
		events: make(chan tea.Msg, 16), stopped: make(chan struct{}), state: "working", status: "等待检查设备…", width: 80, height: 24,
		viewport: viewport.New()}
	m.reloadDrives()
	return m
}

func (m *humanModel) emit(msg tea.Msg) {
	select {
	case m.events <- msg:
	case <-m.stopped:
	}
}

func (m *humanModel) listen() tea.Msg {
	select {
	case msg := <-m.events:
		return msg
	case <-m.stopped:
		return nil
	}
}

// Own workers independently of Bubble Tea so an early UI exit still waits for cleanup.
func (m *humanModel) task(work func() tea.Msg) {
	m.tasks.Add(1)
	go func() {
		defer m.tasks.Done()
		msg := work()
		if result, ok := msg.(commandDoneMsg); ok {
			m.lastResult = result
		}
		// Completion shares the event queue so it cannot overtake progress or a page prompt.
		m.emit(msg)
	}()
}

func (m *humanModel) start(discovery bool) {
	m.working, m.state, m.notice = true, "working", ""
	r := m.request
	if discovery {
		r.command = "discover"
		m.status = "正在发现设备…"
	} else if r.command == "info" {
		m.status = "正在读取设备能力…"
	} else {
		m.status = "正在准备扫描…"
	}
	m.task(func() tea.Msg {
		hooks := commandHooks{event: func(event scanEvent) { m.emit(event) }}
		if r.manual {
			hooks.wait = func(ctx context.Context) (bool, error) {
				reply := make(chan bool, 1)
				m.emit(nextPageMsg{reply: reply})
				select {
				case more := <-reply:
					return more, nil
				case <-ctx.Done():
					return false, ctx.Err()
				}
			}
		}
		result, code := executeCommand(m.ctx, r, m.find, hooks, io.Discard, io.Discard)
		return commandDoneMsg{result: result, code: code, discovery: discovery}
	})
}

func (m *humanModel) checkDevice() {
	m.working, m.state, m.status, m.notice = true, "working", "正在检查设备状态和扫描能力…", ""
	r := m.request
	r.command = "info"
	m.task(func() tea.Msg {
		result, code := executeCommand(m.ctx, r, m.find, commandHooks{}, io.Discard, io.Discard)
		return commandDoneMsg{result: result, code: code, checking: true}
	})
}

func (m *humanModel) showReadiness(result commandResult, code int) {
	m.reloadDrives()
	if code == 0 {
		if _, _, err := imageTicket(*result.Info, m.request.opts, strings.ToLower(filepath.Ext(m.request.output))); err != nil {
			result.fail("device_not_ready", err)
			code = 1
		}
	}
	m.capabilities, m.checkedAt, m.code = result.Info, time.Now(), code
	if code == 0 {
		m.state, m.status, m.notice = "ready", "就绪（设备报告 Idle；当前参数有效）", ""
	} else {
		m.state, m.status, m.notice = "unavailable", "无法确认就绪；未启动扫描", result.Error.Message
	}
}

func (m *humanModel) Init() tea.Cmd {
	var focus tea.Cmd
	if m.request.command == "discover" {
		m.start(true)
	} else if m.request.endpoint == "" && m.request.host == "" && m.request.recordID == "" {
		focus = m.editDevice()
	} else if m.request.command == "info" {
		m.start(false)
	} else {
		m.checkDevice()
	}
	return tea.Batch(focus, m.listen, func() tea.Msg { <-m.ctx.Done(); return cancelMsg{} })
}

func (m *humanModel) finish(result commandResult, code int) {
	m.result, m.code, m.state, m.working, m.notice = result, code, "done", false, ""
	m.result.Warnings = append(m.warnings, m.result.Warnings...)
	m.viewport.GotoTop()
}

func (m *humanModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case cancelMsg:
		if !m.working {
			m.code = 130
			return m, tea.Quit
		}
		m.status, m.state = "正在取消并保存已完成页面，请稍候…", "working"
	case scanEvent:
		if m.ctx.Err() != nil {
			return m, m.listen
		}
		switch msg.kind {
		case "scanning":
			m.page = msg.page
			m.status = fmt.Sprintf("正在检查能力并扫描第 %d 页…", msg.page)
		case "scanned":
			m.status = fmt.Sprintf("已扫描第 %d 页；结束时保存 PDF", msg.page)
		case "saved", "original":
			m.latest = msg.path
		case "warning":
			m.notice = msg.warning.Error()
		}
		return m, m.listen
	case nextPageMsg:
		if m.ctx.Err() != nil {
			return m, m.listen
		}
		m.reply, m.state = msg.reply, "waiting"
		m.status = fmt.Sprintf("第 %d 页已扫描，请更换扫描页", m.page)
		return m, m.listen
	case commandDoneMsg:
		m.working = false
		if msg.result.device != nil {
			m.request.endpoint, m.request.protocol, m.request.host = msg.result.device.Endpoint, msg.result.device.Protocol, ""
			m.request.recordID = msg.result.device.RecordID
		}
		if msg.checking && m.ctx.Err() == nil {
			m.showReadiness(msg.result, msg.code)
		} else {
			m.finish(msg.result, msg.code)
		}
		if m.ctx.Err() != nil {
			m.code = 130
			return m, tea.Quit
		}
		return m, m.listen
	case manualDeviceDiscoveryMsg:
		if msg.generation != m.discoveryGeneration || m.state != "devices" || !m.discovering || m.autoDiscover {
			return m, m.listen
		}
		m.discovering = false
		m.warnings = msg.result.Warnings
		if msg.result.Error != nil {
			m.notice = msg.result.Error.Message
			return m, m.listen
		}
		m.reloadDrives()
		if msg.result.Devices != nil {
			m.devices = *msg.result.Devices
		}
		if len(m.devices) == 1 {
			m.verifyScannerDevice(m.devices[0])
		} else {
			m.autoDiscover = true
			m.deviceField = deviceFirstResult
			m.notice = "该 IP 有多条无法确认同一身份的记录；请选择设备，不会自动合并"
		}
		return m, m.listen
	case deviceDiscoveryMsg:
		if msg.generation == m.discoveryGeneration && m.autoDiscover && (m.state == "devices" || m.state == "device-list") {
			m.discovering = false
			m.warnings = msg.result.Warnings
			m.notice = warningText(msg.result.Warnings)
			if msg.result.Error != nil {
				m.notice += " " + msg.result.Error.Message
			}
			if msg.result.Devices != nil {
				m.devices = *msg.result.Devices
			}
			action := m.deviceCursor - len(m.drives)
			m.reloadDrives()
			if action >= 0 {
				m.deviceCursor = len(m.drives) + min(action, 1)
			} else {
				m.deviceCursor = min(m.deviceCursor, len(m.drives)+1)
			}
			m.deviceField = min(m.deviceField, deviceFirstResult+len(m.devices)-1)
		}
		return m, m.listen
	case deviceSavedMsg:
		m.working, m.validatingEndpoint = false, ""
		if m.ctx.Err() != nil {
			m.code = 130
			return m, tea.Quit
		}
		if msg.code != 0 {
			m.state, m.notice = "devices", msg.result.Error.Message
			return m, m.listen
		}
		m.request.endpoint, m.request.protocol, m.request.host = msg.endpoint, msg.protocol, ""
		if msg.result.device != nil {
			m.request.recordID = msg.result.device.RecordID
		}
		m.reloadDrives()
		if m.request.command == "info" {
			m.finish(msg.result, 0)
		} else {
			m.showReadiness(msg.result, 0)
		}
		return m, m.listen
	case tea.KeyPressMsg:
		key := msg.String()
		if key == "ctrl+c" {
			m.cancel()
			if !m.working {
				m.code = 130
				return m, tea.Quit
			}
			m.state, m.status = "working", "正在取消并保存已完成页面，请稍候…"
			return m, nil
		}
		if m.ctx.Err() != nil {
			return m, nil
		}
		switch m.state {
		case "ready", "unavailable":
			switch key {
			case "enter":
				if m.state == "ready" {
					m.start(false)
				}
			case "r":
				m.checkDevice()
			case "d":
				return m, m.editDevice()
			case "s":
				return m, m.editSettings()
			case "q", "esc":
				return m, tea.Quit
			}
		case "device-list":
			return m, m.updateDeviceList(msg)
		case "devices":
			return m, m.updateDevices(msg)
		case "waiting":
			if key == "enter" || key == "esc" {
				m.reply <- key == "enter"
				m.reply, m.state, m.status = nil, "working", "正在保存已完成页面…"
			}
		case "settings":
			return m, m.updateSettings(msg)
		case "done":
			if key == "d" && m.result.Error != nil && m.result.Error.Code == "device_record_not_found" {
				return m, m.editDevice()
			}
			if key == "q" || key == "esc" || key == "enter" {
				return m, tea.Quit
			}
			var cmd tea.Cmd
			m.viewport, cmd = m.viewport.Update(msg)
			return m, cmd
		}
	}
	if m.state == "devices" && m.deviceField < len(m.deviceInputs) && (!m.discovering || m.autoDiscover) {
		var cmd tea.Cmd
		m.deviceInputs[m.deviceField], cmd = m.deviceInputs[m.deviceField].Update(msg)
		return m, cmd
	}
	if m.state == "settings" && m.editing {
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		return m, cmd
	}
	return m, nil
}

func (m *humanModel) View() tea.View {
	width := max(10, m.width-2)
	lines := []string{tuiAccent.Render("GoScanner"), ""}
	footer := "Ctrl+C 取消"
	switch m.state {
	case "ready", "unavailable":
		checkbox := func(enabled bool, label string) string {
			if enabled {
				return tuiSuccess.Render("[x] " + label)
			}
			return tuiMuted.Render("[ ] " + label)
		}
		status := tuiSuccess.Render(m.status)
		if m.state == "unavailable" {
			status = tuiWarning.Render(m.status)
		}
		lines = append(lines, "设备  "+m.deviceLabel(m.request.endpoint, width-6),
			fmt.Sprintf("设置  %s · %s · %d DPI", terminalText(m.request.area), terminalText(m.request.opts.Mode), m.request.opts.DPI),
			fmt.Sprintf("选项  %s · %s · %s · %s", checkbox(m.request.manual, "连续"), checkbox(m.request.singlePage, "独立 PDF"), checkbox(m.request.opts.Deskew, "纠偏"), checkbox(m.request.opts.Autocrop, "裁切")),
			"输出  "+tuiPath.Render(terminalText(m.request.output)), "", "状态  "+status,
			tuiMuted.Render("检查  "+m.checkedAt.Format("15:04:05")+"（按 r 更新）"))
		footer = "r 重新检查 · d 更换设备 · s 设置 · q 退出"
		if m.state == "ready" {
			footer = "Enter 开始扫描 · " + footer
		}
	case "device-list":
		lines = append(lines, m.deviceListView(width))
		footer = "↑/↓ 选择 · Enter 使用/添加 · p 切换首选协议 · Esc 返回"
		if m.deviceCursor == len(m.drives)+1 {
			footer = "Space/Enter 自动发现开关 · r 重新搜索 · Esc 返回"
		}
	case "devices":
		lines = append(lines, m.devicesView(width))
		footer = "↑/↓/Tab 选择 · Enter 下一项/确认 · Esc 返回"
		if m.deviceField == deviceAuto {
			footer = "Space/Enter 自动发现开关 · r 重新搜索 · Esc 返回"
		}
	case "settings":
		lines = append(lines, m.settingsView(width))
		footer = "↑/↓ 选择设置 · Enter 修改/应用 · Space 开关 · Esc 放弃"
		if len(m.choices) > 0 {
			footer = "↑/↓ 选择值 · Enter 确认 · Esc 返回列表"
		}
		if m.editing {
			footer = "Enter 确认输入 · Esc 放弃本项"
		}
	case "working", "waiting":
		status := tuiAccent.Render(m.status)
		if m.state == "waiting" || m.ctx.Err() != nil {
			status = tuiWarning.Render(m.status)
		}
		endpoint := m.request.endpoint
		if m.validatingEndpoint != "" {
			endpoint = m.validatingEndpoint
		}
		lines = append(lines, "设备  "+m.deviceLabel(endpoint, width-6), "状态  "+status)
		if m.latest != "" {
			lines = append(lines, "文件  "+tuiPath.Render(terminalText(m.latest)))
		}
		if m.state == "waiting" {
			footer = "Enter 扫描下一页 · Esc 结束并保存 · Ctrl+C 取消"
		}
	case "done":
		m.viewport.SetWidth(width)
		m.viewport.SetHeight(max(1, m.height-5))
		m.viewport.FillHeight, m.viewport.SoftWrap = false, true
		m.viewport.SetContent(resultText(m.result, true))
		lines = append(lines, m.viewport.View())
		footer = "↑/↓ 滚动 · Enter / q 退出"
		if m.result.Error != nil && m.result.Error.Code == "device_record_not_found" {
			footer = "d 重新选择设备 · Enter / q 退出"
		}
	}
	if m.notice != "" {
		lines = append(lines, tuiWarning.Render("提示  "+terminalText(m.notice)))
	}
	lines = append(lines, "", tuiMuted.Render(footer))
	return tea.NewView(lipgloss.NewStyle().Width(width).Render(strings.Join(lines, "\n")))
}

func resultText(result commandResult, colored bool) string {
	paint := func(style lipgloss.Style, text string) string {
		if colored {
			return style.Render(text)
		}
		return text
	}
	lines := []string{paint(tuiSuccess, "状态  完成")}
	if result.Error != nil {
		lines[0] = paint(tuiError, "状态  失败")
		if result.Status == "partial" {
			lines[0] = paint(tuiWarning, "状态  部分完成；已保留文件")
		}
		lines = append(lines, paint(tuiError, terminalText(result.Error.Message)))
	}
	if result.Pages > 0 {
		lines = append(lines, paint(tuiSuccess, fmt.Sprintf("已保存 %d 页", result.Pages)))
	}
	for _, path := range result.Files {
		lines = append(lines, "文件  "+paint(tuiPath, terminalText(path)))
	}
	for _, path := range result.Originals {
		lines = append(lines, "原图  "+paint(tuiPath, terminalText(path)))
	}
	for _, path := range result.Retained {
		lines = append(lines, paint(tuiWarning, "暂存  "+terminalText(path)))
	}
	for _, warning := range result.Warnings {
		lines = append(lines, paint(tuiWarning, "警告  "+terminalText(warning.Error())))
	}
	for _, page := range result.Processing {
		angle := "不可用"
		if page.AngleReliable {
			angle = fmt.Sprintf("%.2f°", page.Angle)
		}
		lines = append(lines, fmt.Sprintf("第 %d 页处理  检出 %t · 角度 %s · 已修改 %t", page.Page, page.Detected, angle, page.Changed))
	}
	if result.Devices != nil {
		if len(*result.Devices) == 0 {
			lines = append(lines, "未发现 WSD 扫描仪")
		}
		for _, device := range *result.Devices {
			lines = append(lines, terminalText(device.Name+" · "+device.Model+" · "+device.Endpoint))
		}
	}
	if cap := result.Info; cap != nil {
		stateStyle := tuiSuccess
		if cap.State != "Idle" {
			stateStyle = tuiWarning
		}
		lines = append(lines, "设备状态  "+paint(stateStyle, terminalText(cap.State)),
			fmt.Sprintf("水平 DPI  %v", cap.WidthsDPI), fmt.Sprintf("垂直 DPI  %v", cap.HeightsDPI),
			"颜色  "+terminalText(strings.Join(cap.Colors, ", ")), "格式  "+terminalText(strings.Join(cap.Formats, ", ")),
			fmt.Sprintf("尺寸  %v ～ %v（%s）", cap.MinSize, cap.MaxSize, terminalText(cap.SizeUnit)))
	}
	return strings.Join(lines, "\n")
}

func runTUI(parent context.Context, request commandRequest, stdout, stderr io.Writer, find discoveryFunc) int {
	ctx, cancel := context.WithCancel(parent)
	model := newHumanModel(ctx, cancel, request, find)
	_, err := tea.NewProgram(model, tea.WithInput(os.Stdin), tea.WithOutput(stdout), tea.WithoutSignalHandler()).Run()
	close(model.stopped)
	cancel()
	model.tasks.Wait()
	if err != nil {
		fmt.Fprintln(stderr, "终端界面失败；已等待任务清理，勿立即重试扫描：", err)
		if !model.lastResult.discovery && model.lastResult.result.Status != "" {
			fmt.Fprintln(stderr, resultText(model.lastResult.result, false))
		}
		return exitCode(parent)
	}
	return model.code
}
