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

func TestScanPages(t *testing.T) {
	for _, ext := range []string{".jpeg", ".tiff", ".png"} {
		for _, scenario := range []string{"two-pages", "existing-second-page", "second-page-fails", "wait-error", "cancel-wait", "single-page", "page-timeout"} {
			t.Run(ext+"/"+scenario, func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				output := filepath.Join(t.TempDir(), "scan"+ext)
				second := filepath.Join(filepath.Dir(output), "scan-002"+ext)
				if scenario == "existing-second-page" {
					if err := os.WriteFile(second, []byte("keep"), 0600); err != nil {
						t.Fatal(err)
					}
				}
				image, data := jpegSample(t), fixture(t)
				if ext != ".jpeg" {
					image = tiffSample(t, false)
				}
				var jobs, cancels atomic.Int32
				c := testClient(t, func(w http.ResponseWriter, r *http.Request, action string, _ []byte) {
					switch action {
					case "GetScannerElements":
						_, _ = w.Write(data)
					case "CreateScanJob":
						jobs.Add(1)
						if scenario == "page-timeout" {
							<-r.Context().Done()
							return
						}
						io.WriteString(w, responseXML("CreateScanJobResponse", "<w:JobId>7</w:JobId><w:JobToken>token</w:JobToken>"))
					case "RetrieveImage":
						if scenario == "second-page-fails" && jobs.Load() == 2 {
							http.Error(w, "failed", http.StatusInternalServerError)
							return
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
				var wait func(context.Context) (bool, error) = func(waitCtx context.Context) (bool, error) {
					waits++
					if jobs.Load() != int32(waits) {
						t.Fatal("scanned ahead of user input")
					}
					path := filepath.Join(filepath.Dir(output), fmt.Sprintf("scan-%03d%s", waits, ext))
					assertSavedImage(t, path, image)
					switch scenario {
					case "wait-error":
						return false, io.ErrUnexpectedEOF
					case "cancel-wait":
						cancel()
						return false, waitCtx.Err()
					case "two-pages":
						if waits == 1 {
							time.Sleep(600 * time.Millisecond) // 比每页超时长，等待不应影响下一页。
						}
					}
					return waits < 2, nil
				}
				if scenario == "single-page" {
					wait = nil
				}
				var stdout, stderr bytes.Buffer
				err := scanPages(ctx, c, defaultOptions(), output, false, 500*time.Millisecond, wait, &stdout, &stderr)
				wantError := scenario != "two-pages" && scenario != "single-page"
				if (err != nil) != wantError {
					t.Fatalf("error = %v, wantError = %v", err, wantError)
				}
				if scenario == "two-pages" && (jobs.Load() != 2 || waits != 2 || !strings.Contains(stdout.String(), "共保存 2 页")) {
					t.Fatalf("jobs=%d waits=%d output=%s", jobs.Load(), waits, stdout.String())
				}
				if scenario == "single-page" {
					if _, err := os.Stat(output); err != nil || jobs.Load() != 1 || waits != 0 {
						t.Fatalf("single-page behavior changed: %v, jobs=%d waits=%d", err, jobs.Load(), waits)
					}
				} else if scenario != "page-timeout" {
					if _, err := os.Stat(filepath.Join(filepath.Dir(output), "scan-001"+ext)); err != nil {
						t.Fatal("lost completed first page", err)
					}
				}
				if scenario == "existing-second-page" {
					got, _ := os.ReadFile(second)
					if string(got) != "keep" || jobs.Load() != 1 {
						t.Fatal("existing page overwritten or extra job created")
					}
				}
				if scenario == "second-page-fails" && (jobs.Load() != 2 || cancels.Load() != 1 || waits != 1) {
					t.Fatal("failed page retried, not cancelled, or prompted again")
				}
				if scenario == "wait-error" && !errors.Is(err, io.ErrUnexpectedEOF) {
					t.Fatal(err)
				}
				if scenario == "cancel-wait" && !errors.Is(err, context.Canceled) {
					t.Fatal(err)
				}
				if scenario == "page-timeout" && (!errors.Is(err, context.DeadlineExceeded) || jobs.Load() != 1 || waits != 0) {
					t.Fatalf("per-page timeout failed: %v jobs=%d waits=%d", err, jobs.Load(), waits)
				}
				if scenario != "two-pages" && scenario != "single-page" && scenario != "second-page-fails" && jobs.Load() != 1 {
					t.Fatal("unexpected scan after failure")
				}
			})
		}
	}
}
