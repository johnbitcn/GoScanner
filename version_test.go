package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestApplicationVersionMatchesPackage(t *testing.T) {
	data, err := os.ReadFile("package.json")
	if err != nil {
		t.Fatal(err)
	}
	var metadata struct{ Version string }
	if err := json.Unmarshal(data, &metadata); err != nil {
		t.Fatal(err)
	}
	version, err := applicationVersion()
	if err != nil || version == "" || version != metadata.Version {
		t.Fatalf("embedded version=%q package version=%q err=%v", version, metadata.Version, err)
	}
}

func TestVersionFlagsDoNotAccessConfigurationOrDevices(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("GOSCANNER_MODEL_PATH", "/missing/model.onnx")
	t.Setenv("GOSCANNER_ONNXRUNTIME_PATH", "/missing/runtime")
	path, err := configPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	broken := []byte("{broken configuration")
	if err := os.WriteFile(path, broken, 0600); err != nil {
		t.Fatal(err)
	}
	find := func(context.Context) ([]scannerDevice, []diagnostic, error) {
		t.Fatal("version must not discover devices")
		return nil, nil, nil
	}
	version, err := applicationVersion()
	if err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"--version"}, {"-v"},
		{"--lang", "zh-CN", "--version"}, {"--theme", "light", "--lang=en", "-v"},
		{"--agent", "--version"}, {"--agent", "-v"},
		{"--agent", "--theme=auto", "--version"},
	} {
		var out, stderr bytes.Buffer
		if code := runWithDiscovery(context.Background(), args, &out, &stderr, find); code != 0 || stderr.Len() != 0 {
			t.Fatalf("%v failed: code=%d stdout=%q stderr=%q", args, code, out.String(), stderr.String())
		}
		if args[0] == "--agent" {
			var result commandResult
			if err := json.Unmarshal(out.Bytes(), &result); err != nil || result.Status != "ok" || result.Version != version || result.Help != "" {
				t.Fatalf("Agent version must be JSON: %q err=%v", out.String(), err)
			}
		} else if out.String() != version+"\n" {
			t.Fatalf("unexpected version output: %q", out.String())
		}
	}
	if data, err := os.ReadFile(path); err != nil || !bytes.Equal(data, broken) {
		t.Fatal("version changed user configuration", err)
	}
	for _, args := range [][]string{{"--version"}, {"--agent", "-v"}} {
		var stderr bytes.Buffer
		if code := runWithDiscovery(context.Background(), args, failingWriter{}, &stderr, find); code != 1 || stderr.Len() == 0 {
			t.Fatal("version output failure was not reported", code, stderr.String())
		}
	}
	for _, help := range []string{renderHelp("", 80, false, english), renderAgentHelp("")} {
		if !strings.Contains(help, "-v, --version") {
			t.Fatal("root help does not advertise version flags")
		}
	}
}

func TestVersionRejectsInvalidEmbeddedMetadata(t *testing.T) {
	for _, data := range []string{`{`, `{}`, `{"version":123}`} {
		t.Run(data, func(t *testing.T) {
			original := packageMetadata
			packageMetadata = []byte(data)
			t.Cleanup(func() { packageMetadata = original })
			for _, args := range [][]string{{"--version"}, {"--agent", "-v"}} {
				var out, stderr bytes.Buffer
				if code := run(context.Background(), args, &out, &stderr); code != 1 {
					t.Fatalf("invalid metadata accepted: %q", data)
				}
				if args[0] == "--agent" {
					var result commandResult
					if err := json.Unmarshal(out.Bytes(), &result); err != nil || result.Error == nil || result.Error.Code != "version_unavailable" || result.Version != "" {
						t.Fatalf("invalid metadata must produce an Agent error: %q %v", out.String(), err)
					}
				} else if out.Len() != 0 || stderr.Len() == 0 {
					t.Fatal("invalid metadata must not print a version")
				}
			}
		})
	}
}
