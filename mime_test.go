package main

import (
	"bytes"
	"encoding/base64"
	"strings"
	"testing"
)

func TestMTOMStandardAndHP(t *testing.T) {
	image := jpegSample(t)
	for _, boundary := range []string{"standard-boundary", "_DPWS_v1.0_MimeBoundary_WSD"} {
		t.Run(boundary, func(t *testing.T) {
			contentType, data := mtom(image, boundary, "cid:image%40scanner")
			if strings.HasPrefix(boundary, "_DPWS") {
				data = bytes.ReplaceAll(data, []byte("\r\n--"+boundary), []byte("--"+boundary))
			}
			result, err := jpegFromResponse(contentType, data)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(result, image) {
				t.Fatal("image was modified by framing repair")
			}
		})
	}
}

func TestMTOMRejectsInvalidResponses(t *testing.T) {
	image := jpegSample(t)
	contentType, standard := mtom(image, "boundary", "cid:image@scanner")
	_, wrongCID := mtom(image, "boundary", "cid:missing")
	_, remote := mtom(image, "boundary", "https://example.test/image")
	_, invalidJPEG := mtom([]byte("not-jpeg"), "boundary", "cid:image@scanner")
	duplicate := bytes.Replace(standard, []byte("<root@scanner>"), []byte("<image@scanner>"), 1)
	cases := []struct {
		name, ct string
		data     []byte
	}{
		{"bad content type", "invalid", standard},
		{"no boundary", "multipart/related", standard},
		{"missing cid", contentType, wrongCID},
		{"external URL", contentType, remote},
		{"invalid JPEG", contentType, invalidJPEG},
		{"truncated", contentType, bytes.TrimSuffix(standard, []byte("--boundary--\r\n"))},
		{"duplicate cid", contentType, duplicate},
		{"missing root", strings.ReplaceAll(contentType, "root@scanner", "missing"), standard},
		{"plain soap", "application/soap+xml", []byte(responseXML("RetrieveImageResponse", ""))},
		{"XML junk", contentType, bytes.Replace(standard, []byte("</s:Envelope>"), []byte("</s:Envelope>junk"), 1)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := jpegFromResponse(tc.ct, tc.data); err == nil {
				t.Fatal("accepted invalid response")
			}
		})
	}
}

func TestMTOMBase64(t *testing.T) {
	image := jpegSample(t)
	contentType, data := mtom(image, "boundary", "cid:image@scanner")
	data = bytes.Replace(data, []byte("Content-Transfer-Encoding: binary"), []byte("Content-Transfer-Encoding: base64"), 1)
	data = bytes.Replace(data, image, []byte(base64.StdEncoding.EncodeToString(image)), 1)
	result, err := jpegFromResponse(contentType, data)
	if err != nil || !bytes.Equal(result, image) {
		t.Fatalf("base64 parsing failed: %v", err)
	}
}

func TestFramingRepairIsIdempotent(t *testing.T) {
	_, data := mtom(jpegSample(t), "_DPWS_v1.0_MimeBoundary_WSD", "cid:image@scanner")
	if !bytes.Equal(data, repairHPFraming(data)) {
		t.Fatal("changed standards-compliant framing")
	}
	broken := bytes.ReplaceAll(data, []byte("\r\n--_DPWS"), []byte("--_DPWS"))
	repaired := repairHPFraming(broken)
	if !bytes.Equal(data, repaired) || !bytes.Equal(repaired, repairHPFraming(repaired)) {
		t.Fatal("repair is not idempotent")
	}
}
