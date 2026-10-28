package main

import (
	"context"
	"encoding/xml"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestDiscoveryPipelineOnLoopback(t *testing.T) {
	var metadataRequests atomic.Int32
	c := testClient(t, func(w http.ResponseWriter, r *http.Request, action string, _ []byte) {
		if action != "GetMetadata" {
			t.Errorf("discovery attempted %s", action)
		}
		metadataRequests.Add(1)
		_, _ = w.Write(metadataReply("http://"+r.Host+"/vendor/scan", "w:ScannerServiceType"))
	})
	server, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close() })
	done := make(chan error, 1)
	go func() {
		buffer := make([]byte, 8192)
		for i := 0; i < 2; i++ {
			_ = server.SetReadDeadline(time.Now().Add(2 * time.Second))
			n, peer, err := server.ReadFromUDP(buffer)
			if err != nil {
				done <- err
				return
			}
			var root element
			if err := xml.Unmarshal(buffer[:n], &root); err != nil {
				done <- err
				return
			}
			id := root.child(soapNS, "Header").child(addrNS, "MessageID").text()
			reply := probeReply(id, c.endpoint, "w:ScanDeviceType")
			// Each probe is duplicated, as is common with multiple interfaces/replies.
			for j := 0; j < 2; j++ {
				if _, err := server.WriteToUDP(reply, peer); err != nil {
					done <- err
					return
				}
			}
		}
		done <- nil
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	networks := []lanNetwork{{"loopback-test", netip.MustParseAddr("127.0.0.1"), netip.MustParsePrefix("127.0.0.0/8")}}
	devices, warnings, err := discoverOn(ctx, networks, server.LocalAddr().(*net.UDPAddr))
	if err != nil || len(warnings) != 0 || len(devices) != 1 {
		t.Fatalf("pipeline: %+v %v %v", devices, warnings, err)
	}
	if count := metadataRequests.Load(); count != 1 {
		t.Fatalf("metadata was not deduplicated: %d", count)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestDiscoveryDeadlineAndNoInterfaces(t *testing.T) {
	server, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	target := server.LocalAddr().(*net.UDPAddr)
	if _, _, err := discoverOn(context.Background(), nil, target); err == nil {
		t.Fatal("accepted no network interfaces")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	networks := []lanNetwork{{"loopback-test", netip.MustParseAddr("127.0.0.1"), netip.MustParsePrefix("127.0.0.0/8")}}
	if _, _, err := discoverOn(ctx, networks, target); err == nil {
		t.Fatal("discovery ignored deadline")
	}
}

func TestDiscoveryCandidateLimit(t *testing.T) {
	server, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close() })
	done := make(chan error, 1)
	go func() {
		buffer := make([]byte, 8192)
		_ = server.SetReadDeadline(time.Now().Add(2 * time.Second))
		n, peer, err := server.ReadFromUDP(buffer)
		if err != nil {
			done <- err
			return
		}
		var root element
		if err := xml.Unmarshal(buffer[:n], &root); err != nil {
			done <- err
			return
		}
		id := root.child(soapNS, "Header").child(addrNS, "MessageID").text()
		base := string(probeReply(id, "http://127.0.0.1:8018/wsd/0", "w:ScanDeviceType"))
		start := strings.Index(base, "<d:ProbeMatch>")
		end := strings.Index(base, "</d:ProbeMatch>") + len("</d:ProbeMatch>")
		match := base[start:end]
		// macOS rejects oversized UDP datagrams; model a flood with several small replies.
		for first := 0; first < 65; first += 16 {
			var matches strings.Builder
			for i := first; i < min(first+16, 65); i++ {
				matches.WriteString(strings.ReplaceAll(match, "/wsd/0", fmt.Sprintf("/wsd/%d", i)))
			}
			if _, err = server.WriteToUDP([]byte(base[:start]+matches.String()+base[end:]), peer); err != nil {
				done <- err
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
		done <- nil
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	networks := []lanNetwork{{"loopback-test", netip.MustParseAddr("127.0.0.1"), netip.MustParsePrefix("127.0.0.0/8")}}
	_, _, err = discoverOn(ctx, networks, server.LocalAddr().(*net.UDPAddr))
	if err == nil || !strings.Contains(err.Error(), "64") {
		select {
		case serverErr := <-done:
			t.Fatalf("candidate limit not enforced: %v; UDP sender: %v", err, serverErr)
		default:
			t.Fatalf("candidate limit not enforced: %v", err)
		}
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
