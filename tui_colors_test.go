package main

import (
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

var sgrSequence = regexp.MustCompile(`\x1b\[[0-9;:]*m`)

func hasTerminalColor(text string) bool {
	for _, sequence := range sgrSequence.FindAllString(text, -1) {
		for _, parameter := range strings.FieldsFunc(sequence[2:len(sequence)-1], func(r rune) bool { return r == ';' || r == ':' }) {
			code, _ := strconv.Atoi(parameter)
			if (code >= 30 && code <= 38) || (code >= 40 && code <= 48) || code == 58 || (code >= 90 && code <= 107) {
				return true
			}
		}
	}
	return false
}

func TestHumanUsesThemeColors(t *testing.T) {
	m := humanTestModel(t)
	m.notice = "请检查设备"
	for _, state := range []string{"ready", "unavailable", "working", "waiting", "done"} {
		m.state = state
		view := m.View().Content
		if !hasTerminalColor(view) || !strings.Contains(view, m.styles.accent.Render("GoScanner")) || !strings.Contains(ansi.Strip(view), "提示  "+m.notice) {
			t.Fatalf("%s must use theme colors and retain emphasis and notices: %q", state, view)
		}
	}
}

func TestHumanSelectionUsesMarkers(t *testing.T) {
	m := humanTestModel(t)
	m.editDevice()
	m.deviceCursor = len(m.drives)
	if view := m.View().Content; !hasTerminalColor(view) || !strings.Contains(ansi.Strip(view), "> 手动添加") {
		t.Fatal("device list must use theme colors and a selection marker", view)
	}
	m.editSettings()
	if view := m.View().Content; !hasTerminalColor(view) || !strings.Contains(view, m.styles.accent.Render("150 DPI")) || !strings.Contains(ansi.Strip(view), "> 分辨率") {
		t.Fatal("selected setting lost emphasis or marker", view)
	}
	m.openSetting()
	if view := m.View().Content; !hasTerminalColor(view) || !strings.Contains(ansi.Strip(view), "> 150 DPI") {
		t.Fatal("choice list must use theme colors and a selection marker", view)
	}
}

func TestHumanInputsUseThemeColors(t *testing.T) {
	m := humanTestModel(t)
	m.editDeviceForm()
	for _, value := range []string{"", "192.168.1.100"} {
		m.deviceInputs[deviceHost].SetValue(value)
		m.deviceInputs[deviceHost].Focus()
		if view := m.View().Content; !hasTerminalColor(view) {
			t.Fatalf("focused device input lost theme colors: %q", view)
		}
		m.deviceInputs[deviceHost].Blur()
		if view := m.View().Content; !hasTerminalColor(view) {
			t.Fatalf("blurred device input lost theme colors: %q", view)
		}
	}
	m.editSettings()
	m.field = settingPath
	m.beginSettingInput()
	for _, value := range []string{"", "/tmp/scan.pdf"} {
		m.input.SetValue(value)
		m.input.Focus()
		if view := m.View().Content; !hasTerminalColor(view) {
			t.Fatalf("focused settings input lost theme colors: %q", view)
		}
		m.input.Blur()
		if view := m.View().Content; !hasTerminalColor(view) {
			t.Fatalf("blurred settings input lost theme colors: %q", view)
		}
	}
}

func TestHumanDeviceLabelUsesThemeColors(t *testing.T) {
	m := humanTestModel(t)
	label := m.deviceLabel(m.request.endpoint, 40)
	if !hasTerminalColor(label) || !strings.Contains(ansi.Strip(label), "WSD") || strings.Contains(ansi.Strip(label), "eSCL + WSD") {
		t.Fatalf("protocol must use theme colors and show only the current protocol: %q", label)
	}
}

func TestHumanOptionCheckboxes(t *testing.T) {
	m := humanTestModel(t)
	for _, state := range []string{"ready", "unavailable"} {
		m.state = state
		for _, tc := range []struct {
			manual, singlePage, deskew, autocrop bool
			want                                 string
		}{
			{want: "选项  [ ] 连续 · [ ] 独立 PDF · [ ] 纠偏 · [ ] 裁切"},
			{manual: true, want: "选项  [x] 连续 · [ ] 独立 PDF · [ ] 纠偏 · [ ] 裁切"},
			{singlePage: true, want: "选项  [ ] 连续 · [x] 独立 PDF · [ ] 纠偏 · [ ] 裁切"},
			{deskew: true, want: "选项  [ ] 连续 · [ ] 独立 PDF · [x] 纠偏 · [ ] 裁切"},
			{autocrop: true, want: "选项  [ ] 连续 · [ ] 独立 PDF · [ ] 纠偏 · [x] 裁切"},
			{manual: true, singlePage: true, deskew: true, autocrop: true, want: "选项  [x] 连续 · [x] 独立 PDF · [x] 纠偏 · [x] 裁切"},
		} {
			m.request.manual, m.request.singlePage = tc.manual, tc.singlePage
			m.request.opts.Deskew, m.request.opts.Autocrop = tc.deskew, tc.autocrop
			view := m.View().Content
			plain := ansi.Strip(view)
			if !hasTerminalColor(view) || !strings.Contains(plain, tc.want) || strings.Contains(plain, "true") || strings.Contains(plain, "false") {
				t.Fatalf("unexpected option summary: %s", view)
			}
		}
	}
}

func TestHumanResultStylePreservesPlainFallback(t *testing.T) {
	for _, result := range []commandResult{
		{Status: "ok", Files: []string{"scan.pdf"}, Pages: 1},
		{Status: "error", Error: &commandError{Code: "scan_failed", Message: "失败\x1b[2J"}},
		{Status: "partial", Files: []string{"scan.pdf"}, Pages: 1, Originals: []string{"original.jpg"}, Retained: []string{"staging"}, Warnings: []diagnostic{diagnosticf("Preprocessing warning.", "预处理警告")}, Error: &commandError{Code: "scan_failed", Message: "后续页失败"}},
		{Status: "ok", Info: &info{State: "Busy", Colors: []string{"RGB24"}, Formats: []string{formatJPEG}}},
	} {
		styled, plain := resultText(result, true, chinese, newTUIStyles(true)), resultText(result, false, chinese, newTUIStyles(true))
		if !hasTerminalColor(styled) || strings.ContainsRune(plain, '\x1b') || ansi.Strip(styled) != plain {
			t.Fatalf("style changed result content or theme colors: styled=%q plain=%q", styled, plain)
		}
		if result.Status == "error" && !strings.Contains(styled, newTUIStyles(true).failure.Render("状态  失败")) {
			t.Fatal("failure status lost emphasis")
		}
		if result.Status == "partial" && !strings.Contains(styled, newTUIStyles(true).warning.Render("状态  部分完成；已保留文件")) {
			t.Fatal("partial status lost emphasis")
		}
	}
}
