package main

import (
	"net"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
)

type deviceConfig struct {
	RecordID string `json:"record_id,omitempty"`
	Host     string `json:"host,omitempty"`
	Port     int    `json:"port,omitempty"`
	Path     string `json:"path,omitempty"`
	Protocol string `json:"protocol,omitempty"`
	Scheme   string `json:"scheme,omitempty"`
}

func (d deviceConfig) endpoint() (string, error) {
	if d.RecordID != "" {
		return "", diagnosticf("A scanner record reference cannot be used as a direct service address.", "设备记录引用不能用作直接服务地址")
	}
	ip, err := netip.ParseAddr(d.Host)
	if err != nil {
		return "", diagnosticf("device.host / --host must be an IP address, not a URL or hostname.", "device.host / --host 必须是 IP 地址，不是 URL 或主机名")
	}
	if d.Port < 1 || d.Port > 65535 {
		return "", diagnosticf("device.port / --port must be an integer from 1 to 65535.", "device.port / --port 必须是 1 到 65535 的整数")
	}
	if !strings.HasPrefix(d.Path, "/") || strings.ContainsAny(d.Path, "?#\r\n\t ") {
		return "", diagnosticf("device.path / --path must start with / and contain no whitespace, query, or fragment.", "device.path / --path 必须以 / 开头，不含空白、查询参数或片段")
	}
	scheme := d.Scheme
	if scheme == "" {
		scheme = "http"
	}
	base := url.URL{Scheme: scheme, Host: net.JoinHostPort(ip.Unmap().String(), strconv.Itoa(d.Port))}
	return checkedScannerEndpoint(base.String()+d.Path, d.Protocol)
}

func deviceFromEndpoint(raw string) (deviceConfig, error) {
	return deviceFromProtocolEndpoint(raw, "")
}

func deviceFromProtocolEndpoint(raw, protocol string) (deviceConfig, error) {
	endpoint, err := checkedScannerEndpoint(raw, protocol)
	if err != nil {
		return deviceConfig{}, err
	}
	u, err := url.Parse(endpoint)
	if err != nil {
		return deviceConfig{}, err
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil {
		return deviceConfig{}, err
	}
	d := deviceConfig{Host: u.Hostname(), Port: port, Path: u.EscapedPath()}
	if protocolName(protocol) == "escl" {
		d.Protocol = "escl"
		d.Scheme = u.Scheme
	}
	if _, err := d.endpoint(); err != nil {
		return deviceConfig{}, err
	}
	return d, nil
}
