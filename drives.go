//go:build darwin || linux

package main

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
)

const maxDrivesBytes = 1 << 20

type savedDevice struct {
	scannerDevice
	RecordAliases []string `json:"record_aliases,omitempty"`
	Discovered    bool     `json:"discovered"`
	Verified      bool     `json:"verified"`
	Info          *info    `json:"info,omitempty"`
}

func drivesPath() (string, error) {
	path, err := configPath()
	return filepath.Join(filepath.Dir(path), "drives.json"), err
}

func loadDrives() ([]savedDevice, error) {
	path, err := drivesPath()
	if err != nil {
		return nil, err
	}
	stat, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return []savedDevice{}, nil
	}
	if err != nil {
		return nil, err
	}
	if !stat.Mode().IsRegular() {
		return nil, fmt.Errorf("设备信息表 %s 不是普通文件，拒绝读取或替换", path)
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxDrivesBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxDrivesBytes {
		return nil, errors.New("设备信息表超过 1 MiB")
	}
	var devices []savedDevice
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&devices); err != nil {
		return nil, fmt.Errorf("解析设备信息表 %s：%w", path, err)
	}
	if devices == nil {
		return nil, errors.New("设备信息表必须是 JSON 数组，空表使用 []")
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return nil, errors.New("设备信息表只能包含一个 JSON 数组")
	}
	seen := make(map[string]bool)
	for i := range devices {
		endpoint, err := checkedScannerEndpoint(devices[i].Endpoint, devices[i].Protocol)
		if err != nil {
			return nil, fmt.Errorf("设备信息表地址无效：%w", err)
		}
		devices[i].Endpoint = endpoint
		if len(devices[i].Endpoints) > 16 {
			return nil, errors.New("每台设备最多保存 16 个协议地址")
		}
		for j := range devices[i].Endpoints {
			e := &devices[i].Endpoints[j]
			raw, err := checkedScannerEndpoint(e.Endpoint, e.Protocol)
			if err != nil {
				return nil, fmt.Errorf("设备备用地址无效：%w", err)
			}
			e.Endpoint = raw
			e.Protocol = protocolName(e.Protocol)
		}
		if preferred := devices[i].PreferredProtocol; preferred != "" {
			if preferred != "wsd" && preferred != "escl" {
				return nil, errors.New("设备首选协议必须是 wsd 或 escl")
			}
			e, err := devices[i].scannerDevice.endpointFor(preferred)
			if err != nil {
				return nil, err
			}
			if e.Endpoint != devices[i].Endpoint {
				devices[i].Info = nil
				devices[i].Verified = false
			}
			devices[i].scannerDevice = devices[i].scannerDevice.withEndpoint(e)
		}
		if seen[devices[i].Endpoint] {
			return nil, errors.New("设备信息表包含重复扫描地址")
		}
		seen[devices[i].Endpoint] = true
	}
	if err := validateRecordIDs(devices); err != nil {
		return nil, err
	}
	return devices, nil
}

func validRecordID(id string) bool {
	if len(id) != len("scanner_")+32 || id[:len("scanner_")] != "scanner_" {
		return false
	}
	for _, c := range id[len("scanner_"):] {
		if !('0' <= c && c <= '9') && !('a' <= c && c <= 'f') {
			return false
		}
	}
	return true
}

func validateRecordIDs(devices []savedDevice) error {
	seen := make(map[string]bool)
	for _, d := range devices {
		ids := append([]string(nil), d.RecordAliases...)
		if d.RecordID != "" {
			ids = append(ids, d.RecordID)
		} else if len(ids) != 0 {
			return errors.New("设备记录别名缺少主 ID")
		}
		for _, id := range ids {
			if !validRecordID(id) || seen[id] {
				return errors.New("设备记录 ID 格式无效或重复")
			}
			seen[id] = true
		}
	}
	return nil
}

func fillRecordIDs(devices []savedDevice) error {
	used := make(map[string]bool)
	for _, d := range devices {
		used[d.RecordID] = true
		for _, id := range d.RecordAliases {
			used[id] = true
		}
	}
	for i := range devices {
		if devices[i].RecordID != "" {
			continue
		}
		for {
			var random [16]byte
			if _, err := rand.Read(random[:]); err != nil {
				return err
			}
			id := "scanner_" + hex.EncodeToString(random[:])
			if !used[id] {
				devices[i].RecordID = id
				used[id] = true
				break
			}
		}
	}
	return nil
}

func mergeRecordIDs(a *savedDevice, b savedDevice) {
	if a.RecordID == "" {
		a.RecordID = b.RecordID
	}
	seen := map[string]bool{a.RecordID: true}
	for _, id := range a.RecordAliases {
		seen[id] = true
	}
	for _, id := range append([]string{b.RecordID}, b.RecordAliases...) {
		if id != "" && !seen[id] {
			a.RecordAliases = append(a.RecordAliases, id)
			seen[id] = true
		}
	}
}

// Serialize read/merge/rename across processes; keep the lock inode after unlocking.
func rememberDrives(updates []savedDevice) error {
	if len(updates) == 0 {
		return nil
	}
	return updateDrives(func(devices []savedDevice) ([]savedDevice, error) { return mergeSavedDevices(devices, updates) })
}

func updateDrives(change func([]savedDevice) ([]savedDevice, error)) error {
	path, err := drivesPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	lock, err := os.OpenFile(filepath.Join(filepath.Dir(path), ".drives.lock"), os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	devices, err := loadDrives()
	if err != nil {
		return err
	}
	if err := fillRecordIDs(devices); err != nil {
		return err
	}
	devices, err = change(devices)
	if err != nil {
		return err
	}
	if err := validateRecordIDs(devices); err != nil {
		return err
	}
	if err := fillRecordIDs(devices); err != nil {
		return err
	}
	for _, d := range devices {
		if len(d.Endpoints) > 16 {
			return errors.New("每台设备最多保存 16 个协议地址，未写入")
		}
	}
	data, err := json.MarshalIndent(devices, "", "  ")
	if err != nil {
		return err
	}
	if len(data)+1 > maxDrivesBytes {
		return errors.New("设备信息表超过 1 MiB，未写入")
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".drives-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	defer file.Close()
	if _, err := file.Write(append(data, '\n')); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), path)
}

func mergeSavedDevices(devices, updates []savedDevice) ([]savedDevice, error) {
	for _, update := range updates {
		endpoint, err := checkedScannerEndpoint(update.Endpoint, update.Protocol)
		if err != nil {
			return nil, err
		}
		update.Endpoint = endpoint
		index := -1
		for _, e := range scannerEndpoints(update.scannerDevice) {
			if _, err := checkedScannerEndpoint(e.Endpoint, e.Protocol); err != nil {
				return nil, err
			}
		}
		// Refresh the advertised endpoint before considering cross-protocol identity.
		for i := range devices {
			if deviceHasEndpoint(devices[i].scannerDevice, endpoint) {
				index = i
				break
			}
		}
		if index < 0 {
			for _, e := range scannerEndpoints(update.scannerDevice) {
				for i := range devices {
					if deviceHasEndpoint(devices[i].scannerDevice, e.Endpoint) {
						index = i
						break
					}
				}
				if index >= 0 {
					break
				}
			}
		}
		if index >= 0 && !update.Discovered && update.Info != nil && update.ID == "" && !conflictingSerials(devices[index].scannerDevice, update.scannerDevice) {
			for _, e := range scannerEndpoints(devices[index].scannerDevice) {
				if e.Endpoint == endpoint && e.Protocol == protocolName(update.Protocol) {
					update.ID = e.ID
					break
				}
			}
		}
		if index < 0 {
			for i := range devices {
				if sameScanner(devices[i].scannerDevice, update.scannerDevice) {
					index = i
					break
				}
			}
		}
		if index < 0 {
			devices = append(devices, update)
			continue
		}
		old := &devices[index]
		// An identity-free info response updates only this endpoint, not device ownership.
		if update.Verified && !update.Discovered && update.ID == "" && update.CapabilityUUID == "" && update.SerialNumber == "" && deviceHasEndpoint(old.scannerDevice, endpoint) {
			if old.Endpoint == endpoint {
				old.Verified = true
				if update.Info != nil {
					old.Info = update.Info
				}
			}
			continue
		}
		if !sameScanner(old.scannerDevice, update.scannerDevice) {
			// Missing identity evidence cannot establish that an existing endpoint changed ownership.
			if !endpointIdentityChanged(old.scannerDevice, update.scannerDevice) {
				knownHere := false
				for _, e := range scannerEndpoints(old.scannerDevice) {
					if e.Endpoint == endpoint && e.Protocol == protocolName(update.Protocol) && (e.ID != "" || serialIdentity(e.SerialNumber) != "" || uuidIdentity(e.CapabilityUUID) != "") {
						knownHere = true
						break
					}
				}
				if !identityFree(old.scannerDevice) && !knownHere && !identityFree(update.scannerDevice) {
					// A newly identified anonymous endpoint cannot borrow another endpoint's identity.
					*old = update
					continue
				}
				if !identityFree(old.scannerDevice) {
					// Keep identity evidence and accept metadata only for already-associated endpoints.
					known := scannerEndpoints(old.scannerDevice)
					for _, incoming := range scannerEndpoints(update.scannerDevice) {
						for _, e := range known {
							if e.Protocol != incoming.Protocol || e.Endpoint != incoming.Endpoint {
								continue
							}
							metadata := scannerDevice{Protocol: incoming.Protocol, Endpoint: incoming.Endpoint, ID: incoming.ID, SerialNumber: incoming.SerialNumber, CapabilityUUID: incoming.CapabilityUUID, Name: update.Name, Model: update.Model, Interface: update.Interface}
							if serialIdentity(metadata.SerialNumber) == "" {
								metadata.SerialNumber = ""
							}
							if uuidIdentity(metadata.CapabilityUUID) == "" {
								metadata.CapabilityUUID = ""
							}
							old.scannerDevice = mergeScanner(old.scannerDevice, metadata)
						}
					}
					old.Discovered = old.Discovered || update.Discovered
					if update.Discovered {
						old.Verified = false
						old.Info = nil
					}
					continue
				}
				update.RecordID, update.RecordAliases = old.RecordID, old.RecordAliases
				if old.PreferredProtocol != "" {
					if _, err := update.scannerDevice.endpointFor(old.PreferredProtocol); err != nil {
						return nil, errors.New("无法确认原首选协议的关联；保留原记录，请重新发现或确认设备")
					}
					update.PreferredProtocol = old.PreferredProtocol
				}
			}
			// A different discovery ID at the same address must not inherit verification.
			*old = update
			continue
		}
		if old.PreferredProtocol != "" && update.PreferredProtocol != "" && old.PreferredProtocol != update.PreferredProtocol {
			return nil, errors.New("设备首选协议冲突；请先统一偏好")
		}
		mergeRecordIDs(old, update)
		previousEndpoint := old.Endpoint
		old.scannerDevice = mergeScanner(old.scannerDevice, update.scannerDevice)
		if old.Endpoint != previousEndpoint {
			old.Info = nil
			old.Verified = false
		}
		old.Discovered = old.Discovered || update.Discovered
		old.Verified = old.Verified || (update.Verified && update.Endpoint == old.Endpoint)
		if update.Info != nil && update.Endpoint == old.Endpoint {
			old.Info = update.Info
		}
	}
	// New serial evidence can join entries that earlier discoveries could not verify.
	for i := 0; i < len(devices); i++ {
		for j := i + 1; j < len(devices); {
			if !sameScanner(devices[i].scannerDevice, devices[j].scannerDevice) {
				j++
				continue
			}
			a, b := devices[i], devices[j]
			if a.PreferredProtocol != "" && b.PreferredProtocol != "" && a.PreferredProtocol != b.PreferredProtocol {
				return nil, errors.New("设备首选协议冲突；请先统一偏好")
			}
			mergeRecordIDs(&a, b)
			a.scannerDevice = mergeScanner(a.scannerDevice, b.scannerDevice)
			a.Discovered = a.Discovered || b.Discovered
			if a.Endpoint == b.Endpoint && b.Info != nil {
				a.Info = b.Info
				a.Verified = b.Verified
			} else if a.Endpoint != devices[i].Endpoint {
				a.Info = nil
				a.Verified = false
			}
			devices[i] = a
			devices = append(devices[:j], devices[j+1:]...)
		}
	}
	return devices, nil
}

func savePreferredProtocol(recordID, protocol string) error {
	return updateDrives(func(devices []savedDevice) ([]savedDevice, error) {
		for i := range devices {
			if !recordMatches(devices[i], recordID) {
				continue
			}
			e, err := devices[i].scannerDevice.endpointFor(protocol)
			if err != nil {
				return nil, err
			}
			if e.Endpoint != devices[i].Endpoint {
				devices[i].Info = nil
				devices[i].Verified = false
			}
			devices[i].PreferredProtocol = protocolName(protocol)
			devices[i].scannerDevice = devices[i].scannerDevice.withEndpoint(e)
			return devices, nil
		}
		return nil, errors.New("设备记录已不存在；请重新发现设备")
	})
}
