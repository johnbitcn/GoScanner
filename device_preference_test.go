//go:build darwin || linux

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

func preferencePair(t *testing.T) (scannerDevice, *atomic.Int32, *atomic.Int32) {
	t.Helper()
	wsdCalls, esclCalls := new(atomic.Int32), new(atomic.Int32)
	data := fixture(t)
	wsd := testClient(t, func(w http.ResponseWriter, r *http.Request, action string, _ []byte) {
		wsdCalls.Add(1)
		if action != "GetScannerElements" {
			t.Errorf("unexpected WSD operation %s", action)
		}
		w.Write(data)
	})
	escl := esclTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		esclCalls.Add(1)
		if !serveESCLInfo(w, r, esclTestCapabilities(esclDefaultProfiles()), "Idle") {
			t.Errorf("unexpected eSCL operation %s %s", r.Method, r.URL)
			w.WriteHeader(404)
		}
	})
	d := mergeScanner(scannerDevice{Protocol: "wsd", Endpoint: wsd.endpoint, ID: identityUUID, SerialNumber: "serial-1", Model: "Example MFP"}, scannerDevice{Protocol: "escl", Endpoint: escl.endpoint, ID: identityUUID, SerialNumber: "serial-1", Model: "Example MFP"})
	if err := rememberDrives([]savedDevice{{scannerDevice: d, Discovered: true}}); err != nil {
		t.Fatal(err)
	}
	return persistedScanner(t, d.Endpoint), wsdCalls, esclCalls
}

func TestPreferredProtocolSurvivesDiscoveryAndInfo(t *testing.T) {
	d, _, _ := preferencePair(t)
	if d.Protocol != "escl" {
		t.Fatal("new dual-protocol device must initially use eSCL")
	}
	if err := savePreferredProtocol(d.RecordID, "wsd"); err != nil {
		t.Fatal(err)
	}
	for _, e := range scannerEndpoints(d) {
		update := savedDevice{scannerDevice: scannerDevice{Protocol: e.Protocol, Endpoint: e.Endpoint, ID: e.ID, SerialNumber: e.SerialNumber, Model: d.Model}, Discovered: true}
		if err := rememberDrives([]savedDevice{update}); err != nil {
			t.Fatal(err)
		}
	}
	e, _ := d.endpointFor("escl")
	if err := rememberDrives([]savedDevice{{scannerDevice: scannerDevice{Protocol: "escl", Endpoint: e.Endpoint, SerialNumber: "serial-1", CapabilityUUID: identityUUID}, Verified: true, Info: &info{SerialNumber: "serial-1", UUID: identityUUID}}}); err != nil {
		t.Fatal(err)
	}
	devices, err := loadDrives()
	if err != nil || len(devices) != 1 || devices[0].Protocol != "wsd" || devices[0].PreferredProtocol != "wsd" || len(devices[0].Endpoints) != 2 {
		t.Fatalf("%+v %v", devices, err)
	}
	// Only the preferred endpoint's capability snapshot may be shown as verified.
	if devices[0].Info != nil || devices[0].Verified {
		t.Fatal("eSCL snapshot used as WSD readiness")
	}
	discovered := applySavedPreferences([]scannerDevice{d}, devices)
	if discovered[0].Protocol != "wsd" {
		t.Fatal("discovery reset user preference")
	}
}

func TestCLIUsesStoredPreferenceAndTemporaryOverride(t *testing.T) {
	d, wsdCalls, esclCalls := preferencePair(t)
	original, _ := deviceFromProtocolEndpoint(d.Endpoint, "escl")
	if err := saveDefaultDevice(original); err != nil {
		t.Fatal(err)
	}
	if err := savePreferredProtocol(d.RecordID, "wsd"); err != nil {
		t.Fatal(err)
	}
	store, _ := drivesPath()
	before, _ := os.ReadFile(store)
	for _, args := range [][]string{
		{"--agent", "info"},
		{"--agent", "info", "--host", original.Host},
		{"--agent", "info", "--endpoint", d.Endpoint},
	} {
		var out, diagnostics bytes.Buffer
		if code := runWithDiscovery(context.Background(), args, &out, &diagnostics, nil); code != 0 {
			t.Fatalf("%v: %d %s", args, code, out.String())
		}
	}
	if wsdCalls.Load() != 3 || esclCalls.Load() != 0 {
		t.Fatalf("ignored preferred WSD: WSD=%d eSCL=%d", wsdCalls.Load(), esclCalls.Load())
	}
	// Protocol alone must resolve the saved scanner, not guess port 80 or /eSCL.
	w, _ := d.endpointFor("wsd")
	wsdConfig, _ := deviceFromProtocolEndpoint(w.Endpoint, "wsd")
	if err := saveDefaultDevice(wsdConfig); err != nil {
		t.Fatal(err)
	}
	var out, diagnostics bytes.Buffer
	if code := runWithDiscovery(context.Background(), []string{"--agent", "info", "--protocol", "escl"}, &out, &diagnostics, nil); code != 0 {
		t.Fatalf("override failed: %d %s", code, out.String())
	}
	if wsdCalls.Load() != 3 || esclCalls.Load() != 2 {
		t.Fatalf("wrong override: WSD=%d eSCL=%d", wsdCalls.Load(), esclCalls.Load())
	}
	after, _ := os.ReadFile(store)
	if !bytes.Equal(before, after) {
		t.Fatal("temporary CLI override changed stored preference")
	}
	devices, err := loadDrives()
	if err != nil || devices[0].PreferredProtocol != "wsd" {
		t.Fatalf("%+v %v", devices, err)
	}
}

func TestProtocolOverrideDoesNotBecomeDefaultDuringDeviceSelection(t *testing.T) {
	for _, ui := range []string{"tui", "text"} {
		t.Run(ui, func(t *testing.T) {
			d, wsdCalls, esclCalls := preferencePair(t)
			r := commandRequest{command: "info", endpoint: d.Endpoint, protocol: "wsd", protocolExplicit: true, timeout: time.Second}
			if ui == "tui" {
				ctx, cancel := context.WithCancel(context.Background())
				m := newHumanModel(ctx, cancel, r, nil)
				t.Cleanup(func() { close(m.stopped); cancel(); m.tasks.Wait() })
				m.verifyScannerDevice(d)
				m.Update(m.listen())
				if m.code != 0 || m.request.protocol != "wsd" {
					t.Fatalf("override ignored: %+v", m.result)
				}
			} else {
				r.endpoint = ""
				find := func(context.Context) ([]scannerDevice, []diagnostic, error) { return []scannerDevice{d}, nil, nil }
				choose := func(context.Context, []scannerDevice) (string, error) { return d.Endpoint, nil }
				var out, diagnostics bytes.Buffer
				if result, code := executeCommand(context.Background(), r, find, commandHooks{choose: choose}, &out, &diagnostics); code != 0 {
					t.Fatalf("%+v %d", result, code)
				}
			}
			cfg, err := loadConfig()
			if err != nil || configuredRecord(t, cfg.Device).Protocol != "escl" || wsdCalls.Load() != 1 || esclCalls.Load() != 0 {
				t.Fatalf("override persisted or wrong protocol: %+v %v WSD=%d eSCL=%d", cfg.Device, err, wsdCalls.Load(), esclCalls.Load())
			}
		})
	}
}

func TestIPOnlyCLIUsesDiscoveredPortAndPath(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	var requests, finds atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Method != "GET" {
			t.Errorf("discovery/validation scanned: %s", r.Method)
		}
		switch r.URL.Path {
		case "/vendor/scanner/ScannerCapabilities":
			w.Write([]byte(esclTestCapabilities(esclDefaultProfiles())))
		case "/vendor/scanner/ScannerStatus":
			w.Write([]byte(`<scan:ScannerStatus xmlns:scan="` + esclNS + `" xmlns:pwg="` + pwgNS + `"><pwg:State>Idle</pwg:State></scan:ScannerStatus>`))
		default:
			t.Errorf("guessed service URL: %s", r.URL)
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	endpoint := server.URL + "/vendor/scanner"
	find := func(context.Context) ([]scannerDevice, []diagnostic, error) {
		finds.Add(1)
		return []scannerDevice{
			{Protocol: "escl", Endpoint: "http://192.0.2.9:8080/eSCL", ID: "unrelated"},
			{Protocol: "escl", Endpoint: endpoint, ID: identityUUID},
		}, nil, nil
	}
	var out, diagnostics bytes.Buffer
	if code := runWithDiscovery(context.Background(), []string{"--agent", "info", "--host", "127.0.0.1"}, &out, &diagnostics, find); code != 0 {
		t.Fatalf("%d %s %s", code, out.String(), diagnostics.String())
	}
	if finds.Load() != 1 || requests.Load() != 2 {
		t.Fatalf("find=%d requests=%d", finds.Load(), requests.Load())
	}
	if path, _ := drivesPath(); fileExists(path) {
		t.Fatal("Agent IP lookup wrote device table")
	}
}

func fileExists(path string) bool { _, err := os.Stat(path); return err == nil }

func TestPreferenceListToggleAndOrangeCurrentProtocol(t *testing.T) {
	m := humanTestModel(t)
	d := mergeScanner(scannerDevice{Protocol: "wsd", Endpoint: m.request.endpoint, ID: identityUUID, SerialNumber: "DEVICE-ABC", Model: "Example MFP"}, scannerDevice{Protocol: "escl", Endpoint: "http://127.0.0.1:1/eSCL", ID: identityUUID, SerialNumber: "DEVICE-ABC", Model: "Example MFP"})
	if err := rememberDrives([]savedDevice{{scannerDevice: d, Discovered: true}}); err != nil {
		t.Fatal(err)
	}
	m.editDevice()
	view := m.View().Content
	if !strings.Contains(view, tuiProtocol.Render("eSCL")) || strings.Contains(ansi.Strip(view), "eSCL + WSD") {
		t.Fatal("list must show orange selected protocol, not supported protocols", view)
	}
	press(m, 'p')
	devices, err := loadDrives()
	if err != nil || len(devices) != 1 || devices[0].PreferredProtocol != "wsd" || m.request.protocol != "wsd" {
		t.Fatalf("%+v %v request=%+v", devices, err, m.request)
	}
	view = m.View().Content
	if !strings.Contains(view, tuiProtocol.Render("WSD")) || !strings.Contains(ansi.Strip(view), "Example MFP") {
		t.Fatal("preferred protocol label did not update", view)
	}
	m.editDevice()
	if m.drives[0].Protocol != "wsd" {
		t.Fatal("reopening list lost preference")
	}
}

func TestManualIPAmbiguityAndCancellationDoNotSelectDevice(t *testing.T) {
	t.Run("ambiguous identities", func(t *testing.T) {
		m := humanTestModel(t)
		old := m.request.endpoint
		m.editDeviceForm()
		m.deviceInputs[deviceHost].SetValue("192.168.1.9")
		m.find = func(context.Context) ([]scannerDevice, []diagnostic, error) {
			return []scannerDevice{
				{Protocol: "escl", Endpoint: "http://192.168.1.9:8080/vendor", ID: identityUUID},
				{Protocol: "wsd", Endpoint: "http://192.168.1.9:9000/scan", ID: "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"},
			}, nil, nil
		}
		m.verifyDevice()
		m.Update(m.listen())
		if !m.autoDiscover || len(m.devices) != 2 || m.working || m.request.endpoint != old {
			t.Fatal("IP ambiguity silently selected a protocol")
		}
		if fileExists(deviceConfigPath(t)) {
			t.Fatal("ambiguous IP changed default device")
		}
	})
	t.Run("cancelled and stale", func(t *testing.T) {
		m := humanTestModel(t)
		old := m.request.endpoint
		started, stopped := make(chan struct{}), make(chan struct{})
		m.find = func(ctx context.Context) ([]scannerDevice, []diagnostic, error) {
			close(started)
			<-ctx.Done()
			close(stopped)
			return []scannerDevice{{Protocol: "escl", Endpoint: "http://192.168.1.9:80/eSCL"}}, nil, nil
		}
		m.editDeviceForm()
		m.deviceInputs[deviceHost].SetValue("192.168.1.9")
		m.verifyDevice()
		<-started
		press(m, tea.KeyEscape)
		<-stopped
		m.Update(m.listen())
		if m.state != "device-list" || m.request.endpoint != old || fileExists(deviceConfigPath(t)) {
			t.Fatal("cancelled IP lookup selected a device")
		}
	})
}

func TestIPDiscoveryFailureDoesNotGuessService(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	find := func(context.Context) ([]scannerDevice, []diagnostic, error) { return nil, nil, nil }
	var out, diagnostics bytes.Buffer
	code := runWithDiscovery(context.Background(), []string{"--agent", "info", "--host", "192.168.1.9"}, &out, &diagnostics, find)
	var result commandResult
	if err := json.Unmarshal(out.Bytes(), &result); err != nil || code == 0 || result.Error == nil || !strings.Contains(result.Error.Message, "No port or path was guessed.") {
		t.Fatalf("%d %s %v", code, out.String(), err)
	}
}

func TestCancelledIPLookupDoesNotPersistLateResults(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	find := func(context.Context) ([]scannerDevice, []diagnostic, error) {
		cancel()
		return []scannerDevice{{Protocol: "escl", Endpoint: "http://192.168.1.9:8080/vendor", ID: identityUUID}}, nil, nil
	}
	var out, diagnostics bytes.Buffer
	result, code := executeCommand(ctx, commandRequest{command: "info", host: "192.168.1.9", timeout: time.Second}, find, commandHooks{}, &out, &diagnostics)
	path, _ := drivesPath()
	if code != 130 || result.Error == nil || result.Error.Code != "cancelled" || fileExists(path) {
		t.Fatalf("late cancelled result persisted: %+v %d", result, code)
	}
}

func TestTooManyRememberedEndpointsPreserveUsableStore(t *testing.T) {
	d, _, _ := preferencePair(t)
	path, _ := drivesPath()
	before, _ := os.ReadFile(path)
	var updates []savedDevice
	for port := 8100; port < 8115; port++ {
		updates = append(updates, savedDevice{scannerDevice: scannerDevice{Protocol: "escl", Endpoint: "http://127.0.0.1:" + strconv.Itoa(port) + "/eSCL", ID: d.ID, SerialNumber: d.SerialNumber}, Discovered: true})
	}
	if err := rememberDrives(updates); err == nil {
		t.Fatal("oversized endpoint history was persisted")
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Fatal("oversized history damaged previous device table")
	}
	if _, err := loadDrives(); err != nil {
		t.Fatal(err)
	}
}

func TestSingleProtocolCannotChooseUnsupportedPreference(t *testing.T) {
	m := humanTestModel(t)
	m.editDevice()
	path, _ := drivesPath()
	before, _ := os.ReadFile(path)
	press(m, 'p')
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) || !strings.Contains(m.notice, "只确认支持") {
		t.Fatal("single protocol toggle changed device table")
	}
}

func TestSavedHTTPSAnchorResolvesWithoutRequiredProtocol(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	d := mergeScanner(scannerDevice{Protocol: "wsd", Endpoint: "http://192.168.1.9:8018/wsd/scan", ID: identityUUID}, scannerDevice{Protocol: "escl", Endpoint: "https://192.168.1.9:443/eSCL", ID: identityUUID})
	if err := rememberDrives([]savedDevice{{scannerDevice: d, Discovered: true}}); err != nil {
		t.Fatal(err)
	}
	for _, protocol := range []string{"", "wsd"} {
		args := []string{"info", "--endpoint", "https://192.168.1.9/eSCL"}
		want := "escl"
		if protocol != "" {
			args = append(args, "--protocol", protocol)
			want = protocol
		}
		r, _, failure, _ := parseRequest(args, false)
		if failure != nil {
			t.Fatal(failure)
		}
		r, _, _, err := resolveRequestDevice(context.Background(), r, nil)
		e, _ := d.endpointFor(want)
		if err != nil || r.protocol != want || r.endpoint != e.Endpoint {
			t.Fatalf("%+v %v", r, err)
		}
	}
}

func TestBareIPRequestDoesNotContainGuessedEndpoint(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	r, _, failure, _ := parseRequest([]string{"info", "--host", "192.168.1.9"}, false)
	if failure != nil || r.host != "192.168.1.9" || r.endpoint != "" {
		t.Fatalf("%+v %v", r, failure)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, _, _, err := resolveRequestDevice(ctx, r, func(context.Context) ([]scannerDevice, []diagnostic, error) { return nil, nil, nil })
	if err == nil {
		t.Fatal("undiscovered IP was turned into a scan endpoint")
	}
}
