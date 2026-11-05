package main

import (
	"fmt"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

const (
	settingDPI = iota
	settingMode
	settingArea
	settingFormat
	settingPath
	settingManual
	settingSinglePage
	settingDeskew
	settingAutocrop
	settingLanguage
	settingApply
)

var settingNames = []string{"分辨率", "颜色", "扫描区域", "输出格式", "输出路径", "连续扫描", "PDF 每页独立保存", "AI 纠偏", "AI 裁切", "界面语言", "保存并应用"}

type settingChoice struct{ label, value string }

func (m *humanModel) editSettings() tea.Cmd {
	m.draft, m.state, m.field, m.notice = m.request, "settings", settingDPI, ""
	m.choices, m.editing = nil, false
	m.draftLanguage = "auto"
	if cfg, err := loadConfig(); err == nil {
		m.draftLanguage = cfg.Language
	} else {
		m.notice = m.lang.error(err)
	}
	m.viewport.GotoTop()
	return nil
}

func (m *humanModel) settingToggle() *bool {
	switch m.field {
	case settingManual:
		return &m.draft.manual
	case settingSinglePage:
		return &m.draft.singlePage
	case settingDeskew:
		return &m.draft.opts.Deskew
	case settingAutocrop:
		return &m.draft.opts.Autocrop
	default:
		return nil
	}
}

func outputFormatLabel(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".jpg", ".jpeg":
		return "JPEG"
	case ".tif", ".tiff":
		return "TIFF"
	case ".png":
		return "PNG"
	case ".pdf":
		return "PDF"
	default:
		return "未知格式"
	}
}

func (m *humanModel) settingValues() []string {
	mode := m.lang.text("彩色")
	if m.draft.opts.Mode == "gray" {
		mode = m.lang.text("灰度")
	}
	area := fmt.Sprintf(m.lang.text("自定义（%s）"), m.draft.area)
	if strings.EqualFold(m.draft.area, "a4") {
		area = "A4"
	}
	if strings.EqualFold(m.draft.area, "full") {
		area = m.lang.text("完整平板")
	}
	onOff := func(value bool) string {
		if value {
			return m.lang.text("开启")
		}
		return m.lang.text("关闭")
	}
	single := onOff(m.draft.singlePage)
	if outputFormatLabel(m.draft.output) != "PDF" {
		single = m.lang.text("不可用（仅 PDF）")
	}
	language := m.lang.text("自动（系统语言）")
	if m.draftLanguage == "en" {
		language = "English"
	} else if m.draftLanguage == "zh-CN" {
		language = "简体中文"
	}
	return []string{fmt.Sprintf("%d DPI", m.draft.opts.DPI), mode, area, m.lang.text(outputFormatLabel(m.draft.output)), m.draft.output,
		onOff(m.draft.manual), single, onOff(m.draft.opts.Deskew), onOff(m.draft.opts.Autocrop), language, m.lang.text("保存配置，重新检查设备")}
}

func (m *humanModel) settingChoices() []settingChoice {
	if m.field == settingLanguage {
		return []settingChoice{{m.lang.text("自动（系统语言）"), "auto"}, {"English", "en"}, {"简体中文", "zh-CN"}}
	}
	if m.settingToggle() != nil {
		return []settingChoice{{m.lang.text("关闭"), "false"}, {m.lang.text("开启"), "true"}}
	}
	if m.field == settingArea {
		return []settingChoice{{"A4", "a4"}, {m.lang.text("完整平板"), "full"}, {m.lang.text("自定义区域…"), "custom"}}
	}
	cap := m.capabilities
	if cap == nil {
		return nil
	}
	var choices []settingChoice
	switch m.field {
	case settingDPI:
		values := slices.Clone(cap.WidthsDPI)
		slices.Sort(values)
		for _, dpi := range slices.Compact(values) {
			if slices.Contains(cap.HeightsDPI, dpi) {
				choices = append(choices, settingChoice{fmt.Sprintf("%d DPI", dpi), strconv.Itoa(dpi)})
			}
		}
	case settingMode:
		if slices.Contains(cap.Colors, "RGB24") {
			choices = append(choices, settingChoice{m.lang.text("彩色"), "color"})
		}
		if slices.Contains(cap.Colors, "Grayscale8") {
			choices = append(choices, settingChoice{m.lang.text("灰度"), "gray"})
		}
	case settingFormat:
		if slices.Contains(cap.Formats, formatJPEG) {
			choices = append(choices, settingChoice{"JPEG", ".jpg"}, settingChoice{"PDF", ".pdf"})
		}
		if slices.Contains(cap.Formats, formatTIFF) {
			choices = append(choices, settingChoice{"TIFF", ".tiff"}, settingChoice{"PNG", ".png"})
		} else if cap.escl != nil && (slices.Contains(cap.Formats, formatPNG) || slices.Contains(cap.Formats, formatJPEG)) {
			choices = append(choices, settingChoice{"PNG", ".png"})
		}
	}
	return choices
}

func (m *humanModel) beginSettingInput() tea.Cmd {
	m.input = textinput.New()
	m.input.SetVirtualCursor(true)
	styles := m.input.Styles()
	styles.Focused.Prompt, styles.Focused.Placeholder = tuiAccent, tuiMuted
	styles.Cursor.Color = lipgloss.Color("6")
	m.input.SetStyles(styles)
	m.input.SetWidth(max(5, m.width-6))
	value := m.draft.output
	if m.field == settingArea {
		value = m.draft.area
		if strings.EqualFold(value, "a4") || strings.EqualFold(value, "full") {
			value = ""
		}
		m.input.Placeholder = m.lang.text("x,y,width,height（毫米）")
	}
	m.input.SetValue(value)
	m.editing, m.notice = true, ""
	return m.input.Focus()
}

func (m *humanModel) openSetting() tea.Cmd {
	m.notice = ""
	if m.field == settingSinglePage && outputFormatLabel(m.draft.output) != "PDF" {
		m.notice = m.lang.text("请先将输出格式设为 PDF")
		return nil
	}
	if m.field == settingApply {
		if err := m.draft.validateScan(); err != nil {
			m.notice = m.lang.error(err)
			return nil
		}
		selected := m.lang
		if !m.request.languageExplicit {
			var err error
			selected, err = selectLanguage(m.draftLanguage)
			if err != nil {
				m.notice = m.lang.error(err)
				return nil
			}
		}
		if err := saveHumanSettings(m.draft, m.draftLanguage); err != nil {
			m.notice = m.lang.error(diagnosticf("Cannot save settings: %w", "保存设置失败：%w", err))
			return nil
		}
		m.lang, m.draft.language = selected, selected
		m.request = m.draft
		m.checkDevice()
		return nil
	}
	if m.field == settingPath {
		return m.beginSettingInput()
	}
	m.choices, m.choice = m.settingChoices(), 0
	if len(m.choices) == 0 {
		m.notice = m.lang.text("没有可选值；请返回扫描页重新检查设备能力")
		return nil
	}
	current := m.settingValues()[m.field]
	for i, choice := range m.choices {
		if choice.label == current || (choice.value == "custom" && strings.HasPrefix(current, m.lang.text("自定义（"))) {
			m.choice = i
		}
	}
	return nil
}

func (m *humanModel) confirmSettingChoice() tea.Cmd {
	choice := m.choices[m.choice]
	m.choices = nil
	switch m.field {
	case settingLanguage:
		m.draftLanguage = choice.value
	case settingDPI:
		m.draft.opts.DPI, _ = strconv.Atoi(choice.value)
	case settingMode:
		m.draft.opts.Mode = choice.value
	case settingArea:
		if choice.value == "custom" {
			return m.beginSettingInput()
		}
		m.draft.area = choice.value
	case settingFormat:
		if outputFormatLabel(m.draft.output) != choice.label {
			m.draft.output = strings.TrimSuffix(m.draft.output, filepath.Ext(m.draft.output)) + choice.value
		}
		if choice.label != "PDF" && m.draft.singlePage {
			m.draft.singlePage = false
			m.notice = m.lang.text("已关闭 PDF 每页独立保存（仅适用于 PDF）")
		}
	default:
		*m.settingToggle() = choice.value == "true"
	}
	return nil
}

func (m *humanModel) updateSettings(msg tea.KeyPressMsg) tea.Cmd {
	key := msg.String()
	if m.editing {
		switch key {
		case "esc":
			m.editing, m.notice = false, ""
			m.input.Blur()
			return nil
		case "enter":
			draft := m.draft
			if m.field == settingArea {
				draft.area = m.input.Value()
			} else {
				draft.output = m.input.Value()
				if outputFormatLabel(draft.output) != "PDF" {
					draft.singlePage = false
				}
			}
			if err := draft.validateScan(); err != nil {
				m.notice = m.lang.error(err)
				return nil
			}
			m.notice = ""
			if m.draft.singlePage && !draft.singlePage {
				m.notice = m.lang.text("已关闭 PDF 每页独立保存（仅适用于 PDF）")
			}
			m.draft, m.editing = draft, false
			m.input.Blur()
			return nil
		}
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		return cmd
	}
	if len(m.choices) > 0 {
		switch key {
		case "up":
			m.choice = max(0, m.choice-1)
		case "down":
			m.choice = min(len(m.choices)-1, m.choice+1)
		case "enter":
			return m.confirmSettingChoice()
		case "esc":
			m.choices = nil
		}
		return nil
	}
	switch key {
	case "up", "shift+tab":
		m.field = max(0, m.field-1)
		m.notice = ""
	case "down", "tab":
		m.field = min(len(settingNames)-1, m.field+1)
		m.notice = ""
	case "enter":
		return m.openSetting()
	case "space":
		if value := m.settingToggle(); value != nil {
			if m.field == settingSinglePage && outputFormatLabel(m.draft.output) != "PDF" {
				m.notice = m.lang.text("请先将输出格式设为 PDF")
			} else {
				*value = !*value
				m.notice = ""
			}
		}
	case "esc":
		m.checkDevice()
	}
	return nil
}

func (m *humanModel) settingsView(width int) string {
	lines := []string{tuiAccent.Render(m.lang.text("设置（确认后保存）")),
		tuiMuted.Render(m.lang.text("Esc 放弃修改；--lang 优先于语言偏好。输出路径仅保存目录和格式。"))}
	values, focus := m.settingValues(), 0
	for field, name := range settingNames {
		name = m.lang.text(name)
		prefix := "  "
		if field == m.field {
			prefix, focus = "> ", len(lines)
		}
		label := prefix + lipgloss.NewStyle().Width(18).Render(name)
		value := terminalText(values[field])
		if field == m.field {
			label, value = tuiAccent.Render(label), tuiAccent.Render(value)
		} else {
			label = tuiMuted.Render(label)
			if values[field] == m.lang.text("开启") {
				value = tuiSuccess.Render(value)
			}
			if values[field] == m.lang.text("关闭") || strings.HasPrefix(values[field], m.lang.text("不可用")) {
				value = tuiMuted.Render(value)
			}
			if field == settingPath {
				value = tuiPath.Render(value)
			}
		}
		lines = append(lines, label+"  "+value)
		if field != m.field {
			continue
		}
		if m.editing {
			m.input.SetWidth(max(5, width-6))
			focus = len(lines)
			lines = append(lines, "    "+m.input.View())
		}
		for i, choice := range m.choices {
			prefix := "      "
			if i == m.choice {
				prefix, focus = "    > ", len(lines)
			}
			text := prefix + choice.label
			if i == m.choice {
				text = tuiAccent.Render(text)
			}
			lines = append(lines, text)
		}
	}
	m.viewport.SetWidth(width)
	m.viewport.SetHeight(max(1, m.height-8))
	m.viewport.FillHeight, m.viewport.SoftWrap = false, true
	m.viewport.SetContent(strings.Join(lines, "\n"))
	m.viewport.EnsureVisible(focus, 0, 0)
	return m.viewport.View()
}
