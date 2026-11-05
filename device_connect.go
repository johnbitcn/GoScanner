//go:build darwin || linux

package main

import (
	"context"
	"net/netip"
	"net/url"
)

func discoveryIP(raw string) (netip.Addr, error) {
	ip, err := netip.ParseAddr(raw)
	if err != nil || (!ip.IsGlobalUnicast() && !ip.IsLoopback()) {
		return netip.Addr{}, diagnosticf("Specify a valid scanner unicast IP address.", "请输入有效的扫描仪单播 IP 地址")
	}
	return ip.Unmap(), nil
}

func devicesAtIP(devices []scannerDevice, ip netip.Addr) []scannerDevice {
	var matched []scannerDevice
	for _, d := range devices {
		for _, e := range scannerEndpoints(d) {
			u, err := url.Parse(e.Endpoint)
			if err != nil {
				continue
			}
			address, err := netip.ParseAddr(u.Hostname())
			if err == nil && address.Unmap() == ip {
				matched = append(matched, d)
				break
			}
		}
	}
	return matched
}

func applySavedPreferences(devices []scannerDevice, saved []savedDevice) []scannerDevice {
	for i, d := range devices {
		match := -1
		for j, old := range saved {
			if sameScanner(old.scannerDevice, d) {
				if match >= 0 {
					match = -2
					break
				}
				match = j
			}
		}
		if match >= 0 {
			devices[i] = mergeScanner(saved[match].scannerDevice, d)
		}
	}
	return devices
}

// An IP is only a discovery filter, never a guessed scan-service address.
func discoverDeviceIP(ctx context.Context, raw string, find discoveryFunc) ([]scannerDevice, []diagnostic, error) {
	ip, err := discoveryIP(raw)
	if err != nil {
		return nil, nil, err
	}
	if find == nil {
		return nil, nil, diagnosticf("No scanner discovery service is available.", "没有可用的设备发现服务")
	}
	devices, warnings, err := find(ctx)
	if err == nil {
		err = ctx.Err()
	}
	if err != nil {
		return nil, warnings, err
	}
	devices = devicesAtIP(devices, ip)
	if len(devices) == 0 {
		return nil, warnings, diagnosticf("No scan service was discovered at IP %s. Check the scanner, power, local network permissions, and multicast network. No port or path was guessed.", "未发现 IP %s 的扫描服务；请检查设备、电源、本地网络权限和组播网络，未猜测端口或路径", ip)
	}
	return devices, warnings, nil
}

func resolveRequestDevice(ctx context.Context, r commandRequest, find discoveryFunc) (commandRequest, []scannerDevice, []diagnostic, error) {
	saved, err := loadDrives()
	if err != nil {
		return r, nil, nil, err
	}
	var matches, discovered []scannerDevice
	var warnings []diagnostic
	if r.recordID != "" {
		d, err := lookupRecord(saved, r.recordID)
		if err != nil {
			return r, nil, nil, err
		}
		matches = []scannerDevice{d.scannerDevice}
	} else if r.host != "" {
		ip, err := discoveryIP(r.host)
		if err != nil {
			return r, nil, nil, err
		}
		for _, d := range saved {
			matches = append(matches, d.scannerDevice)
		}
		matches = devicesAtIP(matches, ip)
		if len(matches) == 0 {
			discovered, warnings, err = discoverDeviceIP(ctx, r.host, find)
			if err != nil {
				return r, nil, warnings, err
			}
			matches = applySavedPreferences(discovered, saved)
		}
	} else {
		for _, d := range saved {
			if deviceHasEndpoint(d.scannerDevice, r.endpoint) {
				matches = append(matches, d.scannerDevice)
			}
		}
		// Keep explicit legacy addresses usable when they have not been registered.
		if len(matches) == 0 {
			return r, nil, nil, nil
		}
	}
	protocol := ""
	if r.protocolExplicit {
		protocol = r.protocol
	}
	var endpoints []scannerEndpoint
	for _, d := range matches {
		e, err := d.endpointFor(protocol)
		if err == nil {
			endpoints = append(endpoints, e)
			copy := d
			r.device = &copy
		}
	}
	if len(endpoints) != 1 {
		if len(endpoints) == 0 {
			return r, discovered, warnings, diagnosticf("The scanner registry does not confirm support for the specified protocol. The protocol was not switched.", "设备列表未确认支持所指定的协议；未切换协议")
		}
		return r, discovered, warnings, diagnosticf("This IP matches multiple scanner records whose common identity has not been confirmed. Select a scanner from the list, or explicitly specify --protocol / --endpoint.", "该 IP 对应多条尚未确认同一身份的设备记录；请在设备列表选择设备，或显式指定 --protocol / --endpoint")
	}
	r.endpoint, r.protocol, r.host = endpoints[0].Endpoint, endpoints[0].Protocol, ""
	if r.device != nil {
		r.recordID = r.device.RecordID
	}
	return r, discovered, warnings, nil
}
