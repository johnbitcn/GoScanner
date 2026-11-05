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
			if (code >= 30 && code <= 38) || (code >= 40 && code <= 48) || (code >= 90 && code <= 107) {
				return true
			}
		}
	}
	return false
}

func TestHumanStatusAndSelectionColors(t *testing.T) {
	m := humanTestModel(t)
	if view := m.View().Content; !strings.Contains(view, tuiSuccess.Render(m.status)) {
		t.Fatal("ready status is not green", view)
	}
	m.state, m.status, m.notice = "unavailable", "设备忙碌", "请稍后重新检查"
	if view := m.View().Content; !strings.Contains(view, tuiWarning.Render(m.status)) || !strings.Contains(view, tuiWarning.Render("提示  "+m.notice)) {
		t.Fatal("unavailable status or notice is not yellow", view)
	}
	m.editSettings()
	if view := m.View().Content; !strings.Contains(view, tuiAccent.Render("150 DPI")) || !strings.Contains(ansi.Strip(view), "> 分辨率") {
		t.Fatal("selected row lost color or marker", view)
	}
	m.openSetting()
	if view := m.View().Content; !hasTerminalColor(view) || !strings.Contains(ansi.Strip(view), "> 150 DPI") {
		t.Fatal("choice list lost color or marker", view)
	}
}

func TestHumanCurrentProtocolIsOrange(t *testing.T) {
	m := humanTestModel(t)
	label := m.deviceLabel(m.request.endpoint, 40)
	if !strings.Contains(label, "\x1b[38;5;208mWSD") || strings.Contains(ansi.Strip(label), "eSCL + WSD") {
		t.Fatalf("protocol must be orange ANSI 208: %q", label)
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
			colored := m.View().Content
			view := ansi.Strip(colored)
			if !strings.Contains(view, tc.want) || strings.Contains(view, "true") || strings.Contains(view, "false") {
				t.Fatalf("unexpected option summary: %s", view)
			}
			for i, label := range []string{"连续", "独立 PDF", "纠偏", "裁切"} {
				want := tuiMuted.Render("[ ] " + label)
				if []bool{tc.manual, tc.singlePage, tc.deskew, tc.autocrop}[i] {
					want = tuiSuccess.Render("[x] " + label)
				}
				if !strings.Contains(colored, want) {
					t.Fatalf("option marker and label must share a color: %q", label)
				}
			}
		}
	}
}

func TestHumanResultColorsPreservePlainFallback(t *testing.T) {
	for _, result := range []commandResult{
		{Status: "ok", Files: []string{"scan.pdf"}, Pages: 1},
		{Status: "error", Error: &commandError{Code: "scan_failed", Message: "失败\x1b[2J"}},
		{Status: "partial", Files: []string{"scan.pdf"}, Pages: 1, Originals: []string{"original.jpg"}, Retained: []string{"staging"}, Warnings: []diagnostic{diagnosticf("Preprocessing warning.", "预处理警告")}, Error: &commandError{Code: "scan_failed", Message: "后续页失败"}},
	} {
		colored, plain := resultText(result, true, chinese), resultText(result, false, chinese)
		if !hasTerminalColor(colored) || strings.ContainsRune(plain, '\x1b') || ansi.Strip(colored) != plain {
			t.Fatalf("color changed result content: colored=%q plain=%q", colored, plain)
		}
		if result.Status == "error" && !strings.Contains(colored, tuiError.Render("状态  失败")) {
			t.Fatal("failure status is not red")
		}
		if result.Status == "partial" && !strings.Contains(colored, tuiWarning.Render("状态  部分完成；已保留文件")) {
			t.Fatal("partial status is not yellow")
		}
	}
}
