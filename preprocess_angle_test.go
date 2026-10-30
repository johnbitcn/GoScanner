package main

import (
	"fmt"
	"image"
	"math"
	"testing"
)

func detectionWithAngles(t *testing.T, angles [4]float64) documentDetection {
	t.Helper()
	var vectors [4]documentCorner
	for i, angle := range angles {
		rad := angle * math.Pi / 180
		vectors[i] = documentCorner{math.Cos(rad), math.Sin(rad)}
	}
	// Close the quadrilateral while keeping unequal edge lengths and the requested directions.
	x := 300*vectors[0].X + 100*vectors[1].X
	y := 300*vectors[0].Y + 100*vectors[1].Y
	det := vectors[2].X*vectors[3].Y - vectors[2].Y*vectors[3].X
	length2 := (-x*vectors[3].Y + y*vectors[3].X) / det
	length3 := (-vectors[2].X*y + vectors[2].Y*x) / det
	if length2 <= 0 || length3 <= 0 || math.IsNaN(length2) || math.IsNaN(length3) {
		t.Fatalf("directions cannot form the test quadrilateral: %v", angles)
	}
	d := documentDetection{Peaks: [4]float64{.8, .8, .8, .8}}
	d.Corners[0] = documentCorner{400, 300}
	for i, length := range []float64{300, 100, length2} {
		d.Corners[i+1] = documentCorner{d.Corners[i].X + length*vectors[i].X, d.Corners[i].Y + length*vectors[i].Y}
	}
	if !validDocument(d, 1000, 800) {
		t.Fatalf("invalid test quadrilateral: %+v", d)
	}
	return d
}

func TestDocumentAngleConsensus(t *testing.T) {
	for _, tc := range []struct {
		name     string
		angles   [4]float64
		want     float64
		reliable bool
	}{
		{"rectangle", [4]float64{12, 102, 192, 282}, 12, true},
		{"one abnormal edge", [4]float64{12, 102, 192, 290}, 12, true},
		{"two adjacent references", [4]float64{12, 102, 210, 305}, 12, true},
		{"two opposite references", [4]float64{12, 85, 192, 260}, 12, true},
		{"within tolerance", [4]float64{12, 104.9, 192, 284.9}, 13.45, true},
		{"at tolerance and equal weights", [4]float64{12, 105, 192, 285}, 13.5, true},
		{"over tolerance", [4]float64{12, 105.1, 192, 285.1}, 0, false},
		{"four degree pair mismatch", [4]float64{-2, 92, 178, 272}, 0, false},
		{"chain is not consensus", [4]float64{12, 104, 196, 288}, 0, false},
		{"competing triples", [4]float64{12, 103.5, 195, 286.5}, 0, false},
		{"parallel adjacent edges are not perpendicular", [4]float64{12, 14, 192, 194}, 0, false},
		{"no supporting pair", [4]float64{0, 70, 190, 300}, 0, false},
		{"circular boundary", [4]float64{44, 136, 224, 316}, 45, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := detectionWithAngles(t, tc.angles)
			for _, reverse := range []bool{false, true} {
				for start := 0; start < 4; start++ {
					ordered := d
					for i := range ordered.Corners {
						j := (start + i) % 4
						if reverse {
							j = (start - i + 4) % 4
						}
						ordered.Corners[i] = d.Corners[j]
					}
					angle, reliable := documentAngle(ordered)
					if reliable != tc.reliable || (reliable && math.Abs(math.Remainder(angle-tc.want, 90)) > 1e-7) {
						t.Fatalf("reverse=%t start=%d: angle=%v reliable=%t; want %v %t", reverse, start, angle, reliable, tc.want, tc.reliable)
					}
				}
			}
		})
	}
}

func TestProcessorAmbiguousAngleFlags(t *testing.T) {
	d := documentDetection{
		Corners: [4]documentCorner{{100, 100}, {400, 100}, {450, 300}, {150, 300}},
		Peaks:   [4]float64{.8, .8, .8, .8},
	}
	img := image.NewGray(image.Rect(0, 0, 1000, 800))
	for _, deskew := range []bool{false, true} {
		for _, crop := range []bool{false, true} {
			t.Run(fmt.Sprintf("deskew=%t/crop=%t", deskew, crop), func(t *testing.T) {
				p := imageProcessor{detector: &fakeDetector{detection: d}, deskew: deskew, autocrop: crop}
				out, info, err := p.Process(img)
				if err != nil || info.Detected != (deskew || crop) || info.AngleReliable || info.Changed != crop || len(info.Warnings) != btoi(deskew) {
					t.Fatalf("info=%+v err=%v", info, err)
				}
				if crop {
					if out.Bounds() != image.Rect(0, 0, 362, 212) {
						t.Fatalf("crop must use unrotated corners: %v", out.Bounds())
					}
				} else if out != img {
					t.Fatal("ambiguous angle must preserve image without crop")
				}
			})
		}
	}
}
