package main

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"math"
	"sort"

	"gocv.io/x/gocv"
)

func analysisGray(src gocv.Mat, gray *gocv.Mat) error {
	w, h := src.Cols(), src.Rows()
	scale := math.Min(1, 1600/float64(max(w, h)))
	small := gocv.NewMat()
	defer small.Close()
	if err := gocv.Resize(src, &small, image.Pt(max(1, int(float64(w)*scale)), max(1, int(float64(h)*scale))), 0, 0, gocv.InterpolationArea); err != nil {
		return err
	}
	if small.Channels() == 1 {
		return small.CopyTo(gray)
	}
	return gocv.CvtColor(small, gray, gocv.ColorBGRToGray)
}

// shortcut: crop only light pages on dark backgrounds; extend after validating real scan samples.
func paperBoundary(src gocv.Mat, expanded bool) (image.Rectangle, float64, bool, error) {
	w, h := src.Cols(), src.Rows()
	scale := math.Min(1, 1600/float64(max(w, h)))
	gray := gocv.NewMat()
	defer gray.Close()
	if err := analysisGray(src, &gray); err != nil {
		return image.Rectangle{}, 0, false, err
	}
	sw, sh := gray.Cols(), gray.Rows()
	if sw < 20 || sh < 20 {
		return image.Rectangle{}, 0, false, nil
	}
	// Inset the samples so expanded white rotation corners do not hide the background.
	for side := 0; side < 4; side++ {
		dark, n := 0, 0
		length := sw
		if side >= 2 {
			length = sh
		}
		for i := length / 10; i < length-length/10; i++ {
			x, y := i, sh*8/100
			switch side {
			case 1:
				y = sh - 1 - sh*8/100
			case 2:
				x, y = sw*8/100, i
			case 3:
				x, y = sw-1-sw*8/100, i
			}
			if gray.GetUCharAt(y, x) < 180 {
				dark++
			}
			n++
		}
		if dark*100 < n*65 {
			return image.Rectangle{}, 0, false, nil
		}
	}
	binary := gocv.NewMat()
	defer binary.Close()
	gocv.Threshold(gray, &binary, 200, 255, gocv.ThresholdBinary)
	contours := gocv.FindContours(binary, gocv.RetrievalExternal, gocv.ChainApproxSimple)
	defer contours.Close()
	bestArea := 0.0
	components := 0
	var best image.Rectangle
	var bestAngle float64
	for i := 0; i < contours.Size(); i++ {
		// At returns a borrowed vector owned by contours.
		c := contours.At(i)
		area := gocv.ContourArea(c)
		if area < 16 {
			continue
		}
		b := gocv.BoundingRect(c)
		touchesEdge := b.Min.X <= 1 || b.Min.Y <= 1 || b.Max.X >= sw-1 || b.Max.Y >= sh-1
		if expanded && touchesEdge {
			continue // Ignore the known white corners added by canvas expansion.
		}
		components++
		if touchesEdge || area < float64(sw*sh)*0.30 || area <= bestArea {
			continue
		}
		approx := gocv.ApproxPolyDP(c, 0.02*gocv.ArcLength(c, true), true)
		if approx.Size() != 4 {
			approx.Close()
			continue
		}
		r := gocv.MinAreaRect(approx)
		approx.Close()
		if r.Width <= 0 || r.Height <= 0 || area/float64(r.Width*r.Height) < 0.92 || len(r.Points) != 4 {
			continue
		}
		p, q := r.Points[0], r.Points[1]
		angle := math.Atan2(float64(q.Y-p.Y), float64(q.X-p.X)) * 180 / math.Pi
		for angle < -45 {
			angle += 90
		}
		for angle > 45 {
			angle -= 90
		}
		if math.Abs(angle) > 10 {
			continue
		}
		bestArea, best, bestAngle = area, b, angle
	}
	// Disconnected bright regions can be parts of one page divided by printed content.
	if bestArea == 0 || components != 1 {
		return image.Rectangle{}, 0, false, nil
	}
	// Round outward and retain a small background margin to protect edge marks.
	margin := 3.0 / scale
	return image.Rect(max(0, int(math.Floor(float64(best.Min.X)/scale-margin))), max(0, int(math.Floor(float64(best.Min.Y)/scale-margin))), min(w, int(math.Ceil(float64(best.Max.X)/scale+margin))), min(h, int(math.Ceil(float64(best.Max.Y)/scale+margin)))), bestAngle, true, nil
}

// textSkew requires agreement across multiple rows, not a single frame or photo edge.
func textSkew(src gocv.Mat) (float64, bool, error) {
	gray := gocv.NewMat()
	defer gray.Close()
	if err := analysisGray(src, &gray); err != nil {
		return 0, false, err
	}
	w, h := gray.Cols(), gray.Rows()
	if w < 100 || h < 100 {
		return 0, false, nil
	}
	edges, lines := gocv.NewMat(), gocv.NewMat()
	defer edges.Close()
	defer lines.Close()
	if err := gocv.Canny(gray, &edges, 50, 150); err != nil {
		return 0, false, err
	}
	if err := gocv.HoughLinesPWithParams(edges, &lines, 1, math.Pi/720, 40, float32(w)/8, float32(w)/40); err != nil {
		return 0, false, err
	}
	type segment struct{ angle, length, y float64 }
	var segments []segment
	total := 0.0
	for i := 0; i < lines.Rows(); i++ {
		p := lines.GetVeciAt(i, 0)
		dx, dy := float64(p[2]-p[0]), float64(p[3]-p[1])
		if dx == 0 {
			continue
		}
		angle := math.Atan(dy/dx) * 180 / math.Pi
		if math.Abs(angle) > 10 {
			continue
		}
		length := math.Hypot(dx, dy)
		y := float64(p[1]) + (float64(w)/2-float64(p[0]))*dy/dx
		segments = append(segments, segment{angle, length, y})
		total += length
	}
	if len(segments) < 5 || total < float64(w) {
		return 0, false, nil
	}
	sort.Slice(segments, func(i, j int) bool { return segments[i].angle < segments[j].angle })
	median, weight := 0.0, 0.0
	for _, s := range segments {
		weight += s.length
		if weight >= total/2 {
			median = s.angle
			break
		}
	}
	var rows []float64
	angle, agreed := 0.0, 0.0
	for _, s := range segments {
		if math.Abs(s.angle-median) <= 0.75 {
			angle += s.angle * s.length
			agreed += s.length
			rows = append(rows, s.y)
		}
	}
	if agreed < total*0.8 || len(rows) < 5 {
		return 0, false, nil
	}
	sort.Float64s(rows)
	count, last := 1, rows[0]
	for _, y := range rows[1:] {
		if y-last >= math.Max(10, float64(h)/60) {
			count++
			last = y
		}
	}
	if count < 5 || last-rows[0] < float64(h)*0.2 {
		return 0, false, nil
	}
	return angle / agreed, true, nil
}

func preprocessJPEG(data []byte, deskew, autocrop bool) ([]byte, []string, error) {
	if !deskew && !autocrop {
		return data, nil, nil
	}
	if len(data) < 2 || !bytes.Equal(data[:2], []byte{0xff, 0xd8}) {
		return nil, nil, fmt.Errorf("preprocess: invalid JPEG")
	}
	config, err := jpeg.DecodeConfig(bytes.NewReader(data))
	if err != nil || config.Width <= 0 || config.Height <= 0 || int64(config.Width)*int64(config.Height) > 40_000_000 {
		return nil, nil, fmt.Errorf("preprocess: invalid or oversized JPEG")
	}
	if _, err := jpeg.Decode(bytes.NewReader(data)); err != nil {
		return nil, nil, fmt.Errorf("preprocess: invalid JPEG: %w", err)
	}
	readMode := gocv.IMReadColor
	if config.ColorModel == color.GrayModel {
		readMode = gocv.IMReadGrayScale
	}
	src, err := gocv.IMDecode(data, readMode)
	defer src.Close()
	if err != nil {
		return nil, nil, fmt.Errorf("preprocess: decode JPEG: %w", err)
	}
	if src.Empty() {
		return nil, nil, fmt.Errorf("preprocess: invalid JPEG")
	}
	var warnings []string
	changed := false
	current := &src
	rotated := gocv.NewMat()
	defer rotated.Close()
	if deskew {
		_, angle, ok, err := paperBoundary(*current, false)
		if err == nil && !ok {
			angle, ok, err = textSkew(*current)
		}
		if err != nil {
			return nil, nil, fmt.Errorf("preprocess: detect paper: %w", err)
		}
		if !ok {
			warnings = append(warnings, "无法可靠判断倾斜角度，保留原图")
		} else if math.Abs(angle) >= 0.3 {
			w, h := current.Cols(), current.Rows()
			rad := angle * math.Pi / 180
			nw := int(math.Ceil(math.Abs(float64(w)*math.Cos(rad)) + math.Abs(float64(h)*math.Sin(rad))))
			nh := int(math.Ceil(math.Abs(float64(h)*math.Cos(rad)) + math.Abs(float64(w)*math.Sin(rad))))
			if int64(nw)*int64(nh) > 40_000_000 {
				warnings = append(warnings, "纠偏后的图像超过像素上限，跳过纠偏")
			} else {
				matrix := gocv.GetRotationMatrix2D(image.Pt(w/2, h/2), angle, 1)
				matrix.SetDoubleAt(0, 2, matrix.GetDoubleAt(0, 2)+float64(nw-w)/2)
				matrix.SetDoubleAt(1, 2, matrix.GetDoubleAt(1, 2)+float64(nh-h)/2)
				err = gocv.WarpAffineWithParams(*current, &rotated, matrix, image.Pt(nw, nh), gocv.InterpolationLinear, gocv.BorderConstant, color.RGBA{255, 255, 255, 255})
				matrix.Close()
				if err != nil {
					return nil, nil, fmt.Errorf("preprocess: rotate: %w", err)
				}
				current = &rotated
				changed = true
			}
		}
	}
	if autocrop {
		bounds, _, ok, err := paperBoundary(*current, current == &rotated)
		if err != nil {
			return nil, nil, fmt.Errorf("preprocess: detect paper: %w", err)
		}
		if !ok {
			warnings = append(warnings, "无法可靠检测纸张边界，保留图像")
		} else if bounds != image.Rect(0, 0, current.Cols(), current.Rows()) {
			cropped := current.Region(bounds)
			defer cropped.Close()
			current = &cropped
			changed = true
		}
	}
	if !changed {
		return data, warnings, nil
	}
	encoded, err := gocv.IMEncodeWithParams(gocv.JPEGFileExt, *current, []int{gocv.IMWriteJpegQuality, 95})
	if encoded != nil {
		defer encoded.Close()
	}
	if err != nil {
		return nil, nil, fmt.Errorf("preprocess: encode JPEG: %w", err)
	}
	return append([]byte(nil), encoded.GetBytes()...), warnings, nil
}
