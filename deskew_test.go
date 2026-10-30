package main

import (
	"image"
	"image/color"
	"image/draw"
	"math"
	"os"

	"testing"
)

func TestDeskewAnglesAndMargins(t *testing.T) {
	for _, angle := range []float64{-20, 20} {
		img := image.NewGray(image.Rect(0, 0, 1000, 800))
		draw.Draw(img, img.Bounds(), image.White, image.Point{}, draw.Src)
		d := rectangle(500, 400, 280, 180, angle)
		for _, pt := range d.Corners {
			x, y := int(math.Round(pt.X)), int(math.Round(pt.Y))
			draw.Draw(img, image.Rect(x-2, y-2, x+3, y+3), image.NewUniform(color.Black), image.Point{}, draw.Src)
		}
		p := imageProcessor{detector: &fakeDetector{detection: d}, deskew: true, autocrop: true}
		out, info, err := p.Process(img)
		if err != nil || !info.Changed || math.Abs(info.Angle-angle) > .01 {
			t.Fatalf("angle %v: %+v %v", angle, info, err)
		}
		if out.Bounds().Dx() < 280 || out.Bounds().Dy() < 180 || out.Bounds().Dx() > 330 || out.Bounds().Dy() > 230 {
			t.Fatal(out.Bounds())
		}
		gray := out.(*image.Gray)
		counts := [4]int{}
		for y := 0; y < gray.Bounds().Dy(); y++ {
			for x := 0; x < gray.Bounds().Dx(); x++ {
				if gray.GrayAt(x, y).Y < 128 {
					i := 0
					if x >= gray.Bounds().Dx()/2 {
						i++
					}
					if y >= gray.Bounds().Dy()/2 {
						i += 2
					}
					counts[i]++
				}
			}
		}
		for i, n := range counts {
			if n == 0 {
				t.Fatalf("edge mark %d lost", i)
			}
		}

	}
}
func TestDeskewIgnoresAbnormalEdge(t *testing.T) {
	for _, angle := range []float64{-20, 20} {
		d := documentDetection{Peaks: [4]float64{.8, .8, .8, .8}}
		rad := angle * math.Pi / 180
		c, s := math.Cos(rad), math.Sin(rad)
		// The slanted bottom edge is longer than each of the three normal edges.
		for i, pt := range [4]documentCorner{{0, 0}, {300, 0}, {300, 100}, {0, 300}} {
			d.Corners[i] = documentCorner{400 + c*pt.X - s*pt.Y, 300 + s*pt.X + c*pt.Y}
		}
		p := imageProcessor{detector: &fakeDetector{detection: d}, deskew: true}
		out, info, err := p.Process(image.NewGray(image.Rect(0, 0, 1000, 800)))
		if err != nil || !info.Detected || !info.Changed || math.Abs(info.Angle-angle) > .01 {
			t.Fatalf("angle %v: %+v %v", angle, info, err)
		}
		w := int(math.Ceil(1000*math.Cos(rad) + 800*math.Abs(math.Sin(rad))))
		h := int(math.Ceil(800*math.Cos(rad) + 1000*math.Abs(math.Sin(rad))))
		if out.Bounds() != image.Rect(0, 0, w, h) {
			t.Fatalf("unexpected rotated canvas: %v", out.Bounds())
		}
	}
}

func TestDeskewPortrait(t *testing.T) {
	d := rectangle(500, 600, 400, 600, 20)
	p := imageProcessor{detector: &fakeDetector{detection: d}, deskew: true, autocrop: true}
	out, info, err := p.Process(image.NewGray(image.Rect(0, 0, 1000, 1200)))
	if err != nil || math.Abs(info.Angle-20) > .01 || out.Bounds().Dy() <= out.Bounds().Dx() {
		t.Fatalf("portrait: %v %+v %v", out.Bounds(), info, err)
	}
}

func TestProcessorRealModelSynthetic(t *testing.T) {
	if os.Getenv("GOSCANNER_TEST_MODEL") != "1" {
		t.Skip("set GOSCANNER_TEST_MODEL=1")
	}
	p, err := newImageProcessor(true, true)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	img := image.NewGray(image.Rect(0, 0, 256, 256))
	draw.Draw(img, image.Rect(41, 51, 210, 200), image.White, image.Point{}, draw.Src)
	out, info, err := p.Process(img)
	if err != nil || !info.Detected || !info.AngleReliable || !info.Changed || math.Abs(info.Angle) > 3 {
		t.Fatalf("model geometry: %+v %v bounds %v", info, err, out.Bounds())
	}
}
