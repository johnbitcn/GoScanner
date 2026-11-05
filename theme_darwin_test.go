//go:build darwin

package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
)

func TestThemePTYLiveUpdates(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	dir := t.TempDir()
	configDir := filepath.Join(dir, ".config", "goscanner")
	if err := os.MkdirAll(configDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "config.json"), []byte(`{"theme":"dark"}`), 0600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestManualCLIProcess$", "--", "--theme", "auto", "--lang", "en")
	cmd.Env = append(os.Environ(), "GOSCANNER_TEST_MANUAL_CHILD=1", "HOME="+dir, "TERM=xterm-256color", "COLORTERM=truecolor", "NO_COLOR=", "CLICOLOR_FORCE=0")
	keys, master := testKeyInput(t)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = keys.file, keys.file, keys.file
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	chunks := make(chan string, 64)
	go func() {
		defer close(chunks)
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
	write := func(s string) {
		if _, err := master.Write([]byte(s)); err != nil {
			t.Fatal(err)
		}
	}
	stage, raw, transcript := 0, "", ""
	for {
		select {
		case chunk, ok := <-chunks:
			if !ok {
				chunks = nil
				continue
			}
			raw, transcript = raw+chunk, transcript+chunk
			switch {
			case stage == 0 && strings.Contains(raw, "38;2;130;170;255") && strings.Contains(raw, "\x1b]11;?"):
				// No reply was needed for the initial dark fallback; CLI auto overrides config dark.
				write("\x1b]11;rgb:ffff/ffff/ffff\x1b\\\x1b[?2031;2$y")
				stage, raw = 1, ""
			case stage == 1 && strings.Contains(raw, "38;2;36;87;178") && strings.Contains(raw, ansi.SetModeLightDark):
				write("\x1b[?997;1n")
				stage, raw = 2, ""
			case stage == 2 && strings.Contains(raw, "\x1b]11;?"):
				write("\x1b]11;rgb:0000/0000/0000\x1b\\")
				stage, raw = 3, ""
			case stage == 3 && strings.Contains(raw, "38;2;130;170;255"):
				// No notification: the periodic query must still discover the next change.
				stage, raw = 4, ""
			case stage == 4 && strings.Contains(raw, "\x1b]11;?"):
				write("\x1b]11;rgb:ffff/ffff/ffff\x1b\\")
				stage, raw = 5, ""
			case stage == 5 && strings.Contains(raw, "38;2;36;87;178"):
				write("\x1b")
				stage = 6
			}
		case err := <-exited:
			var restored syscall.Termios
			modeErr := terminalMode(keys.file, getTerminalMode, &restored)
			keys.file.Close()
			if chunks != nil {
				for chunk := range chunks {
					transcript += chunk
				}
			}
			if err != nil || stage != 6 || modeErr != nil || restored != keys.original {
				t.Fatalf("theme PTY failed: stage=%d err=%v terminal=%v transcript=%q", stage, err, modeErr, transcript)
			}
			if !strings.Contains(transcript, ansi.ResetModeLightDark) {
				t.Fatal("enabled color scheme notifications were not restored on exit")
			}
			return
		case <-ctx.Done():
			master.Close()
			<-exited
			t.Fatalf("theme PTY timed out at stage %d: %q", stage, transcript)
		}
	}
}
