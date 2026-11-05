//go:build darwin || linux

package main

import "testing"

func TestReusedEndpointDoesNotInheritBackup(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	old := scannerDevice{Protocol: "escl", Endpoint: "http://192.168.1.9:80/eSCL", Endpoints: []scannerEndpoint{
		{Protocol: "escl", Endpoint: "http://192.168.1.9:80/eSCL"},
		{Protocol: "wsd", Endpoint: "http://192.168.1.8:8018/wsd/scan", ID: "urn:uuid:" + identityUUID},
	}}
	fresh := scannerDevice{Protocol: "escl", Endpoint: old.Endpoint, ID: "a1111111-2222-3333-4444-555555555555"}
	if sameScanner(old, fresh) {
		t.Fatal("same address is not identity evidence")
	}
	if err := rememberDrives([]savedDevice{{scannerDevice: old, Discovered: true}}); err != nil {
		t.Fatal(err)
	}
	before, err := loadDrives()
	if err != nil {
		t.Fatal(err)
	}
	oldRecordID := before[0].RecordID
	if err := rememberDrives([]savedDevice{{scannerDevice: fresh, Discovered: true}}); err != nil {
		t.Fatal(err)
	}
	got, err := loadDrives()
	if err != nil || len(got) != 1 || len(got[0].Endpoints) != 0 || got[0].ID != fresh.ID || got[0].RecordID == oldRecordID || len(got[0].RecordAliases) != 0 {
		t.Fatalf("%+v %v", got, err)
	}
}

func TestCapabilityUUIDCrossValidation(t *testing.T) {
	wsd := scannerDevice{Protocol: "wsd", Endpoint: "http://192.168.1.9:8018/wsd/scan", ID: identityUUID}
	escl := scannerDevice{Protocol: "escl", Endpoint: "http://192.168.1.9:80/eSCL", ID: "invalid", CapabilityUUID: identityUUID}
	if !sameScanner(wsd, escl) {
		t.Fatal("invalid TXT must not hide valid capability UUID")
	}
	escl.ID = identityUUID
	escl.CapabilityUUID = "a1111111-2222-3333-4444-555555555555"
	if sameScanner(wsd, escl) {
		t.Fatal("contradictory capability UUID must block UUID merge")
	}
	wsd.SerialNumber = "CANON-123"
	escl.SerialNumber = "CANON-123"
	if !sameScanner(wsd, escl) {
		t.Fatal("matching strong serial may confirm manufacturer UUID differences")
	}
}

func TestNewEvidenceConsolidatesPreviouslySeparateEntries(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	wsd := scannerDevice{Protocol: "wsd", Endpoint: "http://192.168.1.9:8018/wsd/scan", ID: identityUUID}
	escl := scannerDevice{Protocol: "escl", Endpoint: "http://192.168.1.9:80/eSCL", ID: "a1111111-2222-3333-4444-555555555555"}
	if err := rememberDrives([]savedDevice{{scannerDevice: wsd, Discovered: true}, {scannerDevice: escl, Discovered: true}}); err != nil {
		t.Fatal(err)
	}
	before, err := loadDrives()
	if err != nil || len(before) != 2 {
		t.Fatalf("%+v %v", before, err)
	}
	wsd.SerialNumber = "CANON-123"
	escl.SerialNumber = "CANON-123"
	merged := mergeScanner(wsd, escl)
	if err := rememberDrives([]savedDevice{{scannerDevice: merged, Discovered: true}}); err != nil {
		t.Fatal(err)
	}
	after, err := loadDrives()
	if err != nil || len(after) != 1 || len(after[0].Endpoints) != 2 {
		t.Fatalf("%+v %v", after, err)
	}
}

func TestESCLPartialConfigUsesProtocolDefaults(t *testing.T) {
	c, err := decodeConfig([]byte(`{"device":{"host":"192.168.1.9","protocol":"escl","scheme":"https"}}`), "test")
	if err != nil || c.Device.Port != 443 || c.Device.Path != "/eSCL" {
		t.Fatalf("%+v %v", c.Device, err)
	}
}
