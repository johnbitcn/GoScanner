package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCLIValidation(t *testing.T) {
	cases := []struct {
		args []string
		code int
	}{
		{nil, 0}, {[]string{"help"}, 0}, {[]string{"--help"}, 0},
		{[]string{"scan", "-h"}, 0}, {[]string{"unknown"}, 2},
		{[]string{"scan", "--dpi", "bad"}, 2}, {[]string{"scan", "--dpi", "0"}, 2},
		{[]string{"scan", "--mode", "bad"}, 2}, {[]string{"scan", "--mode", "bw"}, 2}, {[]string{"scan", "--area", "NaN,0,1,1"}, 2},
		{[]string{"info", "--host", "http://192.168.1.8"}, 2},
		{[]string{"info", "--timeout", "0s"}, 2}, {[]string{"info", "extra"}, 2},
		{[]string{"scan", "-o", "not-jpeg.png"}, 1},
		{[]string{"scan", "--single-page"}, 2},
		{[]string{"scan", "--single-page", "-o", "scan.jpg"}, 2},
		{[]string{"info", "--single-page"}, 2},
		{[]string{"discover", "-h"}, 0}, {[]string{"discover", "--host", "127.0.0.1"}, 2},
		{[]string{"info", "--host", ""}, 2}, {[]string{"info", "--endpoint", ""}, 2},
		{[]string{"info", "--host", "127.0.0.1", "--endpoint", "http://127.0.0.1/wsd/scan"}, 2},
		{[]string{"info", "--endpoint", "http://scanner.local/scan"}, 2},
		{[]string{"info", "--endpoint", "http://user:password@127.0.0.1/scan"}, 2},
	}
	for _, tc := range cases {
		t.Run(fmt.Sprint(tc.args), func(t *testing.T) {
			var out, stderr bytes.Buffer
			if code := run(context.Background(), tc.args, &out, &stderr); code != tc.code {
				t.Fatalf("exit=%d, expected=%d; %s", code, tc.code, stderr.String())
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if exitCode(ctx) != 130 || exitCode(context.Background()) != 1 {
		t.Fatal("wrong cancellation exit code")
	}
}

func TestCLIExplicitEndpoint(t *testing.T) {
	for _, command := range []string{"info", "scan", "scan-pdf", "scan-pdf-single"} {
		t.Run(command, func(t *testing.T) {
			cap := fixture(t)
			image := jpegSample(t)
			c := testClient(t, func(w http.ResponseWriter, r *http.Request, action string, _ []byte) {
				switch action {
				case "GetScannerElements":
					_, _ = w.Write(cap)
				case "CreateScanJob":
					io.WriteString(w, responseXML("CreateScanJobResponse", "<w:JobId>7</w:JobId><w:JobToken>dummy</w:JobToken>"))
				case "RetrieveImage":
					ct, data := mtom(image, "boundary", "cid:image@scanner")
					w.Header().Set("Content-Type", ct)
					_, _ = w.Write(data)
				default:
					t.Errorf("unexpected action %s", action)
				}
			})
			cliCommand := command
			if strings.HasPrefix(command, "scan") {
				cliCommand = "scan"
			}
			args := []string{cliCommand, "--endpoint", c.endpoint}
			output := filepath.Join(t.TempDir(), "scan.jpg")
			if strings.HasPrefix(command, "scan-pdf") {
				output = filepath.Join(filepath.Dir(output), "scan.PDF")
			}
			if cliCommand == "scan" {
				args = append(args, "-o", output)
			}
			if command == "scan-pdf-single" {
				args = append(args, "--single-page")
			}
			var stdout, stderr bytes.Buffer
			if code := run(context.Background(), args, &stdout, &stderr); code != 0 {
				t.Fatalf("exit %d: %s", code, stderr.String())
			}
			if cliCommand == "scan" {
				if strings.HasPrefix(command, "scan-pdf") {
					assertPDF(t, output, 1)
				}
				if _, err := os.Stat(output); err != nil {
					t.Fatal(err)
				}
			} else if !strings.Contains(stdout.String(), `"state": "Idle"`) {
				t.Fatal("missing capability JSON")
			}
		})
	}
}
