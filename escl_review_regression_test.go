//go:build darwin || linux

package main

import (
	"bytes"
	"context"
	"net/http"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestSerialConflictDoesNotBorrowOldAdvertisement(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	endpoint := "http://192.168.1.9:80/eSCL"
	old := scannerDevice{Protocol: "escl", Endpoint: endpoint, ID: identityUUID, SerialNumber: "DEVICE-OLD"}
	if err := rememberDrives([]savedDevice{{scannerDevice: old, Discovered: true}}); err != nil {
		t.Fatal(err)
	}
	fresh := savedDevice{scannerDevice: scannerDevice{Protocol: "escl", Endpoint: endpoint, SerialNumber: "DEVICE-NEW"}, Verified: true, Info: &info{SerialNumber: "DEVICE-NEW"}}
	if err := rememberDrives([]savedDevice{fresh}); err != nil {
		t.Fatal(err)
	}
	got, err := loadDrives()
	if err != nil || len(got) != 1 || got[0].ID != "" {
		t.Fatalf("new device borrowed old UUID: %+v %v", got, err)
	}
	wsd := scannerDevice{Protocol: "wsd", Endpoint: "http://192.168.1.8:8018/wsd/scan", ID: identityUUID}
	if err := rememberDrives([]savedDevice{{scannerDevice: wsd, Discovered: true}}); err != nil {
		t.Fatal(err)
	}
	got, err = loadDrives()
	if err != nil || len(got) != 2 {
		t.Fatalf("new eSCL incorrectly merged with old WSD: %+v %v", got, err)
	}
}

func TestInfoRefreshPreservesConflictingAdvertisement(t *testing.T) {
	for _, reversed := range []bool{false, true} {
		t.Run(map[bool]string{false: "escl first", true: "wsd first"}[reversed], func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			a, b := identityUUID, "a1111111-2222-3333-4444-555555555555"
			escl := scannerDevice{Protocol: "escl", Endpoint: "http://192.168.1.9:80/eSCL", ID: a, CapabilityUUID: b}
			wsd := scannerDevice{Protocol: "wsd", Endpoint: "http://192.168.1.9:8018/wsd/scan", ID: b}
			initial := []savedDevice{{scannerDevice: escl, Discovered: true}, {scannerDevice: wsd, Discovered: true}}
			if reversed {
				initial[0], initial[1] = initial[1], initial[0]
			}
			if err := rememberDrives(initial); err != nil {
				t.Fatal(err)
			}
			update := savedDevice{scannerDevice: scannerDevice{Protocol: "escl", Endpoint: escl.Endpoint, CapabilityUUID: b}, Verified: true, Info: &info{UUID: b}}
			if err := rememberDrives([]savedDevice{update}); err != nil {
				t.Fatal(err)
			}
			got, err := loadDrives()
			if err != nil || len(got) != 2 {
				t.Fatalf("%+v %v", got, err)
			}
			for _, d := range got {
				if d.Protocol == "escl" && (d.ID != a || d.CapabilityUUID != b) {
					t.Fatalf("lost conflict evidence: %+v", d)
				}
			}
		})
	}
}

func TestESCLPNGReadinessAndChoicesMatchAcquisition(t *testing.T) {
	for _, format := range []string{formatJPEG, formatPNG} {
		t.Run(format, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			cap, err := parseESCLCapabilities([]byte(esclTestCapabilities(esclTestProfile("RGB24", `<pwg:DocumentFormat>`+map[string]string{formatJPEG: "image/jpeg", formatPNG: "image/png"}[format]+`</pwg:DocumentFormat>`, esclDiscrete(300)))))
			if err != nil {
				t.Fatal(err)
			}
			cap.State = "Idle"
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			m := newHumanModel(ctx, cancel, commandRequest{command: "scan", protocol: "escl", output: "page.png", opts: options{DPI: 300, Mode: "color", Area: region{Width: 100, Height: 100}}}, nil)
			m.showReadiness(commandResult{Status: "ok", Info: &cap}, 0)
			if m.state != "ready" {
				t.Fatalf("%s %s", m.state, m.notice)
			}
			m.field = settingFormat
			found := false
			for _, choice := range m.settingChoices() {
				if choice.value == ".png" {
					found = true
				}
			}
			if !found {
				t.Fatalf("PNG missing from %+v", m.settingChoices())
			}
		})
	}
}

func TestTUIExplicitServiceVerificationDoesNotFallback(t *testing.T) {
	var wsdRequests atomic.Int32
	wsd := testClient(t, func(w http.ResponseWriter, r *http.Request, _ string, _ []byte) {
		wsdRequests.Add(1)
		w.Write(fixture(t))
	})
	escl := esclTestClient(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(503) })
	d := mergeScanner(scannerDevice{Protocol: "wsd", Endpoint: wsd.endpoint, ID: identityUUID}, scannerDevice{Protocol: "escl", Endpoint: escl.endpoint, ID: identityUUID})
	if err := rememberDrives([]savedDevice{{scannerDevice: d, Discovered: true}}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	m := newHumanModel(ctx, cancel, commandRequest{command: "info", endpoint: escl.endpoint, protocol: "escl", timeout: time.Second}, nil)
	t.Cleanup(func() { close(m.stopped); cancel(); m.tasks.Wait() })
	m.find = func(context.Context) ([]scannerDevice, []string, error) { return []scannerDevice{d}, nil, nil }
	m.editDeviceForm()
	m.verifyDevice()
	m.Update(m.listen())
	m.Update(m.listen())
	if m.state != "devices" || wsdRequests.Load() != 0 {
		t.Fatalf("state=%s WSD=%d", m.state, wsdRequests.Load())
	}
	cfg, err := loadConfig()
	if err != nil || cfg.Device.Host != "" {
		t.Fatalf("failed validation saved device: %+v %v", cfg.Device, err)
	}
}

func TestDiscoverySelectionHonorsExplicitProtocol(t *testing.T) {
	for _, scenario := range []string{"dual wsd", "escl only", "wsd only"} {
		t.Run(scenario, func(t *testing.T) {
			var wsdRequests, esclRequests atomic.Int32
			wsd := testClient(t, func(w http.ResponseWriter, r *http.Request, action string, _ []byte) {
				wsdRequests.Add(1)
				if action != "GetScannerElements" {
					t.Errorf("unexpected %s", action)
				}
				w.Write(fixture(t))
			})
			escl := esclTestClient(t, func(w http.ResponseWriter, r *http.Request) { esclRequests.Add(1); w.WriteHeader(503) })
			w := scannerDevice{Protocol: "wsd", Endpoint: wsd.endpoint, ID: identityUUID}
			e := scannerDevice{Protocol: "escl", Endpoint: escl.endpoint, ID: identityUUID}
			d := mergeScanner(w, e)
			protocol := "wsd"
			if scenario == "escl only" {
				d = e
			}
			if scenario == "wsd only" {
				d = w
				protocol = "escl"
			}
			find := func(context.Context) ([]scannerDevice, []string, error) { return []scannerDevice{d}, nil, nil }
			choose := func(context.Context, []scannerDevice) (string, error) { return d.Endpoint, nil }
			r := commandRequest{command: "info", protocol: protocol, protocolExplicit: true, timeout: time.Second, output: filepath.Join(t.TempDir(), "unused.jpg")}
			var out, diagnostics bytes.Buffer
			result, code := executeCommand(context.Background(), r, find, commandHooks{choose: choose}, &out, &diagnostics)
			if scenario == "dual wsd" {
				if code != 0 || wsdRequests.Load() != 1 || esclRequests.Load() != 0 {
					t.Fatalf("%+v code=%d WSD=%d eSCL=%d", result, code, wsdRequests.Load(), esclRequests.Load())
				}
				cfg, err := loadConfig()
				if err != nil || configuredRecord(t, cfg.Device).Protocol != "escl" {
					t.Fatalf("%+v %v", cfg.Device, err)
				}
			} else if code == 0 || result.Error == nil || !strings.Contains(result.Error.Message, "未确认支持") || wsdRequests.Load() != 0 || esclRequests.Load() != 0 {
				t.Fatalf("%+v code=%d WSD=%d eSCL=%d", result, code, wsdRequests.Load(), esclRequests.Load())
			}
		})
	}
}
