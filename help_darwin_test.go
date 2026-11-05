package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"github.com/charmbracelet/x/ansi"
)

func TestHelpPTY(t *testing.T) {
	for _, tc := range []struct {
		name                 string
		width                int
		term, noColor, force string
		agent                bool
		colored              bool
	}{
		{"wide-color", 140, "xterm-256color", "", "0", false, true},
		{"normal-color", 80, "xterm-256color", "", "0", false, true},
		{"narrow-color", 40, "xterm-256color", "", "0", false, true},
		{"no-color", 80, "xterm-256color", "1", "1", false, false},
		{"dumb", 80, "dumb", "", "1", false, false},
		{"agent", 40, "xterm-256color", "", "1", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
			defer cancel()
			args := []string{"-test.run=^TestManualCLIProcess$", "--"}
			if tc.agent {
				args = append(args, "--agent")
			}
			args = append(args, "scan", "--help")
			cmd := exec.CommandContext(ctx, os.Args[0], args...)
			cmd.Env = append(os.Environ(), "GOSCANNER_TEST_MANUAL_CHILD=1", "HOME="+t.TempDir(), "TERM="+tc.term, "NO_COLOR="+tc.noColor, "CLICOLOR=1", "CLICOLOR_FORCE="+tc.force)
			keys, master := testKeyInput(t)
			window := [4]uint16{24, uint16(tc.width), 0, 0}
			if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, keys.file.Fd(), syscall.TIOCSWINSZ, uintptr(unsafe.Pointer(&window[0]))); errno != 0 {
				t.Fatal(errno)
			}
			var output, stderr bytes.Buffer
			cmd.Stdout, cmd.Stderr = keys.file, &stderr
			cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
			done := make(chan struct{})
			go func() { io.Copy(&output, master); close(done) }()
			err := cmd.Run()
			var restored syscall.Termios
			modeErr := terminalMode(keys.file, getTerminalMode, &restored)
			keys.file.Close()
			select {
			case <-done:
			case <-ctx.Done():
				master.Close()
				<-done
				t.Fatal("PTY did not finish")
			}
			if err != nil || stderr.Len() != 0 {
				t.Fatalf("help failed: %v %s", err, stderr.String())
			}
			if modeErr != nil || restored != keys.original {
				t.Fatal("help changed terminal mode", modeErr)
			}
			raw := strings.ReplaceAll(output.String(), "\r", "")
			if hasTerminalColor(raw) != tc.colored {
				t.Fatalf("wrong terminal colors: %q", raw)
			}
			if !tc.colored && strings.ContainsRune(raw, '\x1b') {
				t.Fatal("plain help contains terminal escapes")
			}
			for _, escape := range []string{"\x1b[2J", "\x1b[?1049h", "\x1b[?25l"} {
				if strings.Contains(raw, escape) {
					t.Fatal("help started an interactive display")
				}
			}
			if tc.agent {
				var result commandResult
				if err := json.Unmarshal([]byte(raw), &result); err != nil || result.Status != "ok" || result.Help != renderHelp("scan", 80, false) {
					t.Fatalf("Agent must retain stable plain JSON help: %s %v", raw, err)
				}
				return
			}
			if ansi.Strip(raw) != renderHelp("scan", min(tc.width, 88), false) {
				t.Fatal("actual terminal layout differs from rendered help", raw)
			}
			// Defaults can wrap across lines; assert their style without fixing one numeric value.
			mutedPrefix := strings.Split(tuiMuted.Render("x"), "x")[0]
			if tc.colored && (!strings.Contains(raw, "\x1b[1m--port PORT") || !strings.Contains(raw, mutedPrefix)) {
				t.Fatal("option styling missing in actual terminal")
			}
		})
	}
}
