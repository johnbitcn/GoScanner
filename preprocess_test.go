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

func testJPEG(t *testing.T, w, h int, angle float64, page bool) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	a := angle * math.Pi / 180
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			xx, yy := float64(x-w/2), float64(y-h/2)
			px := xx*math.Cos(a) + yy*math.Sin(a)
			py := -xx*math.Sin(a) + yy*math.Cos(a)
			c := uint8(255)
			if page {
				c = 45
				if math.Abs(px) < float64(w)*.35 && math.Abs(py) < float64(h)*.35 {
					c = 245
					if math.Abs(px) > float64(w)*.32 && math.Abs(py) > float64(h)*.32 {
						c = 20
					}
				}
			}
			img.SetRGBA(x, y, color.RGBA{c, c, c, 255})
		}
	}
	var b bytes.Buffer
	if err := jpeg.Encode(&b, img, &jpeg.Options{Quality: 100}); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func decodedImage(t *testing.T, data []byte) image.Image {
	t.Helper()
	img, err := jpeg.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	return img
}

func TestPreprocessJPEGNoFlagsAndInvalid(t *testing.T) {
	data := []byte("not jpeg")
	got, w, err := preprocessJPEG(data, false, false)
	if err != nil || len(w) != 0 || !bytes.Equal(got, data) {
		t.Fatal("disabled preprocessing changed data")
	}
	for _, bad := range [][]byte{data, {0xff, 0xd8, 0xff}, nil} {
		if _, _, err := preprocessJPEG(bad, true, false); err == nil {
			t.Fatalf("accepted invalid JPEG %x", bad)
		}
	}
}

func TestPreprocessJPEGOversizedHeader(t *testing.T) {
	data := testJPEG(t, 40, 40, 0, false)
	for i := 0; i+8 < len(data); i++ {
		if data[i] == 0xff && data[i+1] == 0xc0 {
			data[i+5], data[i+6], data[i+7], data[i+8] = 0xff, 0xff, 0xff, 0xff
			if _, _, err := preprocessJPEG(data, true, false); err == nil {
				t.Fatal("accepted oversized JPEG")
			}
			return
		}
	}
	t.Fatal("JPEG frame header not found")
}

func TestPreprocessJPEGBlankAndInteriorBox(t *testing.T) {
	blank := testJPEG(t, 800, 700, 0, false)
	got, w, err := preprocessJPEG(blank, true, true)
	if err != nil || !bytes.Equal(got, blank) || len(w) != 2 {
		t.Fatalf("blank: warnings=%v error=%v", w, err)
	}
	img := image.NewRGBA(image.Rect(0, 0, 800, 700))
	for y := 0; y < 700; y++ {
		for x := 0; x < 800; x++ {
			c := uint8(250)
			if x > 100 && x < 700 && y > 100 && y < 600 && (x < 105 || x > 695 || y < 105 || y > 595) {
				c = 0
			}
			img.SetRGBA(x, y, color.RGBA{c, c, c, 255})
		}
	}
	var b bytes.Buffer
	if err := jpeg.Encode(&b, img, nil); err != nil {
		t.Fatal(err)
	}
	got, w, err = preprocessJPEG(b.Bytes(), false, true)
	if err != nil || !bytes.Equal(got, b.Bytes()) || len(w) != 1 {
		t.Fatalf("interior box: warnings=%v error=%v", w, err)
	}
}

func TestPreprocessJPEGDeskewPaper(t *testing.T) {
	for _, angle := range []float64{-5, 5} {
		t.Run(fmtAngle(angle), func(t *testing.T) {
			data := testJPEG(t, 900, 800, angle, true)
			got, w, err := preprocessJPEG(data, true, false)
			if err != nil || len(w) != 0 || bytes.Equal(got, data) {
				t.Fatalf("warnings=%v error=%v", w, err)
			}
			im, err := gocv.IMDecode(got, gocv.IMReadColor)
			if err != nil {
				t.Fatal(err)
			}
			defer im.Close()
			_, remaining, ok, err := paperBoundary(im, true)
			if err != nil || !ok || math.Abs(remaining) > 1 {
				t.Fatalf("remaining angle=%v detected=%v error=%v", remaining, ok, err)
			}
			if im.Cols() < 900 || im.Rows() < 800 {
				t.Fatal("rotation clipped canvas")
			}
		})
	}
}
func fmtAngle(a float64) string {
	if a < 0 {
		return "negative"
	}
	return "positive"
}

func TestPreprocessJPEGDeskewThenCrop(t *testing.T) {
	data := testJPEG(t, 900, 800, 5, true)
	got, warnings, err := preprocessJPEG(data, true, true)
	if err != nil || len(warnings) != 0 {
		t.Fatalf("warnings=%v error=%v", warnings, err)
	}
	bounds := decodedImage(t, got).Bounds()
	if bounds.Dx() >= 850 || bounds.Dy() >= 750 || bounds.Dx() < 630 || bounds.Dy() < 560 {
		t.Fatalf("unexpected cropped bounds %v", bounds)
	}
}

func TestPreprocessJPEGCropAndResolution(t *testing.T) {
	data := testJPEG(t, 2000, 1800, 0, true)
	got, w, err := preprocessJPEG(data, false, true)
	if err != nil || len(w) != 0 {
		t.Fatalf("warnings=%v error=%v", w, err)
	}
	img := decodedImage(t, got)
	b := img.Bounds()
	if b.Dx() < 1400 || b.Dy() < 1260 || b.Dx() > 1450 || b.Dy() > 1310 {
		t.Fatalf("crop dimensions %v", b)
	}
	// The dark corner marks lie close to all four page corners.
	for _, p := range []image.Point{{25, 25}, {b.Dx() - 25, 25}, {25, b.Dy() - 25}, {b.Dx() - 25, b.Dy() - 25}} {
		r, _, _, _ := img.At(p.X, p.Y).RGBA()
		if r > 30000 {
			t.Errorf("edge mark missing at %v", p)
		}
	}
}

func TestPreprocessJPEGPreservesGrayscale(t *testing.T) {
	original := decodedImage(t, testJPEG(t, 900, 800, 0, true))
	gray := image.NewGray(original.Bounds())
	for y := 0; y < gray.Bounds().Dy(); y++ {
		for x := 0; x < gray.Bounds().Dx(); x++ {
			gray.Set(x, y, original.At(x, y))
		}
	}
	var b bytes.Buffer
	if err := jpeg.Encode(&b, gray, nil); err != nil {
		t.Fatal(err)
	}
	got, warnings, err := preprocessJPEG(b.Bytes(), false, true)
	if err != nil || len(warnings) != 0 {
		t.Fatalf("warnings=%v error=%v", warnings, err)
	}
	config, err := jpeg.DecodeConfig(bytes.NewReader(got))
	if err != nil || config.ColorModel != color.GrayModel {
		t.Fatalf("output is not grayscale: %v %v", config.ColorModel, err)
	}
}

func TestPreprocessJPEGRejectsSplitPaper(t *testing.T) {
	for _, band := range []image.Rectangle{image.Rect(390, 70, 410, 630), image.Rect(250, 70, 270, 630), image.Rect(80, 340, 720, 360)} {
		t.Run(band.String(), func(t *testing.T) {
			img := image.NewGray(image.Rect(0, 0, 800, 700))
			draw.Draw(img, img.Bounds(), image.NewUniform(color.Gray{Y: 40}), image.Point{}, draw.Src)
			draw.Draw(img, image.Rect(80, 70, 720, 630), image.White, image.Point{}, draw.Src)
			draw.Draw(img, band, image.Black, image.Point{}, draw.Src)
			var b bytes.Buffer
			if err := jpeg.Encode(&b, img, &jpeg.Options{Quality: 95}); err != nil {
				t.Fatal(err)
			}
			got, warnings, err := preprocessJPEG(b.Bytes(), false, true)
			if err != nil || !bytes.Equal(got, b.Bytes()) || len(warnings) != 1 {
				t.Fatalf("internal band must not turn half a page into a crop: warnings=%v, err=%v", warnings, err)
			}
		})
	}
}

func TestPreprocessJPEGOnlyCropDoesNotDeskew(t *testing.T) {
	data := testJPEG(t, 900, 800, 5, true)
	got, w, err := preprocessJPEG(data, false, true)
	if err != nil || len(w) != 0 {
		t.Fatalf("warnings=%v error=%v", w, err)
	}
	im, err := gocv.IMDecode(got, gocv.IMReadColor)
	if err != nil {
		t.Fatal(err)
	}
	defer im.Close()
	// Crop must preserve the source rotation; inspect thresholded paper in the original-sized context.
	if decodedImage(t, got).Bounds().Dx() >= 900 || im.Empty() {
		t.Fatal("crop was not applied")
	}
	// Deskew changes the canvas and pixels; crop alone must retain the sloping page edge.
	edgeY := func(x int) int {
		for y := 0; y < im.Rows(); y++ {
			if im.GetUCharAt3(y, x, 0) > 200 {
				return y
			}
		}
		return -1
	}
	left, right := edgeY(im.Cols()/3), edgeY(2*im.Cols()/3)
	if left < 0 || right < 0 || int(math.Abs(float64(left-right))) < 10 {
		t.Fatalf("page edge unexpectedly level: %d %d", left, right)
	}
}
