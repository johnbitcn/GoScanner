package main

import (
	"strings"
	"time"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
)

func validateTheme(value string) error {
	switch value {
	case "auto", "light", "dark":
		return nil
	default:
		return diagnosticf("--theme / theme must be auto, light, or dark.", "--theme / theme 必须是 auto、light 或 dark")
	}
}

// Leave language options in place for the existing language parser.
func leadingTheme(args []string) ([]string, string, error) {
	var kept []string
	theme := ""
	for len(args) > 0 {
		switch {
		case args[0] == "--theme":
			if len(args) < 2 {
				return kept, theme, validateTheme("")
			}
			theme, args = args[1], args[2:]
			if err := validateTheme(theme); err != nil {
				return append(kept, args...), theme, err
			}
		case strings.HasPrefix(args[0], "--theme="):
			theme, args = strings.TrimPrefix(args[0], "--theme="), args[1:]
			if err := validateTheme(theme); err != nil {
				return append(kept, args...), theme, err
			}
		case args[0] == "--lang" && len(args) > 1:
			kept, args = append(kept, args[:2]...), args[2:]
		case strings.HasPrefix(args[0], "--lang="):
			kept, args = append(kept, args[0]), args[1:]
		default:
			return append(kept, args...), theme, nil
		}
	}
	return kept, theme, nil
}

type tuiStyles struct {
	text, muted, accent, value, success, warning, failure lipgloss.Style
}

func newTUIStyles(dark bool) tuiStyles {
	colors := []string{"#263445", "#586779", "#2457B2", "#006B68", "#2D6F36", "#8A5300", "#B4233C"}
	if dark {
		colors = []string{"#D8DEE9", "#9AA8BB", "#82AAFF", "#7DD3C7", "#A3D98B", "#F2C879", "#FF8A95"}
	}
	style := func(i int) lipgloss.Style { return lipgloss.NewStyle().Foreground(lipgloss.Color(colors[i])) }
	return tuiStyles{style(0), style(1), style(2).Bold(true), style(3), style(4), style(5), style(6)}
}

func (s tuiStyles) inputStyles() textinput.Styles {
	return textinput.Styles{
		Focused: textinput.StyleState{Text: s.text, Prompt: s.accent, Placeholder: s.muted, Suggestion: s.muted},
		Blurred: textinput.StyleState{Text: s.text, Prompt: s.text, Placeholder: s.muted, Suggestion: s.muted},
		Cursor:  textinput.CursorStyle{Color: s.accent.GetForeground(), Blink: true},
	}
}

func (m *humanModel) themeLabel(preference string) string {
	switch preference {
	case "light":
		return m.lang.text("浅色背景")
	case "dark":
		return m.lang.text("深色背景")
	default:
		return m.lang.text("自动")
	}
}

func (m *humanModel) autoTheme() bool { return m.request.theme == "" || m.request.theme == "auto" }

func (m *humanModel) applyTheme() {
	dark := m.request.theme != "light"
	if m.autoTheme() && m.backgroundKnown {
		dark = m.backgroundDark
	}
	m.styles = newTUIStyles(dark)
	m.input.SetStyles(m.styles.inputStyles())
	for i := range m.deviceInputs {
		m.deviceInputs[i].SetStyles(m.styles.inputStyles())
	}
}

func (m *humanModel) queryTheme() tea.Cmd {
	if !m.autoTheme() {
		return nil
	}
	return tea.Batch(tea.RequestBackgroundColor, tea.Raw(ansi.RequestModeLightDark))
}

type themeTickMsg struct{}

func themeTick() tea.Cmd {
	return tea.Tick(time.Second, func(time.Time) tea.Msg { return themeTickMsg{} })
}

func (m *humanModel) updateTheme(msg tea.Msg) (bool, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.BackgroundColorMsg:
		if m.autoTheme() && msg.Color != nil {
			m.backgroundKnown, m.backgroundDark = true, msg.IsDark()
			m.applyTheme()
		}
	case themeTickMsg:
		cmd := themeTick()
		// Poll only after a successful reply; unsupported terminals stay on dark.
		if m.autoTheme() && m.backgroundKnown {
			cmd = tea.Batch(cmd, tea.RequestBackgroundColor)
		}
		return true, cmd
	case tea.FocusMsg, uv.DarkColorSchemeEvent, uv.LightColorSchemeEvent:
		if m.autoTheme() {
			// Scheme notifications may describe the OS, so query the terminal itself.
			return true, tea.RequestBackgroundColor
		}
	case tea.ModeReportMsg:
		if m.autoTheme() && msg.Mode == ansi.ModeLightDark && msg.Value == ansi.ModeReset && !m.restoreThemeNotifications {
			m.restoreThemeNotifications = true
			return true, tea.Raw(ansi.SetModeLightDark)
		}
	default:
		return false, nil
	}
	return true, nil
}
