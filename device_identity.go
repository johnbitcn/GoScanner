//go:build darwin || linux

package main

import (
	"encoding/hex"
	"errors"
	"net/netip"
	"sort"
	"strings"
)

// Each transport keeps its own identity; some scanners use different UUIDs.
type scannerEndpoint struct {
	Protocol       string `json:"protocol"`
	Endpoint       string `json:"endpoint"`
	ID             string `json:"id,omitempty"`
	SerialNumber   string `json:"serial_number,omitempty"`
	CapabilityUUID string `json:"capability_uuid,omitempty"`
}

func protocolName(protocol string) string {
	if protocol == "" {
		return "wsd"
	}
	return strings.ToLower(protocol)
}

func checkedScannerEndpoint(raw, protocol string) (string, error) {
	switch protocolName(protocol) {
	case "wsd":
		return checkedEndpoint(raw, netip.Addr{})
	case "escl":
		return checkedESCLEndpoint(raw)
	default:
		return "", errors.New("协议必须是 wsd 或 escl")
	}
}

func scannerEndpoints(d scannerDevice) []scannerEndpoint {
	endpoints := append([]scannerEndpoint(nil), d.Endpoints...)
	for i := range endpoints {
		endpoints[i].Protocol = protocolName(endpoints[i].Protocol)
	}
	if d.Endpoint != "" {
		found := false
		for i := range endpoints {
			if endpoints[i].Endpoint == d.Endpoint && endpoints[i].Protocol == protocolName(d.Protocol) {
				if d.ID != "" {
					endpoints[i].ID = d.ID
				}
				if d.SerialNumber != "" {
					endpoints[i].SerialNumber = d.SerialNumber
				}
				if d.CapabilityUUID != "" {
					endpoints[i].CapabilityUUID = d.CapabilityUUID
				}
				found = true
				break
			}
		}
		if !found {
			endpoints = append(endpoints, scannerEndpoint{Protocol: protocolName(d.Protocol), Endpoint: d.Endpoint, ID: d.ID, SerialNumber: d.SerialNumber, CapabilityUUID: d.CapabilityUUID})
		}
	}
	return endpoints
}

func uuidIdentity(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.TrimPrefix(strings.TrimPrefix(s, "urn:uuid:"), "uuid:")
	s = strings.Trim(s, "{}")
	if len(s) != 36 || s[8] != '-' || s[13] != '-' || s[18] != '-' || s[23] != '-' {
		return ""
	}
	raw := strings.ReplaceAll(s, "-", "")
	if _, err := hex.DecodeString(raw); err != nil || raw == strings.Repeat("0", 32) || raw == strings.Repeat("f", 32) {
		return ""
	}
	return s
}

func serialIdentity(s string) string {
	s = strings.ToUpper(strings.TrimSpace(s))
	switch s {
	case "", "UNKNOWN", "NONE", "N/A", "NA", "NULL", "DEFAULT", "SERIALNUMBER", "SERIAL NUMBER", "123456789", "1234567890":
		return ""
	}
	if len(s) < 3 || len(s) > 256 || strings.Trim(s, "0- ") == "" {
		return ""
	}
	if strings.Trim(s, string(s[0])) == "" {
		return ""
	}
	return s
}

func endpointUUID(e scannerEndpoint) string {
	advertised, capability := uuidIdentity(e.ID), uuidIdentity(e.CapabilityUUID)
	if advertised != "" && capability != "" && advertised != capability {
		return ""
	}
	if capability != "" {
		return capability
	}
	return advertised
}

func conflictingSerials(a, b scannerDevice) bool {
	for _, x := range scannerEndpoints(a) {
		for _, y := range scannerEndpoints(b) {
			sx, sy := serialIdentity(x.SerialNumber), serialIdentity(y.SerialNumber)
			if sx != "" && sy != "" && sx != sy {
				return true
			}
		}
	}
	return false
}

func sameScanner(a, b scannerDevice) bool {
	ae, be := scannerEndpoints(a), scannerEndpoints(b)
	// Contradictory serial numbers override weaker address or UUID evidence.
	if conflictingSerials(a, b) {
		return false
	}
	for _, x := range ae {
		for _, y := range be {
			if x.Protocol == y.Protocol && x.Endpoint == y.Endpoint && x.ID != "" && x.ID == y.ID && uuidIdentity(x.ID) == "" && x.CapabilityUUID == "" && y.CapabilityUUID == "" {
				return true
			}
			sx, sy := serialIdentity(x.SerialNumber), serialIdentity(y.SerialNumber)
			if sx != "" && sx == sy {
				return true
			}
			ux, uy := endpointUUID(x), endpointUUID(y)
			if ux != "" && ux == uy {
				return true
			}
		}
	}
	return false
}

func mergeScanner(a, b scannerDevice) scannerDevice {
	if a.RecordID == "" {
		a.RecordID = b.RecordID
	}
	if a.PreferredProtocol == "" {
		a.PreferredProtocol = b.PreferredProtocol
	}
	if b.Name != "" {
		a.Name = b.Name
	}
	if b.Model != "" {
		a.Model = b.Model
	}
	if b.Interface != "" {
		a.Interface = b.Interface
	}
	endpoints := scannerEndpoints(a)
	for _, e := range scannerEndpoints(b) {
		found := false
		for i := range endpoints {
			if endpoints[i].Protocol == e.Protocol && endpoints[i].Endpoint == e.Endpoint {
				if e.ID != "" {
					endpoints[i].ID = e.ID
				}
				// Placeholder responses must not erase usable identity evidence.
				if e.SerialNumber != "" && (serialIdentity(e.SerialNumber) != "" || serialIdentity(endpoints[i].SerialNumber) == "") {
					endpoints[i].SerialNumber = e.SerialNumber
				}
				if e.CapabilityUUID != "" && (uuidIdentity(e.CapabilityUUID) != "" || uuidIdentity(endpoints[i].CapabilityUUID) == "") {
					endpoints[i].CapabilityUUID = e.CapabilityUUID
				}
				found = true
				break
			}
		}
		if !found {
			endpoints = append(endpoints, e)
		}
	}
	sort.SliceStable(endpoints, func(i, j int) bool {
		if endpoints[i].Protocol != endpoints[j].Protocol {
			return endpoints[i].Protocol == "escl"
		}
		return endpoints[i].Endpoint < endpoints[j].Endpoint
	})
	if len(endpoints) > 1 || len(a.Endpoints) > 0 || len(b.Endpoints) > 0 {
		a.Endpoints = endpoints
	}
	if len(endpoints) > 0 {
		selected := endpoints[0]
		for _, e := range endpoints {
			if e.Protocol == a.PreferredProtocol {
				selected = e
				break
			}
		}
		a = a.withEndpoint(selected)
	}
	return a
}

func mergeDiscovered(devices []scannerDevice) []scannerDevice {
	result := make([]scannerDevice, 0, len(devices))
	for _, d := range devices {
		match := -1
		for i := range result {
			if sameScanner(result[i], d) {
				if match >= 0 {
					match = -2
					break
				}
				match = i
			}
		}
		if match >= 0 {
			result[match] = mergeScanner(result[match], d)
		} else {
			result = append(result, d)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Endpoint < result[j].Endpoint })
	return result
}

func scannerProtocolLabel(d scannerDevice) string {
	has := map[string]bool{}
	for _, e := range scannerEndpoints(d) {
		has[e.Protocol] = true
	}
	if has["escl"] && has["wsd"] {
		return "eSCL + WSD"
	}
	if has["escl"] {
		return "eSCL"
	}
	return "WSD"
}

func protocolLabel(protocol string) string {
	if protocolName(protocol) == "escl" {
		return "eSCL"
	}
	return "WSD"
}

func (d scannerDevice) withEndpoint(e scannerEndpoint) scannerDevice {
	d.Protocol, d.Endpoint, d.ID = e.Protocol, e.Endpoint, e.ID
	d.SerialNumber, d.CapabilityUUID = e.SerialNumber, e.CapabilityUUID
	return d
}

func (d scannerDevice) endpointFor(protocol string) (scannerEndpoint, error) {
	if protocol == "" {
		return scannerEndpoint{Protocol: protocolName(d.Protocol), Endpoint: d.Endpoint, ID: d.ID, SerialNumber: d.SerialNumber, CapabilityUUID: d.CapabilityUUID}, nil
	}
	for _, e := range scannerEndpoints(d) {
		if e.Protocol == protocolName(protocol) {
			return e, nil
		}
	}
	return scannerEndpoint{}, errors.New("所选设备未确认支持 " + protocol + "；未切换协议或启动扫描")
}

func deviceHasEndpoint(d scannerDevice, endpoint string) bool {
	for _, e := range scannerEndpoints(d) {
		if e.Endpoint == endpoint {
			return true
		}
		if normalized, err := checkedScannerEndpoint(endpoint, e.Protocol); err == nil && normalized == e.Endpoint {
			return true
		}
	}
	return false
}
