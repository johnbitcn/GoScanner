package main

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func fixture(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile("testdata/scanner-elements.xml")
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func responseXML(name, payload string) string {
	return fmt.Sprintf(`<s:Envelope xmlns:s="%s" xmlns:w="%s" xmlns:xop="%s"><s:Body><w:%s>%s</w:%s></s:Body></s:Envelope>`, soapNS, scanNS, xopNS, name, payload, name)
}

func jpegSample(t *testing.T) []byte {
	t.Helper()
	picture := image.NewRGBA(image.Rect(0, 0, 8, 8))
	picture.Set(0, 0, color.RGBA{R: 200, A: 255})
	var out bytes.Buffer
	if err := jpeg.Encode(&out, picture, nil); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func mtom(image []byte, boundary, href string) (string, []byte) {
	// Root deliberately comes second: follow start/cid, not part order.
	root := responseXML("RetrieveImageResponse", `<w:Document><xop:Include href="`+href+`"/></w:Document>`)
	body := []byte("--" + boundary + "\r\nContent-Type: application/binary\r\nContent-Transfer-Encoding: binary\r\nContent-ID: <image@scanner>\r\n\r\n")
	body = append(body, image...)
	body = append(body, []byte("\r\n--"+boundary+"\r\nContent-Type: application/xop+xml\r\nContent-ID: <root@scanner>\r\n\r\n"+root+"\r\n--"+boundary+"--\r\n")...)
	return `multipart/related; boundary="` + boundary + `"; start="<root@scanner>"`, body
}

func testClient(t *testing.T, handler func(http.ResponseWriter, *http.Request, string, []byte)) *client {
	t.Helper()
	cache := t.TempDir()
	t.Setenv("HOME", cache)
	t.Setenv("XDG_CACHE_HOME", cache)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			return
		}
		var envelope element
		if err := xml.Unmarshal(data, &envelope); err != nil {
			t.Error(err)
			return
		}
		action := envelope.child(soapNS, "Header").child(addrNS, "Action").text()
		if !strings.HasPrefix(action, scanNS+"/") {
			t.Errorf("invalid SOAP action: %q", action)
		}
		if r.Method != "POST" || r.URL.Path != "/wsd/scan" || r.Header.Get("User-Agent") != "WSDAPI" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		handler(w, r, strings.TrimPrefix(action, scanNS+"/"), data)
	}))
	t.Cleanup(server.Close)
	c, err := newClient("127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	c.endpoint = server.URL + "/wsd/scan"
	return c
}

func TestParseInfo(t *testing.T) {
	cap, err := parseInfo(fixture(t))
	if err != nil {
		t.Fatal(err)
	}
	if cap.State != "Idle" || cap.MaxSize.Width != 8503 || cap.MaxSize.Height != 11732 || len(cap.WidthsDPI) != 5 || len(cap.Colors) != 3 {
		t.Fatalf("unexpected capabilities: %+v", cap)
	}
	bad := bytes.Replace(fixture(t), []byte("<wscn:Width>8503</wscn:Width>"), []byte("<wscn:Width>bad</wscn:Width>"), 1)
	if bytes.Equal(bad, fixture(t)) {
		t.Fatal("fixture mutation did not match")
	}
	if _, err := parseInfo(bad); err == nil {
		t.Fatal("accepted invalid platen size")
	}
}

func TestSOAPValidation(t *testing.T) {
	fault := fmt.Sprintf(`<s:Envelope xmlns:s="%s"><s:Body><s:Fault><s:Reason><s:Text>ScannerBusy</s:Text></s:Reason></s:Fault></s:Body></s:Envelope>`, soapNS)
	for name, data := range map[string]string{
		"fault": fault, "doctype": "<!DOCTYPE x><x/>", "wrong namespace": "<Envelope><Body/></Envelope>",
		"trailing": responseXML("CancelJobResponse", "") + "garbage", "wrong operation": responseXML("OtherResponse", ""),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := soapResponse([]byte(data), "CancelJobResponse"); err == nil {
				t.Fatal("accepted invalid SOAP")
			}
		})
	}
	if _, err := soapResponse(bytes.Repeat([]byte("x"), maxXML+1), "CancelJobResponse"); err == nil {
		t.Fatal("accepted oversized XML")
	}
}

func TestHTTPFaultAndQName(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request, action string, data []byte) {
		if action != "GetScannerElements" || !bytes.Contains(data, []byte("<w:Name>w:ScannerConfiguration</w:Name>")) || !bytes.Contains(data, []byte(`xmlns:w="`+scanNS+`"`)) {
			t.Error("missing operation/QName namespace")
		}
		w.Header().Set("Content-Type", "application/soap+xml")
		io.WriteString(w, fmt.Sprintf(`<s:Envelope xmlns:s="%s"><s:Body><s:Fault><s:Reason><s:Text>Busy</s:Text></s:Reason></s:Fault></s:Body></s:Envelope>`, soapNS))
	})
	if _, err := c.info(context.Background()); err == nil || !strings.Contains(err.Error(), "Busy") {
		t.Fatalf("fault: %v", err)
	}
}

func TestNoRedirectOrProxy(t *testing.T) {
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:1")
	var hits int
	c := testClient(t, func(w http.ResponseWriter, r *http.Request, action string, data []byte) {
		hits++
		w.Header().Set("Location", "/other")
		w.WriteHeader(http.StatusTemporaryRedirect)
	})
	if c.http.Transport.(*http.Transport).Proxy != nil {
		t.Fatal("LAN traffic could use proxy")
	}
	if _, err := c.info(context.Background()); err == nil || !strings.Contains(err.Error(), "307") {
		t.Fatalf("redirect: %v", err)
	}
	if hits != 1 {
		t.Fatalf("redirect followed: %d requests", hits)
	}
}

func TestHTTPTruncationAndLimit(t *testing.T) {
	for _, kind := range []string{"truncated", "oversized"} {
		t.Run(kind, func(t *testing.T) {
			c := testClient(t, func(w http.ResponseWriter, r *http.Request, action string, data []byte) {
				if kind == "truncated" {
					w.Header().Set("Content-Length", "1000")
					io.WriteString(w, "short")
					return
				}
				_, _ = w.Write(bytes.Repeat([]byte("x"), maxXML+1))
			})
			if _, err := c.info(context.Background()); err == nil {
				t.Fatal("accepted invalid HTTP body")
			}
		})
	}
}

func TestHTTPContextDeadline(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request, action string, data []byte) { <-r.Context().Done() })
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := c.info(ctx); err == nil {
		t.Fatal("deadline ignored")
	}
}

func TestHostValidation(t *testing.T) {
	for _, host := range []string{"", "http://192.168.1.8", "host.local", "user:pass@192.168.1.8"} {
		if _, err := newClient(host); err == nil {
			t.Errorf("accepted host %q", host)
		}
	}
	c, err := newClient("::1")
	if err != nil || c.endpoint != "http://[::1]:8018/wsd/scan" {
		t.Fatalf("IPv6 endpoint: %+v %v", c, err)
	}
}
