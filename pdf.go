package main

import (
	"bytes"
	"errors"
	"fmt"
	"image/color"
	"image/jpeg"
	"io"
	"os"
	"path/filepath"
)

// writePDF embeds the original JPEG bytes and publishes only a complete document.
func writePDF(output string, pages []string, dpi int) (err error) {
	if len(pages) == 0 || dpi <= 0 {
		return errors.New("PDF 需要至少一页图像和有效的 DPI")
	}
	file, err := os.CreateTemp(filepath.Dir(output), ".goscanner-*.pdf.part")
	if err != nil {
		return err
	}
	keep := false
	defer func() {
		_ = file.Close()
		if !keep {
			if removeErr := os.Remove(file.Name()); removeErr != nil {
				err = errors.Join(err, removeErr)
			}
		}
	}()
	if err = renderPDF(file, pages, dpi); err != nil {
		return err
	}
	if err = file.Sync(); err != nil {
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	if err = os.Link(file.Name(), output); err != nil {
		keep = true
		return fmt.Errorf("发布 PDF 失败；完整 PDF 保留在 %s：%w", file.Name(), err)
	}
	return nil
}

func renderPDF(file *os.File, pages []string, dpi int) error {
	var err error
	write := func(format string, args ...any) {
		if err == nil {
			_, err = fmt.Fprintf(file, format, args...)
		}
	}
	offsets := make([]int64, 3+3*len(pages))
	object := func(id int) {
		if err == nil {
			offsets[id], err = file.Seek(0, io.SeekCurrent)
		}
		write("%d 0 obj\n", id)
	}
	write("%%PDF-1.4\n%%\xe2\xe3\xcf\xd3\n")
	object(1)
	write("<< /Type /Catalog /Pages 2 0 R >>\nendobj\n")
	object(2)
	write("<< /Type /Pages /Count %d /Kids [", len(pages))
	for i := range pages {
		write("%d 0 R ", 3+3*i)
	}
	write("] >>\nendobj\n")
	for i, path := range pages {
		if err != nil {
			return err
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		config, decodeErr := jpeg.DecodeConfig(bytes.NewReader(data))
		if decodeErr != nil || config.Width <= 0 || config.Height <= 0 {
			return fmt.Errorf("PDF 页面不是有效 JPEG：%s", path)
		}
		space := "/DeviceRGB"
		switch config.ColorModel {
		case color.GrayModel:
			space = "/DeviceGray"
		case color.YCbCrModel:
		default:
			return fmt.Errorf("PDF 仅支持彩色 RGB 和灰度 JPEG：%s", path)
		}
		width, height := float64(config.Width)*72/float64(dpi), float64(config.Height)*72/float64(dpi)
		id := 3 + 3*i
		object(id)
		write("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 %.4f %.4f] /Resources << /XObject << /Image %d 0 R >> >> /Contents %d 0 R >>\nendobj\n", width, height, id+1, id+2)
		object(id + 1)
		write("<< /Type /XObject /Subtype /Image /Width %d /Height %d /ColorSpace %s /BitsPerComponent 8 /Filter /DCTDecode /Length %d >>\nstream\n", config.Width, config.Height, space, len(data))
		if err == nil {
			_, err = file.Write(data)
		}
		write("\nendstream\nendobj\n")
		content := fmt.Sprintf("q\n%.4f 0 0 %.4f 0 0 cm\n/Image Do\nQ\n", width, height)
		object(id + 2)
		write("<< /Length %d >>\nstream\n%sendstream\nendobj\n", len(content), content)
	}
	if err != nil {
		return err
	}
	xref, err := file.Seek(0, io.SeekCurrent)
	if err != nil {
		return err
	}
	write("xref\n0 %d\n0000000000 65535 f \n", len(offsets))
	for _, offset := range offsets[1:] {
		write("%010d 00000 n \n", offset)
	}
	write("trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(offsets), xref)
	return err
}
