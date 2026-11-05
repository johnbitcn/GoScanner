//go:build darwin || linux

package main

import (
	"context"
	"errors"
	"fmt"
)

var errRecordNotFound = errors.New("默认设备记录已失效；请重新选择设备")
var errIdentityConflict = errors.New("设备身份与已保存记录冲突；未登记默认设备或提交扫描任务")

func identityFree(d scannerDevice) bool {
	for _, e := range scannerEndpoints(d) {
		if e.ID != "" || e.CapabilityUUID != "" || e.SerialNumber != "" {
			return false
		}
	}
	return true
}

func endpointIdentityChanged(a, b scannerDevice) bool {
	if conflictingSerials(a, b) {
		return true
	}
	for _, old := range scannerEndpoints(a) {
		for _, current := range scannerEndpoints(b) {
			if old.Protocol != current.Protocol || old.Endpoint != current.Endpoint {
				continue
			}
			if old.ID != "" && current.ID != "" && old.ID != current.ID {
				u, v := uuidIdentity(old.ID), uuidIdentity(current.ID)
				if u == "" || v == "" || u != v {
					return true
				}
			}
			u, v := uuidIdentity(old.CapabilityUUID), uuidIdentity(current.CapabilityUUID)
			if u != "" && v != "" && u != v {
				return true
			}
		}
	}
	return false
}

func recordMatches(d savedDevice, id string) bool {
	if id == "" {
		return false
	}
	if d.RecordID == id {
		return true
	}
	for _, alias := range d.RecordAliases {
		if alias == id {
			return true
		}
	}
	return false
}

func lookupRecord(devices []savedDevice, id string) (savedDevice, error) {
	for _, d := range devices {
		if recordMatches(d, id) {
			return d, nil
		}
	}
	return savedDevice{}, fmt.Errorf("%w：%s", errRecordNotFound, id)
}

func ensureStoredRecordIDs() error {
	devices, err := loadDrives()
	if err != nil {
		return err
	}
	for _, d := range devices {
		if d.RecordID == "" {
			return updateDrives(func(ds []savedDevice) ([]savedDevice, error) { return ds, nil })
		}
	}
	return nil
}

func checkScannerIdentity(d *scannerDevice, cap info, protocol, endpoint string) error {
	if d == nil {
		return nil
	}
	serial := serialIdentity(cap.SerialNumber)
	for _, e := range scannerEndpoints(*d) {
		if old := serialIdentity(e.SerialNumber); old != "" && serial != "" && old != serial {
			return errIdentityConflict
		}
		if e.Protocol == protocolName(protocol) && e.Endpoint == endpoint {
			old, current := uuidIdentity(e.CapabilityUUID), uuidIdentity(cap.UUID)
			if old != "" && current != "" && old != current {
				return errIdentityConflict
			}
		}
	}
	return nil
}

// Return the ID from the same locked transaction that publishes the verified record.
func rememberVerifiedDevice(ctx context.Context, c *client, cap info) (scannerDevice, error) {
	if err := ctx.Err(); err != nil {
		return scannerDevice{}, err
	}
	update := savedDevice{scannerDevice: scannerDevice{Endpoint: c.endpoint, Protocol: protocolName(c.protocol), CapabilityUUID: cap.UUID, SerialNumber: cap.SerialNumber, Model: cap.MakeAndModel}, Verified: true, Info: &cap}
	var selected scannerDevice
	err := updateDrives(func(ds []savedDevice) ([]savedDevice, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var err error
		ds, err = mergeSavedDevices(ds, []savedDevice{update})
		if err != nil {
			return nil, err
		}
		if err = fillRecordIDs(ds); err != nil {
			return nil, err
		}
		for _, d := range ds {
			if deviceHasEndpoint(d.scannerDevice, c.endpoint) {
				selected = d.scannerDevice
				return ds, nil
			}
		}
		return nil, errRecordNotFound
	})
	return selected, err
}

func saveDefaultRecord(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return saveDefaultDevice(deviceConfig{RecordID: id})
}
