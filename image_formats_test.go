package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/image/tiff"
)

func tiffSample(t *testing.T, gray bool) []byte {
	t.Helper()
	var picture image.Image
	if gray {
		p := image.NewGray(image.Rect(0, 0, 8, 6))
		p.SetGray(0, 0, color.Gray{Y: 123})
		picture = p
	} else {
		p := image.NewRGBA(image.Rect(0, 0, 8, 6))
		for y := 0; y < 6; y++ {
			for x := 0; x < 8; x++ {
				p.SetRGBA(x, y, color.RGBA{R: uint8(x * 30), G: uint8(y * 40), B: 200, A: 255})
			}
		}
		picture = p
	}
	var out bytes.Buffer
	if err := tiff.Encode(&out, picture, &tiff.Options{Compression: tiff.Uncompressed}); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func assertSavedImage(t *testing.T, path string, source []byte) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.EqualFold(filepath.Ext(path), ".png") {
		if !bytes.Equal(got, source) {
			t.Fatal("native image bytes changed")
		}
		return
	}
	picture, format, err := image.Decode(bytes.NewReader(got))
	if err != nil || format != "png" {
		t.Fatalf("invalid PNG: format=%s error=%v", format, err)
	}
	original, _, err := image.Decode(bytes.NewReader(source))
	if err != nil {
		t.Fatal(err)
	}
	if picture.Bounds() != original.Bounds() {
		t.Fatalf("image dimensions changed: %v != %v", picture.Bounds(), original.Bounds())
	}
	for y := 0; y < picture.Bounds().Dy(); y++ {
		for x := 0; x < picture.Bounds().Dx(); x++ {
			if color.NRGBA64Model.Convert(picture.At(x, y)) != color.NRGBA64Model.Convert(original.At(x, y)) {
				t.Fatalf("pixel (%d,%d) changed", x, y)
			}
		}
	}
}

func TestNativeTIFFAndPNG(t *testing.T) {
	for _, ext := range []string{".tif", ".TIFF", ".png", ".PNG"} {
		for _, gray := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/gray=%v", ext, gray), func(t *testing.T) {
				source := tiffSample(t, gray)
				cap := fixture(t)
				jobs := 0
				c := testClient(t, func(w http.ResponseWriter, r *http.Request, action string, data []byte) {
					switch action {
					case "GetScannerElements":
						_, _ = w.Write(cap)
					case "CreateScanJob":
						jobs++
						if !bytes.Contains(data, []byte("<w:Format>"+formatTIFF+"</w:Format>")) {
							t.Error("did not request native TIFF")
						}
						io.WriteString(w, responseXML("CreateScanJobResponse", "<w:JobId>7</w:JobId><w:JobToken>token</w:JobToken>"))
					case "RetrieveImage":
						if !bytes.Contains(data, []byte("<w:DocumentName>IMAGE000.TIF</w:DocumentName>")) {
							t.Error("retrieval filename does not match native TIFF")
						}
						ct, body := mtom(source, "_DPWS_v1.0_MimeBoundary_WSD", "cid:image@scanner")
						body = bytes.ReplaceAll(body, []byte("\r\n--_DPWS"), []byte("--_DPWS"))
						w.Header().Set("Content-Type", ct)
						_, _ = w.Write(body)
					default:
						t.Errorf("unexpected action %s", action)
					}
				})
				opts := defaultOptions()
				if gray {
					opts.Mode = "gray"
				}
				path := filepath.Join(t.TempDir(), "scan"+ext)
				result, err := c.scan(context.Background(), opts, path)
				if err != nil {
					t.Fatal(err)
				}
				assertSavedImage(t, path, source)
				stat, err := os.Stat(path)
				if err != nil || result.Width != 8 || result.Height != 6 || result.Bytes != int(stat.Size()) || jobs != 1 {
					t.Fatalf("incorrect result: %+v jobs=%d error=%v", result, jobs, err)
				}
				staging, _ := filepath.Glob(filepath.Join(filepath.Dir(path), ".goscanner-*.part"))
				if len(staging) != 0 {
					t.Fatalf("staging left behind: %v", staging)
				}
			})
		}
	}
}

func TestTIFFFailures(t *testing.T) {
	for _, ext := range []string{".tiff", ".png"} {
		for _, scenario := range []string{"unsupported", "wrong-format", "truncated", "oversized", "overflow-size"} {
			t.Run(ext+"/"+scenario, func(t *testing.T) {
				cap, source := fixture(t), tiffSample(t, false)
				switch scenario {
				case "unsupported":
					cap = bytes.ReplaceAll(cap, []byte(formatTIFF), []byte("unsupported-tiff"))
				case "wrong-format":
					source = jpegSample(t)
				case "truncated":
					source = source[:len(source)/2]
				case "oversized", "overflow-size":
					size := uint32(65535)
					if scenario == "overflow-size" {
						size = ^uint32(0)
					}
					ifd := int(binary.LittleEndian.Uint32(source[4:8]))
					count := int(binary.LittleEndian.Uint16(source[ifd:]))
					for i := 0; i < count; i++ {
						entry := source[ifd+2+i*12:]
						tag := binary.LittleEndian.Uint16(entry)
						if tag == 256 || tag == 257 {
							binary.LittleEndian.PutUint32(entry[8:], size)
						}
					}
				}
				jobs, cancels := 0, 0
				c := testClient(t, func(w http.ResponseWriter, r *http.Request, action string, _ []byte) {
					switch action {
					case "GetScannerElements":
						_, _ = w.Write(cap)
					case "CreateScanJob":
						jobs++
						io.WriteString(w, responseXML("CreateScanJobResponse", "<w:JobId>7</w:JobId><w:JobToken>token</w:JobToken>"))
					case "RetrieveImage":
						ct, body := mtom(source, "boundary", "cid:image@scanner")
						w.Header().Set("Content-Type", ct)
						_, _ = w.Write(body)
					case "CancelJob":
						cancels++
						io.WriteString(w, responseXML("CancelJobResponse", ""))
					default:
						t.Errorf("unexpected action %s", action)
					}
				})
				path := filepath.Join(t.TempDir(), "scan"+ext)
				if _, err := c.scan(context.Background(), defaultOptions(), path); err == nil {
					t.Fatal("accepted invalid or unsupported TIFF")
				}
				wantJobs := 1
				if scenario == "unsupported" {
					wantJobs = 0
				}
				if jobs != wantJobs || cancels != wantJobs {
					t.Fatalf("jobs=%d cancels=%d, want %d", jobs, cancels, wantJobs)
				}
				if _, err := os.Stat(path); !os.IsNotExist(err) {
					t.Fatalf("invalid output published: %v", err)
				}
				staging, _ := filepath.Glob(filepath.Join(filepath.Dir(path), ".goscanner-*.part"))
				if len(staging) != 0 {
					t.Fatalf("partial images left behind: %v", staging)
				}
			})
		}
	}
}
