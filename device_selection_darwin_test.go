package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

func TestDeviceSelectionProcess(t *testing.T) {
	raw := os.Getenv("GOSCANNER_TEST_DEVICES")
	if raw == "" {
		return
	}
	var devices []scannerDevice
	if err := json.Unmarshal([]byte(raw), &devices); err != nil {
		t.Fatal(err)
	}
	for i, arg := range os.Args {
		if arg == "--" {
			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			code := runWithDiscovery(ctx, os.Args[i+1:], os.Stdout, os.Stderr, func(context.Context) ([]scannerDevice, []string, error) {
				return devices, []string{"模拟发现警告：仍需明确选择设备"}, nil
			})
			stop()
			os.Exit(code)
		}
	}
	t.Fatal("missing CLI arguments")
}

func TestDeviceSelectionCLI(t *testing.T) {
	for _, command := range []string{"info", "scan"} {
		for _, scenario := range []string{"single", "multiple", "retry", "quit", "ctrl-c", "no-terminal", "save-failure"} {
			t.Run(command+"/"+scenario, func(t *testing.T) {
				cap, image := fixture(t), jpegSample(t)
				var requests, jobs atomic.Int32
				c := testClient(t, func(w http.ResponseWriter, r *http.Request, action string, _ []byte) {
					requests.Add(1)
					saved, err := loadConfig()
					if action == "GetScannerElements" {
						if saved.Device.RecordID != "" {
							t.Error("default saved before read-only validation")
						}
						if err != nil && scenario != "save-failure" {
							t.Error(err)
						}
					} else if err != nil || configuredRecord(t, saved.Device).Endpoint != "http://"+r.Host+"/wsd/scan" {
						t.Error("scan started before saving verified reference", err)
					}
					switch action {
					case "GetScannerElements":
						_, _ = w.Write(cap)
					case "CreateScanJob":
						jobs.Add(1)
						io.WriteString(w, responseXML("CreateScanJobResponse", "<w:JobId>7</w:JobId><w:JobToken>token</w:JobToken>"))
					case "RetrieveImage":
						ct, body := mtom(image, "boundary", "cid:image@scanner")
						w.Header().Set("Content-Type", ct)
						_, _ = w.Write(body)
					default:
						t.Errorf("unexpected action %s", action)
					}
				})
				original := []byte(`{"scan":{"dpi":150},"output":{"format":"png"}}`)
				path := writeTestConfig(t, string(original))
				devices := []scannerDevice{{Name: "Test scanner", Model: "Mock WSD", Endpoint: c.endpoint}}
				choice := "1\n"
				if scenario == "multiple" {
					devices = append([]scannerDevice{{Name: "Other scanner", Endpoint: "http://127.0.0.1:1/unused"}}, devices...)
					choice = "2\n"
				}
				raw, err := json.Marshal(devices)
				if err != nil {
					t.Fatal(err)
				}
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				timeout := "2s"
				if scenario == "single" {
					timeout = "500ms"
				}
				args := []string{"-test.run=^TestDeviceSelectionProcess$", "--", command, "--timeout=" + timeout}
				outputPath := filepath.Join(t.TempDir(), "scan.jpg")
				if command == "scan" {
					args = append(args, "-o", outputPath)
				}
				cmd := exec.CommandContext(ctx, os.Args[0], args...)
				cmd.Env = append(os.Environ(), "GOSCANNER_TEST_DEVICES="+string(raw))
				cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
				keys, master := testKeyInput(t)
				// 消费终端回显，避免 macOS 在子进程关闭 tty 时等待输出排空。
				go io.Copy(io.Discard, master)
				if scenario != "no-terminal" {
					cmd.Stdin = keys.file
					cmd.SysProcAttr.Setctty = true
				}
				output, err := cmd.StdoutPipe()
				if err != nil {
					t.Fatal(err)
				}
				cmd.Stderr = cmd.Stdout
				if err := cmd.Start(); err != nil {
					t.Fatal(err)
				}
				var log strings.Builder
				prompts := 0
				lines := bufio.NewScanner(output)
				for lines.Scan() {
					log.WriteString(lines.Text() + "\n")
					if !strings.Contains(lines.Text(), "请输入设备编号") {
						continue
					}
					prompts++
					if prompts == 1 {
						delay := 150 * time.Millisecond
						if scenario == "single" {
							delay = 600 * time.Millisecond // 确认等待超过任务超时，后续操作仍须成功。
						}
						time.Sleep(delay)
						data, _ := os.ReadFile(path)
						if requests.Load() != 0 || !bytes.Equal(data, original) {
							t.Error("queried or saved device before explicit confirmation")
						}
					}
					input := choice
					switch scenario {
					case "retry":
						if prompts == 1 {
							input = "\n"
						}
					case "quit":
						input = "q\n"
					case "ctrl-c":
						input = "\x03"
					case "save-failure":
						original = []byte("malformed while choosing")
						if err := os.WriteFile(path, original, 0600); err != nil {
							t.Error(err)
						}
					}
					if _, err := master.Write([]byte(input)); err != nil {
						t.Error(err)
						cancel()
					}
				}
				err = cmd.Wait()
				success := scenario == "single" || scenario == "multiple" || scenario == "retry"
				wantCode := 1
				if success {
					wantCode = 0
				} else if scenario == "ctrl-c" {
					wantCode = 130
				}
				if cmd.ProcessState.ExitCode() != wantCode || ctx.Err() != nil {
					t.Fatalf("exit=%d err=%v context=%v\n%s", cmd.ProcessState.ExitCode(), err, ctx.Err(), log.String())
				}
				data, _ := os.ReadFile(path)
				if success {
					cfg, err := loadConfig()
					if err != nil || configuredRecord(t, cfg.Device).Endpoint != c.endpoint || cfg.Output.Format != "png" || requests.Load() == 0 {
						t.Fatalf("selected device not saved/used: %+v %v\n%s", cfg, err, log.String())
					}
					if command == "scan" {
						assertSavedImage(t, outputPath, image)
					}
				} else {
					wantQueries := int32(0)
					if scenario == "save-failure" {
						wantQueries = 1
					}
					if requests.Load() != wantQueries || !bytes.Equal(data, original) {
						t.Fatalf("failure changed config or queried unexpectedly: queries=%d\n%s", requests.Load(), log.String())
					}
				}
				wantJobs := int32(0)
				if success && command == "scan" {
					wantJobs = 1
				}
				if jobs.Load() != wantJobs {
					t.Fatalf("jobs=%d, want %d\n%s", jobs.Load(), wantJobs, log.String())
				}
			})
		}
	}
}
