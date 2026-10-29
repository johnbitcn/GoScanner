package main

import (
	"bytes"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"math"
	"testing"

	"gocv.io/x/gocv"
)

func textJPEG(t *testing.T, angle float64, rows int) []byte {
	t.Helper()
	page := image.NewGray(image.Rect(0, 0, 900, 1000))
	draw.Draw(page, page.Bounds(), image.White, image.Point{}, draw.Src)
	for row := 0; row < rows; row++ {
		y := 130 + row*50
		for col := 0; col < 40; col++ {
			x := 100 + col*16
			// Small E-shaped glyphs leave word-like gaps rather than solid ruled lines.
			draw.Draw(page, image.Rect(x, y, x+2, y+16), image.Black, image.Point{}, draw.Src)
			for _, dy := range []int{0, 7, 14} {
				draw.Draw(page, image.Rect(x, y+dy, x+9, y+dy+2), image.Black, image.Point{}, draw.Src)
			}
		}
	}
	rotated := image.NewGray(page.Bounds())
	a := angle * math.Pi / 180
	for y := 0; y < 1000; y++ {
		for x := 0; x < 900; x++ {
			dx, dy := float64(x-450), float64(y-500)
			sx := int(math.Round(dx*math.Cos(a) + dy*math.Sin(a) + 450))
			sy := int(math.Round(-dx*math.Sin(a) + dy*math.Cos(a) + 500))
			c := color.Gray{Y: 255}
			if image.Pt(sx, sy).In(page.Bounds()) {
				c = page.GrayAt(sx, sy)
			}
			rotated.SetGray(x, y, c)
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, rotated, &jpeg.Options{Quality: 95}); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestDeskewWithoutVisiblePaperBoundary(t *testing.T) {
	for _, angle := range []float64{-4, 4} {
		t.Run(fmtAngle(angle), func(t *testing.T) {
			original := textJPEG(t, angle, 14)
			input, err := gocv.IMDecode(original, gocv.IMReadGrayScale)
			if err != nil {
				t.Fatal(err)
			}
			defer input.Close()
			before, ok, err := textSkew(input)
			if err != nil || !ok || math.Abs(before-angle) > 0.5 {
				t.Fatalf("wrong input angle: %v, reliable=%v, err=%v", before, ok, err)
			}
			processed, warnings, err := preprocessJPEG(original, true, true)
			if err != nil || len(warnings) != 1 || bytes.Equal(processed, original) {
				t.Fatalf("expected deskew and only a crop warning: %v, %v", warnings, err)
			}
			output, err := gocv.IMDecode(processed, gocv.IMReadGrayScale)
			if err != nil {
				t.Fatal(err)
			}
			defer output.Close()
			after, ok, err := textSkew(output)
			if err != nil || !ok || math.Abs(after) > 0.5 {
				t.Fatalf("remaining angle %v, reliable=%v, err=%v", after, ok, err)
			}
			if output.Cols() < input.Cols() || output.Rows() < input.Rows() {
				t.Fatal("deskew clipped the canvas without a paper boundary")
			}
		})
	}
}

func TestDeskewRejectsUnreliableContent(t *testing.T) {
	mixed := image.NewGray(image.Rect(0, 0, 900, 1000))
	draw.Draw(mixed, image.Rect(0, 0, 900, 500), decodedImage(t, textJPEG(t, 4, 14)), image.Point{}, draw.Src)
	draw.Draw(mixed, image.Rect(0, 500, 900, 1000), decodedImage(t, textJPEG(t, -4, 14)), image.Pt(0, 500), draw.Src)
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, mixed, &jpeg.Options{Quality: 95}); err != nil {
		t.Fatal(err)
	}
	for name, original := range map[string][]byte{
		"sparse":      textJPEG(t, 4, 1),
		"large-angle": textJPEG(t, 14, 14),
		"mixed":       buf.Bytes(),
	} {
		t.Run(name, func(t *testing.T) {
			got, warnings, err := preprocessJPEG(original, true, false)
			if err != nil || len(warnings) != 1 || !bytes.Equal(got, original) {
				t.Fatalf("unreliable content must not be rotated: %v, %v", warnings, err)
			}
		})
	}
}
