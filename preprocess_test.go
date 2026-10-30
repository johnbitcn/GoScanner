package main

import (
	"errors"
	"image"
	"image/color"
	"image/draw"
	"math"
	"testing"
)

type fakeDetector struct {
	detection     documentDetection
	err           error
	calls, closed int
}

func (f *fakeDetector) Detect(image.Image) (documentDetection, error) {
	f.calls++
	return f.detection, f.err
}
func (f *fakeDetector) Close() error { f.closed++; return nil }
func rectangle(cx, cy, w, h, angle float64) documentDetection {
	a := angle * math.Pi / 180
	c, s := math.Cos(a), math.Sin(a)
	d := documentDetection{Peaks: [4]float64{.8, .8, .8, .8}}
	for i, p := range [4][2]float64{{-w / 2, -h / 2}, {w / 2, -h / 2}, {w / 2, h / 2}, {-w / 2, h / 2}} {
		d.Corners[i] = documentCorner{cx + c*p[0] - s*p[1], cy + s*p[0] + c*p[1]}
	}
	return d
}
func TestProcessorCropPaddingFollowsObject(t *testing.T) {
	for name, bounds := range map[string]image.Rectangle{
		"flatbed":        image.Rect(0, 0, 1000, 1200),
		"larger flatbed": image.Rect(0, 0, 2000, 2400),
	} {
		t.Run(name, func(t *testing.T) {
			img := image.NewRGBA(bounds)
			draw.Draw(img, bounds, image.White, image.Point{}, draw.Src)
			// The old flatbed-sized margin included this separate object.
			draw.Draw(img, image.Rect(583, 330, 586, 333), image.NewUniform(color.RGBA{R: 255, A: 255}), image.Point{}, draw.Src)
			for _, point := range []image.Point{{330, 340}, {570, 340}, {570, 660}, {330, 660}} {
				img.SetRGBA(point.X, point.Y, color.RGBA{A: 255})
			}
			f := &fakeDetector{detection: rectangle(450, 500, 240, 320, 0)}
			p := imageProcessor{detector: f, autocrop: true}
			out, info, err := p.Process(img)
			if err != nil || !info.Detected || !info.Changed || f.calls != 1 {
				t.Fatalf("info=%+v calls=%d err=%v", info, f.calls, err)
			}
			if out.Bounds() != image.Rect(0, 0, 254, 334) {
				t.Fatalf("padding changed with unused flatbed area: %v", out.Bounds())
			}
			black := 0
			for y := 0; y < out.Bounds().Dy(); y++ {
				for x := 0; x < out.Bounds().Dx(); x++ {
					c := color.RGBAModel.Convert(out.At(x, y)).(color.RGBA)
					if c.R == 255 && c.G == 0 && c.B == 0 {
						t.Fatal("neighbor leaked into crop")
					}
					if c.R == 0 && c.G == 0 && c.B == 0 {
						black++
					}
				}
			}
			if black != 4 {
				t.Fatalf("lost a target corner: retained %d/4", black)
			}
		})
	}
}

func TestProcessorCropPaddingMinimum(t *testing.T) {
	img := image.NewGray(image.Rect(0, 0, 200, 200))
	p := imageProcessor{detector: &fakeDetector{detection: rectangle(100, 100, 40, 20, 0)}, autocrop: true}
	out, info, err := p.Process(img)
	if err != nil || !info.Detected || out.Bounds() != image.Rect(0, 0, 46, 26) {
		t.Fatalf("minimum padding: bounds=%v info=%+v err=%v", out.Bounds(), info, err)
	}
}

func TestProcessorFlagsAndDetection(t *testing.T) {
	img := image.NewGray(image.Rect(10, 20, 1010, 820))
	d := rectangle(500, 400, 300, 190, 20)
	for _, flags := range [][2]bool{{false, false}, {true, false}, {false, true}, {true, true}} {
		f := &fakeDetector{detection: d}
		p := imageProcessor{detector: f, deskew: flags[0], autocrop: flags[1]}
		got, info, err := p.Process(img)
		if err != nil {
			t.Fatal(err)
		}
		if f.calls != btoi(flags[0] || flags[1]) || info.Detected != (flags[0] || flags[1]) || info.AngleReliable != info.Detected || info.Changed != (flags[0] || flags[1]) {
			t.Fatalf("%v: %+v calls %d", flags, info, f.calls)
		}
		if !flags[0] && !flags[1] && got != img {
			t.Fatal("disabled changed image")
		}
		if info.Detected && math.Abs(info.Angle-20) > .01 {
			t.Fatal(info.Angle)
		}
		if _, ok := got.(*image.Gray); !ok {
			t.Fatal("lost grayscale")
		}
		if flags[0] && !flags[1] && (got.Bounds().Dx() < 1000 || got.Bounds().Dy() < 800) {
			t.Fatal("rotation clipped canvas")
		}
		if flags[1] && (got.Bounds().Dx() >= 1000 || got.Bounds().Dy() >= 800) {
			t.Fatal("crop not applied")
		}
		if err := p.Close(); err != nil {
			t.Fatal(err)
		}
		_ = p.Close()
		if f.closed != 1 {
			t.Fatal("close", f.closed)
		}
	}
}
func btoi(b bool) int {
	if b {
		return 1
	}
	return 0
}
func TestProcessorInvalidDetection(t *testing.T) {
	good := rectangle(500, 400, 300, 200, 0)
	cases := map[string]documentDetection{}
	d := good
	d.Peaks[0] = .1
	cases["low peak"] = d
	d = good
	d.Corners[0].X = math.NaN()
	cases["nan"] = d
	d = good
	d.Corners[0].Y = math.Inf(1)
	cases["infinite corner"] = d
	d = good
	d.Corners[0].X = -1
	cases["outside image"] = d
	d = good
	d.Peaks[0] = math.NaN()
	cases["nan peak"] = d
	d = good
	d.Peaks[0] = math.Inf(1)
	cases["infinite peak"] = d
	cases["short edge"] = rectangle(500, 400, 1, 200, 0)
	cases["tiny area"] = rectangle(500, 400, 10, 10, 0)
	d = good
	d.Corners[0] = d.Corners[1]
	cases["missing"] = d
	d = good
	d.Corners[0] = documentCorner{500, 400}
	cases["concave"] = d
	d = good
	d.Corners[1], d.Corners[2] = d.Corners[2], d.Corners[1]
	cases["crossed"] = d
	img := image.NewGray(image.Rect(0, 0, 1000, 800))
	for name, d := range cases {
		t.Run(name, func(t *testing.T) {
			f := &fakeDetector{detection: d}
			p := imageProcessor{detector: f, autocrop: true}
			out, info, err := p.Process(img)
			if err != nil || out != img || info.Detected || info.AngleReliable || info.Changed || len(info.Warnings) == 0 {
				t.Fatalf("%+v %v", info, err)
			}
		})
	}
	f := &fakeDetector{err: errors.New("inference")}
	p := imageProcessor{detector: f, deskew: true}
	out, _, err := p.Process(img)
	if err == nil || out != img {
		t.Fatal("inference error lost")
	}
}
func TestProcessorNoChangeAndEdge(t *testing.T) {
	img := image.NewGray(image.Rect(0, 0, 1000, 800))
	img.SetGray(0, 0, color.Gray{0})
	for _, d := range []documentDetection{rectangle(500, 400, 1000, 800, 0), rectangle(500, 400, 1000, 800, .05)} {
		f := &fakeDetector{detection: d}
		p := imageProcessor{detector: f, deskew: true, autocrop: true}
		out, info, err := p.Process(img)
		// The slightly rotated full-image rectangle extends outside the input and is rejected.
		if err != nil || out != img || info.Changed {
			t.Fatalf("%+v %v", info, err)
		}
	}
	f := &fakeDetector{detection: rectangle(500, 400, 1000, 800, 0)}
	p := imageProcessor{detector: f, autocrop: true}
	_, info, err := p.Process(img)
	if err != nil || !info.Detected {
		t.Fatalf("edge rejected %+v %v", info, err)
	}
}

type hugeImage struct{ bounds image.Rectangle }

func (h hugeImage) ColorModel() color.Model { return color.GrayModel }
func (h hugeImage) Bounds() image.Rectangle { return h.bounds }
func (h hugeImage) At(int, int) color.Color { return color.White }
func TestProcessorLimits(t *testing.T) {
	f := &fakeDetector{detection: rectangle(2500, 4000, 2000, 3000, 20)}
	p := imageProcessor{detector: f, deskew: true}
	_, _, err := p.Process(hugeImage{image.Rect(0, 0, 5000, 8001)})
	if err == nil || f.calls != 0 {
		t.Fatal("input limit")
	}
	_, _, err = p.Process(hugeImage{image.Rect(0, 0, 5000, 8000)})
	if err == nil || f.calls != 1 {
		t.Fatal("rotation limit")
	}
}

func TestProcessorNoModelAndSmallAngle(t *testing.T) {
	t.Setenv("GOSCANNER_MODEL_PATH", "/nonexistent/model")
	p, err := newImageProcessor(false, false)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	img := image.NewGray(image.Rect(0, 0, 1000, 800))
	out, info, err := p.Process(img)
	if err != nil || out != img || info.Changed {
		t.Fatal("disabled", info, err)
	}
	f := &fakeDetector{detection: rectangle(500, 400, 300, 200, .1)}
	p = &imageProcessor{detector: f, deskew: true}
	out, info, err = p.Process(img)
	if err != nil || out != img || !info.Detected || info.Changed || math.Abs(info.Angle-.1) > .001 {
		t.Fatal("small angle", info, err)
	}
}
func TestProcessorGray16(t *testing.T) {
	img := image.NewGray16(image.Rect(0, 0, 1000, 800))
	p := imageProcessor{detector: &fakeDetector{detection: rectangle(500, 400, 300, 200, 20)}, deskew: true, autocrop: true}
	out, _, err := p.Process(img)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := out.(*image.Gray); !ok {
		t.Fatalf("gray16 output %T", out)
	}
}
