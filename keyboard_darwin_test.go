package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
	"unsafe"
)

func testKeyInput(t *testing.T) (*keyInput, *os.File) {
	t.Helper()
	master, err := os.OpenFile("/dev/ptmx", os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { master.Close() })
	for _, request := range []uintptr{syscall.TIOCPTYGRANT, syscall.TIOCPTYUNLK} {
		if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, master.Fd(), request, 0); errno != 0 {
			t.Fatal(errno)
		}
	}
	var name [128]byte
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, master.Fd(), syscall.TIOCPTYGNAME, uintptr(unsafe.Pointer(&name[0]))); errno != 0 {
		t.Fatal(errno)
	}
	slave, err := os.OpenFile(string(name[:bytes.IndexByte(name[:], 0)]), os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { slave.Close() })
	keys := &keyInput{file: slave}
	if err := terminalMode(slave, getTerminalMode, &keys.original); err != nil {
		t.Fatal(err)
	}
	return keys, master
}

type promptNotice chan struct{}

func (ready promptNotice) Write(data []byte) (int, error) {
	ready <- struct{}{}
	return len(data), nil
}

func TestManualCLIProcess(t *testing.T) {
	if os.Getenv("GOSCANNER_TEST_MANUAL_CHILD") != "1" {
		return
	}
	for i, arg := range os.Args {
		if arg == "--" {
			os.Args = append([]string{"goscanner"}, os.Args[i+1:]...)
			main()
		}
	}
	t.Fatal("missing CLI arguments")
}

func TestManualCLI(t *testing.T) {
	for _, format := range []string{"jpg", "pdf", "pdf-single-page"} {
		for _, scenario := range []string{"escape", "two-pages", "ctrl-c", "no-terminal"} {
			t.Run(format+"/"+scenario, func(t *testing.T) {
				data, image := fixture(t), jpegSample(t)
				var jobs atomic.Int32
				c := testClient(t, func(w http.ResponseWriter, r *http.Request, action string, _ []byte) {
					switch action {
					case "GetScannerElements":
						_, _ = w.Write(data)
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
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				dir := t.TempDir()
				ext := ".jpg"
				if format != "jpg" {
					ext = ".pdf"
				}
				path := filepath.Join(dir, "page"+ext)
				args := []string{"-test.run=^TestManualCLIProcess$", "--", "scan", "--manual", "--endpoint", c.endpoint, "-o", path}
				if format == "pdf-single-page" {
					args = append(args, "--single-page")
				}
				cmd := exec.CommandContext(ctx, os.Args[0], args...)
				cmd.Env = append(os.Environ(), "GOSCANNER_TEST_MANUAL_CHILD=1", "HOME="+dir, "XDG_CACHE_HOME="+dir)
				cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
				keys, master := testKeyInput(t)
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
				prompts := 0
				var log strings.Builder
				lines := bufio.NewScanner(output)
				for lines.Scan() {
					log.WriteString(lines.Text() + "\n")
					if strings.Contains(lines.Text(), "请更换扫描页") {
						prompts++
						key := "\x1b"
						if scenario == "two-pages" && prompts == 1 {
							key = " "
						} else if scenario == "ctrl-c" {
							key = "\x03"
						}
						if _, err := master.Write([]byte(key)); err != nil {
							t.Error(err)
							cancel()
						}
					}
				}
				err = cmd.Wait()
				wantCode, wantPages := 0, 1
				if scenario == "two-pages" {
					wantPages = 2
				} else if scenario == "ctrl-c" {
					wantCode = 130
				} else if scenario == "no-terminal" {
					wantCode, wantPages = 1, 0
				}
				if cmd.ProcessState.ExitCode() != wantCode || ctx.Err() != nil || jobs.Load() != int32(wantPages) {
					t.Fatalf("exit=%d jobs=%d err=%v context=%v\n%s", cmd.ProcessState.ExitCode(), jobs.Load(), err, ctx.Err(), log.String())
				}
				files, _ := filepath.Glob(filepath.Join(dir, "page-*"+ext))
				if format == "pdf" && wantPages > 0 {
					assertPDF(t, path, wantPages)
					if len(files) != 0 {
						t.Fatal("merged PDF must not use numbered paths", files)
					}
				} else {
					if len(files) != wantPages {
						t.Fatalf("files=%v\n%s", files, log.String())
					}
					if format == "pdf-single-page" {
						for _, file := range files {
							assertPDF(t, file, 1)
						}
					}
				}
				if prompts != wantPages {
					t.Fatalf("prompts=%d\n%s", prompts, log.String())
				}
				staged, _ := filepath.Glob(filepath.Join(dir, ".goscanner*"))
				if len(staged) != 0 {
					t.Fatal("staging leaked", staged)
				}
			})
		}
	}
}

func TestKeyInput(t *testing.T) {
	for _, scenario := range []string{"space", "enter", "unicode", "arrow", "split-arrow", "escape", "cancel", "cancel-after-escape", "discard-old-input"} {
		t.Run(scenario, func(t *testing.T) {
			keys, master := testKeyInput(t)
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			if scenario == "discard-old-input" {
				if _, err := master.Write([]byte("old input\n")); err != nil {
					t.Fatal(err)
				}
			}
			ready := make(promptNotice, 1)
			type result struct {
				more bool
				err  error
			}
			done := make(chan result, 1)
			go func() {
				more, err := keys.wait(ctx, ready)
				done <- result{more, err}
			}()
			select {
			case <-ready:
			case <-ctx.Done():
				t.Fatal("no prompt")
			}
			key := " "
			switch scenario {
			case "enter":
				key = "\r"
			case "unicode":
				key = "中"
			case "arrow":
				key = "\x1b[A"
			case "escape", "discard-old-input":
				key = "\x1b"
			case "split-arrow":
				if _, err := master.Write([]byte("\x1b")); err != nil {
					t.Fatal(err)
				}
				time.Sleep(20 * time.Millisecond)
				key = "[A"
			case "cancel", "cancel-after-escape":
				if scenario == "cancel-after-escape" {
					if _, err := master.Write([]byte("\x1b")); err != nil {
						t.Fatal(err)
					}
					time.Sleep(20 * time.Millisecond)
				}
				cancel()
				key = ""
			}
			if _, err := master.Write([]byte(key)); err != nil {
				t.Fatal(err)
			}
			got := <-done
			if scenario == "cancel" || scenario == "cancel-after-escape" {
				if !errors.Is(got.err, context.Canceled) {
					t.Fatalf("cancellation lost: %v", got.err)
				}
			} else {
				wantMore := scenario != "escape" && scenario != "discard-old-input"
				if got.more != wantMore || got.err != nil {
					t.Fatalf("more=%v error=%v", got.more, got.err)
				}
			}
			var restored syscall.Termios
			if err := terminalMode(keys.file, getTerminalMode, &restored); err != nil || restored != keys.original {
				t.Fatalf("terminal mode not restored: %v", err)
			}
		})
	}
}
