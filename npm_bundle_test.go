package main

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/jpeg"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// This test exercises the installed npm command against a loopback scanner, never a real device.
func TestNpmBundleScan(t *testing.T) {
	cli := os.Getenv("GOSCANNER_TEST_CLI")
	if cli == "" {
		t.Skip("set GOSCANNER_TEST_CLI to the installed npm command")
	}
	picture := image.NewRGBA(image.Rect(0, 0, 256, 256))
	for y := 0; y < 256; y++ {
		for x := 0; x < 256; x++ {
			v := uint8(0)
			if x > 40 && x < 210 && y > 50 && y < 200 {
				v = 255
			}
			picture.SetRGBA(x, y, color.RGBA{v, v, v, 255})
		}
	}
	var encoded bytes.Buffer
	if err := jpeg.Encode(&encoded, picture, nil); err != nil {
		t.Fatal(err)
	}
	caps := fixture(t)
	c := testClient(t, func(w http.ResponseWriter, r *http.Request, action string, data []byte) {
		w.Header().Set("Content-Type", "application/soap+xml")
		switch action {
		case "GetScannerElements":
			_, _ = w.Write(caps)
		case "CreateScanJob":
			io.WriteString(w, responseXML("CreateScanJobResponse", "<w:JobId>7</w:JobId><w:JobToken>npm-test</w:JobToken>"))
		case "RetrieveImage":
			ct, body := mtom(encoded.Bytes(), "npm-boundary", "cid:image@scanner")
			w.Header().Set("Content-Type", ct)
			_, _ = w.Write(body)
		case "CancelJob":
			io.WriteString(w, responseXML("CancelJobResponse", ""))
		default:
			t.Errorf("unexpected action %s", action)
		}
	})
	endpoint, err := url.Parse(c.endpoint)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	for _, ai := range []bool{false, true} {
		t.Run(map[bool]string{false: "ordinary", true: "ai"}[ai], func(t *testing.T) {
			directory := t.TempDir()
			args := []string{"scan", "--host", endpoint.Hostname(), "--port", endpoint.Port(), "-o", "scan.jpg"}
			if ai {
				args = append(args, "--deskew", "--autocrop")
			}
			cmd := exec.CommandContext(ctx, cli, args...)
			cmd.Dir = directory
			for _, entry := range os.Environ() {
				if !strings.HasPrefix(entry, "GOSCANNER_MODEL_PATH=") && !strings.HasPrefix(entry, "GOSCANNER_ONNXRUNTIME_PATH=") && !strings.HasPrefix(entry, "HOME=") {
					cmd.Env = append(cmd.Env, entry)
				}
			}
			cmd.Env = append(cmd.Env, "HOME="+directory)
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("installed CLI: %v\n%s", err, out)
			}
			file, err := os.Open(filepath.Join(directory, "scan.jpg"))
			if err != nil {
				t.Fatal(err)
			}
			defer file.Close()
			if _, err := jpeg.Decode(file); err != nil {
				t.Fatal(err)
			}
			if ai && (!bytes.Contains(out, []byte("Detected:")) || bytes.Contains(out, []byte("preprocess: detection:"))) {
				t.Fatalf("AI processing did not complete: %s", out)
			}
		})
	}
}
