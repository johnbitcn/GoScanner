package main

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"testing"
	"time"
)

func probeReply(id, addresses, types string) []byte {
	return []byte(fmt.Sprintf(`<?xml version="1.0"?><s:Envelope xmlns:s="%s" xmlns:a="%s" xmlns:d="%s" xmlns:w="%s" xmlns:other="urn:not-a-scanner"><s:Header><a:RelatesTo>%s</a:RelatesTo><a:Action>%s/ProbeMatches</a:Action></s:Header><s:Body><d:ProbeMatches><d:ProbeMatch><a:EndpointReference><a:Address>urn:uuid:example-scanner</a:Address></a:EndpointReference><d:Types>%s</d:Types><d:XAddrs>%s</d:XAddrs></d:ProbeMatch></d:ProbeMatches></s:Body></s:Envelope>`, soapNS, addrNS, discoveryNS, scanNS, escape(id), discoveryNS, types, escape(addresses)))
}

func metadataReply(endpoint, types string) []byte {
	return []byte(fmt.Sprintf(`<s:Envelope xmlns:s="%s" xmlns:a="%s" xmlns:m="%s" xmlns:p="%s" xmlns:w="%s"><s:Body><m:Metadata>
<m:MetadataSection><p:ThisDevice><p:FriendlyName>Example Scanner</p:FriendlyName></p:ThisDevice></m:MetadataSection>
<m:MetadataSection><p:ThisModel><p:ModelName>Example Model</p:ModelName></p:ThisModel></m:MetadataSection>
<m:MetadataSection><p:Relationship><p:Hosted><a:EndpointReference><a:Address>%s</a:Address></a:EndpointReference><p:Types>%s</p:Types></p:Hosted></p:Relationship></m:MetadataSection>
</m:Metadata></s:Body></s:Envelope>`, soapNS, addrNS, metadataNS, profileNS, scanNS, escape(endpoint), types))
}

func TestDiscoveryProbes(t *testing.T) {
	messages, err := discoveryProbes()
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 2 || messages[0].id == messages[1].id {
		t.Fatal("expected two distinct probe IDs")
	}
	for i, message := range messages {
		if !bytes.HasPrefix(message.data, []byte("<?xml")) {
			t.Fatal("missing explicit XML declaration")
		}
		root, err := soapEnvelope(message.data)
		if err != nil {
			t.Fatal(err)
		}
		if root.child(soapNS, "Header").child(addrNS, "MessageID").text() != message.id {
			t.Fatal("message ID mismatch")
		}
		probe := root.child(soapNS, "Body").child(discoveryNS, "Probe")
		if probe == nil || (probe.child(discoveryNS, "Types") != nil) != (i == 1) {
			t.Fatal("expected generic and scanner-specific probes")
		}
	}
}

func TestProbeMatchesValidation(t *testing.T) {
	peer := netip.MustParseAddr("192.0.2.10")
	ids := map[string]bool{"request-id": true}
	base := probeReply("request-id", "http://192.0.2.10:8018/wsd http://192.0.2.20/wsd", "w:ScanDeviceType")
	matches, err := parseMatches(base, peer, ids)
	if err != nil || len(matches) != 1 || matches[0].metadata != "http://192.0.2.10:8018/wsd" {
		t.Fatalf("matches: %+v %v", matches, err)
	}
	for name, data := range map[string][]byte{
		"wrong request":      probeReply("unrelated", "http://192.0.2.10/wsd", "w:ScanDeviceType"),
		"print only":         probeReply("request-id", "http://192.0.2.10/wsd", "w:PrintDeviceType"),
		"fake namespace":     probeReply("request-id", "http://192.0.2.10/wsd", "other:ScanDeviceType"),
		"shadowed namespace": bytes.Replace(base, []byte("<d:Types>"), []byte(`<d:Types xmlns:w="urn:wrong">`), 1),
		"external metadata":  probeReply("request-id", "http://198.51.100.1/wsd", "w:ScanDeviceType"),
		"missing id":         bytes.Replace(base, []byte("urn:uuid:example-scanner"), nil, 1),
		"DOCTYPE":            []byte("<!DOCTYPE x><x/>"),
		"wrong action":       bytes.Replace(base, []byte(discoveryNS+"/ProbeMatches"), []byte("urn:wrong"), 1),
	} {
		t.Run(name, func(t *testing.T) {
			result, _ := parseMatches(data, peer, ids)
			if len(result) != 0 {
				t.Fatalf("accepted invalid reply: %+v", result)
			}
		})
	}
	alias := bytes.Replace(base, []byte(`<d:Types>`), []byte(`<d:Types xmlns:alias="`+scanNS+`">`), 1)
	alias = bytes.Replace(alias, []byte("w:ScanDeviceType"), []byte("alias:ScanDeviceType"), 1)
	if result, err := parseMatches(alias, peer, ids); err != nil || len(result) != 1 {
		t.Fatalf("valid alias rejected: %v", err)
	}
}

func TestEndpointBoundaries(t *testing.T) {
	peer := netip.MustParseAddr("192.0.2.10")
	for _, raw := range []string{
		"http://192.0.2.20/wsd", "http://127.0.0.1/wsd", "http://scanner.local/wsd",
		"http://user:password@192.0.2.10/wsd", "http://192.0.2.10/wsd?token=example", "http://192.0.2.10/wsd#fragment",
		"https://192.0.2.10/wsd", "file:///wsd", "http://192.0.2.10:99999/wsd", "http://224.0.0.1/wsd", "http://0.0.0.0/wsd",
	} {
		if _, err := checkedEndpoint(raw, peer); err == nil {
			t.Errorf("accepted unsafe endpoint %q", raw)
		}
	}
	if result, err := checkedEndpoint("http://192.0.2.10", peer); err != nil || result != "http://192.0.2.10:80/" {
		t.Fatalf("normalization: %s %v", result, err)
	}
	if result, err := checkedEndpoint("http://[::ffff:192.0.2.10]:8018/wsd", peer); err != nil || result != "http://192.0.2.10:8018/wsd" {
		t.Fatalf("IPv4-mapped normalization: %s %v", result, err)
	}
}

func TestMetadataScannerTypeAndScope(t *testing.T) {
	source := candidate{id: "urn:uuid:example-scanner", peer: netip.MustParseAddr("192.0.2.10"), iface: "test-lan"}
	base := metadataReply("http://192.0.2.10:1234/custom/scan", "w:ScannerServiceType")
	devices, err := metadataDevices(base, source)
	if err != nil || len(devices) != 1 || devices[0].Endpoint != "http://192.0.2.10:1234/custom/scan" || devices[0].Name != "Example Scanner" {
		t.Fatalf("metadata: %+v %v", devices, err)
	}
	for name, data := range map[string][]byte{
		"print service":    metadataReply("http://192.0.2.10:1234/print", "w:PrinterServiceType"),
		"wrong address":    metadataReply("http://198.51.100.1/scan", "w:ScannerServiceType"),
		"shadowed prefix":  bytes.Replace(base, []byte("<p:Hosted>"), []byte(`<p:Hosted xmlns:w="urn:not-scanner">`), 1),
		"missing metadata": []byte(responseXML("OtherResponse", "")),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := metadataDevices(data, source); err == nil {
				t.Fatal("accepted invalid scanner service")
			}
		})
	}
	// A default namespace can supply the QName's namespace on a prefixed Types tag.
	defaultType := bytes.Replace(base, []byte("<p:Types>w:ScannerServiceType"), []byte(`<p:Types xmlns="`+scanNS+`">ScannerServiceType`), 1)
	if result, err := metadataDevices(defaultType, source); err != nil || len(result) != 1 {
		t.Fatalf("default QName namespace rejected: %v", err)
	}
}

func TestProbeInterfaceLocalUDP(t *testing.T) {
	server, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close() })
	messages, err := discoveryProbes()
	if err != nil {
		t.Fatal(err)
	}
	serverDone := make(chan error, 1)
	go func() {
		buffer := make([]byte, 8192)
		for range messages {
			_ = server.SetReadDeadline(time.Now().Add(2 * time.Second))
			n, peer, err := server.ReadFromUDP(buffer)
			if err != nil {
				serverDone <- err
				return
			}
			var root element
			if err := xml.Unmarshal(buffer[:n], &root); err != nil {
				serverDone <- err
				return
			}
			id := root.child(soapNS, "Header").child(addrNS, "MessageID").text()
			_, _ = server.WriteToUDP([]byte("unrelated UDP noise"), peer)
			_, err = server.WriteToUDP(probeReply(id, "http://127.0.0.1:8018/wsd", "w:ScanDeviceType"), peer)
			if err != nil {
				serverDone <- err
				return
			}
		}
		serverDone <- nil
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	out := make(chan candidate, 4)
	done := make(chan error, 1)
	network := lanNetwork{"loopback-test", netip.MustParseAddr("127.0.0.1"), netip.MustParsePrefix("127.0.0.0/8")}
	go func() { done <- probeInterface(ctx, network, server.LocalAddr().(*net.UDPAddr), messages, out) }()
	select {
	case match := <-out:
		if match.iface != "loopback-test" || match.peer != network.ip {
			t.Fatalf("unexpected source: %+v", match)
		}
	case <-ctx.Done():
		t.Fatal("UDP probe did not discover local simulated scanner")
	}
	if err := <-serverDone; err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("UDP listener did not stop")
	}
}

func TestResolveCandidatesAndSelection(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request, action string, _ []byte) {
		if action != "GetMetadata" {
			t.Errorf("unexpected action %s", action)
		}
		_, _ = w.Write(metadataReply("http://"+r.Host+"/vendor/scan", "w:ScannerServiceType"))
	})
	source := candidate{id: "example", metadata: c.endpoint, peer: netip.MustParseAddr("127.0.0.1"), iface: "test"}
	devices, warnings, err := resolveCandidates(context.Background(), []candidate{source, source}, nil)
	if err != nil || len(warnings) != 0 || len(devices) != 1 {
		t.Fatalf("resolve/dedupe: %+v %v %v", devices, warnings, err)
	}
	endpoint, err := chooseDevice(devices, warnings)
	if err != nil || !strings.HasSuffix(endpoint, "/vendor/scan") {
		t.Fatalf("single device selection: %s %v", endpoint, err)
	}
	for _, result := range []struct {
		devices  []scannerDevice
		warnings []string
	}{
		{nil, nil}, {append(devices, devices[0]), nil}, {devices, []string{"an interface failed"}},
	} {
		if _, err := chooseDevice(result.devices, result.warnings); err == nil {
			t.Fatal("unsafe automatic selection")
		}
	}
	invalid := source
	invalid.peer = netip.MustParseAddr("192.0.2.20")
	_, warnings, err = resolveCandidates(context.Background(), []candidate{invalid}, nil)
	if err != nil || len(warnings) != 1 {
		t.Fatal("missing warning for unsafe metadata candidate")
	}
}

func TestMetadataDeadlineAndFault(t *testing.T) {
	for _, scenario := range []string{"deadline", "fault"} {
		t.Run(scenario, func(t *testing.T) {
			c := testClient(t, func(w http.ResponseWriter, r *http.Request, action string, _ []byte) {
				if scenario == "deadline" {
					<-r.Context().Done()
					return
				}
				w.WriteHeader(http.StatusInternalServerError)
				io.WriteString(w, fmt.Sprintf(`<s:Envelope xmlns:s="%s"><s:Body><s:Fault><s:Reason><s:Text>Unavailable</s:Text></s:Reason></s:Fault></s:Body></s:Envelope>`, soapNS))
			})
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()
			source := candidate{id: "test", metadata: c.endpoint, peer: netip.MustParseAddr("127.0.0.1")}
			_, warnings, err := resolveCandidates(ctx, []candidate{source}, nil)
			if len(warnings) != 1 {
				t.Fatal("missing metadata failure warning")
			}
			if scenario == "deadline" && err == nil {
				t.Fatal("missing context error")
			}
		})
	}
}
