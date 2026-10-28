package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestScanPDFPages(t *testing.T) {
	for _, scenario := range []string{"merged", "split", "single", "existing", "existing-split-second", "second-fails", "first-fails", "wait-error", "cancel", "publish-race", "split-publish-race"} {
		t.Run(scenario, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			dir := t.TempDir()
			output := filepath.Join(dir, "scan.pdf")
			split := scenario == "split" || scenario == "existing-split-second" || scenario == "split-publish-race"
			if scenario == "existing" || scenario == "existing-split-second" {
				path := output
				if split {
					path = filepath.Join(dir, "scan-002.pdf")
				}
				if err := os.WriteFile(path, []byte("keep"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			image, cap := jpegSample(t), fixture(t)
			var jobs, cancels atomic.Int32
			c := testClient(t, func(w http.ResponseWriter, r *http.Request, action string, _ []byte) {
				switch action {
				case "GetScannerElements":
					_, _ = w.Write(cap)
				case "CreateScanJob":
					jobs.Add(1)
					io.WriteString(w, responseXML("CreateScanJobResponse", "<w:JobId>7</w:JobId><w:JobToken>token</w:JobToken>"))
				case "RetrieveImage":
					if scenario == "first-fails" || scenario == "second-fails" && jobs.Load() == 2 {
						http.Error(w, "failed", http.StatusInternalServerError)
						return
					}
					if scenario == "split-publish-race" {
						if err := os.WriteFile(filepath.Join(dir, "scan-001.pdf"), []byte("keep"), 0600); err != nil {
							t.Error(err)
						}
					}
					ct, body := mtom(image, "boundary", "cid:image@scanner")
					w.Header().Set("Content-Type", ct)
					_, _ = w.Write(body)
				case "CancelJob":
					cancels.Add(1)
					io.WriteString(w, responseXML("CancelJobResponse", ""))
				default:
					t.Errorf("unexpected action %s", action)
				}
			})
			waits := 0
			var wait func(context.Context) (bool, error) = func(ctx context.Context) (bool, error) {
				waits++
				if jobs.Load() != int32(waits) {
					t.Fatal("scanned ahead of user input")
				}
				if split {
					assertPDF(t, filepath.Join(dir, fmt.Sprintf("scan-%03d.pdf", waits)), 1)
				} else if _, err := os.Stat(output); !os.IsNotExist(err) {
					t.Fatal("merged PDF must be published on completion, not mid-scan")
				}
				switch scenario {
				case "wait-error":
					return false, io.ErrUnexpectedEOF
				case "cancel":
					cancel()
					return false, ctx.Err()
				case "publish-race":
					if err := os.WriteFile(output, []byte("keep"), 0600); err != nil {
						t.Fatal(err)
					}
					return false, nil
				}
				return waits < 2, nil
			}
			if scenario == "single" {
				wait = nil
			}
			var stdout, stderr bytes.Buffer
			err := scanPages(ctx, c, defaultOptions(), output, split, time.Second, wait, &stdout, &stderr)
			wantError := scenario != "merged" && scenario != "split" && scenario != "single"
			if (err != nil) != wantError {
				t.Fatalf("error=%v wantError=%v stdout=%s stderr=%s", err, wantError, &stdout, &stderr)
			}
			switch scenario {
			case "merged":
				assertPDF(t, output, 2)
				if jobs.Load() != 2 || waits != 2 || !strings.Contains(stdout.String(), "共保存 2 页") {
					t.Fatal("incorrect merged scan", jobs.Load(), waits, stdout.String())
				}
			case "split":
				assertPDF(t, filepath.Join(dir, "scan-001.pdf"), 1)
				assertPDF(t, filepath.Join(dir, "scan-002.pdf"), 1)
				if _, err := os.Stat(output); !os.IsNotExist(err) {
					t.Fatal("unexpected merged file")
				}
			case "single", "second-fails", "wait-error", "cancel":
				assertPDF(t, output, 1)
			case "existing":
				got, _ := os.ReadFile(output)
				if string(got) != "keep" || jobs.Load() != 0 {
					t.Fatal("existing PDF overwritten or scan started")
				}
			case "existing-split-second":
				assertPDF(t, filepath.Join(dir, "scan-001.pdf"), 1)
				got, _ := os.ReadFile(filepath.Join(dir, "scan-002.pdf"))
				if string(got) != "keep" || jobs.Load() != 1 {
					t.Fatal("existing second PDF overwritten or extra scan started")
				}
			case "first-fails":
				if _, err := os.Stat(output); !os.IsNotExist(err) {
					t.Fatal("empty PDF published")
				}
			case "publish-race", "split-publish-race":
				path := output
				if split {
					path = filepath.Join(dir, "scan-001.pdf")
				}
				got, _ := os.ReadFile(path)
				if string(got) != "keep" || !strings.Contains(err.Error(), "完整 PDF 保留在") {
					t.Fatal("race overwrote output or recovery path missing", err)
				}
				staging, _ := filepath.Glob(filepath.Join(dir, ".goscanner-pdf-*", "*.jpg"))
				if len(staging) != 1 {
					t.Fatal("completed JPEG lost", staging)
				}
				got, _ = os.ReadFile(staging[0])
				if !bytes.Equal(got, image) {
					t.Fatal("recovery JPEG damaged")
				}
				return
			}
			if scenario == "cancel" && !errors.Is(err, context.Canceled) || scenario == "wait-error" && !errors.Is(err, io.ErrUnexpectedEOF) {
				t.Fatal("lost original error", err)
			}
			if scenario == "second-fails" && (jobs.Load() != 2 || cancels.Load() != 1 || waits != 1) {
				t.Fatal("unexpected retry, missing cancel, or extra prompt")
			}
			staging, _ := filepath.Glob(filepath.Join(dir, ".goscanner*"))
			if len(staging) != 0 {
				t.Fatal("temporary files leaked", staging)
			}
		})
	}
}
