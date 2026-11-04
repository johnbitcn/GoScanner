package main

import (
	"bytes"
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

func humanTestModel(t *testing.T) *humanModel {
	t.Helper()
	data := fixture(t)
	c := testClient(t, func(w http.ResponseWriter, r *http.Request, action string, _ []byte) {
		if action != "GetScannerElements" {
			t.Errorf("unexpected action %s", action)
		}
		w.Write(data)
	})
	ctx, cancel := context.WithCancel(context.Background())
	m := newHumanModel(ctx, cancel, commandRequest{command: "scan", endpoint: c.endpoint, area: "a4", output: "scan.pdf", timeout: time.Second, opts: options{DPI: 150, Mode: "color"}}, nil)
	t.Cleanup(func() { close(m.stopped); cancel(); m.tasks.Wait() })
	if m.state == "ready" {
		t.Fatal("unchecked device was marked ready")
	}
	m.checkDevice()
	m.Update(m.listen())
	return m
}

func press(m *humanModel, code rune) tea.Cmd {
	_, cmd := m.Update(tea.KeyPressMsg{Code: code})
	return cmd
}

func TestHumanReadyDoesNotScan(t *testing.T) {
	m := humanTestModel(t)
	m.Init()
	if !m.working || m.state != "working" || strings.Contains(m.View().Content, "Enter 开始扫描") {
		t.Fatal("unchecked device can start scanning")
	}
	m.Update(m.listen())
	if m.working || m.state != "ready" || m.checkedAt.IsZero() {
		t.Fatalf("unexpected automatic scan: %+v", m)
	}
	if content := m.View().Content; !strings.Contains(content, "Enter 开始扫描") || strings.Contains(content, "\x1b[?1049") {
		t.Fatal(content)
	}
	if cmd := press(m, 'q'); cmd == nil {
		t.Fatal("q did not quit")
	}
}

func TestHumanReadinessFailuresAndRetry(t *testing.T) {
	for _, scenario := range []string{"busy", "stopped", "http-error", "malformed", "timeout", "dpi", "png-format", "tiff-format"} {
		t.Run(scenario, func(t *testing.T) {
			data := fixture(t)
			var recovered atomic.Bool
			var queries atomic.Int32
			c := testClient(t, func(w http.ResponseWriter, r *http.Request, action string, _ []byte) {
				if action != "GetScannerElements" {
					t.Errorf("readiness check created a task: %s", action)
					w.WriteHeader(500)
					return
				}
				queries.Add(1)
				if !recovered.Load() {
					switch scenario {
					case "busy":
						w.Write(bytes.ReplaceAll(data, []byte(">Idle<"), []byte(">Processing<")))
						return
					case "stopped":
						w.Write(bytes.ReplaceAll(data, []byte(">Idle<"), []byte(">Stopped<")))
						return
					case "http-error":
						w.WriteHeader(503)
						return
					case "malformed":
						w.Write([]byte("not SOAP"))
						return
					case "timeout":
						<-r.Context().Done()
						return
					case "png-format", "tiff-format":
						w.Write(bytes.ReplaceAll(data, []byte(formatTIFF), []byte("unsupported")))
						return
					}
				}
				w.Write(data)
			})
			ctx, cancel := context.WithCancel(context.Background())
			r := commandRequest{command: "scan", endpoint: c.endpoint, output: filepath.Join(t.TempDir(), "scan.pdf"), area: "a4", timeout: time.Second, opts: options{DPI: 150, Mode: "color"}}
			if scenario == "dpi" {
				r.opts.DPI = 123
			}
			if scenario == "png-format" {
				r.output += ".png"
			}
			if scenario == "tiff-format" {
				r.output += ".TIFF"
			}
			if scenario == "timeout" {
				r.timeout = 10 * time.Millisecond
			}
			m := newHumanModel(ctx, cancel, r, nil)
			t.Cleanup(func() { close(m.stopped); cancel(); m.tasks.Wait() })
			m.Init()
			press(m, tea.KeyEnter)
			m.Update(m.listen())
			if m.state != "unavailable" || m.working || m.notice == "" || strings.Contains(m.View().Content, "Enter 开始扫描") {
				t.Fatalf("false ready: state=%s notice=%s", m.state, m.notice)
			}
			press(m, tea.KeyEnter)
			if m.working || queries.Load() != 1 {
				t.Fatal("unavailable device accepted Enter")
			}
			if _, err := os.Stat(r.output); !os.IsNotExist(err) {
				t.Fatal("readiness check created output", err)
			}
			recovered.Store(true)
			m.request.opts.DPI, m.request.timeout = 150, time.Second
			press(m, 'r')
			if !m.working {
				t.Fatal("recheck did not start")
			}
			m.Update(m.listen())
			if m.state != "ready" || m.code != 0 || queries.Load() != 2 {
				t.Fatalf("retry state=%s notice=%s queries=%d", m.state, m.notice, queries.Load())
			}
		})
	}
}

func TestHumanReadinessCancellation(t *testing.T) {
	started := make(chan struct{})
	c := testClient(t, func(w http.ResponseWriter, r *http.Request, action string, _ []byte) {
		if action != "GetScannerElements" {
			t.Errorf("unexpected action %s", action)
		}
		close(started)
		<-r.Context().Done()
	})
	ctx, cancel := context.WithCancel(context.Background())
	m := newHumanModel(ctx, cancel, commandRequest{command: "scan", endpoint: c.endpoint, timeout: time.Second}, nil)
	t.Cleanup(func() { close(m.stopped); cancel(); m.tasks.Wait() })
	m.Init()
	<-started
	m.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	_, cmd := m.Update(m.listen())
	if cmd == nil || m.state != "done" || m.code != 130 || m.result.Error.Code != "cancelled" {
		t.Fatalf("state=%s code=%d result=%+v", m.state, m.code, m.result)
	}
}

func TestHumanManualAndCancellation(t *testing.T) {
	m := humanTestModel(t)
	m.working = true
	reply := make(chan bool, 1)
	m.Update(nextPageMsg{reply: reply})
	press(m, tea.KeyEsc)
	if <-reply || m.state != "working" {
		t.Fatal("Esc must finish and save")
	}
	m.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	if m.ctx.Err() == nil || !m.working {
		t.Fatal("cancel must wait for worker")
	}
	result := commandResult{Status: "partial", Files: []string{"scan.pdf"}, Pages: 1, Error: &commandError{Code: "cancelled", Message: "cancelled"}}
	_, cmd := m.Update(commandDoneMsg{result: result, code: 130})
	if cmd == nil || m.code != 130 || m.result.Pages != 1 {
		t.Fatal("cancel lost completed pages")
	}
}

func TestHumanCompletionQueueSurvivesCancellation(t *testing.T) {
	m := humanTestModel(t)
	m.working = true
	m.cancel()
	m.task(func() tea.Msg {
		m.emit(nextPageMsg{reply: make(chan bool, 1)})
		return commandDoneMsg{result: commandResult{Status: "error", Error: &commandError{Code: "cancelled", Message: "cancelled"}}, code: 130}
	})
	m.tasks.Wait()
	first := m.listen()
	if _, ok := first.(nextPageMsg); !ok {
		t.Fatalf("completion overtook prompt: %T", first)
	}
	m.Update(first)
	if m.state == "waiting" {
		t.Fatal("cancelled UI returned to page prompt")
	}
	m.Update(m.listen())
	if m.state != "done" || m.code != 130 {
		t.Fatalf("lost completion: %s %d", m.state, m.code)
	}
}

func TestHumanTerminalTextAndSmallWindow(t *testing.T) {
	m := humanTestModel(t)
	m.request.endpoint = "scanner\x1b[2J\r\n\x07bad"
	m.Update(tea.WindowSizeMsg{Width: 32, Height: 12})
	if text := terminalText(m.request.endpoint); strings.ContainsAny(text, "\x1b\r\n\x07") {
		t.Fatal("terminal injection", text)
	}
	m.finish(commandResult{Status: "ok", Files: []string{strings.Repeat("中文路径", 30)}, Pages: 1}, 0)
	view := m.View()
	if view.AltScreen || view.MouseMode != 0 {
		t.Fatal("unexpected full-screen or mouse mode")
	}
	if len(strings.Split(view.Content, "\n")) > 12 {
		t.Fatal("small terminal overflow", view.Content)
	}
}
