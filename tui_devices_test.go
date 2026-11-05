package main

import (
	"bytes"
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func selectDeviceField(m *humanModel, field int) {
	for m.deviceField > field {
		press(m, tea.KeyUp)
	}
	for m.deviceField < field {
		press(m, tea.KeyDown)
	}
}

func setDeviceFields(t *testing.T, m *humanModel, endpoint string) {
	t.Helper()
	device, err := deviceFromEndpoint(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	m.deviceInputs[deviceHost].SetValue(device.Host)
	m.find = func(context.Context) ([]scannerDevice, []diagnostic, error) {
		return []scannerDevice{{Protocol: "wsd", Endpoint: endpoint}}, nil, nil
	}
}

func deviceConfigPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(os.Getenv("HOME"), ".config", "goscanner", "config.json")
}

func TestHumanDeviceFormDefaultsAndInput(t *testing.T) {
	m := humanTestModel(t)
	m.request.endpoint = ""
	m.request.recordID = ""
	m.find = func(context.Context) ([]scannerDevice, []diagnostic, error) {
		t.Fatal("discovery must be opt-in")
		return nil, nil, nil
	}
	m.Init()
	if m.state != "device-list" {
		t.Fatal("saved devices should be shown before manual fields")
	}
	m.deviceCursor = len(m.drives)
	press(m, tea.KeyEnter)
	if m.state != "devices" || m.working || m.autoDiscover || m.deviceInputs[deviceHost].Value() != "" || len(m.deviceInputs) != 1 {
		t.Fatal("manual defaults are wrong")
	}
	m.Update(tea.PasteMsg{Content: "192.168.1.100"})
	press(m, tea.KeyEnter)
	if m.deviceField != deviceConfirm || m.deviceInputs[deviceHost].Value() != "192.168.1.100" {
		t.Fatal("manual input or Enter navigation failed")
	}
	press(m, tea.KeyDown)
	if m.deviceField != deviceAuto {
		t.Fatal("arrow navigation failed")
	}
	m.Update(tea.WindowSizeMsg{Width: 40, Height: 12})
	view := m.View().Content
	if !strings.Contains(view, "192.168.1.100") || strings.Contains(view, "端口") || strings.Contains(view, "路径") || len(strings.Split(view, "\n")) > 12 {
		t.Fatal("device form overflow", view)
	}
}

func TestHumanManualDeviceValidationBeforeSave(t *testing.T) {
	for _, scenario := range []string{"ready", "busy", "http-error", "not-wsd", "save-error", "info"} {
		t.Run(scenario, func(t *testing.T) {
			data := fixture(t)
			if scenario == "busy" {
				data = bytes.ReplaceAll(data, []byte(">Idle<"), []byte(">Processing<"))
			}
			var queries atomic.Int32
			var path string
			var previous []byte
			c := testClient(t, func(w http.ResponseWriter, r *http.Request, action string, _ []byte) {
				if action != "GetScannerElements" {
					t.Errorf("validation created a scan: %s", action)
				}
				queries.Add(1)
				if scenario != "save-error" {
					current, err := os.ReadFile(path)
					if err != nil || !bytes.Equal(current, previous) {
						t.Errorf("default device changed before validation: %v", err)
					}
				}
				if scenario == "http-error" {
					w.WriteHeader(503)
					return
				}
				if scenario == "not-wsd" {
					w.Write([]byte("not a scanner"))
					return
				}
				w.Write(data)
			})
			m := humanTestModel(t)
			original := m.request.endpoint
			device, _ := deviceFromEndpoint(original)
			if err := saveDefaultDevice(device); err != nil {
				t.Fatal(err)
			}
			path = deviceConfigPath(t)
			var err error
			previous, err = os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			m.editDeviceForm()
			setDeviceFields(t, m, c.endpoint)
			if scenario == "info" {
				m.request.command = "info"
			}
			if scenario == "save-error" {
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
			}
			selectDeviceField(m, deviceConfirm)
			press(m, tea.KeyEnter)
			if !m.discovering || m.working {
				t.Fatal("IP must be discovered before validation")
			}
			m.Update(m.listen())
			if !m.working || !strings.Contains(m.status, "验证 WSD") || m.request.endpoint != original || m.validatingEndpoint != c.endpoint {
				t.Fatal("validation was skipped")
			}
			m.Update(m.listen())
			if queries.Load() != 1 {
				t.Fatalf("device was queried %d times", queries.Load())
			}
			switch scenario {
			case "http-error", "not-wsd", "save-error":
				if m.state != "devices" || m.notice == "" || m.request.endpoint != original {
					t.Fatal("invalid device replaced default")
				}
				if scenario != "save-error" {
					after, _ := os.ReadFile(path)
					if !bytes.Equal(after, previous) {
						t.Fatal("failed validation modified config")
					}
				}
			default:
				if m.request.endpoint != c.endpoint {
					t.Fatal("valid device was not adopted")
				}
				cfg, err := loadConfig()
				if err != nil {
					t.Fatal(err)
				}
				endpoint := configuredRecord(t, cfg.Device).Endpoint
				if endpoint != c.endpoint {
					t.Fatal("valid device was not saved", endpoint, err)
				}
				want := "ready"
				if scenario == "busy" {
					want = "unavailable"
				}
				if scenario == "info" {
					want = "done"
				}
				if m.state != want || m.working {
					t.Fatalf("state=%s want=%s", m.state, want)
				}
			}
		})
	}
}

func TestHumanInvalidDeviceFields(t *testing.T) {
	m := humanTestModel(t)
	m.editDeviceForm()
	for _, tc := range []struct {
		field int
		value string
	}{
		{deviceHost, ""}, {deviceHost, "scanner.local"}, {deviceHost, "http://127.0.0.1"},
		{deviceHost, "0.0.0.0"}, {deviceHost, "239.255.255.250"},
	} {
		setDeviceFields(t, m, m.request.endpoint)
		m.deviceInputs[tc.field].SetValue(tc.value)
		m.verifyDevice()
		if m.working || m.state != "devices" || m.notice == "" {
			t.Fatalf("invalid input accepted: %+v", tc)
		}
	}
	if _, err := os.Stat(deviceConfigPath(t)); !os.IsNotExist(err) {
		t.Fatal("invalid input changed configuration", err)
	}
}

func TestHumanDeviceDiscoveryOptInAndConfirmation(t *testing.T) {
	m := humanTestModel(t)
	var finds atomic.Int32
	m.find = func(context.Context) ([]scannerDevice, []diagnostic, error) {
		finds.Add(1)
		return []scannerDevice{{Name: "Only scanner", Model: "Only scanner", Endpoint: m.request.endpoint}}, nil, nil
	}
	m.editDeviceForm()
	if finds.Load() != 0 || m.autoDiscover {
		t.Fatal("automatic discovery started on entry")
	}
	selectDeviceField(m, deviceAuto)
	press(m, tea.KeySpace)
	if !m.autoDiscover || !m.discovering {
		t.Fatal("switch did not start discovery")
	}
	m.Update(m.listen())
	if m.state != "devices" || m.working || finds.Load() != 1 || len(m.devices) != 1 {
		t.Fatal("discovery auto-selected a device")
	}
	if _, err := os.Stat(deviceConfigPath(t)); !os.IsNotExist(err) {
		t.Fatal("discovery saved a default device", err)
	}
	selectDeviceField(m, deviceFirstResult)
	m.Update(tea.WindowSizeMsg{Width: 40, Height: 12})
	if !strings.Contains(m.View().Content, "Only scanner") {
		t.Fatal("selected discovery result is not visible in small terminal")
	}
	press(m, tea.KeyEnter)
	m.Update(m.listen())
	if m.state != "ready" || m.working {
		t.Fatal("selection must validate, save and prepare, not start scanning")
	}
}

func TestHumanDeviceDiscoveryCancellationAndStaleResults(t *testing.T) {
	m := humanTestModel(t)
	started, cancelled := make(chan struct{}), make(chan struct{})
	endpoint := m.request.endpoint
	m.find = func(ctx context.Context) ([]scannerDevice, []diagnostic, error) {
		close(started)
		<-ctx.Done()
		close(cancelled)
		return []scannerDevice{{Name: "Late result", Endpoint: endpoint}}, nil, nil
	}
	m.editDeviceForm()
	selectDeviceField(m, deviceAuto)
	press(m, tea.KeyEnter)
	<-started
	generation := m.discoveryGeneration
	press(m, tea.KeySpace)
	<-cancelled
	m.Update(m.listen())
	if m.autoDiscover || m.discovering || len(m.devices) != 0 || generation == m.discoveryGeneration {
		t.Fatal("disabled discovery accepted a stale result")
	}
	m.find = func(context.Context) ([]scannerDevice, []diagnostic, error) {
		return []scannerDevice{{Name: "Fresh", Endpoint: endpoint}}, nil, nil
	}
	press(m, tea.KeyEnter)
	m.Update(m.listen())
	if !m.autoDiscover || len(m.devices) != 1 || m.devices[0].Name != "Fresh" {
		t.Fatal("discovery could not restart")
	}
}

func TestHumanDeviceValidationCancellation(t *testing.T) {
	started := make(chan struct{})
	c := testClient(t, func(w http.ResponseWriter, r *http.Request, action string, _ []byte) {
		close(started)
		<-r.Context().Done()
	})
	m := humanTestModel(t)
	original := m.request.endpoint
	m.editDeviceForm()
	setDeviceFields(t, m, c.endpoint)
	selectDeviceField(m, deviceConfirm)
	press(m, tea.KeyEnter)
	m.Update(m.listen())
	<-started
	m.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	_, cmd := m.Update(m.listen())
	if cmd == nil || m.code != 130 || m.request.endpoint != original {
		t.Fatal("cancelled validation replaced the device")
	}
	if _, err := os.Stat(deviceConfigPath(t)); !os.IsNotExist(err) {
		t.Fatal("cancelled validation wrote configuration", err)
	}
}
