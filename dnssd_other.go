//go:build linux || (darwin && !cgo)

package main

import (
	"context"
	"errors"
)

func browseESCL(context.Context) ([]dnssdService, error) {
	return nil, errors.New("eSCL 自动发现需要 macOS Bonjour 和 CGO；仍可手动指定 eSCL 地址")
}
