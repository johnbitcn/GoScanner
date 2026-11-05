package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"golang.org/x/image/tiff"
)

const (
	formatJPEG = "jfif"
	formatTIFF = "tiff-single-uncompressed"
)

// A zero region selects the full platen after reading device capabilities.
type region struct{ X, Y, Width, Height int }
type options struct {
	DPI              int
	Mode             string
	Area             region
	Format           string
	Deskew, Autocrop bool
	processor        *imageProcessor
	newProcessor     func(bool, bool) (*imageProcessor, error)
	originalPath     string
}
type scanResult struct {
	Bytes, Width, Height int
	processing           processingInfo
	warning              error
	originalPath         string
	retainedPath         string
}

func parseArea(text string) (region, error) {
	if strings.EqualFold(text, "full") {
		return region{}, nil
	}
	if strings.EqualFold(text, "a4") {
		return region{Width: 8268, Height: 11693}, nil
	}
	parts := strings.Split(text, ",")
	if len(parts) != 4 {
		return region{}, diagnosticf("--area must be full, a4, or x,y,width,height in millimeters.", "--area 使用 full、a4 或 x,y,width,height（毫米）")
	}
	var n [4]int
	for i, part := range parts {
		mm, err := strconv.ParseFloat(strings.TrimSpace(part), 64)
		units := math.Round(mm * 1000 / 25.4)
		if err != nil || math.IsNaN(units) || math.IsInf(units, 0) || mm < 0 || units > math.MaxInt32 {
			return region{}, diagnosticf("Scan area values must be finite, nonnegative millimeter values within range.", "扫描区域必须是有限、非负且未超出范围的毫米数值")
		}
		n[i] = int(units)
	}
	if n[2] <= 0 || n[3] <= 0 {
		return region{}, diagnosticf("Scan area width and height must be greater than zero.", "扫描区域宽高必须大于零")
	}
	return region{n[0], n[1], n[2], n[3]}, nil
}

func ticket(cap info, opts options) (string, error) {
	if cap.escl != nil {
		return esclTicket(cap, opts)
	}
	if cap.State != "Idle" {
		return "", diagnosticf("The scanner is not ready. Device state: %q", "扫描器尚未就绪：%s", cap.State)
	}
	format := opts.Format
	if format == "" {
		format = formatJPEG
	}
	if (format != formatJPEG && format != formatTIFF) || !slices.Contains(cap.Formats, format) {
		return "", diagnosticf("The scanner does not declare support for format %q.", "打印机未声明支持扫描格式 %q", format)
	}
	// The device advertises BlackAndWhite1, but its JPEG combination failed live testing.
	colors := map[string]string{"color": "RGB24", "gray": "Grayscale8"}
	color, ok := colors[opts.Mode]
	if !ok || !slices.Contains(cap.Colors, color) {
		return "", diagnosticf("The scan color mode is invalid or unsupported. Use color or gray.", "扫描色彩模式无效或打印机不支持；使用 color 或 gray")
	}
	if !slices.Contains(cap.WidthsDPI, opts.DPI) || !slices.Contains(cap.HeightsDPI, opts.DPI) {
		return "", diagnosticf("The scanner does not support %d DPI.", "打印机不支持 %d DPI", opts.DPI)
	}
	r := opts.Area
	if r == (region{}) {
		r = region{Width: cap.MaxSize.Width, Height: cap.MaxSize.Height}
	}
	if r.Width <= 0 || r.Height <= 0 || r.X < 0 || r.Y < 0 || r.Width < cap.MinSize.Width || r.Height < cap.MinSize.Height ||
		int64(r.X)+int64(r.Width) > int64(cap.MaxSize.Width) || int64(r.Y)+int64(r.Height) > int64(cap.MaxSize.Height) {
		return "", diagnosticf("The scan area exceeds the scanner bounds or is smaller than the minimum size.", "扫描区域超出设备范围或小于最小尺寸")
	}
	return fmt.Sprintf(`<w:ScanTicket>
<w:JobDescription><w:JobName>GoScanner scan</w:JobName><w:JobOriginatingUserName>goscanner</w:JobOriginatingUserName><w:JobInformation>Single-page scan</w:JobInformation></w:JobDescription>
<w:DocumentParameters><w:Format>%s</w:Format><w:ImagesToTransfer>1</w:ImagesToTransfer><w:ContentType>Mixed</w:ContentType>
<w:InputSize><w:InputMediaSize><w:Width>%d</w:Width><w:Height>%d</w:Height></w:InputMediaSize></w:InputSize><w:InputSource>Platen</w:InputSource>
<w:MediaSides><w:MediaFront><w:ColorProcessing>%s</w:ColorProcessing><w:Resolution><w:Width>%d</w:Width><w:Height>%d</w:Height></w:Resolution>
<w:ScanRegion><w:ScanRegionXOffset>%d</w:ScanRegionXOffset><w:ScanRegionYOffset>%d</w:ScanRegionYOffset><w:ScanRegionWidth>%d</w:ScanRegionWidth><w:ScanRegionHeight>%d</w:ScanRegionHeight></w:ScanRegion>
</w:MediaFront></w:MediaSides></w:DocumentParameters></w:ScanTicket>`, format, r.X+r.Width, r.Y+r.Height, color, opts.DPI, opts.DPI, r.X, r.Y, r.Width, r.Height), nil
}

func (c *client) cancel(ctx context.Context, id string) error {
	if c.protocol == "escl" {
		return c.esclCancel(ctx, id)
	}
	_, data, err := c.post(ctx, "CancelJob", `<w:CancelJobRequest><w:JobId>`+escape(id)+`</w:JobId></w:CancelJobRequest>`)
	if err != nil {
		return err
	}
	_, err = soapResponse(data, "CancelJobResponse")
	return err
}

func (c *client) create(ctx context.Context, scanTicket string) (id, token string, err error) {
	if c.protocol == "escl" {
		id, err = c.esclCreate(ctx, scanTicket)
		return id, "", err
	}
	_, data, err := c.post(ctx, "CreateScanJob", "<w:CreateScanJobRequest>"+scanTicket+"</w:CreateScanJobRequest>")
	if err != nil {
		return "", "", err
	}
	created, err := soapResponse(data, "CreateScanJobResponse")
	if err != nil {
		return "", "", err
	}
	if value := created.path("JobId").text(); value != "" {
		if n, parseErr := strconv.ParseUint(value, 10, 32); parseErr == nil && n > 0 {
			id = value
		}
	}
	if node := created.path("JobToken"); node != nil {
		token = node.Text // JobToken is opaque; preserve its whitespace.
	}
	if id == "" || strings.TrimSpace(token) == "" {
		return id, token, diagnosticf("The scan job creation response has no valid JobId or JobToken.", "创建响应缺少有效的 JobId 或 JobToken")
	}
	return id, token, nil
}

func (c *client) retrieve(ctx context.Context, id, token, format string) ([]byte, error) {
	if c.protocol == "escl" {
		return c.esclRetrieve(ctx, id)
	}
	// HP firmware expects this fixed document name even when the requested format is TIFF.
	request := `<w:RetrieveImageRequest><w:DocumentDescription><w:DocumentName>IMAGE000.JPG</w:DocumentName></w:DocumentDescription><w:JobId>` + escape(id) + `</w:JobId><w:JobToken>` + escape(token) + `</w:JobToken></w:RetrieveImageRequest>`
	contentType, data, err := c.post(ctx, "RetrieveImage", request)
	if err != nil {
		return nil, err
	}
	return imageFromResponse(contentType, data, format)
}

// Use the same format negotiation for readiness checks and actual acquisition.
func imageTicket(cap info, opts options, ext string) (string, options, error) {
	opts.Format = formatJPEG
	if ext == ".tif" || ext == ".tiff" || ext == ".png" {
		opts.Format = formatTIFF
	}
	formats := []string{opts.Format}
	if cap.escl != nil && ext == ".png" {
		formats = []string{formatPNG, formatTIFF, formatJPEG}
	}
	var body string
	var err error
	for _, format := range formats {
		opts.Format = format
		body, err = ticket(cap, opts)
		if err == nil {
			return body, opts, nil
		}
	}
	return "", opts, err
}

func (c *client) scan(ctx context.Context, opts options, output string) (result scanResult, err error) {
	ext := strings.ToLower(filepath.Ext(output))
	switch ext {
	case ".jpg", ".jpeg":
		opts.Format = formatJPEG
	case ".tif", ".tiff", ".png":
		opts.Format = formatTIFF
	default:
		return result, diagnosticf("The image output extension must be .jpg, .jpeg, .tif, .tiff, or .png.", "图像输出扩展名必须是 .jpg、.jpeg、.tif、.tiff 或 .png")
	}
	unlock, err := lockScanner(c.endpoint)
	if err != nil {
		return result, err
	}
	defer unlock()
	if err = ctx.Err(); err != nil {
		return result, err
	}
	cap, err := c.info(ctx)
	if err != nil {
		return result, err
	}
	scanTicket, opts, err := imageTicket(cap, opts, ext)
	if err != nil {
		return result, err
	}
	if c.beforeScan != nil {
		if err = c.beforeScan(ctx, cap); err != nil {
			return result, err
		}
	}
	if c.protocol == "escl" && opts.originalPath != "" {
		sourceExt := map[string]string{formatPNG: ".png", formatJPEG: ".jpg", formatTIFF: ".tiff"}[opts.Format]
		opts.originalPath = strings.TrimSuffix(opts.originalPath, filepath.Ext(opts.originalPath)) + sourceExt
	}
	if err = os.MkdirAll(filepath.Dir(output), 0700); err != nil {
		return result, err
	}
	if _, err := os.Lstat(output); err == nil {
		return result, diagnosticf("The output file already exists and will not be overwritten.", "输出文件已存在，不会覆盖")
	} else if !errors.Is(err, os.ErrNotExist) {
		return result, err
	}
	if opts.originalPath != "" {
		if _, err := os.Lstat(opts.originalPath); err == nil {
			return result, diagnosticf("The original image file already exists and will not be overwritten.", "原图文件已存在，不会覆盖")
		} else if !errors.Is(err, os.ErrNotExist) {
			return result, err
		}
	}
	file, err := os.CreateTemp(filepath.Dir(output), ".goscanner-*"+ext+".part")
	if err != nil {
		return result, diagnosticf("Cannot create a staging file: %w", "不能创建暂存文件：%w", err)
	}
	success, keepStaging := false, false
	jobID := ""
	defer func() {
		_ = file.Close()
		if !keepStaging {
			if removeErr := os.Remove(file.Name()); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
				err = errors.Join(err, diagnosticf("Cannot remove a staging file: %w", "删除暂存文件失败：%w", removeErr))
			}
		}
		if !success {
			if jobID != "" {
				// The user's cancelled context must not prevent cleanup of our own job.
				cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				if cancelErr := c.cancel(cleanup, jobID); cancelErr != nil {
					err = errors.Join(err, diagnosticf("Scan job cancellation was not confirmed: %w", "取消任务未确认：%w", cancelErr))
				}
			}
		}
	}()

	var token string
	jobID, token, err = c.create(ctx, scanTicket)
	if err != nil {
		return result, diagnosticf("Scan job creation was not confirmed. Do not immediately repeat the scan: %w", "创建任务结果未确认；不要立即重复扫描：%w", err)
	}
	payload, err := c.retrieve(ctx, jobID, token, opts.Format)
	if err != nil {
		return result, err
	}
	config, sourceFormat, err := image.DecodeConfig(bytes.NewReader(payload))
	wantFormat := "jpeg"
	if opts.Format == formatTIFF {
		wantFormat = "tiff"
	} else if opts.Format == formatPNG {
		wantFormat = "png"
	}
	if err != nil || sourceFormat != wantFormat || config.Width <= 0 || config.Height <= 0 || config.Width > 40_000_000/config.Height {
		return result, diagnosticf("The image format or dimensions are invalid, or the image exceeds 40 million pixels.", "图像格式或尺寸无效，或超过 4000 万像素")
	}
	picture, _, err := image.Decode(bytes.NewReader(payload))
	if err != nil {
		return result, diagnosticf("The image could not be fully decoded.", "图像无法完整解码")
	}
	if opts.originalPath != "" {
		if err = saveOriginal(opts.originalPath, payload); err != nil {
			return result, err
		}
		result.originalPath = opts.originalPath
	}
	if err = ctx.Err(); err != nil {
		return result, err
	}
	if opts.processor != nil {
		var processed image.Image
		processed, result.processing, err = opts.processor.Process(picture)
		if err != nil {
			result.warning = err
			result.processing.Changed = false
			err = nil
		} else {
			picture = processed
		}
	}
	if result.processing.Changed {
		switch ext {
		case ".jpg", ".jpeg":
			err = jpeg.Encode(file, picture, &jpeg.Options{Quality: 95})
		case ".png":
			err = png.Encode(file, picture)
		default:
			err = tiff.Encode(file, picture, nil)
		}
	} else if ext == ".png" {
		err = png.Encode(file, picture)
	} else {
		_, err = file.Write(payload)
	}
	if err != nil {
		return result, err
	}
	if err = ctx.Err(); err != nil {
		return result, err
	}
	stat, err := file.Stat()
	if err != nil {
		return result, err
	}
	if err = file.Sync(); err != nil {
		return result, err
	}
	if err = file.Close(); err != nil {
		return result, err
	}
	// Link publishes the complete file atomically and refuses existing paths.
	if err = os.Link(file.Name(), output); err != nil {
		keepStaging = true
		result.retainedPath = file.Name()
		return result, diagnosticf("Cannot publish the output file. The complete image is retained at %q: %w", "发布文件失败；完整图像保留在 %s：%w", file.Name(), err)
	}
	success = true
	result.Bytes, result.Width, result.Height = int(stat.Size()), picture.Bounds().Dx(), picture.Bounds().Dy()
	if c.protocol == "escl" {
		// Publish first: cleanup failure must not destroy a successfully scanned page.
		cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if cleanupErr := c.cancel(cleanup, jobID); cleanupErr != nil {
			result.retainedPath = output
			return result, diagnosticf("The scanned image was saved to %q. eSCL job cleanup failed. The scan was not automatically retried: %w", "扫描图像已保存至 %s；eSCL 任务清理失败，未自动重试扫描：%w", output, cleanupErr)
		}
	}
	return result, nil
}

func saveOriginal(path string, payload []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".goscanner-original-*.part")
	if err != nil {
		return err
	}
	keep := false
	defer func() {
		if !keep {
			_ = os.Remove(f.Name())
		}
	}()
	if _, err = f.Write(payload); err == nil {
		err = f.Sync()
	}
	if e := f.Close(); err == nil {
		err = e
	}
	if err != nil {
		return diagnosticf("Cannot save the original image: %w", "保存原图失败: %w", err)
	}
	if err = os.Link(f.Name(), path); err != nil {
		keep = true
		return diagnosticf("Cannot publish the original image. It is retained at %q: %w", "原图发布失败；保留在 %s: %w", f.Name(), err)
	}
	return nil
}
