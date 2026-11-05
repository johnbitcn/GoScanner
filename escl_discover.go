//go:build darwin || linux

package main

import (
	"context"
	"errors"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

type dnssdService struct {
	Name, Host, Address string
	Port, Interface     int
	Secure              bool
	TXT                 []byte
}

func parseDNSSDTXT(data []byte) (map[string]string, error) {
	if len(data) > 2048 {
		return nil, diagnosticf("DNS-SD TXT exceeds the 2048-byte limit.", "DNS-SD TXT 超过 2048 字节")
	}
	result := map[string]string{}
	for len(data) > 0 {
		n := int(data[0])
		data = data[1:]
		if n > len(data) {
			return nil, diagnosticf("Invalid DNS-SD TXT length.", "DNS-SD TXT 长度无效")
		}
		value := data[:n]
		data = data[n:]
		if n == 0 {
			continue
		}
		key, text, _ := strings.Cut(string(value), "=")
		key = strings.ToLower(key)
		if !utf8.ValidString(key) || !utf8.ValidString(text) || strings.ContainsAny(key, "\x00\r\n") || strings.ContainsAny(text, "\x00\r\n") {
			return nil, diagnosticf("DNS-SD TXT contains invalid text.", "DNS-SD TXT 包含无效文本")
		}
		if _, exists := result[key]; exists {
			return nil, diagnosticf("DNS-SD TXT contains duplicate fields.", "DNS-SD TXT 包含重复字段")
		}
		result[key] = text
	}
	return result, nil
}

func scannerFromDNSSD(s dnssdService, networks []lanNetwork) (scannerDevice, error) {
	ip, err := netip.ParseAddr(s.Address)
	if err != nil || !ip.Is4() || ip.IsUnspecified() || ip.IsMulticast() {
		return scannerDevice{}, diagnosticf("The Bonjour address is not a valid IPv4 unicast address.", "Bonjour 地址不是有效 IPv4 单播地址")
	}
	iface := ""
	for _, n := range networks {
		if n.prefix.Contains(ip) {
			iface = n.name
			break
		}
	}
	if iface == "" {
		return scannerDevice{}, diagnosticf("The Bonjour address is not on a local network.", "Bonjour 地址不属于本机局域网")
	}
	txt, err := parseDNSSDTXT(s.TXT)
	if err != nil {
		return scannerDevice{}, err
	}
	path := txt["rs"]
	if path == "" {
		path = "eSCL"
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	scheme := "http"
	if s.Secure {
		scheme = "https"
	}
	raw := scheme + "://" + ip.String() + ":" + strconv.Itoa(s.Port) + path
	endpoint, err := checkedESCLEndpoint(raw)
	if err != nil {
		return scannerDevice{}, err
	}
	// The advertised hostname is not a request target: requests stay pinned to this LAN IP.
	d := scannerDevice{ID: txt["uuid"], Name: strings.Join(strings.Fields(s.Name), " "), Model: txt["ty"], Protocol: "escl", Endpoint: endpoint, Interface: iface}
	if len(d.Name) > 512 || len(d.Model) > 512 || len(d.ID) > 512 {
		return scannerDevice{}, diagnosticf("Bonjour scanner information exceeds the length limit.", "Bonjour 设备信息过长")
	}
	return d, nil
}

func discoverESCL(ctx context.Context) ([]scannerDevice, []diagnostic, error) {
	networks, err := localNetworks()
	if err != nil {
		return nil, nil, err
	}
	services, err := browseESCL(ctx)
	if err != nil {
		return nil, nil, err
	}
	var devices []scannerDevice
	var warnings []diagnostic
	seen := map[string]bool{}
	for _, s := range services {
		d, err := scannerFromDNSSD(s, networks)
		if err != nil {
			warnings = append(warnings, diagnosticf("Rejected an eSCL discovery record: %v", "eSCL 发现记录被拒绝：%v", err))
			continue
		}
		if !seen[d.Endpoint] {
			seen[d.Endpoint] = true
			devices = append(devices, d)
		}
	}
	// Read-only capability probes enrich identity, never create a scan job.
	jobs := make(chan int)
	var workers sync.WaitGroup
	var mu sync.Mutex
	for i := 0; i < min(4, len(devices)); i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for i := range jobs {
				request, cancel := context.WithTimeout(ctx, 2*time.Second)
				c, err := newProtocolClient(devices[i].Endpoint, "escl")
				if err == nil {
					var cap info
					cap, err = c.info(request)
					if err == nil {
						devices[i].SerialNumber = cap.SerialNumber
						devices[i].CapabilityUUID = cap.UUID
						advertised, reported := uuidIdentity(devices[i].ID), uuidIdentity(cap.UUID)
						if advertised != "" && reported != "" && advertised != reported {
							mu.Lock()
							warnings = append(warnings, diagnosticf("The DNS-SD UUID and capability UUID for eSCL endpoint %q differ. This UUID will not be used for automatic merging.", "eSCL %s 的 DNS-SD UUID 与能力 UUID 不一致；不使用该 UUID 自动合并", devices[i].Endpoint))
							mu.Unlock()
						}
						if cap.MakeAndModel != "" {
							devices[i].Model = cap.MakeAndModel
						}
					}
				}
				cancel()
				if err != nil {
					mu.Lock()
					warnings = append(warnings, diagnosticf("Cannot read capabilities for eSCL endpoint %q: %v", "eSCL %s 能力读取失败：%v", devices[i].Endpoint, err))
					mu.Unlock()
				}
			}
		}()
	}
	for i := range devices {
		select {
		case jobs <- i:
		case <-ctx.Done():
		}
	}
	close(jobs)
	workers.Wait()
	return devices, warnings, ctx.Err()
}

func discoverBoth(ctx context.Context, wsd, escl discoveryFunc) ([]scannerDevice, []diagnostic, error) {
	type result struct {
		devices  []scannerDevice
		warnings []diagnostic
		err      error
		protocol string
	}
	results := make(chan result, 2)
	for _, backend := range []struct {
		name string
		find discoveryFunc
	}{{"WSD", wsd}, {"eSCL", escl}} {
		go func(name string, find discoveryFunc) { d, w, e := find(ctx); results <- result{d, w, e, name} }(backend.name, backend.find)
	}
	var devices []scannerDevice
	var warnings []diagnostic
	var failures []error
	for range 2 {
		r := <-results
		devices = append(devices, r.devices...)
		warnings = append(warnings, r.warnings...)
		if r.err != nil {
			failures = append(failures, r.err)
			warnings = append(warnings, diagnosticf("%s discovery failed: %v", "%s 发现失败：%v", r.protocol, r.err))
		}
	}
	if ctx.Err() != nil {
		return nil, warnings, ctx.Err()
	}
	if len(failures) == 2 {
		return nil, warnings, errors.Join(failures...)
	}
	return mergeDiscovered(devices), warnings, nil
}

func discover(ctx context.Context) ([]scannerDevice, []diagnostic, error) {
	return discoverBoth(ctx, discoverWSD, discoverESCL)
}
