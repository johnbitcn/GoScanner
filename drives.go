//go:build darwin || linux

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"os"
	"path/filepath"
	"syscall"
)

const maxDrivesBytes = 1 << 20

type savedDevice struct {
	scannerDevice
	Discovered bool  `json:"discovered"`
	Verified   bool  `json:"verified"`
	Info       *info `json:"info,omitempty"`
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
		endpoint, err := checkedEndpoint(devices[i].Endpoint, netip.Addr{})
		if err != nil {
			return nil, fmt.Errorf("设备信息表地址无效：%w", err)
		}
		if seen[endpoint] {
			return nil, errors.New("设备信息表包含重复扫描地址")
		}
		seen[endpoint], devices[i].Endpoint = true, endpoint
	}
	return devices, nil
}

// Serialize read/merge/rename across processes; keep the lock inode after unlocking.
func rememberDrives(updates []savedDevice) error {
	if len(updates) == 0 {
		return nil
	}
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
	for _, update := range updates {
		endpoint, err := checkedEndpoint(update.Endpoint, netip.Addr{})
		if err != nil {
			return err
		}
		update.Endpoint = endpoint
		index := -1
		for i := range devices {
			if devices[i].Endpoint == endpoint {
				index = i
				break
			}
		}
		if index < 0 {
			devices = append(devices, update)
			continue
		}
		old := &devices[index]
		if old.ID != "" && update.ID != "" && old.ID != update.ID {
			// A different discovery ID at the same address must not inherit verification.
			*old = update
			continue
		}
		if update.ID != "" {
			old.ID = update.ID
		}
		if update.Name != "" {
			old.Name = update.Name
		}
		if update.Model != "" {
			old.Model = update.Model
		}
		if update.Protocol != "" {
			old.Protocol = update.Protocol
		}
		if update.Interface != "" {
			old.Interface = update.Interface
		}
		old.Discovered = old.Discovered || update.Discovered
		old.Verified = old.Verified || update.Verified
		if update.Info != nil {
			old.Info = update.Info
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
