package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDeviceAddress(t *testing.T) {
	writeTestConfig(t, `{"device":{"host":"192.0.2.8"}}`)
	cfg, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if endpoint, err := cfg.Device.endpoint(); err != nil || endpoint != "http://192.0.2.8:8018/wsd/scan" {
		t.Fatalf("host-only defaults: %q, %v", endpoint, err)
	}
	for _, raw := range []string{
		"http://192.0.2.8:8018/wsd/scan", "http://192.0.2.8:8080/vendor/scan",
		"http://192.0.2.8/", "http://[2001:db8::1]:8080/a%2Fb/%E4%B8%AD",
		"http://[fe80::1%25en0]:8080/wsd/scan",
	} {
		d, err := deviceFromEndpoint(raw)
		if err != nil {
			t.Fatal(err)
		}
		got, err := d.endpoint()
		want, _ := checkedEndpoint(raw, netip.Addr{})
		if err != nil || got != want {
			t.Fatalf("round trip %q: %+v => %q, %v", raw, d, got, err)
		}
	}
	for _, d := range []deviceConfig{
		{Host: "scanner.local", Port: 8018, Path: "/scan"},
		{Host: "http://192.0.2.8", Port: 8018, Path: "/scan"},
		{Host: "0.0.0.0", Port: 8018, Path: "/scan"},
		{Host: "192.0.2.8", Port: 0, Path: "/scan"},
		{Host: "192.0.2.8", Port: 65536, Path: "/scan"},
		{Host: "192.0.2.8", Port: 8018, Path: "scan"},
		{Host: "192.0.2.8", Port: 8018, Path: "/scan?q=1"},
		{Host: "192.0.2.8", Port: 8018, Path: "/scan#x"},
		{Host: "192.0.2.8", Port: 8018, Path: "/bad path"},
	} {
		if _, err := d.endpoint(); err == nil {
			t.Errorf("accepted invalid device: %+v", d)
		}
	}
}

func TestSaveDefaultDevice(t *testing.T) {
	d := deviceConfig{Host: "192.0.2.8", Port: 8080, Path: "/vendor/scan"}
	for _, original := range []string{"", `{"scan":{"dpi":600},"output":{"format":"pdf"}}`} {
		t.Run(original, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			if original != "" {
				writeTestConfig(t, original)
			}
			if err := saveDefaultDevice(d); err != nil {
				t.Fatal(err)
			}
			cfg, err := loadConfig()
			if err != nil || cfg.Device != d {
				t.Fatalf("saved device: %+v, %v", cfg, err)
			}
			path, _ := configPath()
			info, err := os.Stat(path)
			if err != nil || info.Mode().Perm() != 0600 {
				t.Fatalf("config permissions: %v, %v", info, err)
			}
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(data, &fields); err != nil {
				t.Fatal(err)
			}
			if original == "" {
				if len(fields) != 1 {
					t.Fatalf("unexpected settings added: %s", data)
				}
			} else if cfg.Scan.DPI != 600 || cfg.Output.Format != "pdf" || len(fields) != 3 {
				t.Fatalf("other settings changed: %s", data)
			}
			files, _ := filepath.Glob(filepath.Join(filepath.Dir(path), ".config-*.tmp"))
			if len(files) != 0 {
				t.Fatalf("temporary config leaked: %v", files)
			}
		})
	}
	for _, original := range []string{"broken JSON", `{"device":{"endpoint":"http://192.0.2.8/scan"}}`} {
		path := writeTestConfig(t, original)
		if err := saveDefaultDevice(d); err == nil {
			t.Fatal("overwrote invalid configuration")
		}
		data, _ := os.ReadFile(path)
		if string(data) != original {
			t.Fatal("invalid configuration was modified")
		}
	}
	path := writeTestConfig(t, "{}")
	target := path + ".target"
	if err := os.Rename(path, target); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	if err := saveDefaultDevice(d); err == nil {
		t.Fatal("replaced symlink")
	}
	if info, err := os.Lstat(path); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("config symlink changed", err)
	}
}

func TestSavedDeviceBypassesDiscovery(t *testing.T) {
	cap := fixture(t)
	c := testClient(t, func(w http.ResponseWriter, r *http.Request, action string, _ []byte) {
		if action != "GetScannerElements" {
			t.Errorf("info triggered %s", action)
		}
		_, _ = w.Write(cap)
	})
	cfg := defaultConfig()
	cfg.Device = deviceForTest(t, c.endpoint)
	path := writeConfigForTest(t, cfg)
	original, _ := os.ReadFile(path)
	var out, stderr bytes.Buffer
	code := runWithDiscovery(context.Background(), []string{"info"}, &out, &stderr, func(context.Context) ([]scannerDevice, []diagnostic, error) {
		t.Error("saved device triggered discovery")
		return nil, nil, errors.New("discovery must not run")
	})
	if code != 0 {
		t.Fatalf("saved device exit=%d: %s", code, &stderr)
	}
	data, _ := os.ReadFile(path)
	if !bytes.Equal(data, original) {
		t.Fatal("using a saved device rewrote configuration")
	}
}

func TestDiscoverDoesNotSelectOrSave(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	var out, stderr bytes.Buffer
	code := runWithDiscovery(context.Background(), []string{"discover"}, &out, &stderr, func(context.Context) ([]scannerDevice, []diagnostic, error) {
		return []scannerDevice{{Name: "Only scanner", Endpoint: "http://127.0.0.1:8018/wsd/scan"}}, nil, nil
	})
	var devices []scannerDevice
	if err := json.Unmarshal(out.Bytes(), &devices); code != 0 || err != nil || len(devices) != 1 {
		t.Fatalf("discover exit=%d, JSON error=%v, stderr=%s", code, err, &stderr)
	}
	path, _ := configPath()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("discover wrote config: %v", err)
	}
}

func TestReadDeviceChoice(t *testing.T) {
	devices := []scannerDevice{{Endpoint: "http://192.0.2.1/scan"}, {Endpoint: "http://192.0.2.2/scan"}}
	for _, tc := range []struct{ input, want string }{
		{"1\n", devices[0].Endpoint}, {"0\n99\n2\n", devices[1].Endpoint},
		{"\n1\n", devices[0].Endpoint}, {"q\n", ""}, {"\n", ""}, {"", ""},
	} {
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.WriteString(w, tc.input)
		w.Close()
		var prompt bytes.Buffer
		got, err := readDeviceChoice(context.Background(), devices, r, &prompt)
		r.Close()
		if got != tc.want || (err != nil) != (tc.want == "") {
			t.Fatalf("input %q => %q, %v", tc.input, got, err)
		}
	}
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := readDeviceChoice(ctx, devices, r, io.Discard); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation: %v", err)
	}
	if _, err := chooseDevice(context.Background(), nil, io.Discard); err == nil || !strings.Contains(err.Error(), "未发现") {
		t.Fatalf("missing-device error: %v", err)
	}
}
