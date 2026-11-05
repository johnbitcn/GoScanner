package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func agentRun(t *testing.T, ctx context.Context, args []string, find discoveryFunc) (commandResult, int) {
	t.Helper()
	var out, stderr bytes.Buffer
	code := runWithDiscovery(ctx, append([]string{"--agent"}, args...), &out, &stderr, find)
	var result commandResult
	decoder := json.NewDecoder(&out)
	if err := decoder.Decode(&result); err != nil {
		t.Fatalf("invalid JSON: %v; stdout=%s stderr=%s", err, out.String(), stderr.String())
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		t.Fatalf("extra output: %v %s", err, out.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("agent stderr: %s", stderr.String())
	}
	return result, code
}

func TestAgentValidationAndNoInteraction(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	find := func(context.Context) ([]scannerDevice, []diagnostic, error) {
		t.Fatal("unexpected discovery")
		return nil, nil, nil
	}
	for _, tc := range []struct {
		args []string
		code int
		kind string
	}{
		{nil, 2, "invalid_arguments"},
		{[]string{"unknown"}, 2, "invalid_arguments"},
		{[]string{"scan", "--dpi", "0"}, 2, "invalid_arguments"},
		{[]string{"scan", "--wat"}, 2, "invalid_arguments"},
		{[]string{"scan", "--manual"}, 2, "unsupported_interaction"},
		{[]string{"scan"}, 1, "device_required"},
		{[]string{"info"}, 1, "device_required"},
	} {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			result, code := agentRun(t, context.Background(), tc.args, find)
			if code != tc.code || result.Status != "error" || result.Error == nil || result.Error.Code != tc.kind {
				t.Fatalf("code=%d result=%+v", code, result)
			}
		})
	}
	for _, args := range [][]string{{"--help"}, {"scan", "-h"}} {
		result, code := agentRun(t, context.Background(), args, find)
		if code != 0 || result.Status != "ok" || result.Help == "" {
			t.Fatalf("code=%d result=%+v", code, result)
		}
	}
	if _, err := os.Stat(filepath.Join(os.Getenv("HOME"), ".config", "goscanner", "config.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("configuration changed", err)
	}
}

func TestAgentDiscovery(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	result, code := agentRun(t, context.Background(), []string{"discover"}, func(context.Context) ([]scannerDevice, []diagnostic, error) {
		return nil, []diagnostic{diagnosticf("Partial discovery.", "发现不完整")}, nil
	})
	if code != 0 || result.Devices == nil || len(*result.Devices) != 0 || len(result.Warnings) != 1 {
		t.Fatalf("%d %+v", code, result)
	}
	result, code = agentRun(t, context.Background(), []string{"discover", "--timeout", "1ms"}, func(ctx context.Context) ([]scannerDevice, []diagnostic, error) {
		<-ctx.Done()
		return nil, nil, ctx.Err()
	})
	if code != 1 || result.Error.Code != "timeout" {
		t.Fatalf("%d %+v", code, result)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result, code = agentRun(t, ctx, []string{"discover"}, func(ctx context.Context) ([]scannerDevice, []diagnostic, error) { return nil, nil, ctx.Err() })
	if code != 130 || result.Error.Code != "cancelled" {
		t.Fatalf("%d %+v", code, result)
	}
}

func TestAgentScanAndInfo(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	data, image := fixture(t), jpegSample(t)
	var jobs atomic.Int32
	c := testClient(t, func(w http.ResponseWriter, r *http.Request, action string, _ []byte) {
		switch action {
		case "GetScannerElements":
			w.Write(data)
		case "CreateScanJob":
			jobs.Add(1)
			io.WriteString(w, responseXML("CreateScanJobResponse", "<w:JobId>7</w:JobId><w:JobToken>token</w:JobToken>"))
		case "RetrieveImage":
			ct, body := mtom(image, "boundary", "cid:image@scanner")
			w.Header().Set("Content-Type", ct)
			w.Write(body)
		default:
			t.Errorf("unexpected action %s", action)
		}
	})
	find := func(context.Context) ([]scannerDevice, []diagnostic, error) {
		t.Fatal("unexpected discovery")
		return nil, nil, nil
	}
	result, code := agentRun(t, context.Background(), []string{"info", "--endpoint", c.endpoint}, find)
	if code != 0 || result.Info == nil || result.Info.State != "Idle" || jobs.Load() != 0 {
		t.Fatalf("%d %+v jobs=%d", code, result, jobs.Load())
	}
	for _, ext := range []string{"jpg", "pdf"} {
		path := filepath.Join(t.TempDir(), "扫描件."+ext)
		result, code = agentRun(t, context.Background(), []string{"scan", "--endpoint", c.endpoint, "-o", path}, find)
		if code != 0 || result.Status != "ok" || result.Pages != 1 || len(result.Files) != 1 || result.Files[0] != path {
			t.Fatalf("%d %+v", code, result)
		}
		if ext == "pdf" {
			assertPDF(t, path, 1)
		} else {
			assertSavedImage(t, path, image)
		}
		before := jobs.Load()
		result, code = agentRun(t, context.Background(), []string{"scan", "--endpoint", c.endpoint, "-o", path}, find)
		if code != 1 || result.Status != "error" || result.Error.Code != "scan_failed" || len(result.Files) != 0 || jobs.Load() != before {
			t.Fatalf("overwrite: %d %+v jobs=%d/%d", code, result, jobs.Load(), before)
		}
	}
	device, err := deviceFromEndpoint(c.endpoint)
	if err != nil {
		t.Fatal(err)
	}
	if err := saveDefaultDevice(device); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(os.Getenv("HOME"), ".config", "goscanner", "config.json")
	before, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	result, code = agentRun(t, context.Background(), []string{"info"}, find)
	if code != 0 || result.Info == nil {
		t.Fatalf("%d %+v", code, result)
	}
	after, _ := os.ReadFile(configPath)
	if !bytes.Equal(before, after) {
		t.Fatal("agent modified default device")
	}
}

func TestStructuredPartialPDF(t *testing.T) {
	data, image := fixture(t), jpegSample(t)
	c := testClient(t, func(w http.ResponseWriter, r *http.Request, action string, _ []byte) {
		switch action {
		case "GetScannerElements":
			w.Write(data)
		case "CreateScanJob":
			io.WriteString(w, responseXML("CreateScanJobResponse", "<w:JobId>7</w:JobId><w:JobToken>token</w:JobToken>"))
		case "RetrieveImage":
			ct, body := mtom(image, "boundary", "cid:image@scanner")
			w.Header().Set("Content-Type", ct)
			w.Write(body)
		default:
			t.Errorf("unexpected action %s", action)
		}
	})
	path := filepath.Join(t.TempDir(), "scan.pdf")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := commandRequest{command: "scan", endpoint: c.endpoint, output: path, area: "a4", timeout: time.Second, opts: options{DPI: 150, Mode: "color"}}
	if err := r.validateScan(); err != nil {
		t.Fatal(err)
	}
	hooks := commandHooks{wait: func(context.Context) (bool, error) { cancel(); return false, ctx.Err() }}
	result, code := executeCommand(ctx, r, nil, hooks, io.Discard, io.Discard)
	if code != 130 || result.Status != "partial" || result.Error.Code != "cancelled" || result.Pages != 1 || len(result.Files) != 1 {
		t.Fatalf("%d %+v", code, result)
	}
	assertPDF(t, path, 1)
}

func TestAgentInvalidConfigAndOutputFailure(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	path := filepath.Join(home, ".config", "goscanner", "config.json")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("not json"), 0600); err != nil {
		t.Fatal(err)
	}
	result, code := agentRun(t, context.Background(), []string{"scan"}, nil)
	if code != 2 || result.Error.Code != "invalid_config" {
		t.Fatalf("%d %+v", code, result)
	}
	var stderr bytes.Buffer
	if code := run(context.Background(), []string{"--agent", "--help"}, failingWriter{}, &stderr); code != 1 {
		t.Fatalf("output failure exit=%d", code)
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("write failed") }
