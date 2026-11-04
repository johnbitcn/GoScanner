package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestStructuredProcessingAndRetainedPDF(t *testing.T) {
	data, image := fixture(t), jpegSample(t)
	c := testClient(t, func(w http.ResponseWriter, r *http.Request, action string, _ []byte) {
		switch action {
		case "GetScannerElements":
			w.Write(data)
		case "CreateScanJob":
			io.WriteString(w, responseXML("CreateScanJobResponse", "<w:JobId>7</w:JobId><w:JobToken>token</w:JobToken>"))
		case "RetrieveImage":
			ct, body := mtom(image, "boundary", "cid:image@scanner")
			w.Header().Set("Content-Type", ct)
			w.Write(body)
		default:
			t.Errorf("unexpected action %s", action)
		}
	})
	t.Run("processing", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "scan.jpg")
		opts := defaultOptions()
		opts.Deskew = true
		opts.processor = &imageProcessor{deskew: true, detector: &fakeDetector{}}
		r := commandRequest{command: "scan", endpoint: c.endpoint, output: path, timeout: time.Second, opts: opts}
		result, code := executeCommand(context.Background(), r, nil, commandHooks{}, io.Discard, io.Discard)
		if code != 0 || len(result.Originals) != 1 || len(result.Warnings) == 0 || len(result.Processing) != 1 || result.Processing[0].AngleReliable {
			t.Fatalf("%d %+v", code, result)
		}
		assertSavedImage(t, result.Originals[0], image)
		encoded, err := json.Marshal(result)
		if err != nil || !bytes.Contains(encoded, []byte(`"angle_reliable":false`)) || !bytes.Contains(encoded, []byte(`"page":1`)) {
			t.Fatalf("%s %v", encoded, err)
		}
	})
	t.Run("retained", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "scan.pdf")
		r := commandRequest{command: "scan", endpoint: c.endpoint, output: path, timeout: time.Second, opts: defaultOptions()}
		hooks := commandHooks{wait: func(context.Context) (bool, error) { return false, os.WriteFile(path, []byte("existing"), 0600) }}
		result, code := executeCommand(context.Background(), r, nil, hooks, io.Discard, io.Discard)
		if code != 1 || result.Status != "partial" || len(result.Files) != 0 || result.Pages != 0 || len(result.Retained) == 0 {
			t.Fatalf("%d %+v", code, result)
		}
		kept, err := os.ReadFile(path)
		if err != nil || string(kept) != "existing" {
			t.Fatalf("overwrote output: %q %v", kept, err)
		}
		pages, err := filepath.Glob(filepath.Join(result.Retained[0], "*.jpg"))
		if err != nil || len(pages) != 1 {
			t.Fatalf("retained pages=%v err=%v", pages, err)
		}
		assertSavedImage(t, pages[0], image)
	})
}
