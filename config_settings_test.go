package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSaveHumanSettings(t *testing.T) {
	path := writeTestConfig(t, `{"language":"en","device":{"host":"192.0.2.8","port":8080,"path":"/custom"},"scan":{"timeout":"9m"}}`)
	output := filepath.Join(t.TempDir(), "中文目录", "已有文件.PDF")
	if err := os.MkdirAll(filepath.Dir(output), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(output, []byte("keep this file"), 0600); err != nil {
		t.Fatal(err)
	}
	r := commandRequest{area: "full", output: output, manual: true, singlePage: true, opts: defaultOptions()}
	r.opts.DPI, r.opts.Mode, r.opts.Deskew, r.opts.Autocrop = 300, "gray", true, true
	if err := saveHumanSettings(r, "zh-CN"); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadConfig()
	if err != nil || cfg.Language != "zh-CN" || cfg.Scan.DPI != 300 || cfg.Scan.Mode != "gray" || cfg.Scan.Area != "full" || cfg.Scan.Manual == nil || !*cfg.Scan.Manual || !cfg.Scan.SinglePage || !cfg.Processing.Deskew || !cfg.Processing.Autocrop {
		t.Fatalf("settings not saved: %+v err=%v", cfg, err)
	}
	if cfg.Device.Host != "192.0.2.8" || cfg.Device.Port != 8080 || cfg.Device.Path != "/custom" || cfg.Scan.Timeout != "9m" {
		t.Fatal("unrelated configuration was replaced", cfg)
	}
	if cfg.Output.Directory != filepath.Dir(output) || cfg.Output.Format != "pdf" {
		t.Fatal("output preference was not saved", cfg.Output)
	}
	next, _, failure, code := parseRequest([]string{"scan", "--manual=false"}, true, english)
	if code != 0 || failure != nil || next.manual || !next.singlePage || next.opts.DPI != 300 || next.opts.Mode != "gray" || next.output == output || filepath.Dir(next.output) != filepath.Dir(output) {
		t.Fatalf("restart did not load settings: %+v failure=%+v code=%d", next, failure, code)
	}
	if data, err := os.ReadFile(output); err != nil || string(data) != "keep this file" {
		t.Fatal("saving settings changed the output file", string(data), err)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("configuration permissions", info, err)
	}
	if files, _ := filepath.Glob(filepath.Join(filepath.Dir(path), ".config-*.tmp")); len(files) != 0 {
		t.Fatal("temporary configuration leaked", files)
	}
	for _, ext := range []string{".JPEG", ".TIF"} {
		r.output, r.singlePage = "next"+ext, false
		if err := saveHumanSettings(r, "auto"); err != nil {
			t.Fatal(err)
		}
		cfg, err := loadConfig()
		want := "jpg"
		if ext == ".TIF" {
			want = "tiff"
		}
		if err != nil || cfg.Output.Format != want {
			t.Fatal("format alias was not normalized", cfg.Output, err)
		}
	}
}

func TestSavedManualPreferenceAndAgentSafety(t *testing.T) {
	for _, value := range []string{"{}", `{"scan":{"manual":false}}`, `{"scan":{"manual":true}}`} {
		writeTestConfig(t, value)
		for _, humanDefault := range []bool{false, true} {
			r, _, failure, code := parseRequest([]string{"scan"}, humanDefault, english)
			want := humanDefault
			if strings.Contains(value, "false") {
				want = false
			} else if strings.Contains(value, "true") {
				want = true
			}
			if code != 0 || failure != nil || r.manual != want {
				t.Fatalf("default=%t config=%s request=%+v failure=%+v", humanDefault, value, r, failure)
			}
		}
	}
	result, code := agentRun(t, context.Background(), []string{"scan"}, nil)
	if code != 2 || result.Error == nil || result.Error.Code != "unsupported_interaction" {
		t.Fatal("Agent accepted saved interactive mode", code, result)
	}
	result, code = agentRun(t, context.Background(), []string{"scan", "--manual=false"}, nil)
	if code != 1 || result.Error == nil || result.Error.Code != "device_required" {
		t.Fatal("Agent could not override saved interactive mode", code, result)
	}
}

func TestSaveHumanSettingsRejectsInvalidOrLinkedConfig(t *testing.T) {
	r := commandRequest{area: "a4", output: "scan.pdf", opts: defaultOptions()}
	for _, original := range []string{"broken JSON", `{"unknown":true}`, `{"scan":{"manual":"false"}}`} {
		path := writeTestConfig(t, original)
		if err := saveHumanSettings(r, "en"); err == nil {
			t.Fatal("overwrote invalid configuration")
		}
		if data, _ := os.ReadFile(path); string(data) != original {
			t.Fatal("invalid configuration changed", string(data))
		}
	}
	path := writeTestConfig(t, `{}`)
	target := path + ".target"
	if err := os.Rename(path, target); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	if err := saveHumanSettings(r, "en"); err == nil {
		t.Fatal("replaced a configuration symlink")
	}
	if info, err := os.Lstat(path); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("configuration link changed", info, err)
	}
	if data, _ := os.ReadFile(target); string(data) != `{}` {
		t.Fatal("configuration target changed", string(data))
	}
	path = writeTestConfig(t, `{}`)
	r.opts.DPI = 0
	if err := saveHumanSettings(r, "en"); err == nil {
		t.Fatal("saved invalid scan parameters")
	}
	r.opts.DPI = 150
	if err := saveHumanSettings(r, "fr"); err == nil {
		t.Fatal("saved unsupported language")
	}
	if data, _ := os.ReadFile(path); string(data) != `{}` {
		t.Fatal("invalid draft changed configuration", string(data))
	}
}
