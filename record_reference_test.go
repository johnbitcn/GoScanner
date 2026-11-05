//go:build darwin || linux

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func configuredRecord(t *testing.T, d deviceConfig) scannerDevice {
	t.Helper()
	if !validRecordID(d.RecordID) || d.Host != "" || d.Port != 0 || d.Path != "" || d.Protocol != "" || d.Scheme != "" {
		t.Fatalf("default must only contain a record reference: %+v", d)
	}
	ds, err := loadDrives()
	if err != nil {
		t.Fatal(err)
	}
	record, err := lookupRecord(ds, d.RecordID)
	if err != nil {
		t.Fatal(err)
	}
	return record.scannerDevice
}

func persistedScanner(t *testing.T, endpoint string) scannerDevice {
	t.Helper()
	ds, err := loadDrives()
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range ds {
		if deviceHasEndpoint(d.scannerDevice, endpoint) {
			return d.scannerDevice
		}
	}
	t.Fatal("missing stored scanner", endpoint)
	return scannerDevice{}
}

func TestRecordConfigFormatsAndOverrides(t *testing.T) {
	id := "scanner_0123456789abcdef0123456789abcdef"
	for _, raw := range []string{
		`{"device":{"record_id":"` + id + `","host":""}}`,
		`{"device":{"record_id":"` + id + `","port":0}}`,
		`{"device":{"record_id":""}}`,
		`{"device":{"record_id":null}}`,
		`{"device":{"record_id":"scanner_BAD"}}`,
	} {
		if _, err := decodeConfig([]byte(raw), "test"); err == nil {
			t.Fatal("accepted invalid mixed reference", raw)
		}
	}
	writeTestConfig(t, `{"device":{"record_id":"`+id+`"}}`)
	r, _, failure, _ := parseRequest([]string{"info"}, false, chinese)
	if failure != nil || r.recordID != id || r.endpoint != "" {
		t.Fatalf("%+v %v", r, failure)
	}
	for _, args := range [][]string{{"info", "--host", "192.168.1.9"}, {"info", "--endpoint", "http://192.168.1.9/scan"}} {
		r, _, failure, _ = parseRequest(args, false, chinese)
		if failure != nil || r.recordID != "" {
			t.Fatalf("explicit address did not override reference: %+v %v", r, failure)
		}
	}
	if _, _, failure, _ := parseRequest([]string{"info", "--port", "8888"}, false, chinese); failure == nil {
		t.Fatal("port alone silently changed reference")
	}
}

func TestIdentityFreeRediscoveryKeepsLocalID(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	d := savedDevice{scannerDevice: scannerDevice{Endpoint: "http://192.168.1.9:80/scan", Protocol: "wsd"}, Discovered: true}
	if err := rememberDrives([]savedDevice{d}); err != nil {
		t.Fatal(err)
	}
	before := persistedScanner(t, d.Endpoint)
	if err := rememberDrives([]savedDevice{d}); err != nil {
		t.Fatal(err)
	}
	if after := persistedScanner(t, d.Endpoint); after.RecordID != before.RecordID {
		t.Fatal("rediscovery changed local ID")
	}
}

func TestReferenceAndAliasResolveWithoutWriting(t *testing.T) {
	d, wsdCalls, esclCalls := preferencePair(t)
	ds, _ := loadDrives()
	id := ds[0].RecordID
	alias := "scanner_abcdef0123456789abcdef0123456789"
	if err := updateDrives(func(ds []savedDevice) ([]savedDevice, error) { ds[0].RecordAliases = []string{alias}; return ds, nil }); err != nil {
		t.Fatal(err)
	}
	if err := savePreferredProtocol(id, "wsd"); err != nil {
		t.Fatal(err)
	}
	for _, ref := range []string{id, alias} {
		if err := saveDefaultRecord(context.Background(), ref); err != nil {
			t.Fatal(err)
		}
		cp, _ := configPath()
		dp, _ := drivesPath()
		beforeC, _ := os.ReadFile(cp)
		beforeD, _ := os.ReadFile(dp)
		var out, diag bytes.Buffer
		if code := runWithDiscovery(context.Background(), []string{"--agent", "info"}, &out, &diag, nil); code != 0 {
			t.Fatalf("%d %s %s", code, out.String(), diag.String())
		}
		afterC, _ := os.ReadFile(cp)
		afterD, _ := os.ReadFile(dp)
		if !bytes.Equal(beforeC, afterC) || !bytes.Equal(beforeD, afterD) {
			t.Fatal("Agent wrote configuration")
		}
	}
	if wsdCalls.Load() != 2 || esclCalls.Load() != 0 {
		t.Fatal("reference ignored preferred protocol", d)
	}
}

func TestMissingReferenceDoesNotSelectAnotherDevice(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	writeTestConfig(t, `{"device":{"record_id":"scanner_0123456789abcdef0123456789abcdef"}}`)
	var calls atomic.Int32
	find := func(context.Context) ([]scannerDevice, []diagnostic, error) { calls.Add(1); return nil, nil, nil }
	var out, diag bytes.Buffer
	code := runWithDiscovery(context.Background(), []string{"--agent", "info"}, &out, &diag, find)
	var result commandResult
	json.Unmarshal(out.Bytes(), &result)
	if code == 0 || result.Error == nil || result.Error.Code != "device_record_not_found" || calls.Load() != 0 {
		t.Fatalf("%d %+v calls=%d", code, result, calls.Load())
	}
}

func TestReferenceIdentityConflictBlocksScanAfterIncompleteRediscovery(t *testing.T) {
	for _, variant := range []string{"missing", "invalid", "matching-advertisement"} {
		t.Run(variant, func(t *testing.T) {
			var posts atomic.Int32
			c := esclTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method == "POST" {
					posts.Add(1)
				}
				if !serveESCLInfo(w, r, strings.ReplaceAll(esclTestCapabilities(esclDefaultProfiles()), "serial-1", "DIFFERENT-SERIAL"), "Idle") {
					w.WriteHeader(503)
				}
			})
			initial := scannerDevice{Protocol: "escl", Endpoint: c.endpoint, SerialNumber: "serial-1", CapabilityUUID: identityUUID}
			if variant == "matching-advertisement" {
				initial.ID = identityUUID
			}
			if err := rememberDrives([]savedDevice{{scannerDevice: initial, Discovered: true}}); err != nil {
				t.Fatal(err)
			}
			d := persistedScanner(t, c.endpoint)
			if err := saveDefaultRecord(context.Background(), d.RecordID); err != nil {
				t.Fatal(err)
			}
			incomplete := scannerDevice{Protocol: "escl", Endpoint: c.endpoint, Endpoints: []scannerEndpoint{{Protocol: "wsd", Endpoint: "http://127.0.0.1:1/unconfirmed"}}}
			if variant != "missing" {
				incomplete.SerialNumber = "UNKNOWN"
				incomplete.CapabilityUUID = "not-a-uuid"
			}
			if variant == "matching-advertisement" {
				incomplete.ID = identityUUID
				incomplete.Endpoints = nil
			}
			if err := rememberDrives([]savedDevice{{scannerDevice: incomplete, Discovered: true}}); err != nil {
				t.Fatal(err)
			}
			preserved := persistedScanner(t, c.endpoint)
			if preserved.RecordID != d.RecordID || preserved.SerialNumber != "serial-1" || preserved.CapabilityUUID != identityUUID || len(scannerEndpoints(preserved)) != 1 {
				t.Fatalf("incomplete discovery erased evidence or added an unconfirmed endpoint: %+v", preserved)
			}
			cp, _ := configPath()
			dp, _ := drivesPath()
			beforeC, _ := os.ReadFile(cp)
			beforeD, _ := os.ReadFile(dp)
			var out, diag bytes.Buffer
			code := runWithDiscovery(context.Background(), []string{"--agent", "scan", "--manual=false", "-o", filepath.Join(t.TempDir(), "scan.jpg")}, &out, &diag, nil)
			var result commandResult
			json.Unmarshal(out.Bytes(), &result)
			afterC, _ := os.ReadFile(cp)
			afterD, _ := os.ReadFile(dp)
			if code == 0 || result.Error == nil || result.Error.Code != "device_identity_conflict" || posts.Load() != 0 || !bytes.Equal(beforeC, afterC) || !bytes.Equal(beforeD, afterD) {
				t.Fatalf("%d %+v POST=%d", code, result, posts.Load())
			}
		})
	}
}

func TestIdentityUUIDComparisonUsesOnlySameSource(t *testing.T) {
	a := "12345678-1234-5678-9012-123456789abc"
	b := "87654321-4321-5678-9012-123456789abc"
	d := scannerDevice{Protocol: "escl", Endpoint: "http://192.168.1.9:80/eSCL", ID: a}
	if err := checkScannerIdentity(&d, info{UUID: b}, "escl", d.Endpoint); err != nil {
		t.Fatal("advertised UUID incorrectly compared to capability UUID", err)
	}
	d.CapabilityUUID = a
	if err := checkScannerIdentity(&d, info{UUID: b}, "escl", d.Endpoint); !errors.Is(err, errIdentityConflict) {
		t.Fatal("capability UUID conflict not rejected", err)
	}
}

func TestBeforeScanCancellationStopsCreation(t *testing.T) {
	data := fixture(t)
	var jobs atomic.Int32
	c := testClient(t, func(w http.ResponseWriter, r *http.Request, action string, _ []byte) {
		if action == "GetScannerElements" {
			w.Write(data)
			return
		}
		jobs.Add(1)
		t.Errorf("scan submitted after cancellation: %s", action)
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c.beforeScan = func(pageCtx context.Context, _ info) error { cancel(); return pageCtx.Err() }
	_, err := c.scan(ctx, options{DPI: 300, Mode: "color", Area: region{Width: 8267, Height: 11690}}, filepath.Join(t.TempDir(), "scan.jpg"))
	if !errors.Is(err, context.Canceled) || jobs.Load() != 0 {
		t.Fatalf("%v jobs=%d", err, jobs.Load())
	}
}

func TestFailedReferenceSaveLeavesVerifiedRecord(t *testing.T) {
	data := fixture(t)
	var calls atomic.Int32
	c := testClient(t, func(w http.ResponseWriter, r *http.Request, action string, _ []byte) {
		calls.Add(1)
		if action != "GetScannerElements" {
			t.Errorf("unexpected scan %s", action)
		}
		w.Write(data)
	})
	cp, _ := configPath()
	if err := os.MkdirAll(cp, 0700); err != nil {
		t.Fatal(err)
	}
	d := scannerDevice{Protocol: "wsd", Endpoint: c.endpoint}
	find := func(context.Context) ([]scannerDevice, []diagnostic, error) { return []scannerDevice{d}, nil, nil }
	choose := func(context.Context, []scannerDevice) (string, error) { return d.Endpoint, nil }
	var out, diag bytes.Buffer
	result, code := executeCommand(context.Background(), commandRequest{command: "info", timeout: time.Second}, find, commandHooks{choose: choose}, &out, &diag)
	if code == 0 || result.Error == nil || calls.Load() != 1 {
		t.Fatalf("%d %+v calls=%d", code, result, calls.Load())
	}
	if stored := persistedScanner(t, c.endpoint); !validRecordID(stored.RecordID) {
		t.Fatal("verified device not retained")
	}
}
