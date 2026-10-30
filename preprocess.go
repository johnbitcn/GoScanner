package main

import (
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"math"
	"math/bits"

	xdraw "golang.org/x/image/draw"
	"golang.org/x/image/math/f64"
)

const (
	maxProcessingPixels = 40_000_000
	edgeAngleTolerance  = 3.0
)

type processingInfo struct {
	Changed, Detected, AngleReliable bool
	Angle                            float64
	Warnings                         []string
}

type imageProcessor struct {
	detector         documentDetector
	deskew, autocrop bool
}

func newImageProcessor(deskew, autocrop bool) (*imageProcessor, error) {
	p := &imageProcessor{deskew: deskew, autocrop: autocrop}
	if deskew || autocrop {
		d, err := newDocAligner()
		if err != nil {
			return nil, err
		}
		p.detector = d
	}
	return p, nil
}

func (p *imageProcessor) Close() error {
	if p.detector == nil {
		return nil
	}
	err := p.detector.Close()
	p.detector = nil
	return err
}

func validDocument(d documentDetection, w, h int) bool {
	var area float64
	for i, a := range d.Corners {
		if math.IsNaN(a.X) || math.IsNaN(a.Y) || math.IsInf(a.X, 0) || math.IsInf(a.Y, 0) || a.X < 0 || a.Y < 0 || a.X > float64(w) || a.Y > float64(h) || math.IsNaN(d.Peaks[i]) || math.IsInf(d.Peaks[i], 0) || d.Peaks[i] < 0.3 {
			return false
		}
	}
	for i, a := range d.Corners {
		b, c := d.Corners[(i+1)%4], d.Corners[(i+2)%4]
		dx, dy := b.X-a.X, b.Y-a.Y
		cross := dx*(c.Y-b.Y) - dy*(c.X-b.X)
		if math.Abs(cross) < 1 || (i > 0 && cross*area <= 0) {
			return false
		}
		if i == 0 {
			area = cross
		}
		if math.Hypot(dx, dy) < 2 {
			return false
		}
	}
	// Shoelace area rejects collapsed or implausibly small detections without assuming a page aspect ratio.
	area = 0
	for i, a := range d.Corners {
		b := d.Corners[(i+1)%4]
		area += a.X*b.Y - b.X*a.Y
	}
	return math.Abs(area)/2 >= float64(w)*float64(h)*0.001
}

func documentAngle(d documentDetection) (float64, bool) {
	var angles [4]float64
	for i, a := range d.Corners {
		b := d.Corners[(i+1)%4]
		angles[i] = math.Atan2(b.Y-a.Y, b.X-a.X) * 180 / math.Pi
	}
	bestMask, bestCount, ties := 0, 1, 0
candidates:
	for mask := 1; mask < 16; mask++ {
		count := bits.OnesCount(uint(mask))
		if count < 2 || count < bestCount {
			continue
		}
		for i := 0; i < 4; i++ {
			if mask&(1<<i) == 0 {
				continue
			}
			for j := i + 1; j < 4; j++ {
				if mask&(1<<j) == 0 {
					continue
				}
				// Adjacent edges must be perpendicular; opposite edges must be parallel.
				residual := math.Abs(math.Remainder(angles[j]-angles[i]-float64(j-i)*90, 180))
				if residual > edgeAngleTolerance+1e-9 { // Allow round-off at exactly 3 degrees.
					continue candidates
				}
			}
		}
		if count > bestCount {
			bestMask, bestCount, ties = mask, count, 1
		} else {
			ties++
		}
	}
	if ties != 1 {
		return 0, false
	}
	// The fourfold circular mean treats perpendicular directions as the same skew.
	var sx, sy float64
	for i, angle := range angles {
		if bestMask&(1<<i) != 0 {
			theta := angle * math.Pi / 45
			sx += math.Cos(theta)
			sy += math.Sin(theta)
		}
	}
	return math.Atan2(sy, sx) * 45 / math.Pi, true
}

func (p *imageProcessor) Process(picture image.Image) (image.Image, processingInfo, error) {
	var info processingInfo
	if picture == nil {
		return nil, info, fmt.Errorf("preprocess: nil image")
	}
	if !p.deskew && !p.autocrop {
		return picture, info, nil
	}
	b := picture.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= 0 || h <= 0 || int64(w) > maxProcessingPixels/int64(h) {
		return picture, info, fmt.Errorf("preprocess: invalid or oversized image")
	}
	if p.detector == nil {
		return picture, info, fmt.Errorf("preprocess: detector is closed")
	}
	detection, err := p.detector.Detect(picture)
	if err != nil {
		return picture, info, fmt.Errorf("preprocess: detection: %w", err)
	}
	if !validDocument(detection, w, h) {
		info.Warnings = append(info.Warnings, "无法可靠检测纸张边界，保留原图")
		return picture, info, nil
	}
	info.Detected = true
	angle, reliable := documentAngle(detection)
	info.Angle, info.AngleReliable = angle, reliable
	if p.deskew && !reliable {
		warning := "边线方向不一致或存在歧义，已跳过旋转"
		if p.autocrop {
			warning += "；仍按有效边界裁切"
		}
		info.Warnings = append(info.Warnings, warning)
	}
	current := picture
	points := detection.Corners
	if p.deskew && reliable && math.Abs(angle) >= 0.3 {
		rad := angle * math.Pi / 180
		c, s := math.Cos(rad), math.Sin(rad)
		nw := int(math.Ceil(math.Abs(float64(w)*c) + math.Abs(float64(h)*s)))
		nh := int(math.Ceil(math.Abs(float64(h)*c) + math.Abs(float64(w)*s)))
		if int64(nw) > maxProcessingPixels/int64(nh) {
			return picture, info, fmt.Errorf("preprocess: rotated image exceeds pixel limit")
		}
		// Rotate around the image centre, including any non-zero input bounds.
		tx := float64(nw)/2 - c*float64(w)/2 - s*float64(h)/2
		ty := float64(nh)/2 + s*float64(w)/2 - c*float64(h)/2
		var dst draw.Image
		switch picture.(type) {
		case *image.Gray, *image.Gray16:
			dst = image.NewGray(image.Rect(0, 0, nw, nh))
		default:
			dst = image.NewRGBA(image.Rect(0, 0, nw, nh))
		}
		draw.Draw(dst, dst.Bounds(), image.NewUniform(color.White), image.Point{}, draw.Src)
		matrix := f64.Aff3{c, s, tx - c*float64(b.Min.X) - s*float64(b.Min.Y), -s, c, ty + s*float64(b.Min.X) - c*float64(b.Min.Y)}
		xdraw.BiLinear.Transform(dst, matrix, picture, b, draw.Over, nil)
		for i, pt := range points {
			points[i] = documentCorner{c*pt.X + s*pt.Y + tx, -s*pt.X + c*pt.Y + ty}
		}
		current = dst
		info.Changed = true
	}
	if p.autocrop {
		minx, miny, maxx, maxy := points[0].X, points[0].Y, points[0].X, points[0].Y
		// Size padding to the detected object, not unused flatbed space.
		margin := 3.0
		for i, pt := range points {
			minx = math.Min(minx, pt.X)
			miny = math.Min(miny, pt.Y)
			maxx = math.Max(maxx, pt.X)
			maxy = math.Max(maxy, pt.Y)
			next := points[(i+1)%len(points)]
			margin = math.Max(margin, math.Hypot(next.X-pt.X, next.Y-pt.Y)*0.02)
		}
		cb := current.Bounds()
		crop := image.Rect(max(0, int(math.Floor(minx-margin))), max(0, int(math.Floor(miny-margin))), min(cb.Dx(), int(math.Ceil(maxx+margin))), min(cb.Dy(), int(math.Ceil(maxy+margin))))
		if crop != image.Rect(0, 0, cb.Dx(), cb.Dy()) {
			crop = crop.Add(cb.Min)
			var dst draw.Image
			switch current.(type) {
			case *image.Gray, *image.Gray16:
				dst = image.NewGray(image.Rect(0, 0, crop.Dx(), crop.Dy()))
			default:
				dst = image.NewRGBA(image.Rect(0, 0, crop.Dx(), crop.Dy()))
			}
			draw.Draw(dst, dst.Bounds(), current, crop.Min, draw.Src)
			current = dst
			info.Changed = true
		}
	}
	return current, info, nil
}
