package main

import (
	"bytes"
	"context"
	"image/color"
	"math"
	"os"
	"path/filepath"
	"slices"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
)

func TestLeadingTheme(t *testing.T) {
	for _, tc := range []struct {
		args, want []string
		theme      string
		invalid    bool
	}{
		{[]string{"scan", "--theme", "light"}, []string{"scan", "--theme", "light"}, "", false},
		{[]string{"--theme", "dark", "scan"}, []string{"scan"}, "dark", false},
		{[]string{"--lang", "en", "--theme=light", "scan"}, []string{"--lang", "en", "scan"}, "light", false},
		{[]string{"--theme=dark", "--lang=zh-CN", "--theme", "auto"}, []string{"--lang=zh-CN"}, "auto", false},
		{args: []string{"--theme"}, invalid: true},
		{args: []string{"--theme="}, invalid: true},
		{args: []string{"--lang", "en", "--theme="}, invalid: true},
		{args: []string{"--theme=invalid"}, invalid: true},
	} {
		args, theme, err := leadingTheme(tc.args)
		if (err != nil) != tc.invalid || !tc.invalid && (!slices.Equal(args, tc.want) || theme != tc.theme) {
			t.Fatalf("%v: args=%v theme=%q err=%v", tc.args, args, theme, err)
		}
	}
	for _, args := range [][]string{{"--agent", "--theme=invalid", "info"}, {"--agent", "info", "--theme=invalid"}} {
		var out, diagnostics bytes.Buffer
		if code := run(context.Background(), args, &out, &diagnostics); code != 2 {
			t.Fatalf("invalid theme accepted: %v %s %s", args, out.String(), diagnostics.String())
		}
	}
}

func TestThemeConfigurationPriority(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	path, err := configPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		config, option, want string
	}{
		{`{}`, "", "auto"},
		{`{"theme":"light"}`, "", "light"},
		{`{"theme":"dark"}`, "", "dark"},
		{`{"theme":"light"}`, "dark", "dark"},
		{`{"theme":"dark"}`, "auto", "auto"},
	} {
		if err := os.WriteFile(path, []byte(tc.config), 0600); err != nil {
			t.Fatal(err)
		}
		args := []string{"info", "--host", "127.0.0.1"}
		if tc.option != "" {
			args = append(args, "--theme", tc.option)
		}
		r, _, failure, code := parseRequest(args, false, english)
		if code != 0 || failure != nil || r.theme != tc.want || r.themeExplicit != (tc.option != "") {
			t.Fatalf("%+v: request=%+v failure=%v code=%d", tc, r, failure, code)
		}
		m := &humanModel{request: r}
		m.applyTheme()
		m.Update(tea.BackgroundColorMsg{Color: color.White})
		wantDark := tc.want == "dark"
		if m.styles.accent.Render("x") != newTUIStyles(wantDark).accent.Render("x") {
			t.Fatalf("automatic detection overrode an explicit choice: %+v", tc)
		}
	}
	for _, data := range []string{`{"theme":"invalid"}`, `{"theme":""}`, `{"theme":true}`} {
		if _, err := decodeConfig([]byte(data), path); err == nil {
			t.Fatalf("invalid theme configuration accepted: %s", data)
		}
	}
}

func TestThemeDetectionAndNotifications(t *testing.T) {
	m := &humanModel{}
	m.applyTheme()
	if m.styles.accent.Render("x") != newTUIStyles(true).accent.Render("x") || m.backgroundKnown {
		t.Fatal("unknown background must immediately use the dark fallback")
	}
	m.Update(tea.BackgroundColorMsg{})
	if m.backgroundKnown {
		t.Fatal("nil background is not a reliable detection")
	}
	for _, c := range []color.Color{color.White, color.Black, color.White} {
		m.Update(tea.BackgroundColorMsg{Color: c})
		want := newTUIStyles((tea.BackgroundColorMsg{Color: c}).IsDark())
		if !m.backgroundKnown || m.styles.accent.Render("x") != want.accent.Render("x") || m.input.Styles().Focused.Text.Render("x") != want.text.Render("x") {
			t.Fatal("background update did not refresh the UI and input styles")
		}
	}
	before := m.styles.accent.Render("x")
	for _, event := range []tea.Msg{tea.FocusMsg{}, uv.DarkColorSchemeEvent{}, uv.LightColorSchemeEvent{}} {
		_, cmd := m.Update(event)
		if cmd == nil || m.styles.accent.Render("x") != before {
			t.Fatal("focus/scheme change must query the actual terminal background")
		}
	}
	report := tea.ModeReportMsg{Mode: ansi.ModeLightDark, Value: ansi.ModeReset}
	_, cmd := m.Update(report)
	if cmd == nil || !m.restoreThemeNotifications {
		t.Fatal("supported, disabled notifications must be enabled and marked for restoration")
	}
	if _, cmd := m.Update(report); cmd != nil {
		t.Fatal("duplicate mode reports must not enable notifications twice")
	}
	m.request.theme = "dark"
	m.applyTheme()
	m.Update(tea.BackgroundColorMsg{Color: color.White})
	if m.styles.accent.Render("x") != newTUIStyles(true).accent.Render("x") || m.queryTheme() != nil {
		t.Fatal("explicit themes must not follow terminal changes")
	}
}

func TestThemeNotificationModePreservation(t *testing.T) {
	for _, value := range []ansi.ModeSetting{ansi.ModeNotRecognized, ansi.ModeSet, ansi.ModePermanentlySet, ansi.ModePermanentlyReset} {
		m := &humanModel{}
		_, cmd := m.Update(tea.ModeReportMsg{Mode: ansi.ModeLightDark, Value: value})
		if cmd != nil || m.restoreThemeNotifications {
			t.Fatalf("must preserve unsupported or previously enabled notification mode: %v", value)
		}
	}
}

func TestThemeSettingsPreserveStartupPriority(t *testing.T) {
	m := humanTestModel(t)
	m.request.theme, m.request.themeExplicit = "light", true
	m.applyTheme()
	m.editSettings()
	chooseSetting(t, m, settingTheme, "深色背景")
	m.field = settingApply
	m.openSetting()
	m.Update(m.listen())
	cfg, err := loadConfig()
	if err != nil || cfg.Theme != "dark" || m.request.theme != "light" || m.styles.accent.Render("x") != newTUIStyles(false).accent.Render("x") {
		t.Fatalf("saved preference must not override startup theme: cfg=%+v request=%+v err=%v", cfg, m.request, err)
	}
	m.request.themeExplicit = false
	m.editSettings()
	chooseSetting(t, m, settingTheme, "自动")
	m.field = settingApply
	if m.openSetting() == nil {
		t.Fatal("switching to automatic must request the terminal background")
	}
	m.Update(m.listen())
	m.Update(tea.BackgroundColorMsg{Color: color.White})
	if !m.autoTheme() || m.styles.accent.Render("x") != newTUIStyles(false).accent.Render("x") {
		t.Fatal("automatic preference did not take effect")
	}
}

func TestThemeForegroundContrast(t *testing.T) {
	luminance := func(c color.Color) float64 {
		r, g, b, _ := c.RGBA()
		linear := func(v uint32) float64 {
			x := float64(v) / 65535
			if x <= 0.04045 {
				return x / 12.92
			}
			return math.Pow((x+0.055)/1.055, 2.4)
		}
		return 0.2126*linear(r) + 0.7152*linear(g) + 0.0722*linear(b)
	}
	for _, dark := range []bool{false, true} {
		s := newTUIStyles(dark)
		backgrounds := []color.Color{color.White, color.RGBA{247, 248, 250, 255}}
		if dark {
			backgrounds = []color.Color{color.RGBA{30, 30, 30, 255}, color.RGBA{40, 44, 52, 255}}
		}
		for _, style := range []lipgloss.Style{s.text, s.muted, s.accent, s.value, s.success, s.warning, s.failure} {
			if _, ok := style.GetBackground().(lipgloss.NoColor); !ok {
				t.Fatal("themes must not set a background")
			}
			for _, bg := range backgrounds {
				a, b := luminance(style.GetForeground()), luminance(bg)
				contrast := (max(a, b) + 0.05) / (min(a, b) + 0.05)
				if contrast < 4.5 {
					t.Fatalf("dark=%t foreground=%v background=%v contrast=%.2f", dark, style.GetForeground(), bg, contrast)
				}
			}
		}
	}
}
