package main

import (
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"math"

	xdraw "golang.org/x/image/draw"
	"golang.org/x/image/math/f64"
)

const maxProcessingPixels = 40_000_000

type processingInfo struct {
	Changed, Detected bool
	Angle             float64
	Warnings          []string
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

func validDocument(d documentDetection, w, h int) (float64, bool) {
	var area, sx, sy float64
	for i, a := range d.Corners {
		if math.IsNaN(a.X) || math.IsNaN(a.Y) || math.IsInf(a.X, 0) || math.IsInf(a.Y, 0) || a.X < 0 || a.Y < 0 || a.X > float64(w) || a.Y > float64(h) || math.IsNaN(d.Peaks[i]) || math.IsInf(d.Peaks[i], 0) || d.Peaks[i] < 0.3 {
			return 0, false
		}
	}
	for i, a := range d.Corners {
		b, c := d.Corners[(i+1)%4], d.Corners[(i+2)%4]
		dx, dy := b.X-a.X, b.Y-a.Y
		cross := dx*(c.Y-b.Y) - dy*(c.X-b.X)
		if math.Abs(cross) < 1 || (i > 0 && cross*area <= 0) {
			return 0, false
		}
		if i == 0 {
			area = cross
		}
		length := math.Hypot(dx, dy)
		if length < 2 {
			return 0, false
		}
		theta := math.Atan2(dy, dx) * 4
		sx += length * math.Cos(theta)
		sy += length * math.Sin(theta)
	}
	// Shoelace area rejects collapsed or implausibly small detections without assuming a page aspect ratio.
	area = 0
	for i, a := range d.Corners {
		b := d.Corners[(i+1)%4]
		area += a.X*b.Y - b.X*a.Y
	}
	if math.Abs(area)/2 < float64(w)*float64(h)*0.001 || math.Hypot(sx, sy) < 1e-9 {
		return 0, false
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
	angle, ok := validDocument(detection, w, h)
	if !ok {
		info.Warnings = append(info.Warnings, "无法可靠检测纸张边界，保留原图")
		return picture, info, nil
	}
	info.Detected, info.Angle = true, angle
	current := picture
	points := detection.Corners
	if p.deskew && math.Abs(angle) >= 0.3 {
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
		for _, pt := range points[1:] {
			minx = math.Min(minx, pt.X)
			miny = math.Min(miny, pt.Y)
			maxx = math.Max(maxx, pt.X)
			maxy = math.Max(maxy, pt.Y)
		}
		cb := current.Bounds()
		margin := math.Max(3, float64(max(w, h))*0.02)
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
