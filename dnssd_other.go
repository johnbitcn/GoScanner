//go:build linux || (darwin && !cgo)

package main

import (
	"context"
)

func browseESCL(context.Context) ([]dnssdService, error) {
	return nil, diagnosticf("eSCL discovery requires macOS Bonjour and CGO. An eSCL address can still be specified explicitly.", "eSCL 自动发现需要 macOS Bonjour 和 CGO；仍可手动指定 eSCL 地址")
}
