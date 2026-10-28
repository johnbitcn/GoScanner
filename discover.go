//go:build darwin || linux

package main

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"maps"
	"net"
	"net/netip"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	discoveryNS     = "http://schemas.xmlsoap.org/ws/2005/04/discovery"
	metadataNS      = "http://schemas.xmlsoap.org/ws/2004/09/mex"
	profileNS       = "http://schemas.xmlsoap.org/ws/2006/02/devprof"
	discoveryWindow = 4 * time.Second
)

type scannerDevice struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Model     string `json:"model"`
	Protocol  string `json:"protocol"`
	Endpoint  string `json:"endpoint"`
	Interface string `json:"interface"`
}

type lanNetwork struct {
	name   string
	ip     netip.Addr
	prefix netip.Prefix
}
type probeMessage struct {
	id   string
	data []byte
}
type candidate struct {
	id, metadata, iface string
	peer                netip.Addr
}
type xmlScope struct {
	node     *element
	bindings map[string]string
}

func scoped(node *element, inherited map[string]string) (xmlScope, error) {
	bindings := maps.Clone(inherited)
	if bindings == nil {
		bindings = make(map[string]string)
	}
	for _, a := range node.Attrs {
		if a.Name.Space == "xmlns" {
			bindings[a.Name.Local] = a.Value
		}
		if a.Name.Space == "" && a.Name.Local == "xmlns" {
			bindings[""] = a.Value
		}
	}
	if len(bindings) > 128 {
		return xmlScope{}, errors.New("XML 命名空间数量超限")
	}
	return xmlScope{node, bindings}, nil
}

// Keep QName bindings from each ancestor, including locally rebound prefixes.
func scopedPath(root *element, path ...xml.Name) ([]xmlScope, error) {
	first, err := scoped(root, nil)
	if err != nil {
		return nil, err
	}
	current := []xmlScope{first}
	for _, name := range path {
		var next []xmlScope
		for _, parent := range current {
			for i := range parent.node.Children {
				node := &parent.node.Children[i]
				if node.XMLName != name {
					continue
				}
				child, err := scoped(node, parent.bindings)
				if err != nil {
					return nil, err
				}
				next = append(next, child)
				if len(next) > 256 {
					return nil, errors.New("XML 元素数量超限")
				}
			}
		}
		current = next
	}
	return current, nil
}

func (s xmlScope) hasType(parentNS, typeNS, name string) bool {
	node := s.node.child(parentNS, "Types")
	if node == nil {
		return false
	}
	types, err := scoped(node, s.bindings)
	if err != nil {
		return false
	}
	for _, text := range strings.Fields(node.text()) {
		prefix, local, found := strings.Cut(text, ":")
		if !found {
			local, prefix = prefix, ""
		}
		if local == name && types.bindings[prefix] == typeNS {
			return true
		}
	}
	return false
}

func discoveryProbes() ([]probeMessage, error) {
	var messages []probeMessage
	for _, typed := range []bool{false, true} {
		id, err := newMessageID()
		if err != nil {
			return nil, err
		}
		body := "<d:Probe/>"
		if typed {
			body = "<d:Probe><d:Types>w:ScanDeviceType</d:Types></d:Probe>"
		}
		data := fmt.Sprintf(`<?xml version="1.0" encoding="utf-8"?><s:Envelope xmlns:s="%s" xmlns:a="%s" xmlns:d="%s" xmlns:w="%s"><s:Header><a:MessageID>%s</a:MessageID><a:To>urn:schemas-xmlsoap-org:ws:2005:04:discovery</a:To><a:Action>%s/Probe</a:Action></s:Header><s:Body>%s</s:Body></s:Envelope>`, soapNS, addrNS, discoveryNS, scanNS, id, discoveryNS, body)
		messages = append(messages, probeMessage{id, []byte(data)})
	}
	return messages, nil
}

func parseMatches(data []byte, peer netip.Addr, ids map[string]bool) ([]candidate, error) {
	root, err := soapEnvelope(data)
	if err != nil {
		return nil, err
	}
	header := root.child(soapNS, "Header")
	if !ids[header.child(addrNS, "RelatesTo").text()] || header.child(addrNS, "Action").text() != discoveryNS+"/ProbeMatches" {
		return nil, errors.New("响应不属于当前发现请求")
	}
	matches, err := scopedPath(root, xml.Name{Space: soapNS, Local: "Body"}, xml.Name{Space: discoveryNS, Local: "ProbeMatches"}, xml.Name{Space: discoveryNS, Local: "ProbeMatch"})
	if err != nil {
		return nil, err
	}
	var result []candidate
	for _, match := range matches {
		if !match.hasType(discoveryNS, scanNS, "ScanDeviceType") {
			continue
		}
		id := match.node.child(addrNS, "EndpointReference").child(addrNS, "Address").text()
		if id == "" || len(id) > 512 {
			continue
		}
		addresses := strings.Fields(match.node.child(discoveryNS, "XAddrs").text())
		if len(addresses) > 8 {
			addresses = addresses[:8]
		}
		for _, raw := range addresses {
			if len(raw) > 4096 {
				continue
			}
			endpoint, err := checkedEndpoint(raw, peer)
			if err == nil {
				result = append(result, candidate{id: id, metadata: endpoint, peer: peer})
			}
		}
	}
	return result, nil
}

func localNetworks() ([]lanNetwork, error) {
	interfaces, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	var networks []lanNetwork
	for _, iface := range interfaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagMulticast == 0 || iface.Flags&(net.FlagLoopback|net.FlagPointToPoint) != 0 {
			continue
		}
		addresses, err := iface.Addrs()
		if err != nil {
			return nil, fmt.Errorf("读取接口 %s：%w", iface.Name, err)
		}
		for _, address := range addresses {
			prefix, err := netip.ParsePrefix(address.String())
			if err != nil || !prefix.Addr().Is4() {
				continue
			}
			networks = append(networks, lanNetwork{iface.Name, prefix.Addr(), prefix.Masked()})
			if len(networks) > 32 {
				return nil, errors.New("活动 IPv4 局域网地址超过 32 个")
			}
		}
	}
	if len(networks) == 0 {
		return nil, errors.New("没有可用的 IPv4 多播局域网接口")
	}
	return networks, nil
}

func probeInterface(ctx context.Context, network lanNetwork, target *net.UDPAddr, messages []probeMessage, out chan<- candidate) error {
	if ctx.Err() != nil {
		return nil
	}
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IP(network.ip.AsSlice())})
	if err != nil {
		return err
	}
	defer conn.Close()
	if target.IP.IsMulticast() {
		raw, err := conn.SyscallConn()
		if err != nil {
			return err
		}
		var optionErr error
		err = raw.Control(func(fd uintptr) {
			optionErr = syscall.SetsockoptInet4Addr(int(fd), syscall.IPPROTO_IP, syscall.IP_MULTICAST_IF, network.ip.As4())
			if optionErr == nil {
				optionErr = syscall.SetsockoptInt(int(fd), syscall.IPPROTO_IP, syscall.IP_MULTICAST_TTL, 2)
			}
		})
		if err != nil {
			return err
		}
		if optionErr != nil {
			return optionErr
		}
	}
	ids := make(map[string]bool)
	for _, message := range messages {
		ids[message.id] = true
	}
	buffer := make([]byte, 65536)
	sent, nextSend := 0, time.Now()
	for count := 0; count < 256; {
		if ctx.Err() != nil {
			return nil
		}
		// Retry read-only UDP probes, never scan jobs; keep IDs for reply correlation.
		if sent < len(messages)*3 && !time.Now().Before(nextSend) {
			if _, err := conn.WriteToUDP(messages[sent%len(messages)].data, target); err != nil {
				return err
			}
			sent++
			nextSend = time.Now().Add(500 * time.Millisecond)
		}
		if err := conn.SetReadDeadline(time.Now().Add(100 * time.Millisecond)); err != nil {
			return err
		}
		n, source, err := conn.ReadFromUDPAddrPort(buffer)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			if timeout, ok := err.(net.Error); ok && timeout.Timeout() {
				continue
			}
			return err
		}
		count++
		peer := source.Addr().Unmap()
		if !network.prefix.Contains(peer) || peer.IsUnspecified() || peer.IsMulticast() {
			continue
		}
		matches, err := parseMatches(buffer[:n], peer, ids)
		if err != nil {
			// Unrelated/untrusted UDP is not a discovered scanner.
			continue
		}
		for _, match := range matches {
			match.iface = network.name
			select {
			case out <- match:
			case <-ctx.Done():
				return nil
			}
		}
	}
	return errors.New("收到的数据包超过每接口 256 个限制")
}

func metadataDevices(data []byte, source candidate) ([]scannerDevice, error) {
	root, err := soapEnvelope(data)
	if err != nil {
		return nil, err
	}
	sections, err := scopedPath(root, xml.Name{Space: soapNS, Local: "Body"}, xml.Name{Space: metadataNS, Local: "Metadata"}, xml.Name{Space: metadataNS, Local: "MetadataSection"})
	if err != nil {
		return nil, err
	}
	var name, model string
	for _, section := range sections {
		if v := section.node.child(profileNS, "ThisDevice").child(profileNS, "FriendlyName").text(); v != "" {
			name = v
		}
		if v := section.node.child(profileNS, "ThisModel").child(profileNS, "ModelName").text(); v != "" {
			model = v
		}
	}
	clean := func(s string) string {
		s = strings.Join(strings.Fields(s), " ")
		if len(s) > 512 {
			s = s[:512]
		}
		return s
	}
	name, model = clean(name), clean(model)
	if name == "" {
		name = model
	}
	services, err := scopedPath(root, xml.Name{Space: soapNS, Local: "Body"}, xml.Name{Space: metadataNS, Local: "Metadata"}, xml.Name{Space: metadataNS, Local: "MetadataSection"}, xml.Name{Space: profileNS, Local: "Relationship"}, xml.Name{Space: profileNS, Local: "Hosted"})
	if err != nil {
		return nil, err
	}
	var devices []scannerDevice
	for _, service := range services {
		if !service.hasType(profileNS, scanNS, "ScannerServiceType") {
			continue
		}
		for i := range service.node.Children {
			epr := &service.node.Children[i]
			if epr.XMLName != (xml.Name{Space: addrNS, Local: "EndpointReference"}) {
				continue
			}
			endpoint, err := checkedEndpoint(epr.child(addrNS, "Address").text(), source.peer)
			if err == nil {
				devices = append(devices, scannerDevice{source.id, name, model, "wsd", endpoint, source.iface})
			}
		}
	}
	if len(devices) == 0 {
		return nil, errors.New("元数据没有响应源 IP 上的 WSD 扫描服务")
	}
	return devices, nil
}

func discover(ctx context.Context) ([]scannerDevice, []string, error) {
	networks, err := localNetworks()
	if err != nil {
		return nil, nil, err
	}
	return discoverOn(ctx, networks, &net.UDPAddr{IP: net.IPv4(239, 255, 255, 250), Port: 3702})
}

// Separating interface enumeration allows the entire pipeline to be tested on loopback.
func discoverOn(ctx context.Context, networks []lanNetwork, target *net.UDPAddr) ([]scannerDevice, []string, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	if len(networks) == 0 {
		return nil, nil, errors.New("没有可用的 IPv4 局域网接口")
	}
	messages, err := discoveryProbes()
	if err != nil {
		return nil, nil, err
	}
	listen, stop := context.WithTimeout(ctx, discoveryWindow)
	defer stop()
	packets := make(chan candidate, 64)
	failures := make(chan string, len(networks))
	var probes sync.WaitGroup
	for _, network := range networks {
		probes.Add(1)
		go func(n lanNetwork) {
			defer probes.Done()
			if err := probeInterface(listen, n, target, messages, packets); err != nil {
				failures <- fmt.Sprintf("接口 %s：%v", n.name, err)
			}
		}(network)
	}
	go func() { probes.Wait(); close(packets); close(failures) }()
	var candidates []candidate
	overflow := false
	seen := make(map[string]bool)
	for packet := range packets {
		if seen[packet.metadata] {
			continue
		}
		seen[packet.metadata] = true
		if len(candidates) >= 64 {
			overflow = true
			stop()
			continue
		}
		candidates = append(candidates, packet)
	}
	var warnings []string
	for warning := range failures {
		warnings = append(warnings, warning)
	}
	if err := ctx.Err(); err != nil {
		return nil, warnings, err
	}
	if overflow {
		return nil, warnings, errors.New("发现候选设备超过 64 个限制")
	}
	if len(warnings) == len(networks) {
		return nil, warnings, errors.New("所有局域网接口探测均失败")
	}
	return resolveCandidates(ctx, candidates, warnings)
}

func resolveCandidates(ctx context.Context, candidates []candidate, warnings []string) ([]scannerDevice, []string, error) {
	// Bound metadata traffic; only the advertising IP is permitted for every URL.
	jobs := make(chan candidate)
	type metadataResult struct {
		devices []scannerDevice
		warning string
	}
	results := make(chan metadataResult, len(candidates))
	var workers sync.WaitGroup
	for i := 0; i < min(4, len(candidates)); i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for source := range jobs {
				request, cancel := context.WithTimeout(ctx, 2*time.Second)
				raw, err := checkedEndpoint(source.metadata, source.peer)
				if !source.peer.IsValid() {
					err = errors.New("发现响应缺少源 IP")
				}
				var c *client
				if err == nil {
					c, err = newEndpointClient(raw)
				}
				var devices []scannerDevice
				if err == nil {
					_, data, fetchErr := c.post(request, "GetMetadata", "")
					err = fetchErr
					if err == nil {
						devices, err = metadataDevices(data, source)
					}
				}
				cancel()
				if err != nil {
					results <- metadataResult{warning: fmt.Sprintf("设备 %s 元数据验证失败：%v", source.peer, err)}
				} else {
					results <- metadataResult{devices: devices}
				}
			}
		}()
	}
	go func() {
		defer close(jobs)
		for _, c := range candidates {
			select {
			case jobs <- c:
			case <-ctx.Done():
				return
			}
		}
	}()
	go func() { workers.Wait(); close(results) }()
	devices := make([]scannerDevice, 0)
	seen := make(map[string]bool)
	for result := range results {
		if result.warning != "" {
			warnings = append(warnings, result.warning)
		}
		for _, device := range result.devices {
			if !seen[device.Endpoint] {
				seen[device.Endpoint] = true
				devices = append(devices, device)
			}
		}
	}
	sort.Slice(devices, func(i, j int) bool { return devices[i].Endpoint < devices[j].Endpoint })
	sort.Strings(warnings)
	return devices, warnings, ctx.Err()
}

func chooseDevice(devices []scannerDevice, warnings []string) (string, error) {
	if len(warnings) > 0 {
		return "", errors.New("发现过程不完整；请用 discover 查看结果，并用 --endpoint 显式选择设备")
	}
	if len(devices) == 0 {
		return "", errors.New("未发现 WSD 扫描仪；可唤醒设备后重试或使用 --host / --endpoint")
	}
	if len(devices) > 1 {
		return "", errors.New("发现多个扫描端点；请用 discover 查看结果，并用 --endpoint 显式选择设备")
	}
	return devices[0].Endpoint, nil
}
