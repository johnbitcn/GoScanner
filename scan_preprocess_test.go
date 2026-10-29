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

	"golang.org/x/image/tiff"
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
	detector := &fakeDetector{detection: rectangle(480, 600, 760, 920, 0)}
	processor := &imageProcessor{detector: detector, deskew: true, autocrop: true}
	img, _, err := image.Decode(bytes.NewReader(original))
	if err != nil {
		t.Fatal(err)
	}
	cropped, _, err := processor.Process(img)
	if err != nil {
		t.Fatal(err)
	}
	var encoded bytes.Buffer
	if err := jpeg.Encode(&encoded, cropped, &jpeg.Options{Quality: 95}); err != nil {
		t.Fatal(err)
	}
	processed := encoded.Bytes()
	cfg, err := jpeg.DecodeConfig(bytes.NewReader(processed))
	if err != nil || cfg.Width >= 960 || cfg.Height >= 1200 || cfg.ColorModel != color.GrayModel {
		t.Fatalf("crop: %+v %v", cfg, err)
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
			if scenario == "jpeg" {
				output = strings.TrimSuffix(output, ".pdf") + ".jpg"
			}
			opts := defaultOptions()
			opts.Deskew, opts.Autocrop = true, true
			opts.processor = &imageProcessor{detector: &fakeDetector{detection: rectangle(480, 600, 760, 920, 0)}, deskew: true, autocrop: true}
			var wait func(context.Context) (bool, error)
			if scenario == "merged" || scenario == "split" {
				wait = func(context.Context) (bool, error) { return jobs < 2, nil }
			}
			if err := scanPages(context.Background(), c, opts, output, scenario == "split", time.Second*10, wait, &stdout, &stderr); err != nil {
				t.Fatal(err)
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
	opts := defaultOptions()
	opts.Deskew = true
	opts.processor = &imageProcessor{deskew: true, detector: &fakeDetector{}}
	if err := scanPages(context.Background(), c, opts, output, false, time.Second*10, nil, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(output)
	if err != nil || !bytes.Equal(data, original) || !strings.Contains(stderr.String(), "第 1 页预处理警告") {
		t.Fatalf("uncertain detection must preserve original and warn: %v, %s", err, &stderr)
	}
}

func TestPipelineInfoFailureDoesNotReportDetection(t *testing.T) {
	requests := 0
	c := testClient(t, func(w http.ResponseWriter, r *http.Request, action string, _ []byte) {
		requests++
		if action != "GetScannerElements" {
			t.Errorf("created a job after info failure: %s", action)
		}
		http.Error(w, "failed", http.StatusBadRequest)
	})
	opts := defaultOptions()
	opts.Deskew = true
	opts.processor = &imageProcessor{deskew: true, detector: &fakeDetector{}}
	var out, stderr bytes.Buffer
	dir := t.TempDir()
	err := scanPages(context.Background(), c, opts, filepath.Join(dir, "a.jpg"), false, time.Second, nil, &out, &stderr)
	if err == nil || requests != 1 || strings.Contains(out.String(), "Detected:") {
		t.Fatalf("info failure was reported as a detection: err=%v requests=%d stdout=%s", err, requests, &out)
	}
	files, readErr := os.ReadDir(dir)
	if readErr != nil || len(files) != 0 {
		t.Fatalf("unexpected files after info failure: %v, %v", files, readErr)
	}
}

func TestPipelineModelFailureBeforeJob(t *testing.T) {
	t.Setenv("GOSCANNER_MODEL_PATH", filepath.Join(t.TempDir(), "missing"))
	jobs := 0
	c := testClient(t, func(w http.ResponseWriter, r *http.Request, action string, _ []byte) {
		jobs++
		t.Errorf("unexpected request %s", action)
	})
	opts := defaultOptions()
	opts.Deskew = true
	var out, stderr bytes.Buffer
	if err := scanPages(context.Background(), c, opts, filepath.Join(t.TempDir(), "a.jpg"), false, time.Second, nil, &out, &stderr); err == nil || jobs != 0 {
		t.Fatalf("err=%v requests=%d", err, jobs)
	}
}

func TestPipelineBatchAndOriginalConflict(t *testing.T) {
	original, cap := jpegSample(t), fixture(t)
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
	d := &fakeDetector{err: fmt.Errorf("inference failed")}
	calls := 0
	opts := defaultOptions()
	opts.Deskew = true
	opts.newProcessor = func(a, b bool) (*imageProcessor, error) {
		calls++
		return &imageProcessor{detector: d, deskew: a, autocrop: b}, nil
	}
	dir := t.TempDir()
	output := filepath.Join(dir, "scan.pdf")
	var stdout, stderr bytes.Buffer
	wait := func(context.Context) (bool, error) { return jobs < 2, nil }
	if err := scanPages(context.Background(), c, opts, output, false, 10*time.Second, wait, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if calls != 1 || d.calls != 2 || d.closed != 1 || !strings.Contains(stderr.String(), "inference failed") {
		t.Fatalf("calls=%d detect=%d close=%d stderr=%s", calls, d.calls, d.closed, &stderr)
	}
	for i := 1; i <= 2; i++ {
		p := filepath.Join(dir, fmt.Sprintf("scan-original-%03d.jpg", i))
		got, err := os.ReadFile(p)
		if err != nil || !bytes.Equal(got, original) {
			t.Fatalf("original %s: %v", p, err)
		}
		st, _ := os.Stat(p)
		if st.Mode().Perm() != 0600 {
			t.Fatal(st.Mode())
		}
	}
	assertPDF(t, output, 2)
	// A pre-existing original blocks creation of the next job.
	jpg := filepath.Join(dir, "conflict.jpg")
	orig := filepath.Join(dir, "conflict-original.jpg")
	if err := os.WriteFile(orig, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	before := jobs
	if err := scanPages(context.Background(), c, opts, jpg, false, 10*time.Second, nil, &stdout, &stderr); err == nil || jobs != before {
		t.Fatalf("conflict: %v jobs=%d", err, jobs)
	}
	got, _ := os.ReadFile(orig)
	if string(got) != "keep" {
		t.Fatal("original overwritten")
	}
}

func TestPipelineLosslessTIFFAndPNG(t *testing.T) {
	for _, ext := range []string{".tiff", ".png"} {
		t.Run(ext, func(t *testing.T) {
			img := image.NewGray(image.Rect(0, 0, 100, 100))
			for y := 0; y < 100; y++ {
				for x := 0; x < 100; x++ {
					img.SetGray(x, y, color.Gray{Y: uint8((x*17 + y*11) % 256)})
				}
			}
			var source bytes.Buffer
			if err := tiff.Encode(&source, img, nil); err != nil {
				t.Fatal(err)
			}
			cap := fixture(t)
			c := testClient(t, func(w http.ResponseWriter, r *http.Request, action string, _ []byte) {
				switch action {
				case "GetScannerElements":
					_, _ = w.Write(cap)
				case "CreateScanJob":
					io.WriteString(w, responseXML("CreateScanJobResponse", "<w:JobId>7</w:JobId><w:JobToken>token</w:JobToken>"))
				case "RetrieveImage":
					ct, body := mtom(source.Bytes(), "boundary", "cid:image@scanner")
					w.Header().Set("Content-Type", ct)
					_, _ = w.Write(body)
				default:
					t.Errorf("unexpected %s", action)
				}
			})
			opts := defaultOptions()
			opts.Autocrop = true
			opts.processor = &imageProcessor{autocrop: true, detector: &fakeDetector{detection: rectangle(50, 50, 40, 40, 0)}}
			dir := t.TempDir()
			output := filepath.Join(dir, "page"+ext)
			var out, stderr bytes.Buffer
			if err := scanPages(context.Background(), c, opts, output, false, 10*time.Second, nil, &out, &stderr); err != nil {
				t.Fatal(err)
			}
			original, err := os.ReadFile(filepath.Join(dir, "page-original.tiff"))
			if err != nil || !bytes.Equal(original, source.Bytes()) {
				t.Fatalf("original: %v", err)
			}
			raw, err := os.ReadFile(output)
			if err != nil {
				t.Fatal(err)
			}
			got, format, err := image.Decode(bytes.NewReader(raw))
			if err != nil {
				t.Fatal(err)
			}
			wantFormat := "tiff"
			if ext == ".png" {
				wantFormat = "png"
			}
			if format != wantFormat || got.Bounds().Dx() != 46 || got.Bounds().Dy() != 46 {
				t.Fatalf("%s %v", format, got.Bounds())
			}
			for y := 0; y < 46; y++ {
				for x := 0; x < 46; x++ {
					if color.GrayModel.Convert(got.At(x, y)) != img.GrayAt(x+27, y+27) {
						t.Fatalf("pixel %d,%d changed", x, y)
					}
				}
			}
		})
	}
}

func TestPipelinePDFFailureKeepsOriginal(t *testing.T) {
	dir := t.TempDir()
	output := filepath.Join(dir, "scan.pdf")
	original, cap := jpegSample(t), fixture(t)
	c := testClient(t, func(w http.ResponseWriter, r *http.Request, action string, _ []byte) {
		switch action {
		case "GetScannerElements":
			_, _ = w.Write(cap)
		case "CreateScanJob":
			io.WriteString(w, responseXML("CreateScanJobResponse", "<w:JobId>7</w:JobId><w:JobToken>token</w:JobToken>"))
		case "RetrieveImage":
			if err := os.WriteFile(output, []byte("occupied"), 0600); err != nil {
				t.Error(err)
			}
			ct, body := mtom(original, "boundary", "cid:image@scanner")
			w.Header().Set("Content-Type", ct)
			_, _ = w.Write(body)
		default:
			t.Errorf("unexpected %s", action)
		}
	})
	opts := defaultOptions()
	opts.Deskew = true
	opts.processor = &imageProcessor{deskew: true, detector: &fakeDetector{}}
	var out, stderr bytes.Buffer
	err := scanPages(context.Background(), c, opts, output, false, 10*time.Second, nil, &out, &stderr)
	if err == nil || !strings.Contains(err.Error(), "保留") {
		t.Fatalf("missing PDF recovery: %v", err)
	}
	got, e := os.ReadFile(filepath.Join(dir, "scan-original-001.jpg"))
	if e != nil || !bytes.Equal(got, original) {
		t.Fatalf("original: %v", e)
	}
	got, e = os.ReadFile(output)
	if e != nil || string(got) != "occupied" {
		t.Fatalf("output overwritten: %v", e)
	}
	if !strings.Contains(out.String(), "scan-original-001.jpg") {
		t.Fatal("missing recovery path")
	}
}
