package main

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"image"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestCheckedESCLEndpoint(t *testing.T) {
	for _, raw := range []string{"http://127.0.0.1:8080/eSCL", "https://[::1]/eSCL/"} {
		if _, err := checkedESCLEndpoint(raw); err != nil {
			t.Errorf("%q: %v", raw, err)
		}
	}
	for _, raw := range []string{"http://example.com/eSCL", "http://user@127.0.0.1/eSCL", "http://127.0.0.1/eSCL?x=1", "http://127.0.0.1/eSCL/../x", "http://127.0.0.1/eSCL%2fadmin", "http://224.0.0.1/eSCL", "ftp://127.0.0.1/eSCL"} {
		if _, err := checkedESCLEndpoint(raw); err == nil {
			t.Errorf("accepted %q", raw)
		}
	}
}

func TestCheckedESCLJobLocation(t *testing.T) {
	endpoint := "https://127.0.0.1:8443/eSCL"
	for _, location := range []string{"42", "/eSCL/ScanJobs/42", "https://127.0.0.1:8443/eSCL/ScanJobs/42/"} {
		if _, err := checkedESCLJobLocation(endpoint, location); err != nil {
			t.Errorf("%q: %v", location, err)
		}
	}
	for _, location := range []string{"http://127.0.0.1:8443/eSCL/ScanJobs/42", "https://127.0.0.2:8443/eSCL/ScanJobs/42", "//evil.test/eSCL/ScanJobs/42", "../other", "/eSCL/ScanJobs/42/NextDocument", "/eSCL/ScanJobs/42?x=1", "/eSCL/ScanJobs/%2fadmin", "https://user@127.0.0.1:8443/eSCL/ScanJobs/42", "/eSCL/ScanJobs/a/../42", "../ScanJobs/42", "/eSCL/ScanJobs//42"} {
		if _, err := checkedESCLJobLocation(endpoint, location); err == nil {
			t.Errorf("accepted %q", location)
		}
	}
}

func esclDiscrete(dpi int) string {
	return fmt.Sprintf(`<scan:SupportedResolutions><scan:DiscreteResolutions><scan:DiscreteResolution><scan:XResolution>%d</scan:XResolution><scan:YResolution>%d</scan:YResolution></scan:DiscreteResolution></scan:DiscreteResolutions></scan:SupportedResolutions>`, dpi, dpi)
}

func esclTestProfile(color, formats, resolutions string) string {
	return `<scan:SettingProfile><scan:ColorModes><scan:ColorMode>` + color + `</scan:ColorMode></scan:ColorModes><scan:DocumentFormats>` + formats + `</scan:DocumentFormats>` + resolutions + `</scan:SettingProfile>`
}

func esclTestCapabilities(profiles string) string {
	return fmt.Sprintf(`<scan:ScannerCapabilities xmlns:scan="%s" xmlns:pwg="%s"><pwg:Version>2.0</pwg:Version><scan:UUID>urn:uuid:12345678-1234-1234-1234-123456789abc</scan:UUID><pwg:SerialNumber>serial-1</pwg:SerialNumber><pwg:MakeAndModel>Test Flatbed</pwg:MakeAndModel><scan:ScannerStatus>http://192.0.2.1/evil</scan:ScannerStatus><scan:Platen><scan:PlatenInputCaps><scan:MinWidth>1</scan:MinWidth><scan:MinHeight>1</scan:MinHeight><scan:MaxWidth>2480</scan:MaxWidth><scan:MaxHeight>3508</scan:MaxHeight><scan:SettingProfiles>%s</scan:SettingProfiles></scan:PlatenInputCaps></scan:Platen></scan:ScannerCapabilities>`, esclNS, pwgNS, profiles)
}

func esclDefaultProfiles() string {
	ranges := `<scan:SupportedResolutions><scan:ResolutionRange><scan:XResolution><scan:Min>75</scan:Min><scan:Max>600</scan:Max><scan:Step>75</scan:Step></scan:XResolution><scan:YResolution><scan:Min>75</scan:Min><scan:Max>600</scan:Max><scan:Step>75</scan:Step></scan:YResolution></scan:ResolutionRange></scan:SupportedResolutions>`
	return esclTestProfile("RGB24", `<pwg:DocumentFormat>image/jpeg</pwg:DocumentFormat><scan:DocumentFormatExt>image/png</scan:DocumentFormatExt>`, esclDiscrete(300)) + esclTestProfile("Grayscale8", `<pwg:DocumentFormat>image/jpeg</pwg:DocumentFormat>`, ranges)
}

func serveESCLInfo(w http.ResponseWriter, r *http.Request, capabilities, state string) bool {
	if r.Method != http.MethodGet {
		return false
	}
	switch r.URL.Path {
	case "/eSCL/ScannerCapabilities":
		_, _ = io.WriteString(w, capabilities)
		return true
	case "/eSCL/ScannerStatus":
		_, _ = fmt.Fprintf(w, `<scan:ScannerStatus xmlns:scan="%s" xmlns:pwg="%s"><pwg:State>%s</pwg:State></scan:ScannerStatus>`, esclNS, pwgNS, state)
		return true
	}
	return false
}

func esclTestClient(t *testing.T, handler http.HandlerFunc) *client {
	t.Helper()
	cache := t.TempDir()
	t.Setenv("HOME", cache)
	t.Setenv("XDG_CACHE_HOME", cache)
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	c, err := newProtocolClient(server.URL+"/eSCL", "escl")
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestESCLInfoAndParameterCombinations(t *testing.T) {
	var calls []string
	var mu sync.Mutex
	c := esclTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls = append(calls, r.Method+" "+r.URL.Path)
		mu.Unlock()
		if !serveESCLInfo(w, r, esclTestCapabilities(esclDefaultProfiles()), "Idle") {
			t.Errorf("unexpected URL %s", r.URL)
		}
	})
	cap, err := c.info(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	gotCalls := strings.Join(calls, ",")
	mu.Unlock()
	if gotCalls != "GET /eSCL/ScannerCapabilities,GET /eSCL/ScannerStatus" {
		t.Fatal(gotCalls)
	}
	if cap.UUID != "urn:uuid:12345678-1234-1234-1234-123456789abc" || cap.SerialNumber != "serial-1" || cap.MakeAndModel != "Test Flatbed" {
		t.Fatalf("identity: %+v", cap)
	}
	if cap.MinSize != (size{4, 4}) || cap.MaxSize != (size{8266, 11693}) || cap.SizeUnit != "1/1000 inch" {
		t.Fatalf("units: %+v", cap)
	}
	if !slices.Contains(cap.WidthsDPI, 225) || !slices.Contains(cap.Formats, formatPNG) {
		t.Fatalf("capabilities: %+v", cap)
	}
	for _, tc := range []struct {
		name, mode, format string
		dpi                int
		area               region
		valid              bool
	}{
		{"A4 native rounding", "color", formatPNG, 300, region{Width: 8268, Height: 11693}, true},
		{"native full", "color", formatJPEG, 300, region{}, true},
		{"custom region", "gray", formatJPEG, 225, region{394, 394, 1969, 1969}, true},
		{"profile cross product", "color", formatJPEG, 600, region{}, false},
		{"unsupported format combination", "gray", formatPNG, 300, region{}, false},
		{"range step", "gray", formatJPEG, 100, region{}, false},
		{"range maximum", "gray", formatJPEG, 600, region{}, true},
		{"range overflow", "gray", formatJPEG, 675, region{}, false},
		{"zero DPI", "gray", formatJPEG, 0, region{}, false},
		{"unsupported mode", "bw", formatJPEG, 300, region{}, false},
		{"unsupported PDF", "color", "application/pdf", 300, region{}, false},
		{"below native minimum", "color", formatJPEG, 300, region{Width: 1, Height: 100}, false},
		{"negative offset", "color", formatJPEG, 300, region{-1, 0, 100, 100}, false},
		{"overflow bounds", "color", formatJPEG, 300, region{1 << 62, 0, 100, 100}, false},
		{"outside native bounds", "color", formatJPEG, 300, region{0, 0, 8270, 11693}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data, err := ticket(cap, options{Mode: tc.mode, Format: tc.format, DPI: tc.dpi, Area: tc.area})
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v: %v, %s", tc.valid, err, data)
			}
			if err != nil {
				return
			}
			root, err := parseXML([]byte(data))
			if err != nil || root.XMLName != (xml.Name{Space: esclNS, Local: "ScanSettings"}) {
				t.Fatalf("ticket XML: %v", err)
			}
			if root.child(pwgNS, "InputSource").text() != "Platen" {
				t.Fatal("not platen")
			}
			if (root.child(esclNS, "DocumentFormatExt") != nil) != (tc.format == formatPNG) {
				t.Fatal("DocumentFormatExt does not match the advertised format")
			}
			r := root.child(pwgNS, "ScanRegions").child(pwgNS, "ScanRegion")
			if tc.name == "A4 native rounding" || tc.name == "native full" {
				if r.child(pwgNS, "Width").text() != "2480" || r.child(pwgNS, "Height").text() != "3508" {
					t.Fatal(data)
				}
			}
		})
	}
	cap.State = "Processing"
	if _, err := ticket(cap, defaultOptions()); err == nil {
		t.Fatal("accepted busy scanner")
	}
}

func TestESCLColorSpecificResolutions(t *testing.T) {
	res := esclDiscrete(300) + strings.Replace(esclDiscrete(600), "<scan:SupportedResolutions>", "<scan:SupportedResolutions><scan:ColorMode>RGB24</scan:ColorMode>", 1)
	profile := esclTestProfile("RGB24", `<pwg:DocumentFormat>image/jpeg</pwg:DocumentFormat>`, res)
	profile = strings.Replace(profile, "</scan:ColorModes>", "<scan:ColorMode>Grayscale8</scan:ColorMode></scan:ColorModes>", 1)
	cap, err := parseESCLCapabilities([]byte(esclTestCapabilities(profile)))
	if err != nil {
		t.Fatal(err)
	}
	cap.State = "Idle"
	for _, tc := range []struct {
		mode  string
		dpi   int
		valid bool
	}{{"color", 600, true}, {"color", 300, false}, {"gray", 300, true}, {"gray", 600, false}} {
		_, err := ticket(cap, options{Mode: tc.mode, DPI: tc.dpi})
		if (err == nil) != tc.valid {
			t.Fatalf("%+v: %v", tc, err)
		}
	}
}

func TestESCLScanLifecycle(t *testing.T) {
	for _, scenario := range []string{"png", "jpeg-png", "gray-jpeg-png", "jpeg", "tiff-png", "tiff", "503-success", "cleanup-failure", "cleanup-gone", "corrupt", "wrong-format", "cancelled", "download-404", "create-503", "create-timeout", "unsafe-location", "truncated-create", "delete-failure"} {
		t.Run(scenario, func(t *testing.T) {
			jpegData := jpegSample(t)
			var pngBuffer bytes.Buffer
			if err := png.Encode(&pngBuffer, image.NewGray(image.Rect(0, 0, 8, 8))); err != nil {
				t.Fatal(err)
			}
			payload := pngBuffer.Bytes()
			profiles := esclDefaultProfiles()
			wantMime := "image/png"
			output := filepath.Join(t.TempDir(), "scan.png")
			if scenario == "jpeg-png" || scenario == "jpeg" {
				profiles = esclTestProfile("RGB24", `<pwg:DocumentFormat>image/jpeg</pwg:DocumentFormat>`, esclDiscrete(300))
				payload = jpegData
				wantMime = "image/jpeg"
			}
			if scenario == "gray-jpeg-png" {
				payload = jpegData
				wantMime = "image/jpeg"
			}
			if scenario == "tiff-png" || scenario == "tiff" {
				payload = tiffSample(t, false)
				wantMime = "image/tiff"
				profiles = esclTestProfile("RGB24", `<pwg:DocumentFormat>image/tiff</pwg:DocumentFormat>`, esclDiscrete(300))
			}
			if scenario == "tiff" {
				output = strings.TrimSuffix(output, ".png") + ".tiff"
			}
			if scenario == "jpeg" {
				output = strings.TrimSuffix(output, ".png") + ".jpg"
			}
			var mu sync.Mutex
			var calls []string
			var posts, gets, deletes int
			timeout := 3 * time.Second
			if scenario == "create-timeout" {
				timeout = 100 * time.Millisecond
			}
			ctx, cancel := context.WithTimeout(context.Background(), timeout)
			defer cancel()
			c := esclTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				calls = append(calls, r.Method+" "+r.URL.Path)
				if serveESCLInfo(w, r, esclTestCapabilities(profiles), "Idle") {
					return
				}
				switch r.Method + " " + r.URL.Path {
				case "POST /eSCL/ScanJobs":
					posts++
					body, err := io.ReadAll(r.Body)
					if err != nil {
						t.Error(err)
						return
					}
					root, err := parseXML(body)
					if err != nil || root.child(pwgNS, "DocumentFormat").text() != wantMime {
						t.Errorf("wrong scan format: %s (%v)", body, err)
					}
					if scenario == "create-timeout" {
						<-r.Context().Done()
						return
					}
					if scenario == "create-503" {
						w.WriteHeader(http.StatusServiceUnavailable)
						return
					}
					location := "/eSCL/ScanJobs/42"
					if scenario == "unsafe-location" {
						location = "/admin"
					}
					w.Header().Set("Location", location)
					if scenario == "truncated-create" {
						w.Header().Set("Content-Length", "100")
					}
					w.WriteHeader(http.StatusCreated)
				case "GET /eSCL/ScanJobs/42/NextDocument":
					gets++
					if scenario == "cancelled" {
						cancel()
						w.WriteHeader(http.StatusServiceUnavailable)
						return
					}
					if scenario == "download-404" {
						w.WriteHeader(http.StatusNotFound)
						return
					}
					if scenario == "503-success" && gets == 1 {
						w.WriteHeader(http.StatusServiceUnavailable)
						return
					}
					if scenario == "corrupt" || scenario == "delete-failure" {
						_, _ = w.Write([]byte("not an image"))
						return
					}
					if scenario == "wrong-format" {
						_, _ = w.Write(jpegData)
						return
					}
					// Intentionally inaccurate Content-Type: validate the actual file, not this header.
					w.Header().Set("Content-Type", "application/octet-stream")
					_, _ = w.Write(payload)
				case "DELETE /eSCL/ScanJobs/42":
					deletes++
					if err := r.Context().Err(); err != nil {
						t.Errorf("cancel cleanup used cancelled context: %v", err)
					}
					if scenario == "delete-failure" || scenario == "cleanup-failure" {
						w.WriteHeader(http.StatusInternalServerError)
						return
					}
					if scenario == "cleanup-gone" {
						w.WriteHeader(http.StatusGone)
						return
					}
					w.WriteHeader(http.StatusNoContent)
				default:
					t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
					w.WriteHeader(500)
				}
			})
			opts := defaultOptions()
			if scenario == "gray-jpeg-png" {
				opts.Mode = "gray"
				opts.DPI = 225
			}
			opts.originalPath = filepath.Join(t.TempDir(), "source")
			result, err := c.scan(ctx, opts, output)
			mu.Lock()
			defer mu.Unlock()
			if posts != 1 {
				t.Fatalf("POST count %d: %v", posts, calls)
			}
			success := slices.Contains([]string{"png", "jpeg-png", "gray-jpeg-png", "jpeg", "tiff-png", "tiff", "503-success", "cleanup-failure", "cleanup-gone"}, scenario)
			if success {
				if scenario == "cleanup-failure" {
					if err == nil || !strings.Contains(err.Error(), "任务清理失败，未自动重试扫描") || result.retainedPath != output {
						t.Fatalf("missing partial result: %+v %v", result, err)
					}
				} else if err != nil {
					t.Fatal(err)
				}
				data, err := os.ReadFile(output)
				if err != nil {
					t.Fatal(err)
				}
				config, format, err := image.DecodeConfig(bytes.NewReader(data))
				wantFormat := "png"
				if scenario == "jpeg" {
					wantFormat = "jpeg"
					if !bytes.Equal(data, payload) {
						t.Fatal("JPEG was re-encoded")
					}
				}
				if scenario == "tiff" {
					wantFormat = "tiff"
					if !bytes.Equal(data, payload) {
						t.Fatal("TIFF was re-encoded")
					}
				}
				wantHeight := 8
				if scenario == "tiff" || scenario == "tiff-png" {
					wantHeight = 6
				}
				if err != nil || format != wantFormat || config.Width != 8 || config.Height != wantHeight || result.Width != 8 || result.Height != wantHeight {
					t.Fatalf("bad result %+v %v", result, err)
				}
				wantSourceExt := map[string]string{"image/jpeg": ".jpg", "image/png": ".png", "image/tiff": ".tiff"}[wantMime]
				if result.originalPath != opts.originalPath+wantSourceExt {
					t.Fatalf("wrong source path %s", result.originalPath)
				}
				original, err := os.ReadFile(result.originalPath)
				if err != nil || !bytes.Equal(original, payload) {
					t.Fatal("original source not retained")
				}
				stat, _ := os.Stat(output)
				if stat.Mode().Perm() != 0600 {
					t.Fatal("output not private")
				}
				if deletes != 1 {
					t.Fatalf("successful job not cleaned exactly once: %v", calls)
				}
				wantGets := 1
				if scenario == "503-success" {
					wantGets = 2
				}
				if gets != wantGets {
					t.Fatalf("GET count %d: %v", gets, calls)
				}
			} else {
				if err == nil {
					t.Fatal("expected scan failure")
				}
				if scenario == "cancelled" && !errors.Is(err, context.Canceled) {
					t.Fatalf("lost context error: %v", err)
				}
				if scenario == "delete-failure" && !strings.Contains(err.Error(), "取消任务未确认") {
					t.Fatalf("lost cleanup error: %v", err)
				}
				if _, err := os.Lstat(output); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("failed scan published output")
				}
				wantDeletes := 1
				if scenario == "create-503" || scenario == "create-timeout" || scenario == "unsafe-location" {
					wantDeletes = 0
					if gets != 0 {
						t.Fatal("retrieved unknown job")
					}
				}
				if deletes != wantDeletes {
					t.Fatalf("DELETE count %d: %v", deletes, calls)
				}
			}
			staging, _ := filepath.Glob(filepath.Join(filepath.Dir(output), ".goscanner-*.part"))
			if len(staging) != 0 {
				t.Fatalf("staging left: %v", staging)
			}
		})
	}
}

func TestESCLCreateRetrieveCancel(t *testing.T) {
	image := jpegSample(t)
	var calls atomic.Int32
	c := esclTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		switch n {
		case 1:
			if r.Method != "POST" || r.URL.Path != "/eSCL/ScanJobs" {
				t.Errorf("create: %s %s", r.Method, r.URL.Path)
			}
			w.Header().Set("Location", "42/")
			w.WriteHeader(201)
		case 2:
			if r.Method != "GET" || r.URL.Path != "/eSCL/ScanJobs/42/NextDocument" {
				t.Errorf("retrieve: %s %s", r.Method, r.URL.Path)
			}
			_, _ = w.Write(image)
		case 3:
			if r.Method != "DELETE" || r.URL.Path != "/eSCL/ScanJobs/42/" {
				t.Errorf("cancel: %s %s", r.Method, r.URL.Path)
			}
			w.WriteHeader(204)
		default:
			t.Error("extra call")
		}
	})
	id, token, err := c.create(context.Background(), "<scan/>")
	if err != nil || token != "" {
		t.Fatalf("create: %s %s %v", id, token, err)
	}
	payload, err := c.retrieve(context.Background(), id, token, formatJPEG)
	if err != nil || !bytes.Equal(payload, image) {
		t.Fatalf("retrieve: %v", err)
	}
	if err := c.cancel(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 3 {
		t.Fatal(calls.Load())
	}
}

func TestESCL503RespectsContext(t *testing.T) {
	var calls atomic.Int32
	c := esclTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Retry-After", "999999999")
		w.WriteHeader(http.StatusServiceUnavailable)
	})
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := c.retrieve(ctx, c.endpoint+"/ScanJobs/1", "", formatJPEG)
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > time.Second || calls.Load() != 1 {
		t.Fatalf("unbounded wait: %v calls=%d", err, calls.Load())
	}
}

func TestESCLTransportSecurity(t *testing.T) {
	var proxyCalls atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { proxyCalls.Add(1); w.WriteHeader(502) }))
	defer proxy.Close()
	t.Setenv("HTTP_PROXY", proxy.URL)
	t.Setenv("HTTPS_PROXY", proxy.URL)
	c := esclTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if !serveESCLInfo(w, r, esclTestCapabilities(esclDefaultProfiles()), "Idle") {
			t.Errorf("unexpected request %s", r.URL)
		}
	})
	transport := c.http.Transport.(*http.Transport)
	if transport.Proxy != nil || transport.TLSClientConfig != nil && transport.TLSClientConfig.InsecureSkipVerify {
		t.Fatal("unsafe transport")
	}
	if _, err := c.info(context.Background()); err != nil {
		t.Fatal(err)
	}
	if proxyCalls.Load() != 0 {
		t.Fatal("used proxy")
	}
	tlsServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("untrusted TLS server was reached") }))
	defer tlsServer.Close()
	secure, err := newProtocolClient(tlsServer.URL+"/eSCL", "escl")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := secure.info(context.Background()); err == nil {
		t.Fatal("accepted untrusted TLS")
	}
}

func TestESCLRejectsRedirectsAndUnsafeJobs(t *testing.T) {
	var targetCalls atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { targetCalls.Add(1); w.WriteHeader(200) }))
	defer target.Close()
	for _, operation := range []string{"capabilities", "status", "create", "retrieve", "cancel"} {
		t.Run(operation, func(t *testing.T) {
			var requests atomic.Int32
			c := esclTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				if operation == "status" && r.URL.Path == "/eSCL/ScannerCapabilities" {
					_, _ = io.WriteString(w, esclTestCapabilities(esclDefaultProfiles()))
					return
				}
				w.Header().Set("Location", target.URL+"/eSCL/ScanJobs/1")
				w.WriteHeader(http.StatusTemporaryRedirect)
			})
			var err error
			switch operation {
			case "capabilities", "status":
				_, err = c.info(context.Background())
			case "create":
				_, _, err = c.create(context.Background(), "<scan/>")
			case "retrieve":
				_, err = c.retrieve(context.Background(), c.endpoint+"/ScanJobs/1", "", formatJPEG)
			case "cancel":
				err = c.cancel(context.Background(), c.endpoint+"/ScanJobs/1")
			}
			if err == nil {
				t.Fatal("accepted redirect")
			}
			want := int32(1)
			if operation == "status" {
				want = 2
			}
			if requests.Load() != want {
				t.Fatal("unexpected retries")
			}
		})
	}
	for _, location := range []string{target.URL + "/eSCL/ScanJobs/1", "/eSCL/ScanJobs/../admin", "/eSCL/ScanJobs/1/NextDocument", "/eSCL/ScanJobs/%2e%2e", "//user@127.0.0.1/eSCL/ScanJobs/1"} {
		t.Run(location, func(t *testing.T) {
			var requests atomic.Int32
			c := esclTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				w.Header().Set("Location", location)
				w.WriteHeader(201)
			})
			if id, _, err := c.create(context.Background(), "<scan/>"); err == nil || id != "" {
				t.Fatalf("accepted unsafe location %q", id)
			}
			if _, err := c.retrieve(context.Background(), location, "", formatJPEG); err == nil {
				t.Fatal("unsafe retrieve")
			}
			if err := c.cancel(context.Background(), location); err == nil {
				t.Fatal("unsafe cancel")
			}
			if requests.Load() != 1 {
				t.Fatal("accessed unsafe job")
			}
		})
	}
	if targetCalls.Load() != 0 {
		t.Fatalf("external target accessed %d times", targetCalls.Load())
	}
}

func TestESCLResponseAndXMLLimits(t *testing.T) {
	for _, data := range []string{
		strings.Repeat("x", maxXML+1),
		`<!DOCTYPE x [<!ENTITY v "abc">]><x/>`,
		strings.Repeat("<x>", 65) + strings.Repeat("</x>", 65),
		"<x>" + strings.Repeat("<x/>", 10000) + "</x>",
		"<x/><x/>", "<x/", "<x/>trailing",
	} {
		if _, err := parseXML([]byte(data)); err == nil {
			t.Fatalf("accepted invalid XML of size %d", len(data))
		}
	}
	c := esclTestClient(t, func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, strings.Repeat("x", maxXML+1)) })
	if _, err := c.info(context.Background()); err == nil || !strings.Contains(err.Error(), "大小限制") {
		t.Fatalf("unbounded capabilities: %v", err)
	}
	if _, _, _, err := c.esclRequest(context.Background(), http.MethodGet, c.endpoint, "", 64); err == nil {
		t.Fatal("unbounded binary response")
	}
	for _, mutate := range []func(string) string{
		func(s string) string { return strings.ReplaceAll(s, esclNS, "urn:wrong") },
		func(s string) string {
			return strings.Replace(s, "<scan:MaxWidth>2480</scan:MaxWidth>", "<scan:MaxWidth>0</scan:MaxWidth>", 1)
		},
		func(s string) string {
			return strings.Replace(s, "<scan:MinWidth>1</scan:MinWidth>", "<scan:MinWidth>9999</scan:MinWidth>", 1)
		},
		func(s string) string { return strings.ReplaceAll(s, "<scan:SettingProfile>", "<scan:Unknown>") },
	} {
		if _, err := parseESCLCapabilities([]byte(mutate(esclTestCapabilities(esclDefaultProfiles())))); err == nil {
			t.Fatal("accepted invalid capabilities")
		}
	}
}

func TestNewProtocolClientAndWSDInfoCompatibility(t *testing.T) {
	for _, protocol := range []string{"", "wsd", "WSD", "escl", "eSCL"} {
		c, err := newProtocolClient("http://127.0.0.1/service", protocol)
		if err != nil {
			t.Fatal(err)
		}
		want := strings.ToLower(protocol)
		if want == "" {
			want = "wsd"
		}
		if c.protocol != want {
			t.Fatalf("protocol %q", c.protocol)
		}
	}
	if _, err := newProtocolClient("http://127.0.0.1/service", "ipp"); err == nil {
		t.Fatal("accepted unsupported protocol")
	}
	if _, err := newEndpointClient("https://127.0.0.1/service"); err == nil {
		t.Fatal("changed WSD endpoint rules")
	}
	cap, err := parseInfo(fixture(t))
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(cap)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"uuid", "serial_number", "make_and_model", "escl"} {
		if bytes.Contains(data, []byte(`"`+field+`"`)) {
			t.Fatalf("changed old WSD JSON: %s", data)
		}
	}
	c := testClient(t, func(w http.ResponseWriter, r *http.Request, action string, _ []byte) {
		if action != "GetScannerElements" {
			t.Error(action)
		}
		_, _ = w.Write(fixture(t))
	})
	if c.protocol != "wsd" {
		t.Fatal(c.protocol)
	}
	if _, err := c.info(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestESCLCleanupFailureRetainsPageAndStopsManualPages(t *testing.T) {
	var posts, deletes atomic.Int32
	payload := jpegSample(t)
	c := esclTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if serveESCLInfo(w, r, esclTestCapabilities(esclDefaultProfiles()), "Idle") {
			return
		}
		switch r.Method {
		case "POST":
			posts.Add(1)
			w.Header().Set("Location", "42")
			w.WriteHeader(201)
		case "GET":
			_, _ = w.Write(payload)
		case "DELETE":
			deletes.Add(1)
			w.WriteHeader(500)
		default:
			t.Error("unexpected method")
		}
	})
	output := filepath.Join(t.TempDir(), "page.jpg")
	var stdout, stderr bytes.Buffer
	var events []scanEvent
	wait := func(context.Context) (bool, error) { t.Error("continued after cleanup failure"); return true, nil }
	err := scanPagesWithEvents(context.Background(), c, defaultOptions(), output, false, time.Second, wait, &stdout, &stderr, func(event scanEvent) { events = append(events, event) })
	if err == nil || !strings.Contains(err.Error(), "任务清理失败，未自动重试扫描") {
		t.Fatalf("lost cleanup failure: %v", err)
	}
	path := filepath.Join(filepath.Dir(output), "page-001.jpg")
	data, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(data, payload) {
		t.Fatalf("successful scan lost: %v", err)
	}
	retained := false
	for _, event := range events {
		retained = retained || event.kind == "retained" && event.path == path
	}
	if !retained || posts.Load() != 1 || deletes.Load() != 1 {
		t.Fatalf("retained=%v posts=%d deletes=%d", retained, posts.Load(), deletes.Load())
	}
}

func TestESCLBlockedDocumentRespectsContext(t *testing.T) {
	c := esclTestClient(t, func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() })
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := c.retrieve(ctx, c.endpoint+"/ScanJobs/1", "", formatJPEG); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("lost timeout: %v", err)
	}
}

func TestParseXMLFragmentedText(t *testing.T) {
	root, err := parseXML([]byte("<x>" + strings.Repeat("a<!-- -->", 10000) + "</x>"))
	if err != nil || root.Text != strings.Repeat("a", 10000) {
		t.Fatalf("fragmented text: %v", err)
	}
}

func TestESCLJPEGToPNGWithProcessingPreservesOriginal(t *testing.T) {
	original := integrationPaperJPEG(t)
	profiles := esclTestProfile("RGB24", `<pwg:DocumentFormat>image/jpeg</pwg:DocumentFormat>`, esclDiscrete(300))
	var posts, deletes atomic.Int32
	c := esclTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if serveESCLInfo(w, r, esclTestCapabilities(profiles), "Idle") {
			return
		}
		switch r.Method {
		case "POST":
			posts.Add(1)
			w.Header().Set("Location", "42")
			w.WriteHeader(201)
		case "GET":
			_, _ = w.Write(original)
		case "DELETE":
			deletes.Add(1)
			w.WriteHeader(204)
		default:
			t.Error("unexpected request")
		}
	})
	opts := defaultOptions()
	opts.Deskew, opts.Autocrop = true, true
	opts.processor = &imageProcessor{detector: &fakeDetector{detection: rectangle(480, 600, 760, 920, 0)}, deskew: true, autocrop: true}
	output := filepath.Join(t.TempDir(), "scan.png")
	var stdout, stderr bytes.Buffer
	if err := scanPages(context.Background(), c, opts, output, false, 5*time.Second, nil, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	sourcePath := strings.TrimSuffix(output, ".png") + "-original.jpg"
	data, err := os.ReadFile(sourcePath)
	if err != nil || !bytes.Equal(data, original) {
		t.Fatalf("source JPEG changed or misnamed: %v", err)
	}
	data, err = os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	config, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || format != "png" || config.Width >= 960 || config.Height >= 1200 {
		t.Fatalf("processing bypassed: %+v %s %v", config, format, err)
	}
	if !strings.Contains(stdout.String(), sourcePath) || posts.Load() != 1 || deletes.Load() != 1 {
		t.Fatalf("source path/calls: %s %d %d", stdout.String(), posts.Load(), deletes.Load())
	}
	if _, err := os.Lstat(strings.TrimSuffix(sourcePath, ".jpg") + ".tiff"); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("source JPEG mislabeled TIFF")
	}
}

func TestESCLActualSourcePathRefusesOverwrite(t *testing.T) {
	for _, format := range []string{"image/png", "image/jpeg"} {
		t.Run(format, func(t *testing.T) {
			profiles := esclTestProfile("RGB24", `<pwg:DocumentFormat>`+format+`</pwg:DocumentFormat>`, esclDiscrete(300))
			var posts atomic.Int32
			c := esclTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				if serveESCLInfo(w, r, esclTestCapabilities(profiles), "Idle") {
					return
				}
				posts.Add(1)
				t.Error("job created despite existing original")
				w.WriteHeader(500)
			})
			dir := t.TempDir()
			opts := defaultOptions()
			opts.originalPath = filepath.Join(dir, "original.tiff")
			ext := ".png"
			if format == "image/jpeg" {
				ext = ".jpg"
			}
			actual := filepath.Join(dir, "original"+ext)
			if err := os.WriteFile(actual, []byte("keep"), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := c.scan(context.Background(), opts, filepath.Join(dir, "scan.png")); err == nil {
				t.Fatal("accepted existing source")
			}
			data, err := os.ReadFile(actual)
			if err != nil || string(data) != "keep" || posts.Load() != 0 {
				t.Fatalf("existing source changed: %v", err)
			}
		})
	}
}

func TestESCLManualPagesCleanEachJob(t *testing.T) {
	payload := jpegSample(t)
	profiles := esclTestProfile("RGB24", `<pwg:DocumentFormat>image/jpeg</pwg:DocumentFormat>`, esclDiscrete(300))
	var active atomic.Bool
	var posts, deletes atomic.Int32
	c := esclTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		state := "Idle"
		if active.Load() {
			state = "Processing"
		}
		if serveESCLInfo(w, r, esclTestCapabilities(profiles), state) {
			return
		}
		switch r.Method {
		case "POST":
			if active.Swap(true) {
				t.Error("started a new job before cleaning the previous one")
			}
			id := posts.Add(1)
			w.Header().Set("Location", fmt.Sprint(id))
			w.WriteHeader(201)
		case "GET":
			_, _ = w.Write(payload)
		case "DELETE":
			active.Store(false)
			deletes.Add(1)
			w.WriteHeader(204)
		default:
			t.Error("unexpected request")
		}
	})
	output := filepath.Join(t.TempDir(), "page.png")
	var stdout, stderr bytes.Buffer
	waits := 0
	wait := func(context.Context) (bool, error) { waits++; return waits < 2, nil }
	if err := scanPages(context.Background(), c, defaultOptions(), output, false, time.Second, wait, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if posts.Load() != 2 || deletes.Load() != 2 || waits != 2 || active.Load() {
		t.Fatalf("posts=%d deletes=%d waits=%d active=%v", posts.Load(), deletes.Load(), waits, active.Load())
	}
	for page := 1; page <= 2; page++ {
		data, err := os.ReadFile(filepath.Join(filepath.Dir(output), fmt.Sprintf("page-%03d.png", page)))
		if err != nil {
			t.Fatal(err)
		}
		if _, format, err := image.DecodeConfig(bytes.NewReader(data)); err != nil || format != "png" {
			t.Fatalf("page %d: %s %v", page, format, err)
		}
	}
}
