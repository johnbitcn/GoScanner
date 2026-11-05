//go:build darwin || linux

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"image/png"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestESCLPublishedBrotherCapabilities(t *testing.T) {
	data, err := os.ReadFile("testdata/escl-brother-mfc-j985dw.xml")
	if err != nil {
		t.Fatal(err)
	}
	cap, err := parseESCLCapabilities(data)
	if err != nil {
		t.Fatal(err)
	}
	if cap.MakeAndModel != "Brother MFC-J985DW" || cap.SerialNumber != "REDACTED-SERIAL" || !slices.Contains(cap.WidthsDPI, 300) {
		t.Fatalf("%+v", cap)
	}
	// Status is a separate response, not part of this published capability fixture.
	cap.State = "Idle"
	// This device advertises PDF, but image acquisition must negotiate JPEG.
	body, err := ticket(cap, options{DPI: 300, Mode: "color", Area: region{Width: 8267, Height: 11690}, Format: formatJPEG})
	if err != nil || !strings.Contains(body, "image/jpeg") || strings.Contains(body, "application/pdf") {
		t.Fatalf("%s %v", body, err)
	}
}

func TestESCLAgentInfoAndScanOutputs(t *testing.T) {
	payload := jpegSample(t)
	capabilities := esclTestCapabilities(esclTestProfile("RGB24", `<pwg:DocumentFormat>image/jpeg</pwg:DocumentFormat>`, esclDiscrete(300)))
	var starts, cleanups atomic.Int32
	c := esclTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if serveESCLInfo(w, r, capabilities, "Idle") {
			return
		}
		switch {
		case r.Method == "POST" && r.URL.Path == "/eSCL/ScanJobs":
			starts.Add(1)
			w.Header().Set("Location", "/eSCL/ScanJobs/one")
			w.WriteHeader(201)
		case r.Method == "GET" && r.URL.Path == "/eSCL/ScanJobs/one/NextDocument":
			w.Write(payload)
		case r.Method == "DELETE" && strings.HasPrefix(r.URL.Path, "/eSCL/ScanJobs/one"):
			cleanups.Add(1)
			w.WriteHeader(204)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL)
			w.WriteHeader(404)
		}
	})
	var output, diagnostics bytes.Buffer
	code := runWithDiscovery(context.Background(), []string{"--agent", "info", "--protocol", "escl", "--endpoint", c.endpoint}, &output, &diagnostics, nil)
	var result commandResult
	if err := json.Unmarshal(output.Bytes(), &result); err != nil || code != 0 || result.Info == nil || result.Info.MakeAndModel != "Test Flatbed" {
		t.Fatalf("%d %s %v", code, output.String(), err)
	}
	if _, err := os.Stat(deviceConfigPath(t)); !os.IsNotExist(err) {
		t.Fatalf("agent wrote config: %v", err)
	}
	for i, ext := range []string{"jpg", "png", "pdf"} {
		t.Run(ext, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "page."+ext)
			output.Reset()
			diagnostics.Reset()
			code = runWithDiscovery(context.Background(), []string{"--agent", "scan", "--protocol", "escl", "--endpoint", c.endpoint, "--dpi", "300", "-o", path}, &output, &diagnostics, nil)
			result = commandResult{}
			if err := json.Unmarshal(output.Bytes(), &result); err != nil || code != 0 || result.Pages != 1 || len(result.Files) != 1 {
				t.Fatalf("%d %s %s %v", code, output.String(), diagnostics.String(), err)
			}
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			switch ext {
			case "jpg":
				if !bytes.Equal(data, payload) {
					t.Fatal("JPEG changed")
				}
			case "png":
				if _, err := png.Decode(bytes.NewReader(data)); err != nil {
					t.Fatal(err)
				}
			case "pdf":
				if !bytes.HasPrefix(data, []byte("%PDF-")) {
					t.Fatal("not PDF")
				}
			}
			if starts.Load() != int32(i+1) || cleanups.Load() != int32(i+1) {
				t.Fatalf("POST=%d DELETE=%d", starts.Load(), cleanups.Load())
			}
		})
	}
}

func TestESCLTUIFormPreservesProtocol(t *testing.T) {
	var starts atomic.Int32
	c := esclTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			starts.Add(1)
			t.Error("form verification must not scan")
		}
		if !serveESCLInfo(w, r, esclTestCapabilities(esclDefaultProfiles()), "Idle") {
			w.WriteHeader(404)
		}
	})
	ctx, cancel := context.WithCancel(context.Background())
	m := newHumanModel(ctx, cancel, commandRequest{command: "info", endpoint: c.endpoint, protocol: "escl", timeout: time.Second}, nil)
	t.Cleanup(func() { close(m.stopped); cancel(); m.tasks.Wait() })
	m.find = func(context.Context) ([]scannerDevice, []string, error) {
		return []scannerDevice{{Protocol: "escl", Endpoint: c.endpoint}}, nil, nil
	}
	m.editDeviceForm()
	if len(m.deviceInputs) != 1 {
		t.Fatal("manual form must only ask for IP")
	}
	m.verifyDevice()
	m.Update(m.listen())
	m.Update(m.listen())
	cfg, err := loadConfig()
	if err != nil || configuredRecord(t, cfg.Device).Protocol != "escl" || starts.Load() != 0 {
		t.Fatalf("%+v %v starts=%d", cfg.Device, err, starts.Load())
	}
}

func TestESCLPreferredProtocolNeverFallsBack(t *testing.T) {
	for _, failure := range []string{"info", "create"} {
		t.Run(failure, func(t *testing.T) {
			var wsdRequests, esclStarts atomic.Int32
			wsd := testClient(t, func(w http.ResponseWriter, r *http.Request, action string, _ []byte) {
				wsdRequests.Add(1)
				if action != "GetScannerElements" {
					t.Errorf("unexpected WSD action %s", action)
				}
				w.Write(fixture(t))
			})
			escl := esclTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				if failure == "info" {
					w.WriteHeader(503)
					return
				}
				if serveESCLInfo(w, r, esclTestCapabilities(esclDefaultProfiles()), "Idle") {
					return
				}
				if r.Method == "POST" {
					esclStarts.Add(1)
				}
				w.WriteHeader(503)
			})
			d := mergeScanner(scannerDevice{Protocol: "wsd", Endpoint: wsd.endpoint, ID: identityUUID}, scannerDevice{Protocol: "escl", Endpoint: escl.endpoint, ID: identityUUID})
			if err := rememberDrives([]savedDevice{{scannerDevice: d, Discovered: true}}); err != nil {
				t.Fatal(err)
			}
			command := "info"
			if failure == "create" {
				command = "scan"
			}
			r := commandRequest{command: command, endpoint: escl.endpoint, protocol: "escl", timeout: time.Second, output: filepath.Join(t.TempDir(), "page.jpg"), opts: options{DPI: 300, Mode: "color", Area: region{Width: 8267, Height: 11690}}}
			var out, diagnostics bytes.Buffer
			result, code := executeCommand(context.Background(), r, nil, commandHooks{agent: true}, &out, &diagnostics)
			if failure == "info" {
				if code == 0 || wsdRequests.Load() != 0 || len(result.Warnings) != 0 || esclStarts.Load() != 0 {
					t.Fatalf("%+v %d WSD=%d starts=%d", result, code, wsdRequests.Load(), esclStarts.Load())
				}
			} else {
				if code == 0 || wsdRequests.Load() != 0 || esclStarts.Load() != 1 {
					t.Fatalf("%+v %d WSD=%d starts=%d", result, code, wsdRequests.Load(), esclStarts.Load())
				}
			}
		})
	}
}
