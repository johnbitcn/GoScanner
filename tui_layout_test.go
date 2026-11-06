package main

import (
	"fmt"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

func compactTUIText(text string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return -1
		}
		return r
	}, ansi.Strip(text))
}

func assertTUIFieldColumns(t *testing.T, view string, labels []string, column, width int) {
	t.Helper()
	seen := make(map[string]bool)
	inFields := false
	for _, line := range strings.Split(ansi.Strip(view), "\n") {
		if !utf8.ValidString(line) || ansi.StringWidth(line) > width {
			t.Fatalf("invalid terminal line (width %d): %q", width, line)
		}
		line = strings.TrimRight(line, " ")
		if line == "" {
			inFields = false
			continue
		}
		matched := false
		for _, label := range labels {
			if !strings.HasPrefix(line, label+" ") {
				continue
			}
			rest := strings.TrimPrefix(line, label)
			spaces := len(rest) - len(strings.TrimLeft(rest, " "))
			if actual := ansi.StringWidth(label) + spaces; actual != column {
				t.Fatalf("%s value starts at column %d, want %d: %q", label, actual, column, line)
			}
			seen[label], matched, inFields = true, true, true
			break
		}
		if inFields && !matched && !strings.HasPrefix(line, strings.Repeat(" ", column)) {
			t.Fatalf("continuation escaped the value column: %q", line)
		}
	}
	for _, label := range labels {
		if !seen[label] {
			t.Fatalf("missing field %q in %q", label, ansi.Strip(view))
		}
	}
}

func TestTUIFieldLayoutBreakpoint(t *testing.T) {
	label := newTUIStyles(true).accent.Render("Settings")
	for _, tc := range []struct {
		width, valueWidth int
		stacked           bool
	}{
		{29, 27, true},
		{30, 20, false},
		{31, 21, false},
	} {
		layout := newTUIFieldLayout(tc.width, "Scanner", label)
		if layout.labelWidth != 8 || layout.valueWidth != tc.valueWidth || layout.stacked != tc.stacked {
			t.Fatalf("width %d: unexpected layout %+v", tc.width, layout)
		}
		for _, line := range strings.Split(layout.row(label, "A long value that needs more than one line"), "\n") {
			if ansi.StringWidth(line) > tc.width {
				t.Fatalf("width %d: layout overflow %q", tc.width, line)
			}
		}
	}
}

func TestHumanHomeFieldAlignment(t *testing.T) {
	for _, lang := range []language{chinese, english} {
		for _, width := range []int{40, 80, 120} {
			for _, colored := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%d/colors=%t", lang.String(), width, colored), func(t *testing.T) {
					m := humanTestModel(t)
					m.lang, m.request.language, m.width = lang, lang, width
					m.request.output = "/tmp/扫描👩‍💻/a-very-long-directory-name/another-directory/scan.pdf"
					m.request.manual, m.request.singlePage = true, true
					m.request.opts.Deskew, m.request.opts.Autocrop = true, true
					m.status = "Ready (scanner reports Idle; current options are valid)"
					m.notice = "A long notice that must wrap without losing its value-column indentation."
					if !colored {
						m.styles = tuiStyles{accent: tuiEmphasis}
					}
					labels := []string{"设备", "设置", "选项", "输出", "状态", "检查", "提示"}
					column := 6
					if lang == english {
						labels = []string{"Scanner", "Settings", "Options", "Output", "Status", "Checked", "Notice"}
						column = 10
					}
					for _, state := range []string{"ready", "unavailable"} {
						m.state = state
						view := m.View().Content
						assertTUIFieldColumns(t, view, labels, column, width-2)
						if hasTerminalColor(view) != colored {
							t.Fatal("layout changed color behavior")
						}
						for _, value := range []string{m.request.output, m.status, m.notice} {
							if !strings.Contains(compactTUIText(view), compactTUIText(value)) {
								t.Fatalf("wrapped field lost content: %q", value)
							}
						}
						for _, label := range []string{"连续", "独立 PDF", "纠偏", "裁切"} {
							if !strings.Contains(ansi.Strip(view), "[x] "+lang.text(label)) {
								t.Fatalf("checkbox and label split across lines: %q", label)
							}
						}
						footer := lang.text("r 重新检查 · d 更换设备 · s 设置 · q 退出")
						if state == "ready" {
							footer = lang.text("Enter 开始扫描 · ") + footer
						}
						for _, item := range strings.Split(footer, " · ") {
							if !strings.Contains(ansi.Strip(view), item) {
								t.Fatalf("shortcut and description split across lines: %q", item)
							}
						}
					}
				})
			}
		}
	}
}

func TestHumanStatusFieldAlignment(t *testing.T) {
	m := humanTestModel(t)
	m.lang, m.request.language, m.width = english, english, 40
	m.latest = "/tmp/a-long-directory-name/another-directory/scan.pdf"
	m.status = "Waiting for the next page while keeping the previous pages safe"
	m.notice = "A notice that continues onto a second line"
	for _, state := range []string{"working", "waiting"} {
		m.state = state
		view := m.View().Content
		assertTUIFieldColumns(t, view, []string{"Scanner", "Status", "File", "Notice"}, 10, 38)
		for _, value := range []string{m.latest, m.status, m.notice} {
			if !strings.Contains(compactTUIText(view), compactTUIText(value)) {
				t.Fatalf("status field lost content: %q", value)
			}
		}
	}
}

func TestHumanHomeStacksInVeryNarrowWindows(t *testing.T) {
	m := humanTestModel(t)
	m.width = 20
	for _, lang := range []language{chinese, english} {
		m.lang, m.request.language = lang, lang
		view := ansi.Strip(m.View().Content)
		for _, label := range []string{lang.text("设备"), lang.text("设置"), lang.text("选项"), lang.text("输出"), lang.text("状态"), lang.text("检查")} {
			found := false
			lines := strings.Split(view, "\n")
			for i, line := range lines {
				if strings.TrimRight(line, " ") == label && i+1 < len(lines) && strings.HasPrefix(lines[i+1], "  ") {
					found = true
				}
			}
			if !found {
				t.Fatalf("narrow field %q must stack above its value: %q", label, view)
			}
		}
		for _, line := range strings.Split(view, "\n") {
			if !utf8.ValidString(line) || ansi.StringWidth(line) > 18 {
				t.Fatalf("narrow line exceeds available width: %q", line)
			}
		}
	}
}

func TestHumanHomeRelayoutAfterResize(t *testing.T) {
	m := humanTestModel(t)
	m.lang, m.request.language = english, english
	for _, width := range []int{120, 20, 40, 80} {
		m.Update(tea.WindowSizeMsg{Width: width, Height: 30})
		view := m.View().Content
		if width != 20 {
			assertTUIFieldColumns(t, view, []string{"Scanner", "Settings", "Options", "Output", "Status", "Checked"}, 10, width-2)
			continue
		}
		stacked := false
		lines := strings.Split(ansi.Strip(view), "\n")
		for i, line := range lines {
			if strings.TrimRight(line, " ") == "Settings" && i+1 < len(lines) && strings.HasPrefix(lines[i+1], "  ") {
				stacked = true
			}
		}
		if !stacked {
			t.Fatal("resize did not switch to stacked fields")
		}
	}
}

func TestHumanOtherPagesWrapSharedNotice(t *testing.T) {
	m := humanTestModel(t)
	m.lang, m.request.language, m.width = english, english, 40
	m.editDeviceForm()
	for _, state := range []string{"device-list", "devices", "settings", "done"} {
		if state == "settings" {
			m.editSettings()
		}
		m.state = state
		m.notice = "A shared notice that needs to continue onto another line without losing its indentation."
		view := m.View().Content
		assertTUIFieldColumns(t, view, []string{"Notice"}, 10, 38)
		if !strings.Contains(compactTUIText(view), compactTUIText(m.notice)) {
			t.Fatalf("%s lost its notice content", state)
		}
	}
}

func TestHumanHomeRelayoutAfterLanguageChange(t *testing.T) {
	m := humanTestModel(t)
	for _, lang := range []language{english, chinese, english} {
		m.lang, m.request.language = lang, lang
		labels, column := []string{"Scanner", "Settings", "Options", "Output", "Status", "Checked"}, 10
		if lang == chinese {
			labels, column = []string{"设备", "设置", "选项", "输出", "状态", "检查"}, 6
		}
		assertTUIFieldColumns(t, m.View().Content, labels, column, 78)
	}
}
