package main

import (
	"bytes"
	"fmt"
	"image"
	"image/jpeg"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func assertPDF(t *testing.T, path string, pages int) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(data, []byte("%PDF-1.4\n")) || !bytes.HasSuffix(data, []byte("%%EOF\n")) ||
		!bytes.Contains(data, []byte(fmt.Sprintf("/Count %d /Kids [", pages))) ||
		bytes.Count(data, []byte("/Type /Page /Parent")) != pages {
		t.Fatalf("incomplete PDF or wrong page count: %s", path)
	}
	start := bytes.LastIndex(data, []byte("startxref\n"))
	if start < 0 {
		t.Fatal("missing startxref")
	}
	position, err := strconv.Atoi(strings.Fields(string(data[start:]))[1])
	if err != nil || position < 0 || position >= len(data) || !bytes.HasPrefix(data[position:], []byte("xref\n")) {
		t.Fatal("invalid xref position")
	}
	lines := strings.Split(string(data[position:]), "\n")
	if lines[1] != fmt.Sprintf("0 %d", 3+3*pages) {
		t.Fatal("invalid xref size")
	}
	for id := 1; id < 3+3*pages; id++ {
		offset, err := strconv.Atoi(strings.Fields(lines[2+id])[0])
		if err != nil || offset < 0 || offset >= len(data) || !bytes.HasPrefix(data[offset:], []byte(fmt.Sprintf("%d 0 obj\n", id))) {
			t.Fatalf("invalid object offset for %d", id)
		}
	}
	return data
}

func TestWritePDF(t *testing.T) {
	dir := t.TempDir()
	var gray bytes.Buffer
	if err := jpeg.Encode(&gray, image.NewGray(image.Rect(0, 0, 300, 600)), nil); err != nil {
		t.Fatal(err)
	}
	images := [][]byte{jpegSample(t), gray.Bytes()}
	var pages []string
	for i, data := range images {
		path := filepath.Join(dir, fmt.Sprintf("%d.jpg", i))
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
		pages = append(pages, path)
	}
	output := filepath.Join(dir, "scan.pdf")
	if err := writePDF(output, pages, 300); err != nil {
		t.Fatal(err)
	}
	data := assertPDF(t, output, 2)
	for _, original := range images {
		if bytes.Count(data, original) != 1 {
			t.Fatal("JPEG changed or was not embedded exactly once")
		}
	}
	for _, want := range []string{"/DeviceRGB", "/DeviceGray", "/MediaBox [0 0 72.0000 144.0000]"} {
		if !bytes.Contains(data, []byte(want)) {
			t.Fatalf("missing %s", want)
		}
	}
	if info, err := os.Stat(output); err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("PDF must be private", err)
	}
	if err := writePDF(output, pages, 300); err == nil || !strings.Contains(err.Error(), "完整 PDF 保留在") {
		t.Fatal("existing file must not be overwritten; staging must be retained", err)
	}
	unchanged, _ := os.ReadFile(output)
	if !bytes.Equal(data, unchanged) {
		t.Fatal("overwrote PDF")
	}
	staged, _ := filepath.Glob(filepath.Join(dir, ".goscanner-*.pdf.part"))
	if len(staged) != 1 {
		t.Fatal("missing recoverable PDF", staged)
	}
	assertPDF(t, staged[0], 2)

	// Independent parsers are optional; the built-in structure checks always run.
	if tool, err := exec.LookPath("qpdf"); err == nil {
		if out, err := exec.Command(tool, "--check", output).CombinedOutput(); err != nil {
			t.Fatalf("qpdf: %v\n%s", err, out)
		}
	}
	if tool, err := exec.LookPath("pdfinfo"); err == nil {
		out, err := exec.Command(tool, "-f", "2", "-l", "2", output).CombinedOutput()
		if err != nil || !strings.Contains(string(out), "72 x 144 pts") {
			t.Fatalf("pdfinfo: %v\n%s", err, out)
		}
	}
	if tool, err := exec.LookPath("pdftoppm"); err == nil {
		if out, err := exec.Command(tool, "-r", "72", "-f", "2", "-l", "2", "-gray", output, filepath.Join(dir, "render")).CombinedOutput(); err != nil {
			t.Fatalf("PDF rendering: %v\n%s", err, out)
		}
	}
}

func TestWritePDFInvalidInput(t *testing.T) {
	dir := t.TempDir()
	invalid := filepath.Join(dir, "invalid.jpg")
	if err := os.WriteFile(invalid, []byte("not JPEG"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		pages []string
		dpi   int
	}{
		{nil, 300}, {[]string{invalid}, 0}, {[]string{invalid}, 300}, {[]string{filepath.Join(dir, "missing.jpg")}, 300},
	} {
		output := filepath.Join(dir, "invalid.pdf")
		if err := writePDF(output, tc.pages, tc.dpi); err == nil {
			t.Fatal("invalid PDF input accepted")
		}
		if _, err := os.Stat(output); !os.IsNotExist(err) {
			t.Fatal("published invalid PDF", err)
		}
		staged, _ := filepath.Glob(filepath.Join(dir, ".goscanner-*.pdf.part"))
		if len(staged) != 0 {
			t.Fatal("incomplete PDF staging leaked", staged)
		}
	}
}
