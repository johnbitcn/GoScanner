package main

import (
	"errors"
	"net"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
)

type deviceConfig struct {
	Host string `json:"host"`
	Port int    `json:"port"`
	Path string `json:"path"`
}

func (d deviceConfig) endpoint() (string, error) {
	ip, err := netip.ParseAddr(d.Host)
	if err != nil {
		return "", errors.New("device.host / --host 必须是 IP 地址，不是 URL 或主机名")
	}
	if d.Port < 1 || d.Port > 65535 {
		return "", errors.New("device.port / --port 必须是 1 到 65535 的整数")
	}
	if !strings.HasPrefix(d.Path, "/") || strings.ContainsAny(d.Path, "?#\r\n\t ") {
		return "", errors.New("device.path / --path 必须以 / 开头，不含空白、查询参数或片段")
	}
	base := url.URL{Scheme: "http", Host: net.JoinHostPort(ip.Unmap().String(), strconv.Itoa(d.Port))}
	return checkedEndpoint(base.String()+d.Path, netip.Addr{})
}

func deviceFromEndpoint(raw string) (deviceConfig, error) {
	endpoint, err := checkedEndpoint(raw, netip.Addr{})
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
	if _, err := d.endpoint(); err != nil {
		return deviceConfig{}, err
	}
	return d, nil
}
