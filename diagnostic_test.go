package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
)

func TestDiagnosticPreservesHumanTextAndErrorChain(t *testing.T) {
	raw := errors.New("设备忙碌：请勿重复扫描")
	inner := diagnosticf("Cannot read scanner state: %w", "读取设备状态失败：%w", raw)
	outer := diagnosticf("Scan failed: %w", "扫描失败：%w", errors.Join(inner, context.DeadlineExceeded))
	if outer.Error() != "扫描失败：读取设备状态失败：设备忙碌：请勿重复扫描\ncontext deadline exceeded" || !errors.Is(outer, raw) || !errors.Is(outer, context.DeadlineExceeded) {
		t.Fatalf("human text or error chain changed: %v", outer)
	}
	want := "Scan failed: Cannot read scanner state: External error: \"设备忙碌：请勿重复扫描\"\ncontext deadline exceeded"
	if got := agentError(outer); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	for _, original := range []error{errRecordNotFound, errIdentityConflict, errOutputFormat} {
		wrapped := diagnosticf("Operation failed: %w", "操作失败：%w", original)
		if !errors.Is(wrapped, original) {
			t.Fatalf("lost sentinel: %v", original)
		}
	}
}

func TestAgentLanguageDoesNotFollowLocale(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cases := []struct {
		args []string
		code int
		kind string
		text string
	}{
		{nil, 2, "invalid_arguments", "--agent requires"},
		{[]string{"unknown"}, 2, "invalid_arguments", "Unknown command."},
		{[]string{"scan", "--dpi", "0"}, 2, "invalid_arguments", "--dpi must be greater than zero."},
		{[]string{"scan", "--manual"}, 2, "unsupported_interaction", "Agent mode does not support --manual."},
		{[]string{"info"}, 1, "device_required", "No scanner was specified"},
		{[]string{"scan", "-o", "扫描件.unknown"}, 1, "invalid_arguments", "The output extension must be"},
		{[]string{"scan", "--未知参数"}, 2, "invalid_arguments", "External error: \"flag provided but not defined: -未知参数\""},
	}
	for _, locale := range []string{"C", "en_US.UTF-8", "zh_CN.UTF-8", "fr_FR.UTF-8"} {
		t.Run(locale, func(t *testing.T) {
			for _, key := range []string{"LANG", "LC_ALL", "LC_MESSAGES", "LANGUAGE"} {
				t.Setenv(key, locale)
			}
			for _, tc := range cases {
				result, code := agentRun(t, context.Background(), tc.args, nil)
				if code != tc.code || result.Error == nil || result.Error.Code != tc.kind || !strings.Contains(result.Error.Message, tc.text) {
					t.Fatalf("args=%v code=%d result=%+v", tc.args, code, result)
				}
			}
			for _, command := range []string{"", "scan", "info", "discover"} {
				args := []string{"--help"}
				if command != "" {
					args = []string{command, "--help"}
				}
				result, code := agentRun(t, context.Background(), args, nil)
				if code != 0 || result.Help != renderAgentHelp(command) || !strings.Contains(result.Help, "Usage") {
					t.Fatalf("help args=%v code=%d", args, code)
				}
				assertProjectEnglish(t, result.Help)
				for _, line := range strings.Split(result.Help, "\n") {
					if len(line) > 80 || strings.ContainsRune(line, '\x1b') {
						t.Fatalf("invalid Agent help line: %q", line)
					}
				}
			}
		})
	}
}

func TestAgentDiscoveryDiagnosticsUseEnglish(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	wsd := func(context.Context) ([]scannerDevice, []diagnostic, error) {
		return nil, nil, diagnosticf("Invalid discovery response.", "发现响应无效")
	}
	for _, bothFailed := range []bool{false, true} {
		escl := func(context.Context) ([]scannerDevice, []diagnostic, error) {
			if bothFailed {
				return nil, nil, errors.New("原始发现错误")
			}
			return nil, nil, nil
		}
		find := func(ctx context.Context) ([]scannerDevice, []diagnostic, error) {
			return discoverBoth(ctx, wsd, escl)
		}
		result, code := agentRun(t, context.Background(), []string{"discover"}, find)
		warnings := chinese.warnings(result.Warnings)
		if !strings.Contains(warnings, "WSD discovery failed: Invalid discovery response.") {
			t.Fatalf("lost project warning: %+v", result)
		}
		if bothFailed {
			if code != 1 || result.Error == nil || result.Error.Code != "discovery_failed" || !strings.Contains(result.Error.Message, "Scanner discovery failed:") || !strings.Contains(result.Error.Message, `External error: "原始发现错误"`) || !strings.Contains(warnings, `eSCL discovery failed: External error: "原始发现错误"`) {
				t.Fatalf("lost joined diagnostics: code=%d result=%+v", code, result)
			}
		} else if code != 0 || result.Error != nil {
			t.Fatalf("partial discovery became fatal: code=%d result=%+v", code, result)
		}
	}
}

func TestAgentPreservesExternalContent(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	device := scannerDevice{ID: "scanner-1", Name: "办公室扫描仪", Model: "型号甲", Protocol: "wsd", Endpoint: "http://192.0.2.8/wsd/scan"}
	find := func(context.Context) ([]scannerDevice, []diagnostic, error) {
		return []scannerDevice{device}, []diagnostic{diagnosticf("Discovery warning: %w", "发现警告：%w", errors.New("外部原始诊断"))}, nil
	}
	result, code := agentRun(t, context.Background(), []string{"discover"}, find)
	if code != 0 || result.Devices == nil || (*result.Devices)[0].Name != device.Name || (*result.Devices)[0].Model != device.Model || len(result.Warnings) != 1 || result.Warnings[0].Error() != "Discovery warning: External error: \"外部原始诊断\"" {
		t.Fatalf("external discovery data changed: %+v", result)
	}
	c := testClient(t, func(w http.ResponseWriter, r *http.Request, action string, _ []byte) {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprintf(w, `<s:Envelope xmlns:s="%s"><s:Body><s:Fault><s:Reason><s:Text>设备忙碌，请检查进纸器</s:Text></s:Reason></s:Fault></s:Body></s:Envelope>`, soapNS)
	})
	result, code = agentRun(t, context.Background(), []string{"info", "--endpoint", c.endpoint}, nil)
	if code != 1 || result.Error == nil || result.Error.Code != "info_failed" || result.Error.Message != "Cannot read scanner information: WSD fault. Device diagnostic: \"设备忙碌，请检查进纸器\"" {
		t.Fatalf("device diagnostic changed: code=%d result=%+v", code, result)
	}
	path, _ := configPath()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"未知字段":1}`), 0600); err != nil {
		t.Fatal(err)
	}
	result, code = agentRun(t, context.Background(), []string{"info"}, nil)
	if code != 2 || result.Error == nil || result.Error.Code != "invalid_config" || !strings.Contains(result.Error.Message, "Cannot parse the configuration file") || !strings.Contains(result.Error.Message, `External error: "json: unknown field \"未知字段\""`) {
		t.Fatalf("configuration source boundary changed: code=%d result=%+v", code, result)
	}
}

type externalFailureWriter struct{}

func (externalFailureWriter) Write([]byte) (int, error) { return 0, errors.New("磁盘不可写") }

func TestAgentCleanupFailureRetainsImageWithoutRetry(t *testing.T) {
	var posts, deletes atomic.Int32
	payload := jpegSample(t)
	c := esclTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if serveESCLInfo(w, r, esclTestCapabilities(esclDefaultProfiles()), "Idle") {
			return
		}
		switch r.Method {
		case http.MethodPost:
			posts.Add(1)
			w.Header().Set("Location", "42")
			w.WriteHeader(http.StatusCreated)
		case http.MethodGet:
			w.Write(payload)
		case http.MethodDelete:
			deletes.Add(1)
			w.WriteHeader(http.StatusInternalServerError)
		default:
			t.Error("unexpected method", r.Method)
		}
	})
	path := filepath.Join(t.TempDir(), "扫描件.jpg")
	result, code := agentRun(t, context.Background(), []string{"scan", "--protocol", "escl", "--endpoint", c.endpoint, "--dpi", "300", "-o", path}, nil)
	if code != 1 || result.Status != "partial" || result.Error == nil || result.Error.Code != "scan_failed" || len(result.Retained) != 1 || result.Retained[0] != path || posts.Load() != 1 || deletes.Load() != 1 {
		t.Fatalf("cleanup result changed: code=%d result=%+v error=%+v posts=%d deletes=%d", code, result, result.Error, posts.Load(), deletes.Load())
	}
	want := fmt.Sprintf("The scanned image was saved to %q. eSCL job cleanup failed. The scan was not automatically retried:", path)
	if !strings.Contains(result.Error.Message, want) || !strings.Contains(result.Error.Message, "Cancelling the eSCL job returned HTTP 500.") {
		t.Fatalf("lost cleanup safety information: %s", result.Error.Message)
	}
	assertSavedImage(t, path, payload)
}

func TestAgentOutputFailureUsesEnglishExplanation(t *testing.T) {
	var stderr bytes.Buffer
	code := run(context.Background(), []string{"--agent", "--help"}, externalFailureWriter{}, &stderr)
	if code != 1 || stderr.String() != "Cannot write the command result: External error: \"磁盘不可写\"\n" {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
}

func TestAgentWarningJSONKeepsHumanTextSeparate(t *testing.T) {
	warning := diagnosticf("Page %d preprocessing warning: %w", "第 %d 页预处理警告：%w", 2, diagnosticf("Rotation was skipped.", "已跳过旋转"))
	result := commandResult{Status: "partial", Files: []string{"/扫描件/合同.pdf"}, Warnings: []diagnostic{warning}, Processing: []pageProcessing{{Page: 2, processingInfo: processingInfo{Warnings: []diagnostic{warning}}}}}
	result.fail("scan_failed", diagnosticf("Cleanup failed: %w", "清理失败：%w", errors.New("设备原始错误")))
	var out, stderr bytes.Buffer
	if code := writeResult(&out, &stderr, result, 1, true, chinese); code != 1 || stderr.Len() != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	var decoded commandResult
	if err := json.Unmarshal(out.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	want := "Page 2 preprocessing warning: Rotation was skipped."
	if decoded.Status != "partial" || decoded.Files[0] != result.Files[0] || decoded.Warnings[0].Error() != want || decoded.Processing[0].Warnings[0].Error() != want || decoded.Error.Message != "Cleanup failed: External error: \"设备原始错误\"" || warning.Error() != "第 2 页预处理警告：已跳过旋转" {
		t.Fatalf("language or partial result changed: %s", out.String())
	}
}

func assertProjectEnglish(t *testing.T, text string) {
	t.Helper()
	for _, r := range text {
		if r > 127 {
			t.Fatalf("non-English project text: %q", text)
		}
	}
}

func TestProjectDiagnosticTemplatesAreEnglish(t *testing.T) {
	paths, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range paths {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(file, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			if selector, ok := call.Fun.(*ast.SelectorExpr); ok {
				pkg, ok := selector.X.(*ast.Ident)
				if ok && path != "diagnostic.go" && ((pkg.Name == "fmt" && selector.Sel.Name == "Errorf") || (pkg.Name == "errors" && selector.Sel.Name == "New")) {
					t.Errorf("%s: shared project errors must use diagnosticf to retain their language and origin", path)
				}
			}
			name, ok := call.Fun.(*ast.Ident)
			if !ok || name.Name != "diagnosticf" {
				return true
			}
			literal, ok := call.Args[0].(*ast.BasicLit)
			if !ok || literal.Kind != token.STRING {
				t.Errorf("%s: English diagnostic template must be a string literal", path)
				return true
			}
			text, err := strconv.Unquote(literal.Value)
			if err != nil || text == "" {
				t.Errorf("%s: invalid diagnostic template", path)
			}
			assertProjectEnglish(t, text)
			return true
		})
	}
}
