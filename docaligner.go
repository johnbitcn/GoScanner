package main

import (
	"crypto/sha256"
	"fmt"
	"image"
	"io"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"sync"

	ort "github.com/yalue/onnxruntime_go"
	"gocv.io/x/gocv"
)

type documentCorner struct{ X, Y float64 }
type documentDetection struct {
	Corners [4]documentCorner
	Peaks   [4]float64
}
type documentDetector interface {
	Detect(image.Image) (documentDetection, error)
	Close() error
}

const docModelHash = "7f9f5a8935b2eb22b3ee0245d34996063f54562df390d34714af2d76928695bc"

var docEnvironment struct {
	sync.Mutex
	users int
	path  string
}

type docAligner struct {
	mu            sync.Mutex
	session       *ort.AdvancedSession
	input, output *ort.Tensor[float32]
	owned         bool
}

func newDocAligner() (documentDetector, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	model := os.Getenv("GOSCANNER_MODEL_PATH")
	if model == "" {
		model = filepath.Join(home, ".local/share/goscanner/models/fastvit_sa24_h_e_bifpn_256_fp32.onnx")
	}
	lib := os.Getenv("GOSCANNER_ONNXRUNTIME_PATH")
	if lib == "" {
		name := "libonnxruntime.so.1.31.1"
		if runtime.GOOS == "darwin" {
			name = "libonnxruntime.1.31.1.dylib"
		}
		if runtime.GOOS == "windows" {
			name = "onnxruntime.dll"
		}
		lib = filepath.Join(home, ".local/opt/goscanner/onnxruntime-1.31.1/lib", name)
	}
	f, err := os.Open(model)
	if err != nil {
		return nil, fmt.Errorf("open DocAligner model %q (set GOSCANNER_MODEL_PATH): %w", model, err)
	}
	h := sha256.New()
	_, err = io.Copy(h, f)
	closeErr := f.Close()
	if err != nil {
		return nil, fmt.Errorf("hash model: %w", err)
	}
	if closeErr != nil {
		return nil, closeErr
	}
	if fmt.Sprintf("%x", h.Sum(nil)) != docModelHash {
		return nil, fmt.Errorf("DocAligner model %q SHA256 mismatch; install the expected model", model)
	}
	if info, err := os.Stat(lib); err != nil {
		return nil, fmt.Errorf("open ONNX Runtime %q (set GOSCANNER_ONNXRUNTIME_PATH): %w", lib, err)
	} else if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("ONNX Runtime %q is not a regular file", lib)
	}
	docEnvironment.Lock()
	defer docEnvironment.Unlock()
	if docEnvironment.users == 0 {
		if ort.IsInitialized() {
			return nil, fmt.Errorf("ONNX Runtime environment is already owned by another component")
		}
		ort.SetSharedLibraryPath(lib)
		if err := ort.InitializeEnvironment(); err != nil {
			return nil, fmt.Errorf("initialize ONNX Runtime %q: %w", lib, err)
		}
		if ort.GetVersion() != "1.31.1" {
			version := ort.GetVersion()
			_ = ort.DestroyEnvironment()
			return nil, fmt.Errorf("ONNX Runtime version %s; require 1.31.1", version)
		}
		if err := ort.DisableTelemetry(); err != nil {
			_ = ort.DestroyEnvironment()
			return nil, fmt.Errorf("disable ONNX Runtime telemetry: %w", err)
		}
		docEnvironment.path = lib
	} else if lib != docEnvironment.path {
		return nil, fmt.Errorf("ONNX Runtime already in use from %q, not %q", docEnvironment.path, lib)
	}
	docEnvironment.users++
	d := &docAligner{owned: true}
	fail := func(err error) (documentDetector, error) { _ = d.closeLocked(); return nil, err }
	d.input, err = ort.NewEmptyTensor[float32](ort.NewShape(1, 3, 256, 256))
	if err != nil {
		return fail(err)
	}
	d.output, err = ort.NewEmptyTensor[float32](ort.NewShape(1, 4, 128, 128))
	if err != nil {
		return fail(err)
	}
	d.session, err = ort.NewAdvancedSession(model, []string{"img"}, []string{"heatmap"}, []ort.Value{d.input}, []ort.Value{d.output}, nil)
	if err != nil {
		return fail(fmt.Errorf("create DocAligner session: %w", err))
	}
	// The SHA pins the graph signature; run its named float tensors once instead of loading a metadata-only session.
	if err = d.session.Run(); err != nil {
		return fail(fmt.Errorf("validate DocAligner input/output contract: %w", err))
	}
	for _, v := range d.output.GetData() {
		if math.IsNaN(float64(v)) || math.IsInf(float64(v), 0) || v < 0 || v > 1 {
			return fail(fmt.Errorf("invalid DocAligner heatmap during preflight"))
		}
	}
	return d, nil
}

// closeLocked requires docEnvironment's lock and releases resources in dependency order.
func (d *docAligner) closeLocked() error {
	var err error
	if d.session != nil {
		err = d.session.Destroy()
		d.session = nil
	}
	if d.input != nil {
		if e := d.input.Destroy(); err == nil {
			err = e
		}
		d.input = nil
	}
	if d.output != nil {
		if e := d.output.Destroy(); err == nil {
			err = e
		}
		d.output = nil
	}
	if d.owned {
		d.owned = false
		docEnvironment.users--
		if docEnvironment.users == 0 {
			if e := ort.DestroyEnvironment(); err == nil {
				err = e
			}
			docEnvironment.path = ""
		}
	}
	return err
}
func (d *docAligner) Close() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	docEnvironment.Lock()
	defer docEnvironment.Unlock()
	return d.closeLocked()
}

func docInput(img image.Image, dst []float32) error {
	if img == nil || img.Bounds().Empty() || len(dst) != 3*256*256 {
		return fmt.Errorf("invalid DocAligner image or input tensor")
	}
	b := img.Bounds()
	data := make([]byte, b.Dx()*b.Dy()*3)
	i := 0
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			r, g, bl, _ := img.At(x, y).RGBA()
			data[i] = byte(bl >> 8)
			data[i+1] = byte(g >> 8)
			data[i+2] = byte(r >> 8)
			i += 3
		}
	}
	src, err := gocv.NewMatFromBytes(b.Dy(), b.Dx(), gocv.MatTypeCV8UC3, data)
	if err != nil {
		return err
	}
	defer src.Close()
	resized := gocv.NewMat()
	defer resized.Close()
	if err = gocv.Resize(src, &resized, image.Pt(256, 256), 0, 0, gocv.InterpolationLinear); err != nil {
		return err
	}
	pixels, err := resized.DataPtrUint8()
	if err != nil {
		return err
	}
	for i := 0; i < 256*256; i++ {
		for c := 0; c < 3; c++ {
			dst[c*256*256+i] = float32(pixels[3*i+c]) / 255
		}
	}
	return nil
}
func docHeatmaps(data []float32, width, height int) (documentDetection, error) {
	var result documentDetection
	if len(data) != 4*128*128 || width <= 0 || height <= 0 {
		return result, fmt.Errorf("invalid heatmap shape")
	}
	for c := 0; c < 4; c++ {
		heat := gocv.NewMatWithSize(128, 128, gocv.MatTypeCV32F)
		values, err := heat.DataPtrFloat32()
		if err != nil {
			heat.Close()
			return result, err
		}
		peak := math.Inf(-1)
		for i, v := range data[c*128*128 : (c+1)*128*128] {
			if math.IsNaN(float64(v)) || math.IsInf(float64(v), 0) {
				heat.Close()
				return result, fmt.Errorf("non-finite heatmap for corner %d", c)
			}
			values[i] = v
			if float64(v) > peak {
				peak = float64(v)
			}
		}
		result.Peaks[c] = peak
		resized := gocv.NewMat()
		err = gocv.Resize(heat, &resized, image.Pt(width, height), 0, 0, gocv.InterpolationLinear)
		heat.Close()
		if err != nil {
			resized.Close()
			return result, err
		}
		values, err = resized.DataPtrFloat32()
		if err != nil {
			resized.Close()
			return result, err
		}
		maskData := make([]byte, width*height)
		for i, v := range values {
			if v >= 0.3 {
				maskData[i] = byte(math.Min(float64(v)*255, 255))
			}
		}
		resized.Close()
		mask, err := gocv.NewMatFromBytes(height, width, gocv.MatTypeCV8UC1, maskData)
		if err != nil {
			return result, err
		}
		binary := gocv.NewMat()
		gocv.Threshold(mask, &binary, 0, 255, gocv.ThresholdBinary|gocv.ThresholdOtsu)
		mask.Close()
		contours := gocv.FindContours(binary, gocv.RetrievalExternal, gocv.ChainApproxSimple)
		binary.Close()
		maxArea := 0.0
		var center documentCorner
		for i := 0; i < contours.Size(); i++ {
			contour := contours.At(i)
			area := gocv.ContourArea(contour)
			if area > maxArea {
				contourMat := gocv.NewMatFromPointVector(contour, true)
				m := gocv.Moments(contourMat, false)
				contourMat.Close()
				if m["m00"] > 0 {
					maxArea = area
					center = documentCorner{m["m10"] / m["m00"], m["m01"] / m["m00"]}
				}
			}
		}
		contours.Close()
		if maxArea == 0 {
			return result, fmt.Errorf("missing heatmap corner %d (peak %g)", c, peak)
		}
		result.Corners[c] = center
	}
	return result, nil
}
func (d *docAligner) Detect(img image.Image) (documentDetection, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.session == nil {
		return documentDetection{}, fmt.Errorf("DocAligner is closed")
	}
	if err := docInput(img, d.input.GetData()); err != nil {
		return documentDetection{}, err
	}
	if err := d.session.Run(); err != nil {
		return documentDetection{}, fmt.Errorf("DocAligner inference: %w", err)
	}
	return docHeatmaps(d.output.GetData(), img.Bounds().Dx(), img.Bounds().Dy())
}
