package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"
)

func TestHelpContentAndLayout(t *testing.T) {
	for _, command := range []string{"", "scan", "info", "discover"} {
		for _, width := range []int{24, 40, 60, 71, 72, 80, 88, 140} {
			plain, colored := renderHelp(command, width, false), renderHelp(command, width, true)
			if strings.ContainsRune(plain, '\x1b') || ansi.Strip(colored) != plain || !hasTerminalColor(colored) {
				t.Fatalf("style changed content: %s/%d", command, width)
			}
			for _, line := range strings.Split(colored, "\n") {
				if ansi.StringWidth(line) > min(width, 88) || strings.TrimRight(line, " ") != line || !utf8.ValidString(line) {
					t.Fatalf("invalid line %s/%d: %q", command, width, line)
				}
			}
			if !strings.HasSuffix(plain, "\n") || strings.HasSuffix(plain, "\n\n") {
				t.Fatal("help must end with one newline")
			}
		}
	}
	root := renderHelp("", 80, false)
	if strings.Contains(root, "--dpi") || strings.Contains(root, "--single-page") || !strings.Contains(root, "goscanner <命令> --help") || len(strings.Split(root, "\n")) > 30 {
		t.Fatal("root help should be a short navigation page", root)
	}
	want := map[string][]string{
		"scan":     {"设备", "扫描", "输出", "连续扫描", "图像处理", "超时", "兼容参数", "--host IP", "--port PORT", "--path PATH", "--endpoint URL", "--dpi DPI", "--mode MODE", "--area AREA", "-o FILE", "--single-page", "--manual[=BOOL]", "--deskew", "--autocrop", "--timeout DURATION", "-h, --help", "默认：2m0s。"},
		"info":     {"设备", "超时", "兼容参数", "--host IP", "--port PORT", "--path PATH", "--endpoint URL", "--timeout DURATION", "-h, --help", "默认：2m。"},
		"discover": {"超时", "--timeout DURATION", "-h, --help", "默认：10s。"},
	}
	for command, entries := range want {
		help := renderHelp(command, 80, false)
		for _, entry := range entries {
			if !strings.Contains(help, entry) {
				t.Fatalf("%s help missing %q", command, entry)
			}
		}
		if command != "scan" && strings.Contains(help, "--dpi") || command == "discover" && strings.Contains(help, "--host") {
			t.Fatalf("%s includes unsupported flags", command)
		}
	}
	wide := renderHelp("scan", 80, false)
	if !strings.Contains(wide, "\n                        选用或添加设备。") {
		t.Fatal("wrapped descriptions must use hanging indentation", wide)
	}
	narrow := renderHelp("scan", 40, false)
	if !strings.Contains(narrow, "\n  --port PORT\n    高级地址参数") || strings.Contains(narrow, "\n                        ") {
		t.Fatal("narrow terminal must use stacked options", narrow)
	}
}

func TestHelpUnicodeWrappingOffsets(t *testing.T) {
	for _, text := range []string{
		"扫描仪 IP 地址，不接受主机名或 URL。未配置时可在交互界面选用或添加设备。",
		"~/.config/goscanner/config.json",
		"默认：TUI 开启，文本模式关闭。",
		"中文👩‍💻与 English words 混排。\n下一段 --manual=false 关闭。",
	} {
		for _, width := range []int{8, 16, 20, 36, 56} {
			var joined strings.Builder
			for _, line := range wrapHelp(text, width) {
				if line.text != text[line.start:line.start+len(line.text)] || ansi.StringWidth(line.text) > width {
					t.Fatalf("invalid source offset or width: %d %+v", width, line)
				}
				joined.WriteString(line.text)
			}
			compact := func(s string) string { return strings.NewReplacer(" ", "", "\n", "").Replace(s) }
			if compact(joined.String()) != compact(text) {
				t.Fatalf("wrapping lost content: %q", joined.String())
			}
		}
	}
}

func TestHelpDoesNotReadConfigOrDiscover(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	path, _ := configPath()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("broken config"), 0600); err != nil {
		t.Fatal(err)
	}
	find := func(context.Context) ([]scannerDevice, []string, error) {
		t.Fatal("help must not discover devices")
		return nil, nil, nil
	}
	for _, args := range [][]string{{"--help"}, {"-h"}, {"help"}, {"scan", "--help"}, {"info", "-h"}, {"discover", "--help"}} {
		var out, stderr bytes.Buffer
		if code := runWithDiscovery(context.Background(), args, &out, &stderr, find); code != 0 || stderr.Len() != 0 || out.String() == "" || strings.ContainsRune(out.String(), '\x1b') {
			t.Fatalf("args=%v output=%q stderr=%q code=%d", args, out.String(), stderr.String(), code)
		}
		result, code := agentRun(t, context.Background(), args, find)
		if code != 0 || result.Status != "ok" || result.Help != out.String() {
			t.Fatalf("Agent help changed: %v %+v", args, result)
		}
	}
	registry, _ := drivesPath()
	if _, err := os.Stat(registry); !os.IsNotExist(err) {
		t.Fatal("help modified the registry", err)
	}
	var stderr bytes.Buffer
	if code := run(context.Background(), []string{"scan", "--help"}, failingWriter{}, &stderr); code != 1 || stderr.Len() == 0 {
		t.Fatal("human help output failures must remain observable")
	}
}
