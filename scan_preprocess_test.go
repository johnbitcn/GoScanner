package main

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func integrationPaperJPEG(t *testing.T) []byte {
	t.Helper()
	img := image.NewGray(image.Rect(0, 0, 960, 1200))
	draw.Draw(img, img.Bounds(), image.NewUniform(color.Gray{Y: 40}), image.Point{}, draw.Src)
	draw.Draw(img, image.Rect(100, 140, 860, 1060), image.White, image.Point{}, draw.Src)
	for y := 240; y < 960; y += 50 {
		draw.Draw(img, image.Rect(170, y, 760, y+5), image.Black, image.Point{}, draw.Src)
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 95}); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestPreprocessingOutputPipeline(t *testing.T) {
	original := integrationPaperJPEG(t)
	processed, warnings, err := preprocessJPEG(original, true, true)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := jpeg.DecodeConfig(bytes.NewReader(processed))
	if err != nil || cfg.Width >= 960 || cfg.Height >= 1200 || cfg.Width < 760 || cfg.Height < 920 {
		t.Fatalf("paper crop failed: %+v, %v, warnings=%v", cfg, err, warnings)
	}
	if cfg.ColorModel != color.GrayModel {
		t.Fatal("gray scan became a color JPEG")
	}
	for _, scenario := range []string{"jpeg", "pdf", "merged", "split"} {
		t.Run(scenario, func(t *testing.T) {
			cap := fixture(t)
			jobs := 0
			c := testClient(t, func(w http.ResponseWriter, r *http.Request, action string, _ []byte) {
				switch action {
				case "GetScannerElements":
					_, _ = w.Write(cap)
				case "CreateScanJob":
					jobs++
					io.WriteString(w, responseXML("CreateScanJobResponse", "<w:JobId>7</w:JobId><w:JobToken>token</w:JobToken>"))
				case "RetrieveImage":
					ct, body := mtom(original, "boundary", "cid:image@scanner")
					w.Header().Set("Content-Type", ct)
					_, _ = w.Write(body)
				default:
					t.Errorf("unexpected action %s", action)
				}
			})
			output := filepath.Join(t.TempDir(), "scan.pdf")
			var stdout, stderr bytes.Buffer
			if scenario == "jpeg" || scenario == "pdf" {
				if scenario == "jpeg" {
					output = strings.TrimSuffix(output, ".pdf") + ".jpg"
				}
				args := []string{"scan", "--endpoint", c.endpoint, "--deskew", "--autocrop", "--mode", "gray", "-o", output}
				if code := run(context.Background(), args, &stdout, &stderr); code != 0 {
					t.Fatalf("CLI exit=%d: %s", code, &stderr)
				}
			} else {
				opts := defaultOptions()
				opts.Deskew, opts.Autocrop = true, true
				wait := func(context.Context) (bool, error) { return jobs < 2, nil }
				if err := scanPages(context.Background(), c, opts, output, scenario == "split", time.Second*10, wait, &stdout, &stderr); err != nil {
					t.Fatal(err)
				}
			}
			paths := []string{output}
			if scenario == "split" {
				paths = []string{strings.TrimSuffix(output, ".pdf") + "-001.pdf", strings.TrimSuffix(output, ".pdf") + "-002.pdf"}
			}
			for _, path := range paths {
				data, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				if scenario == "jpeg" {
					if !bytes.Equal(data, processed) || !strings.Contains(stdout.String(), fmt.Sprintf("%d × %d", cfg.Width, cfg.Height)) {
						t.Fatal("saved JPEG or reported dimensions differ from processed image")
					}
				} else {
					pages := 1
					if scenario == "merged" {
						pages = 2
					}
					assertPDF(t, path, pages)
					if bytes.Count(data, processed) != pages || !bytes.Contains(data, []byte(fmt.Sprintf("/Width %d /Height %d /ColorSpace /DeviceGray", cfg.Width, cfg.Height))) {
						t.Fatal("PDF did not embed processed grayscale pages at processed dimensions")
					}
				}
			}
			staging, _ := filepath.Glob(filepath.Join(filepath.Dir(output), ".goscanner*"))
			if len(staging) != 0 {
				t.Fatalf("temporary files leaked: %v", staging)
			}
		})
	}
}

func TestPreprocessingWarningReachesCLI(t *testing.T) {
	original, cap := jpegSample(t), fixture(t)
	c := testClient(t, func(w http.ResponseWriter, r *http.Request, action string, _ []byte) {
		switch action {
		case "GetScannerElements":
			_, _ = w.Write(cap)
		case "CreateScanJob":
			io.WriteString(w, responseXML("CreateScanJobResponse", "<w:JobId>7</w:JobId><w:JobToken>token</w:JobToken>"))
		case "RetrieveImage":
			ct, body := mtom(original, "boundary", "cid:image@scanner")
			w.Header().Set("Content-Type", ct)
			_, _ = w.Write(body)
		default:
			t.Errorf("unexpected action %s", action)
		}
	})
	output := filepath.Join(t.TempDir(), "scan.jpg")
	var stdout, stderr bytes.Buffer
	if code := run(context.Background(), []string{"scan", "--endpoint", c.endpoint, "--deskew", "--autocrop", "-o", output}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit=%d: %s", code, &stderr)
	}
	data, err := os.ReadFile(output)
	if err != nil || !bytes.Equal(data, original) || !strings.Contains(stderr.String(), "第 1 页预处理警告") {
		t.Fatalf("uncertain detection must preserve original and warn: %v, %s", err, &stderr)
	}
}
