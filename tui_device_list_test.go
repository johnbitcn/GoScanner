package main

import (
	"context"
	"os"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

func TestHumanDeviceModelAndIPPresentation(t *testing.T) {
	m := humanTestModel(t)
	model := "HP Laser MFP 1188w"
	if err := rememberDrives([]savedDevice{{scannerDevice: scannerDevice{Endpoint: m.request.endpoint, Model: model}, Discovered: true}}); err != nil {
		t.Fatal(err)
	}
	m.reloadDrives()
	device, _ := deviceFromEndpoint(m.request.endpoint)
	view := m.View().Content
	if !strings.Contains(view, tuiModel.Render(model)) || !strings.Contains(view, tuiPath.Render(device.Host)) {
		t.Fatal("home page must show bold model and colored IP", view)
	}
	press(m, 'd')
	view = m.View().Content
	plain := ansi.Strip(view)
	if m.state != "device-list" || !strings.Contains(view, tuiModel.Render(model)) || !strings.Contains(view, tuiPath.Render(device.Host)) || strings.Index(plain, model) > strings.Index(plain, "手动添加") || strings.Contains(plain, "端口") {
		t.Fatal("saved devices must precede manual inputs", plain)
	}
	m.deviceCursor = len(m.drives)
	press(m, tea.KeyEnter)
	if m.state != "devices" || !strings.Contains(ansi.Strip(m.View().Content), "IP") || strings.Contains(ansi.Strip(m.View().Content), "端口") {
		t.Fatal("manual selection did not open input form")
	}
	press(m, tea.KeyEsc)
	if m.state != "device-list" {
		t.Fatal("manual form Esc should return to saved devices")
	}
	press(m, tea.KeyEsc)
	m.Update(m.listen())
	if m.state != "ready" {
		t.Fatal("saved devices Esc should return to original scanner")
	}
}

func TestHumanDeviceLabelKeepsIPInNarrowTerminal(t *testing.T) {
	m := humanTestModel(t)
	endpoint := "http://192.168.100.100:8018/wsd/scan"
	m.drives = []savedDevice{{scannerDevice: scannerDevice{Endpoint: endpoint, Model: "A very long scanner model name"}}}
	label := m.deviceLabel(endpoint, 24)
	if ansi.StringWidth(label) > 24 || !strings.Contains(label, tuiPath.Render("192.168.100.100")) || !strings.Contains(label, "…") {
		t.Fatal("truncate model, not IP", label)
	}
}

func TestHumanSavedDeviceStillNeedsValidation(t *testing.T) {
	m := humanTestModel(t)
	press(m, 'd')
	if m.state != "device-list" || m.working {
		t.Fatal("opening saved devices started work")
	}
	press(m, tea.KeyEnter)
	if !m.working || m.state != "working" {
		t.Fatal("saved selection skipped live validation")
	}
	m.Update(m.listen())
	if m.state != "ready" || m.working {
		t.Fatal("saved selection should validate without scanning")
	}
}

func TestHumanDiscoveryUpdatesSavedListAndKeepsSwitchSelected(t *testing.T) {
	m := humanTestModel(t)
	endpoint := m.request.endpoint
	m.find = func(_ context.Context) ([]scannerDevice, []diagnostic, error) {
		return []scannerDevice{{Endpoint: endpoint, Model: "HP Laser MFP 1188w"}, {Endpoint: "http://192.168.1.9:8018/wsd/scan", Model: "Other scanner"}}, nil, nil
	}
	press(m, 'd')
	m.deviceCursor = len(m.drives) + 1
	press(m, tea.KeySpace)
	m.Update(m.listen())
	if len(m.drives) != 2 || m.deviceCursor != len(m.drives)+1 || m.drives[1].Verified {
		t.Fatal("discovery lost selection or auto-verified a device")
	}
	if !strings.Contains(ansi.Strip(m.View().Content), "Other scanner") {
		t.Fatal("discovered device was not listed")
	}
	press(m, tea.KeySpace)
	if m.autoDiscover || len(m.drives) != 2 {
		t.Fatal("disabling discovery deleted saved devices")
	}
}

func TestHumanBrokenDeviceTableIsVisibleAndPreserved(t *testing.T) {
	m := humanTestModel(t)
	path, _ := drivesPath()
	if err := os.WriteFile(path, []byte("broken table"), 0600); err != nil {
		t.Fatal(err)
	}
	press(m, 'd')
	if m.state != "device-list" || m.drivesError == nil || m.notice == "" {
		t.Fatal("broken table was silently ignored")
	}
	after, _ := os.ReadFile(path)
	if string(after) != "broken table" {
		t.Fatal("broken table was overwritten")
	}
}
