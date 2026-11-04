package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func writeTestConfig(t *testing.T, data string) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	path := filepath.Join(home, ".config", "goscanner", "config.json")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func deviceForTest(t *testing.T, endpoint string) deviceConfig {
	t.Helper()
	d, err := deviceFromEndpoint(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func writeConfigForTest(t *testing.T, cfg config) string {
	t.Helper()
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return writeTestConfig(t, string(data))
}

func TestLoadConfig(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if c, err := loadConfig(); err != nil || c != defaultConfig() || c.Scan.DPI != 150 || c.Scan.Timeout != "2m0s" || c.Output.Directory != "." {
		t.Fatalf("missing config: %+v, %v", c, err)
	}
	for _, data := range []string{"", "null", "[]", "{", `{"scan":{"dpi":"150"}}`, `{"scan":{"dppi":150}}`, `{"unknown":true}`, `{} {}`, `{} junk`} {
		t.Run(data, func(t *testing.T) {
			path := writeTestConfig(t, data)
			if _, err := loadConfig(); err == nil || !strings.Contains(err.Error(), path) {
				t.Fatalf("expected error with config path, got %v", err)
			}
		})
	}
	writeTestConfig(t, `{"scan":{"dpi":300},"processing":{"deskew":true}}`)
	c, err := loadConfig()
	want := defaultConfig()
	want.Scan.DPI, want.Processing.Deskew = 300, true
	if err != nil || c != want {
		t.Fatalf("partial config: %+v, %v", c, err)
	}
	example, err := os.ReadFile("config.example.json")
	if err != nil {
		t.Fatal(err)
	}
	writeTestConfig(t, string(example))
	if c, err := loadConfig(); err != nil || c != defaultConfig() {
		t.Fatalf("example differs from built-in defaults: %+v, %v", c, err)
	}
}

func TestConfigApply(t *testing.T) {
	for _, command := range []string{"scan", "info", "discover"} {
		t.Run(command, func(t *testing.T) {
			f := flag.NewFlagSet(command, flag.ContinueOnError)
			f.Duration("timeout", time.Second, "")
			if command != "discover" {
				f.String("host", "", "")
				f.String("endpoint", "", "")
				f.Int("port", 8018, "")
				f.String("path", "/wsd/scan", "")
			}
			if command == "scan" {
				f.Int("dpi", 150, "")
				f.String("mode", "color", "")
				f.String("area", "a4", "")
				f.Bool("deskew", false, "")
				f.Bool("autocrop", false, "")
				if err := f.Parse([]string{"--dpi=600", "--deskew=false", "--autocrop=false", "--host=127.0.0.1"}); err != nil {
					t.Fatal(err)
				}
			}
			c := defaultConfig()
			c.Device = deviceConfig{Host: "127.0.0.2", Port: 8000, Path: "/vendor/scan"}
			c.Scan.DPI, c.Scan.Mode, c.Scan.Area, c.Scan.Timeout = 300, "gray", "full", "5s"
			c.Processing.Deskew, c.Processing.Autocrop = true, true
			if err := c.apply(f, command); err != nil {
				t.Fatal(err)
			}
			want := map[string]string{"timeout": "1s"}
			switch command {
			case "scan":
				want = map[string]string{"timeout": "5s", "dpi": "600", "mode": "gray", "area": "full", "deskew": "false", "autocrop": "false", "host": "127.0.0.1", "port": "8000", "path": "/vendor/scan", "endpoint": ""}
			case "info":
				want["host"], want["port"], want["path"], want["endpoint"] = "127.0.0.2", "8000", "/vendor/scan", ""
			}
			for name, value := range want {
				if got := f.Lookup(name).Value.String(); got != value {
					t.Errorf("%s=%q, want %q", name, got, value)
				}
			}
		})
	}
}

func TestConfigOutputPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	c := defaultConfig()
	for _, tc := range []struct{ dir, format, wantDir string }{
		{".", "jpg", "."}, {"relative/scans", "png", filepath.Join("relative", "scans")},
		{"~", "tiff", home}, {"~/scans", "pdf", filepath.Join(home, "scans")},
		{home, "jpg", home},
	} {
		c.Output.Directory, c.Output.Format = tc.dir, tc.format
		path, err := c.outputPath()
		if err != nil || filepath.Dir(path) != tc.wantDir || filepath.Ext(path) != "."+tc.format || !strings.HasPrefix(filepath.Base(path), "scan-") {
			t.Fatalf("output for %+v: %q, %v", tc, path, err)
		}
		stamp := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(path), "scan-"), "."+tc.format)
		if _, err := time.Parse("20060102-150405", stamp); err != nil || len(stamp) != 15 {
			t.Fatalf("timestamp must use second precision: %q, %v", stamp, err)
		}
	}
	for _, tc := range []struct{ dir, format string }{{"", "jpg"}, {"~other/scans", "jpg"}, {".", "gif"}} {
		c.Output.Directory, c.Output.Format = tc.dir, tc.format
		if _, err := c.outputPath(); err == nil {
			t.Fatalf("accepted invalid output: %+v", tc)
		}
	}
}

func TestCLIConfigScan(t *testing.T) {
	cap, image := fixture(t), jpegSample(t)
	for _, scenario := range []string{"defaults", "host", "configured", "override"} {
		t.Run(scenario, func(t *testing.T) {
			dpi, color, width, height := "150", "RGB24", "8268", "11693"
			if scenario == "configured" {
				dpi, color, width, height = "300", "Grayscale8", "8503", "11732"
			}
			c := testClient(t, func(w http.ResponseWriter, r *http.Request, action string, data []byte) {
				switch action {
				case "GetScannerElements":
					_, _ = w.Write(cap)
				case "CreateScanJob":
					for _, want := range []string{
						"<w:Resolution><w:Width>" + dpi + "</w:Width><w:Height>" + dpi + "</w:Height>",
						"<w:ColorProcessing>" + color + "</w:ColorProcessing>",
						"<w:ScanRegionWidth>" + width + "</w:ScanRegionWidth><w:ScanRegionHeight>" + height + "</w:ScanRegionHeight>",
					} {
						if !bytes.Contains(data, []byte(want)) {
							t.Errorf("missing ticket setting %s", want)
						}
					}
					io.WriteString(w, responseXML("CreateScanJobResponse", "<w:JobId>7</w:JobId><w:JobToken>dummy</w:JobToken>"))
				case "RetrieveImage":
					ct, body := mtom(image, "boundary", "cid:image@scanner")
					w.Header().Set("Content-Type", ct)
					_, _ = w.Write(body)
				default:
					t.Errorf("unexpected action %s", action)
				}
			})
			args := []string{"scan", "--endpoint", c.endpoint}
			if scenario == "host" {
				d := deviceForTest(t, c.endpoint)
				args = []string{"scan", "--host", d.Host, "--port", strconv.Itoa(d.Port), "--path", d.Path}
			}
			if scenario != "defaults" && scenario != "host" {
				cfg := defaultConfig()
				cfg.Device = deviceForTest(t, c.endpoint)
				cfg.Scan.DPI, cfg.Scan.Mode, cfg.Scan.Area = 300, "gray", "full"
				cfg.Output.Directory, cfg.Output.Format = "configured", "pdf"
				if scenario == "configured" {
					args = []string{"scan"}
				} else {
					cfg.Device = deviceConfig{Host: "invalid overridden host", Port: -1, Path: "invalid overridden path"}
					cfg.Scan.Timeout = "invalid overridden timeout"
					cfg.Output.Directory, cfg.Output.Format = "", "gif"
					cfg.Processing.Deskew, cfg.Processing.Autocrop = true, true
					args = append(args, "--dpi=150", "--mode=color", "--area=a4", "--timeout=30s", "--deskew=false", "--autocrop=false", "-o", "override.jpg")
				}
				data, err := json.Marshal(cfg)
				if err != nil {
					t.Fatal(err)
				}
				path := writeTestConfig(t, string(data))
				t.Cleanup(func() {
					unchanged, err := os.ReadFile(path)
					if err != nil || !bytes.Equal(data, unchanged) {
						t.Errorf("CLI changed config: %v", err)
					}
				})
			}
			wd := t.TempDir()
			t.Chdir(wd)
			var out, stderr bytes.Buffer
			if code := run(context.Background(), args, &out, &stderr); code != 0 {
				t.Fatalf("exit %d: %s", code, &stderr)
			}
			pattern := "scan-*.jpg"
			if scenario == "configured" {
				pattern = "configured/scan-*.pdf"
			} else if scenario == "override" {
				pattern = "override.jpg"
			}
			files, err := filepath.Glob(pattern)
			if err != nil || len(files) != 1 {
				t.Fatalf("output %q: %v, %v", pattern, files, err)
			}
			if scenario == "configured" {
				assertPDF(t, files[0], 1)
			} else {
				assertSavedImage(t, files[0], image)
			}
			if _, err := os.Stat("scans"); !os.IsNotExist(err) {
				t.Fatalf("unexpected legacy scans directory: %v", err)
			}
		})
	}
}

func TestCLIConfigValidation(t *testing.T) {
	for _, data := range []string{
		`{"scan":{"dpi":0}}`, `{"scan":{"mode":"bw"}}`, `{"scan":{"area":"bad"}}`,
		`{"scan":{"timeout":"bad"}}`, `{"scan":{"timeout":"0s"}}`, `{"scan":{"timeout":"-1s"}}`,
		`{"output":{"format":"gif"}}`, `{"output":{"directory":""}}`,
		`{"device":{"host":"scanner.local"}}`,
		`{"device":{"host":"127.0.0.1","port":0}}`,
		`{"device":{"host":"127.0.0.1","path":"scan"}}`,
	} {
		t.Run(data, func(t *testing.T) {
			writeTestConfig(t, data)
			var out, stderr bytes.Buffer
			args := []string{"scan", "--endpoint", "http://127.0.0.1:1/wsd/scan"}
			if strings.Contains(data, `"device"`) {
				args = []string{"scan"}
			}
			if code := run(context.Background(), args, &out, &stderr); code != 2 {
				t.Fatalf("expected configuration failure, exit=%d: %s", code, &stderr)
			}
		})
	}
	writeTestConfig(t, "invalid JSON")
	for _, args := range [][]string{{"--help"}, {"scan", "--help"}, {"info", "--help"}, {"discover", "--help"}} {
		var out, stderr bytes.Buffer
		if code := run(context.Background(), args, &out, &stderr); code != 0 {
			t.Fatalf("broken config prevents help: %v, %s", args, &stderr)
		}
	}
}

func TestCLIConfigTimeout(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request, action string, _ []byte) {
		if action != "GetScannerElements" {
			t.Errorf("timed-out query triggered %s", action)
		}
		select {
		case <-r.Context().Done():
		case <-time.After(2 * time.Second):
			http.Error(w, "query did not cancel", http.StatusGatewayTimeout)
		}
	})
	cfg := defaultConfig()
	cfg.Device, cfg.Scan.Timeout = deviceForTest(t, c.endpoint), "30ms"
	writeConfigForTest(t, cfg)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var out, stderr bytes.Buffer
	if code := run(ctx, []string{"scan"}, &out, &stderr); code != 1 || !strings.Contains(stderr.String(), "context deadline exceeded") {
		t.Fatalf("configured timeout did not reach HTTP request: exit=%d, %s", code, &stderr)
	}
}

func TestCLIInfoIgnoresScanConfig(t *testing.T) {
	cap := fixture(t)
	c := testClient(t, func(w http.ResponseWriter, r *http.Request, action string, _ []byte) {
		if action != "GetScannerElements" {
			t.Errorf("info triggered %s", action)
		}
		_, _ = w.Write(cap)
	})
	cfg := defaultConfig()
	cfg.Device = deviceForTest(t, c.endpoint)
	cfg.Scan.DPI, cfg.Scan.Timeout, cfg.Output.Format = 0, "bad", "gif"
	cfg.Processing.Deskew, cfg.Processing.Autocrop = true, true
	writeConfigForTest(t, cfg)
	var out, stderr bytes.Buffer
	if code := run(context.Background(), []string{"info"}, &out, &stderr); code != 0 || !strings.Contains(out.String(), `"state": "Idle"`) {
		t.Fatalf("info exit=%d: %s %s", code, &out, &stderr)
	}
}
