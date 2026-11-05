package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestDrivesMergeAndVerification(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	endpoint := "http://192.168.1.8:8018/wsd/scan"
	discovered := savedDevice{scannerDevice: scannerDevice{ID: "uuid:hp", Name: "HP", Model: "HP Laser MFP 1188w", Protocol: "wsd", Endpoint: endpoint, Interface: "en0"}, Discovered: true}
	if err := rememberDrives([]savedDevice{discovered}); err != nil {
		t.Fatal(err)
	}
	cap := info{State: "Idle", WidthsDPI: []int{150}, HeightsDPI: []int{150}}
	if err := rememberDrives([]savedDevice{{scannerDevice: scannerDevice{Endpoint: endpoint}, Verified: true, Info: &cap}}); err != nil {
		t.Fatal(err)
	}
	if err := rememberDrives([]savedDevice{discovered}); err != nil {
		t.Fatal(err)
	}
	devices, err := loadDrives()
	if err != nil || len(devices) != 1 || !devices[0].Verified || !devices[0].Discovered || devices[0].Model != discovered.Model || devices[0].Info == nil {
		t.Fatalf("devices=%+v err=%v", devices, err)
	}
	path, _ := drivesPath()
	if filepath.Base(path) != "drives.json" {
		t.Fatal(path)
	}
	stat, err := os.Stat(path)
	if err != nil || stat.Mode().Perm() != 0600 {
		t.Fatal("device table permissions", stat, err)
	}
	// A replacement device at the same address cannot inherit the old verification.
	discovered.ID, discovered.Model = "uuid:replacement", "Another scanner"
	if err := rememberDrives([]savedDevice{discovered}); err != nil {
		t.Fatal(err)
	}
	devices, err = loadDrives()
	if err != nil || devices[0].Verified || devices[0].Info != nil || devices[0].Model != "Another scanner" {
		t.Fatalf("stale verification: %+v %v", devices, err)
	}
}

func TestDrivesConcurrentUpdates(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	var wg sync.WaitGroup
	for i := range 12 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			device := savedDevice{scannerDevice: scannerDevice{Endpoint: fmt.Sprintf("http://192.168.1.%d:8018/wsd/scan", i+1)}, Discovered: true}
			if err := rememberDrives([]savedDevice{device}); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	devices, err := loadDrives()
	if err != nil || len(devices) != 12 {
		t.Fatalf("lost updates: %d %v", len(devices), err)
	}
}

func TestDrivesRejectInvalidDataWithoutOverwrite(t *testing.T) {
	for _, data := range []string{"not json", "null", "[] []", `[{"endpoint":"http://user:secret@192.168.1.8/scan"}]`, `[{"endpoint":"http://192.168.1.8/scan","unknown":true}]`, `[{"endpoint":"http://192.168.1.8/scan"},{"endpoint":"http://192.168.1.8/scan"}]`} {
		t.Run(data, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			path, _ := drivesPath()
			if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(data), 0600); err != nil {
				t.Fatal(err)
			}
			update := savedDevice{scannerDevice: scannerDevice{Endpoint: "http://192.168.1.9/scan"}}
			if err := rememberDrives([]savedDevice{update}); err == nil {
				t.Fatal("invalid table overwritten")
			}
			after, _ := os.ReadFile(path)
			if string(after) != data {
				t.Fatal("original data changed")
			}
		})
	}
}

func TestDrivesRejectSymlinkAndOversize(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	path, _ := drivesPath()
	os.MkdirAll(filepath.Dir(path), 0700)
	target := filepath.Join(t.TempDir(), "keep.json")
	if err := os.WriteFile(target, []byte("[]"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	if _, err := loadDrives(); err == nil {
		t.Fatal("symlink accepted")
	}
	if err := rememberDrives([]savedDevice{{scannerDevice: scannerDevice{Endpoint: "http://192.168.1.8/scan"}}}); err == nil {
		t.Fatal("symlink replaced")
	}
	after, _ := os.ReadFile(target)
	if string(after) != "[]" {
		t.Fatal("symlink target changed")
	}
	os.Remove(path)
	if err := os.WriteFile(path, bytes.Repeat([]byte(" "), maxDrivesBytes+1), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadDrives(); err == nil {
		t.Fatal("oversized table accepted")
	}
}

func TestDiscoveryAndInfoRememberDevicesButAgentDoesNot(t *testing.T) {
	data := fixture(t)
	c := testClient(t, func(w http.ResponseWriter, r *http.Request, action string, _ []byte) {
		if action != "GetScannerElements" {
			t.Errorf("unexpected action %s", action)
		}
		w.Write(data)
	})
	find := func(context.Context) ([]scannerDevice, []diagnostic, error) {
		return []scannerDevice{{Endpoint: c.endpoint, Model: "HP Laser MFP 1188w"}}, nil, nil
	}
	for _, args := range [][]string{{"--agent", "discover"}, {"--agent", "info", "--endpoint", c.endpoint}} {
		if code := runWithDiscovery(context.Background(), args, io.Discard, io.Discard, find); code != 0 {
			t.Fatal("agent failed", code)
		}
	}
	path, _ := drivesPath()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("Agent modified the registry", err)
	}
	if code := runWithDiscovery(context.Background(), []string{"discover"}, io.Discard, io.Discard, find); code != 0 {
		t.Fatal("discovery failed", code)
	}
	devices, err := loadDrives()
	if err != nil || len(devices) != 1 || devices[0].Verified || devices[0].Model == "" {
		t.Fatalf("%+v %v", devices, err)
	}
	if code := run(context.Background(), []string{"info", "--endpoint", c.endpoint}, io.Discard, io.Discard); code != 0 {
		t.Fatal("info failed", code)
	}
	devices, err = loadDrives()
	if err != nil || !devices[0].Verified || devices[0].Info == nil || devices[0].Model != "HP Laser MFP 1188w" {
		t.Fatalf("%+v %v", devices, err)
	}
	encoded, err := json.Marshal(devices)
	if err != nil || !bytes.Contains(encoded, []byte(`"model":"HP Laser MFP 1188w"`)) {
		t.Fatalf("%s %v", encoded, err)
	}
}
