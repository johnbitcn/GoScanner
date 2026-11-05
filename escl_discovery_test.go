//go:build darwin || linux

package main

import (
	"context"
	"net/netip"
	"strings"
	"testing"
	"time"
)

const identityUUID = "12345678-1234-5678-9012-123456789abc"

func txtRecord(values ...string) []byte {
	var out []byte
	for _, s := range values {
		out = append(out, byte(len(s)))
		out = append(out, s...)
	}
	return out
}

func TestESCLDNSSDRecord(t *testing.T) {
	networks := []lanNetwork{{name: "en0", prefix: netip.MustParsePrefix("192.168.1.0/24")}}
	service := dnssdService{Name: "Scanner", Address: "192.168.1.9", Port: 8080, TXT: txtRecord("rs=scan/eSCL", "ty=Scanner A", "UUID="+identityUUID)}
	d, err := scannerFromDNSSD(service, networks)
	if err != nil || d.Endpoint != "http://192.168.1.9:8080/scan/eSCL" || d.Protocol != "escl" || d.ID != identityUUID {
		t.Fatalf("%+v %v", d, err)
	}
	service.Secure = true
	d, err = scannerFromDNSSD(service, networks)
	if err != nil || !strings.HasPrefix(d.Endpoint, "https://") {
		t.Fatalf("%+v %v", d, err)
	}
	for _, bad := range []dnssdService{
		{Address: "203.0.113.1", Port: 80},
		{Address: "192.168.1.9", Port: 0},
		{Address: "192.168.1.9", Port: 80, TXT: []byte{20, 'x'}},
		{Address: "192.168.1.9", Port: 80, TXT: txtRecord("rs=eSCL", "RS=other")},
		{Address: "192.168.1.9", Port: 80, TXT: txtRecord("rs=../admin")},
		{Address: "192.168.1.9", Port: 80, TXT: txtRecord("rs=eSCL?secret=x")},
	} {
		if _, err := scannerFromDNSSD(bad, networks); err == nil {
			t.Fatalf("accepted %+v", bad)
		}
	}
}

func TestScannerIdentityConservative(t *testing.T) {
	wsd := scannerDevice{ID: "urn:uuid:" + identityUUID, Protocol: "wsd", Name: "Scanner", Model: "Same model", Endpoint: "http://192.168.1.9:8018/wsd/scan"}
	escl := scannerDevice{ID: strings.ToUpper(identityUUID), Protocol: "escl", Name: "Scanner", Model: "Same model", Endpoint: "http://192.168.1.9:80/eSCL"}
	tests := []struct {
		name string
		a, b scannerDevice
		want bool
	}{
		{"same UUID", wsd, escl, true},
		{"same IP and model are insufficient", wsd, func() scannerDevice { d := escl; d.ID = "different-uuid"; return d }(), false},
		{"no identities", func() scannerDevice { d := wsd; d.ID = ""; return d }(), func() scannerDevice { d := escl; d.ID = ""; return d }(), false},
		{"Canon UUID differs serial confirms", func() scannerDevice { d := wsd; d.SerialNumber = "CANON-123"; return d }(), func() scannerDevice { d := escl; d.ID = "different-uuid"; d.SerialNumber = "CANON-123"; return d }(), true},
		{"serial conflict overrides UUID", func() scannerDevice { d := wsd; d.SerialNumber = "SERIAL-A"; return d }(), func() scannerDevice { d := escl; d.SerialNumber = "SERIAL-B"; return d }(), false},
		{"placeholder serial", func() scannerDevice { d := wsd; d.ID = ""; d.SerialNumber = "unknown"; return d }(), func() scannerDevice { d := escl; d.ID = ""; d.SerialNumber = "unknown"; return d }(), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := sameScanner(tt.a, tt.b); got != tt.want {
				t.Fatalf("got %v want %v", got, tt.want)
			}
		})
	}
	devices := mergeDiscovered([]scannerDevice{wsd, escl})
	if len(devices) != 1 || devices[0].Protocol != "escl" || len(devices[0].Endpoints) != 2 || scannerProtocolLabel(devices[0]) != "eSCL + WSD" {
		t.Fatalf("%+v", devices)
	}
}

func TestDiscoveryBackendsStartTogether(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	started := make(chan string, 2)
	release := make(chan struct{})
	backend := func(protocol string) discoveryFunc {
		return func(ctx context.Context) ([]scannerDevice, []diagnostic, error) {
			started <- protocol
			select {
			case <-release:
			case <-ctx.Done():
				return nil, nil, ctx.Err()
			}
			endpoint := "http://192.168.1.9:8018/wsd/scan"
			if protocol == "escl" {
				endpoint = "http://192.168.1.9:80/eSCL"
			}
			return []scannerDevice{{ID: identityUUID, Protocol: protocol, Endpoint: endpoint}}, nil, nil
		}
	}
	done := make(chan []scannerDevice, 1)
	go func() { d, _, _ := discoverBoth(ctx, backend("wsd"), backend("escl")); done <- d }()
	for range 2 {
		select {
		case <-started:
		case <-ctx.Done():
			t.Fatal("发现未并行启动")
		}
	}
	close(release)
	select {
	case d := <-done:
		if len(d) != 1 || d[0].Protocol != "escl" {
			t.Fatalf("%+v", d)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
}

func TestPersistBothProtocolsAndKeepHistory(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	wsd := scannerDevice{ID: "urn:uuid:" + identityUUID, Protocol: "wsd", Endpoint: "http://192.168.1.9:8018/wsd/scan", SerialNumber: "CANON-123", Model: "Canon"}
	escl := scannerDevice{ID: "another-vendor-uuid", Protocol: "escl", Endpoint: "http://192.168.1.9:80/eSCL", SerialNumber: "CANON-123", Model: "Canon"}
	for _, d := range []scannerDevice{wsd, escl, wsd} {
		if err := rememberDrives([]savedDevice{{scannerDevice: d, Discovered: true}}); err != nil {
			t.Fatal(err)
		}
	}
	devices, err := loadDrives()
	if err != nil || len(devices) != 1 || devices[0].Protocol != "escl" || len(devices[0].Endpoints) != 2 {
		t.Fatalf("%+v %v", devices, err)
	}
	if !deviceHasEndpoint(devices[0].scannerDevice, wsd.Endpoint) {
		t.Fatal("lost WSD endpoint")
	}
}

func TestESCLConfigAndFlags(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	d := deviceConfig{Host: "192.168.1.9", Port: 443, Path: "/eSCL", Protocol: "escl", Scheme: "https"}
	if err := saveDefaultDevice(d); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadConfig()
	if err != nil || cfg.Device != d {
		t.Fatalf("%+v %v", cfg.Device, err)
	}
	r, _, failure, _ := parseRequest([]string{"info"}, false)
	if failure != nil || r.protocol != "escl" || r.endpoint != "https://192.168.1.9:443/eSCL" {
		t.Fatalf("%+v %v", r, failure)
	}
	r, _, failure, _ = parseRequest([]string{"info", "--protocol", "wsd", "--host", "192.168.1.8", "--port", "8018", "--path", "/wsd/scan", "--scheme", "http"}, false)
	if failure != nil || r.protocol != "wsd" {
		t.Fatalf("%+v %v", r, failure)
	}
	t.Setenv("HOME", t.TempDir())
	r, _, failure, _ = parseRequest([]string{"info", "--protocol", "escl", "--host", "192.168.1.8"}, false)
	if failure != nil || r.host != "192.168.1.8" || r.endpoint != "" {
		t.Fatalf("%+v %v", r, failure)
	}
	r, _, failure, _ = parseRequest([]string{"info", "--protocol", "escl", "--endpoint", "https://192.168.1.8/eSCL"}, false)
	if failure != nil || r.endpoint != "https://192.168.1.8/eSCL" {
		t.Fatalf("%+v %v", r, failure)
	}
}
