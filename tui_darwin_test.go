package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"github.com/charmbracelet/x/ansi"
)

func TestHumanPTY(t *testing.T) {
	for _, scenario := range []string{"scan", "manual", "cancel-wait", "cancel-active", "preset-recheck", "settings-list", "color", "default-manual", "single-page", "manual-device", "saved-device"} {
		t.Run(scenario, func(t *testing.T) {
			data, image := fixture(t), jpegSample(t)
			var jobs, cancels, queries atomic.Int32
			retrieving := make(chan struct{}, 1)
			c := testClient(t, func(w http.ResponseWriter, r *http.Request, action string, body []byte) {
				switch action {
				case "GetScannerElements":
					if queries.Add(1) == 1 && scenario == "preset-recheck" {
						w.Write(bytes.ReplaceAll(data, []byte(">Idle<"), []byte(">Processing<")))
						return
					}
					w.Write(data)
				case "CreateScanJob":
					if scenario == "settings-list" && !bytes.Contains(body, []byte("<w:Resolution><w:Width>300</w:Width><w:Height>300</w:Height></w:Resolution>")) {
						t.Errorf("settings did not reach scan ticket: %s", body)
					}
					jobs.Add(1)
					io.WriteString(w, responseXML("CreateScanJobResponse", "<w:JobId>7</w:JobId><w:JobToken>token</w:JobToken>"))
				case "RetrieveImage":
					if scenario == "cancel-active" {
						retrieving <- struct{}{}
						<-r.Context().Done()
						return
					}
					ct, body := mtom(image, "boundary", "cid:image@scanner")
					w.Header().Set("Content-Type", ct)
					w.Write(body)
				case "CancelJob":
					cancels.Add(1)
					// Deliberately delay cleanup: the UI must not exit before this response.
					time.Sleep(80 * time.Millisecond)
					io.WriteString(w, responseXML("CancelJobResponse", ""))
				default:
					t.Errorf("unexpected action %s", action)
				}
			})
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			dir := t.TempDir()
			path := filepath.Join(dir, "scan.pdf")
			args := []string{"-test.run=^TestManualCLIProcess$", "--", "scan", "--endpoint", c.endpoint, "-o", path}
			if scenario == "preset-recheck" || scenario == "saved-device" {
				device, err := deviceFromEndpoint(c.endpoint)
				if err != nil {
					t.Fatal(err)
				}
				config, err := json.Marshal(map[string]any{"device": device})
				if err != nil {
					t.Fatal(err)
				}
				configPath := filepath.Join(dir, ".config", "goscanner", "config.json")
				if err := os.MkdirAll(filepath.Dir(configPath), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(configPath, config, 0600); err != nil {
					t.Fatal(err)
				}
				args = []string{"-test.run=^TestManualCLIProcess$", "--", "scan", "-o", path}
				if scenario == "saved-device" {
					data, err := json.Marshal([]savedDevice{{scannerDevice: scannerDevice{Model: "HP Laser MFP 1188w", Endpoint: c.endpoint}, Discovered: true}})
					if err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(filepath.Join(filepath.Dir(configPath), "drives.json"), data, 0600); err != nil {
						t.Fatal(err)
					}
				}
			}
			if scenario == "manual-device" {
				args = []string{"-test.run=^TestManualCLIProcess$", "--", "scan", "-o", path}
			}
			if scenario == "manual" || scenario == "cancel-wait" {
				args = append(args, "--manual")
			}
			if scenario == "single-page" {
				args = append(args, "--manual=false")
			}
			cmd := exec.CommandContext(ctx, os.Args[0], args...)
			cmd.Env = append(os.Environ(), "GOSCANNER_TEST_MANUAL_CHILD=1", "HOME="+dir, "XDG_CACHE_HOME="+dir, "TERM=xterm-256color", "NO_COLOR=1")
			if scenario == "color" {
				cmd.Env = append(cmd.Env, "NO_COLOR=", "CLICOLOR=1", "CLICOLOR_FORCE=0")
			}
			keys, master := testKeyInput(t)
			window := [4]uint16{24, 80, 0, 0}
			if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, keys.file.Fd(), syscall.TIOCSWINSZ, uintptr(unsafe.Pointer(&window[0]))); errno != 0 {
				t.Fatal(errno)
			}
			cmd.Stdin, cmd.Stdout, cmd.Stderr = keys.file, keys.file, keys.file
			cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			chunks := make(chan string, 64)
			go func() {
				buffer := make([]byte, 8192)
				for {
					n, err := master.Read(buffer)
					if n > 0 {
						select {
						case chunks <- string(buffer[:n]):
						case <-ctx.Done():
							return
						}
					}
					if err != nil {
						return
					}
				}
			}()
			exited := make(chan error, 1)
			go func() { exited <- cmd.Wait() }()
			stage, raw, pagePrompts := 0, "", 0
			var transcript strings.Builder
			sawUnavailable := false
			var waitErr error
		loop:
			for {
				select {
				case chunk := <-chunks:
					transcript.WriteString(chunk)
					raw += chunk
					text := ansi.Strip(raw)
					if scenario == "manual-device" && stage == 0 && strings.Contains(text, "手动添加") {
						if jobs.Load() != 0 || queries.Load() != 0 || strings.Contains(text, "端口") {
							t.Fatal("manual fields appeared before selection", text)
						}
						master.Write([]byte("\r"))
						stage, raw = 8, ""
					} else if scenario == "manual-device" && stage == 8 && strings.Contains(text, "更换设备（先验证，再保存）") {
						if jobs.Load() != 0 || queries.Load() != 0 || !strings.Contains(text, "8018") || !strings.Contains(text, "/wsd/scan") {
							t.Fatal("manual device screen queried or lacks defaults", text)
						}
						device, err := deviceFromEndpoint(c.endpoint)
						if err != nil {
							t.Fatal(err)
						}
						master.Write([]byte(device.Host + "\r\x01\x0b" + strconv.Itoa(device.Port) + "\r\x01\x0b" + device.Path + "\r\r"))
						stage, raw = 7, ""
					} else if stage == 7 && strings.Contains(text, "Enter 开始扫描") {
						if jobs.Load() != 0 || queries.Load() != 1 {
							t.Fatal("manual device should validate once without scanning")
						}
						master.Write([]byte("\r"))
						stage, raw = 1, ""
					} else if stage == 9 && strings.Contains(text, "已保存的设备") {
						if !strings.Contains(text, "HP Laser MFP 1188w") || strings.Contains(text, "端口") {
							t.Fatal("saved device list is incorrect", text)
						}
						master.Write([]byte("\r"))
						stage, raw = 10, ""
					} else if stage == 10 && strings.Contains(text, "Enter 开始扫描") {
						if queries.Load() != 2 || jobs.Load() != 0 {
							t.Fatal("saved selection must revalidate before scanning")
						}
						master.Write([]byte("\r"))
						stage, raw = 1, ""
					} else if scenario == "preset-recheck" && !sawUnavailable && strings.Contains(text, "无法确认就绪") {
						if jobs.Load() != 0 || strings.Contains(text, "Enter 开始扫描") {
							t.Fatal("preset busy device falsely ready")
						}
						sawUnavailable = true
						master.Write([]byte("\rr")) // Enter must be ignored; r explicitly checks again.
						raw = ""
					} else if stage == 0 && strings.Contains(text, "Enter 开始扫描") {
						if jobs.Load() != 0 {
							t.Fatal("scan started without Enter")
						}
						if scenario == "saved-device" {
							if !strings.Contains(text, "HP Laser MFP 1188w") {
								t.Fatal("home did not use saved model", text)
							}
							master.Write([]byte("d"))
							stage = 9
						} else if scenario == "settings-list" {
							master.Write([]byte("s"))
							stage = 4
						} else {
							master.Write([]byte("\r"))
							stage = 1
						}
						raw = ""
					} else if stage == 4 && strings.Contains(text, "应用设置") {
						for _, name := range settingNames {
							if !strings.Contains(text, name) {
								t.Fatalf("missing setting %s in terminal: %q", name, text)
							}
						}
						master.Write([]byte("\r"))
						stage, raw = 5, ""
					} else if stage == 5 && strings.Contains(text, "300 DPI") {
						master.Write([]byte("\x1b[B\x1b[B\r" + strings.Repeat("\x1b[B", settingApply) + "\r"))
						stage, raw = 6, ""
					} else if stage == 6 && strings.Contains(text, "Enter 开始扫描") {
						if jobs.Load() != 0 || !strings.Contains(text, "300 DPI") {
							t.Fatal("settings applied incorrectly or started scanning", text)
						}
						master.Write([]byte("\r"))
						stage, raw = 1, ""
					} else if stage == 1 && strings.Contains(text, "请更换扫描页") {
						pagePrompts++
						key := "\x1b"
						if (scenario == "manual" || scenario == "default-manual") && pagePrompts == 1 {
							key = "\r"
						}
						if scenario == "cancel-wait" {
							key = "\x03"
							stage = 2
						}
						master.Write([]byte(key))
						raw = ""
					} else if stage >= 1 && stage != 3 && strings.Contains(text, "Enter / q 退出") {
						master.Write([]byte("\r"))
						stage = 3
					}
				case <-retrieving:
					master.Write([]byte("\x03"))
					stage = 2
				case waitErr = <-exited:
					break loop
				case <-ctx.Done():
					waitErr = <-exited
					t.Fatalf("PTY timed out: %v stage=%d output=%q", waitErr, stage, raw)
				}
			}
			if hasTerminalColor(transcript.String()) != (scenario == "color") {
				t.Fatalf("terminal colors do not respect NO_COLOR: scenario=%s", scenario)
			}
			if scenario == "saved-device" && !strings.Contains(transcript.String(), "\x1b[1mHP Laser MFP 1188w") {
				t.Fatal("saved model was not bold in actual terminal output")
			}
			if scenario == "preset-recheck" && !sawUnavailable {
				t.Fatal("preset device was not checked")
			}
			wantCode, wantPages := 0, 1
			if scenario == "manual" || scenario == "default-manual" {
				wantPages = 2
			}
			if scenario == "default-manual" && pagePrompts != 2 {
				t.Fatal("TUI did not default to manual scanning")
			}
			if scenario == "single-page" && pagePrompts != 0 {
				t.Fatal("--manual=false did not disable page prompts")
			}
			if strings.HasPrefix(scenario, "cancel-") {
				wantCode = 130
			}
			if scenario == "cancel-active" {
				wantPages = 0
				if cancels.Load() != 1 {
					t.Fatalf("cleanup cancels=%d", cancels.Load())
				}
			}
			if cmd.ProcessState.ExitCode() != wantCode || jobs.Load() != int32(max(1, wantPages)) {
				t.Fatalf("exit=%d err=%v jobs=%d stage=%d output=%q", cmd.ProcessState.ExitCode(), waitErr, jobs.Load(), stage, raw)
			}
			if wantPages > 0 {
				assertPDF(t, path, wantPages)
			} else if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Fatal("unexpected output", err)
			}
			var mode syscall.Termios
			if err := terminalMode(keys.file, getTerminalMode, &mode); err != nil {
				t.Fatal(err)
			}
			if mode != keys.original {
				t.Fatal("terminal mode was not restored")
			}
			staged, _ := filepath.Glob(filepath.Join(dir, ".goscanner*"))
			if len(staged) != 0 {
				t.Fatal("staging leaked", staged)
			}
		})
	}
}
