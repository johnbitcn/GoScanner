package main

import (
	"image"
	"image/color"
	"math"
	"os"
	"testing"
)

func TestDocInputBGR(t *testing.T) {
	img := image.NewRGBA(image.Rect(10, 20, 12, 22))
	for y := 20; y < 22; y++ {
		for x := 10; x < 12; x++ {
			img.SetRGBA(x, y, color.RGBA{R: 255, G: 128, B: 64, A: 255})
		}
	}
	dst := make([]float32, 3*256*256)
	if err := docInput(img, dst); err != nil {
		t.Fatal(err)
	}
	for c, want := range []float32{64.0 / 255, 128.0 / 255, 1} {
		if dst[c*256*256] != want {
			t.Fatalf("channel %d: %v", c, dst[c*256*256])
		}
	}
	if docInput(img, dst[:1]) == nil {
		t.Fatal("accepted bad shape")
	}
}
func TestDocHeatmaps(t *testing.T) {
	data := make([]float32, 4*128*128)
	for c := 0; c < 4; c++ {
		for y := 20 + c*20; y < 30+c*20; y++ {
			for x := 10 + c*20; x < 20+c*20; x++ {
				data[c*128*128+y*128+x] = 0.8
			}
		}
	}
	d, err := docHeatmaps(data, 256, 256)
	if err != nil {
		t.Fatal(err)
	}
	for c, p := range d.Corners {
		if math.Abs(p.X-float64(29+c*40)) > 2 || math.Abs(p.Y-float64(49+c*40)) > 2 || d.Peaks[c] < 0.79 {
			t.Fatalf("corner %d: %+v peak %v", c, p, d.Peaks[c])
		}
	}
	for i := 0; i < 128*128; i++ {
		data[i] = 0.2
	}
	if _, err = docHeatmaps(data, 256, 256); err == nil {
		t.Fatal("accepted low peak")
	}
	data[0] = float32(math.NaN())
	if _, err = docHeatmaps(data, 256, 256); err == nil {
		t.Fatal("accepted NaN")
	}
	if _, err = docHeatmaps(data[:1], 256, 256); err == nil {
		t.Fatal("accepted bad shape")
	}
}
func TestDocAlignerModel(t *testing.T) {
	if os.Getenv("GOSCANNER_TEST_MODEL") != "1" {
		t.Skip("set GOSCANNER_TEST_MODEL=1 to test installed model")
	}
	for i := 0; i < 2; i++ {
		d, err := newDocAligner()
		if err != nil {
			t.Fatal(err)
		}
		populated := false
		for _, v := range d.(*docAligner).output.GetData() {
			if math.IsNaN(float64(v)) || math.IsInf(float64(v), 0) || v < 0 || v > 1 {
				t.Fatal("preflight returned an invalid heatmap")
			}
			populated = populated || v > 0
		}
		if !populated {
			t.Fatal("constructor did not run the session preflight")
		}
		img := image.NewRGBA(image.Rect(0, 0, 256, 256))
		for y := 0; y < 256; y++ {
			for x := 0; x < 256; x++ {
				v := uint8(0)
				if x > 40 && x < 210 && y > 50 && y < 200 {
					v = 255
				}
				img.SetRGBA(x, y, color.RGBA{v, v, v, 255})
			}
		}
		_, err = d.Detect(img)
		if err != nil {
			t.Fatalf("synthetic inference: %v", err)
		}
		if err = d.Close(); err != nil {
			t.Fatal(err)
		}
		if err = d.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestDocAlignerBadModel(t *testing.T) {
	path := t.TempDir() + "/model.onnx"
	if err := os.WriteFile(path, []byte("not a model"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GOSCANNER_MODEL_PATH", path)
	if _, err := newDocAligner(); err == nil {
		t.Fatal("accepted incorrect model hash")
	}
}

func TestDocAlignerSharedEnvironment(t *testing.T) {
	if os.Getenv("GOSCANNER_TEST_MODEL") != "1" {
		t.Skip("set GOSCANNER_TEST_MODEL=1 to test installed model")
	}
	a, err := newDocAligner()
	if err != nil {
		t.Fatal(err)
	}
	b, err := newDocAligner()
	if err != nil {
		a.Close()
		t.Fatal(err)
	}
	if err = a.Close(); err != nil {
		b.Close()
		t.Fatal(err)
	}
	img := image.NewRGBA(image.Rect(0, 0, 256, 256))
	for y := 0; y < 256; y++ {
		for x := 0; x < 256; x++ {
			v := uint8(0)
			if x > 40 && x < 210 && y > 50 && y < 200 {
				v = 255
			}
			img.SetRGBA(x, y, color.RGBA{v, v, v, 255})
		}
	}
	if _, err = b.Detect(img); err != nil {
		b.Close()
		t.Fatal(err)
	}
	if err = b.Close(); err != nil {
		t.Fatal(err)
	}
}
