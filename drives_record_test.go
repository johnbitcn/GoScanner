package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRecordIDsReadOnlyAndPersistence(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	path, _ := drivesPath()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	original := `[{"endpoint":"http://192.168.1.8/scan"}]`
	if err := os.WriteFile(path, []byte(original), 0600); err != nil {
		t.Fatal(err)
	}
	devices, err := loadDrives()
	if err != nil || devices[0].RecordID != "" {
		t.Fatalf("%+v %v", devices, err)
	}
	data, _ := os.ReadFile(path)
	if string(data) != original {
		t.Fatal("read changed table")
	}
	if err := rememberDrives([]savedDevice{{scannerDevice: scannerDevice{Endpoint: "http://192.168.1.9/scan"}}}); err != nil {
		t.Fatal(err)
	}
	devices, err = loadDrives()
	if err != nil || len(devices) != 2 || !validRecordID(devices[0].RecordID) || !validRecordID(devices[1].RecordID) || devices[0].RecordID == devices[1].RecordID {
		t.Fatalf("%+v %v", devices, err)
	}
}

func TestRecordIDsMergeAndReplacement(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	a := savedDevice{scannerDevice: scannerDevice{Endpoint: "http://192.168.1.8/scan", SerialNumber: "SERIAL-A"}, Discovered: true}
	b := savedDevice{scannerDevice: scannerDevice{Endpoint: "http://192.168.1.9/scan", SerialNumber: "SERIAL-B"}, Discovered: true}
	if err := rememberDrives([]savedDevice{a, b}); err != nil {
		t.Fatal(err)
	}
	before, err := loadDrives()
	if err != nil {
		t.Fatal(err)
	}
	// New matching serial evidence merges two existing records in the same transaction.
	if err := updateDrives(func(ds []savedDevice) ([]savedDevice, error) {
		ds[1].SerialNumber = ds[0].SerialNumber
		return mergeSavedDevices(ds, nil)
	}); err != nil {
		t.Fatal(err)
	}
	after, err := loadDrives()
	if err != nil || len(after) != 1 || after[0].RecordID != before[0].RecordID || len(after[0].RecordAliases) != 1 || after[0].RecordAliases[0] != before[1].RecordID {
		t.Fatalf("%+v %v", after, err)
	}
	replacement := savedDevice{scannerDevice: scannerDevice{Endpoint: after[0].Endpoint, SerialNumber: "SERIAL-NEW"}, Discovered: true}
	if err := rememberDrives([]savedDevice{replacement}); err != nil {
		t.Fatal(err)
	}
	after, err = loadDrives()
	if err != nil || len(after) != 1 || after[0].RecordID == before[0].RecordID || len(after[0].RecordAliases) != 0 {
		t.Fatalf("%+v %v", after, err)
	}
}

func TestRecordIDsRejectDuplicateAndInvalid(t *testing.T) {
	for _, data := range []string{
		`[{"endpoint":"http://192.168.1.8/scan","record_id":"scanner_BAD"}]`,
		`[{"endpoint":"http://192.168.1.8/scan","record_id":"scanner_0123456789abcdef0123456789abcdef","record_aliases":["scanner_0123456789abcdef0123456789abcdef"]}]`,
	} {
		t.Run(data, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			path, _ := drivesPath()
			os.MkdirAll(filepath.Dir(path), 0700)
			if err := os.WriteFile(path, []byte(data), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := loadDrives(); err == nil {
				t.Fatal("accepted invalid IDs")
			}
		})
	}
}

func TestRecordIDsPreferenceConflictKeepsTable(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	first := savedDevice{scannerDevice: scannerDevice{Endpoint: "http://192.168.1.8/scan", Protocol: "wsd", SerialNumber: "SERIAL-ONE", PreferredProtocol: "wsd"}, Discovered: true}
	second := savedDevice{scannerDevice: scannerDevice{Endpoint: "http://192.168.1.9/eSCL", Protocol: "escl", SerialNumber: "SERIAL-TWO", PreferredProtocol: "escl"}, Discovered: true}
	if err := rememberDrives([]savedDevice{first, second}); err != nil {
		t.Fatal(err)
	}
	path, _ := drivesPath()
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	err = updateDrives(func(ds []savedDevice) ([]savedDevice, error) {
		ds[1].SerialNumber = ds[0].SerialNumber
		return mergeSavedDevices(ds, nil)
	})
	if err == nil {
		t.Fatal("conflicting preferences accepted")
	}
	after, err := os.ReadFile(path)
	if err != nil || string(before) != string(after) {
		t.Fatalf("table changed after failed merge: %v", err)
	}
}
