package main

import (
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

type tuiFieldLayout struct {
	labelWidth, valueWidth int
	stacked                bool
}

func newTUIFieldLayout(width int, labels ...string) tuiFieldLayout {
	labelWidth := 0
	for _, label := range labels {
		labelWidth = max(labelWidth, ansi.StringWidth(label))
	}
	valueWidth := width - labelWidth - 2
	if valueWidth < 20 {
		return tuiFieldLayout{labelWidth, max(1, width-2), true}
	}
	return tuiFieldLayout{labelWidth, valueWidth, false}
}

func (l tuiFieldLayout) row(label, value string) string {
	prefix := lipgloss.NewStyle().Width(l.labelWidth).Render(label) + "  "
	if l.stacked {
		prefix = "  "
	}
	value = lipgloss.NewStyle().Width(l.valueWidth).Render(value)
	row := lipgloss.JoinHorizontal(lipgloss.Top, prefix, value)
	if l.stacked {
		return label + "\n" + row
	}
	return row
}

func wrapTUIItems(items []string, width int) string {
	var lines []string
	line := ""
	for _, item := range items {
		if line != "" && ansi.StringWidth(line+" · "+item) > width {
			lines, line = append(lines, line), ""
		}
		if line != "" {
			line += " · "
		}
		line += item
	}
	if line != "" {
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}
