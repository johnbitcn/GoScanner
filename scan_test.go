package main

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func defaultOptions() options {
	return options{DPI: 300, Mode: "color", Area: region{Width: 8268, Height: 11693}}
}

func TestAreaAndTicketValidation(t *testing.T) {
	cap, err := parseInfo(fixture(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"a4", "10,20,100,100"} {
		area, err := parseArea(text)
		if err != nil {
			t.Fatal(err)
		}
		opts := defaultOptions()
		opts.Area = area
		if _, err := ticket(cap, opts); err != nil {
			t.Fatal(err)
		}
	}
	for _, text := range []string{"", "a3", "1,2,3", "NaN,0,1,1", "0,Inf,1,1", "0,0,-1,1", "0,0,0,1", "1e50,0,1,1"} {
		if _, err := parseArea(text); err == nil {
			t.Errorf("accepted area %q", text)
		}
	}
	for _, mutate := range []func(*options){
		func(o *options) { o.DPI = 600 }, func(o *options) { o.Mode = "invalid" }, func(o *options) { o.Mode = "bw" },
		func(o *options) { o.Area.X = 500 }, func(o *options) { o.Area.Width = 1 },
		func(o *options) { o.Area.X = -1 },
	} {
		opts := defaultOptions()
		mutate(&opts)
		if _, err := ticket(cap, opts); err == nil {
			t.Fatalf("accepted options %+v", opts)
		}
	}
	cap.State = "Processing"
	if _, err := ticket(cap, defaultOptions()); err == nil {
		t.Fatal("accepted busy device")
	}
}

func TestScanLifecycle(t *testing.T) {
	for _, scenario := range []string{"success", "download-fault", "missing-token", "corrupt-jpeg", "cancelled", "create-timeout", "publish-race"} {
		t.Run(scenario, func(t *testing.T) {
			output := filepath.Join(t.TempDir(), "scan.jpg")
			image := jpegSample(t)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if scenario == "create-timeout" {
				var stop context.CancelFunc
				ctx, stop = context.WithTimeout(ctx, 200*time.Millisecond)
				defer stop()
			}
			var mu sync.Mutex
			var actions []string
			fixtureData := fixture(t)
			c := testClient(t, func(w http.ResponseWriter, r *http.Request, action string, data []byte) {
				mu.Lock()
				actions = append(actions, action)
				mu.Unlock()
				w.Header().Set("Content-Type", "application/soap+xml")
				switch action {
				case "GetScannerElements":
					_, _ = w.Write(fixtureData)
				case "CreateScanJob":
					if scenario == "create-timeout" {
						<-r.Context().Done()
						return
					}
					if !bytes.Contains(data, []byte("<w:ImagesToTransfer>1</w:ImagesToTransfer>")) {
						t.Error("not a single-page ticket")
					}
					payload := "<w:JobId>7</w:JobId>"
					if scenario != "missing-token" {
						payload += "<w:JobToken> dummy&lt;&amp;token </w:JobToken>"
					}
					io.WriteString(w, responseXML("CreateScanJobResponse", payload))
				case "RetrieveImage":
					var root element
					if err := xml.Unmarshal(data, &root); err != nil {
						t.Error(err)
					}
					req := root.child(soapNS, "Body").path("RetrieveImageRequest")
					if req.path("JobToken").Text != " dummy<&token " || req.path("JobId").text() != "7" {
						t.Error("job identity or escaping lost")
					}
					if scenario == "cancelled" {
						cancel()
						<-r.Context().Done()
						return
					}
					if scenario == "download-fault" {
						io.WriteString(w, fmt.Sprintf(`<s:Envelope xmlns:s="%s"><s:Body><s:Fault><s:Reason><s:Text>Failed</s:Text></s:Reason></s:Fault></s:Body></s:Envelope>`, soapNS))
						return
					}
					if scenario == "publish-race" {
						if err := os.WriteFile(output, []byte("existing"), 0600); err != nil {
							t.Error(err)
						}
					}
					payload := image
					if scenario == "corrupt-jpeg" {
						payload = []byte{0xff, 0xd8, 0xff, 0xe0, 0xff, 0xd9}
					}
					ct, body := mtom(payload, "boundary", "cid:image@scanner")
					w.Header().Set("Content-Type", ct)
					_, _ = w.Write(body)
				case "CancelJob":
					if r.Context().Err() != nil || !bytes.Contains(data, []byte("<w:JobId>7</w:JobId>")) {
						t.Error("cancel did not use independent context and own job")
					}
					io.WriteString(w, responseXML("CancelJobResponse", ""))
				default:
					t.Errorf("unexpected action %s", action)
				}
			})
			result, err := c.scan(ctx, defaultOptions(), output)
			mu.Lock()
			calls := append([]string(nil), actions...)
			mu.Unlock()
			if scenario == "success" {
				if err != nil {
					t.Fatal(err)
				}
				data, readErr := os.ReadFile(output)
				if readErr != nil || !bytes.Equal(data, image) || result.Width != 8 || result.Height != 8 {
					t.Fatalf("invalid result %+v %v", result, readErr)
				}
				stat, _ := os.Stat(output)
				if stat.Mode().Perm() != 0600 {
					t.Fatal("scan file is not private")
				}
				if strings.Join(calls, ",") != "GetScannerElements,CreateScanJob,RetrieveImage" {
					t.Fatalf("unexpected calls %v", calls)
				}
			} else {
				if err == nil {
					t.Fatal("expected failure")
				}
				if strings.Contains(err.Error(), "dummy") {
					t.Fatal("job token leaked in error")
				}
				if scenario == "publish-race" {
					data, _ := os.ReadFile(output)
					if string(data) != "existing" {
						t.Fatal("overwrote concurrently created file")
					}
				} else if _, statErr := os.Lstat(output); !os.IsNotExist(statErr) {
					t.Fatalf("incomplete destination remains: %v", statErr)
				}
				if scenario == "create-timeout" {
					if strings.Join(calls, ",") != "GetScannerElements,CreateScanJob" {
						t.Fatalf("creation was retried or unknown job cancelled: %v", calls)
					}
				} else if calls[len(calls)-1] != "CancelJob" {
					t.Fatalf("own job not cancelled: %v", calls)
				}
			}
			staging, _ := filepath.Glob(filepath.Join(filepath.Dir(output), ".goscanner-*.jpg.part"))
			if scenario == "publish-race" {
				if len(staging) != 1 {
					t.Fatal("completed image was not retained for recovery")
				}
				data, _ := os.ReadFile(staging[0])
				if !bytes.Equal(data, image) {
					t.Fatal("retained image corrupted")
				}
			} else if len(staging) != 0 {
				t.Fatalf("staging files left behind: %v", staging)
			}
		})
	}
}

func TestExistingOutputAndSymlink(t *testing.T) {
	for _, symlink := range []bool{false, true} {
		t.Run(fmt.Sprint(symlink), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "existing.jpg")
			if symlink {
				if err := os.Symlink("missing-target", path); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.WriteFile(path, []byte("keep"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			data := fixture(t)
			c := testClient(t, func(w http.ResponseWriter, r *http.Request, action string, _ []byte) {
				if action != "GetScannerElements" {
					t.Error("created a job despite existing output")
				}
				_, _ = w.Write(data)
			})
			if _, err := c.scan(context.Background(), defaultOptions(), path); err == nil {
				t.Fatal("accepted existing path")
			}
			if symlink {
				if target, err := os.Readlink(path); err != nil || target != "missing-target" {
					t.Fatal("symlink changed")
				}
			} else {
				data, _ := os.ReadFile(path)
				if string(data) != "keep" {
					t.Fatal("existing file changed")
				}
			}
		})
	}
}

func TestScannerLock(t *testing.T) {
	cache := t.TempDir()
	t.Setenv("HOME", cache)
	t.Setenv("XDG_CACHE_HOME", cache)
	endpoint := "test-lock:" + t.TempDir()
	unlock, err := lockScanner(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	if second, err := lockScanner(endpoint); err == nil {
		second()
		t.Fatal("second writer acquired lock")
	}
	unlock()
	third, err := lockScanner(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	third()
}
