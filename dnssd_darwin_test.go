//go:build darwin && cgo

package main

import (
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestBonjourCallbackLifecycle(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	binary := filepath.Join(t.TempDir(), "dnssd-lifecycle")
	build := exec.CommandContext(ctx, "xcrun", "clang", "-std=c11", "testdata/dnssd_lifecycle.c", "-o", binary)
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("compile Bonjour stubs: %v\n%s", err, output)
	}
	if output, err := exec.CommandContext(ctx, binary).CombinedOutput(); err != nil {
		t.Fatalf("Bonjour callback lifecycle: %v\n%s", err, output)
	}
}

func TestBonjourAlreadyCancelledDoesNotBrowse(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	services, err := browseESCL(ctx)
	if !errors.Is(err, context.Canceled) || len(services) != 0 {
		t.Fatalf("%+v %v", services, err)
	}
}
