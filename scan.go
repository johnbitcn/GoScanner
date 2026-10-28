package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image/jpeg"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

type region struct{ X, Y, Width, Height int }
type options struct {
	DPI  int
	Mode string
	Area region
}
type scanResult struct{ Bytes, Width, Height int }

func parseArea(text string) (region, error) {
	if strings.EqualFold(text, "a4") {
		return region{Width: 8268, Height: 11693}, nil
	}
	parts := strings.Split(text, ",")
	if len(parts) != 4 {
		return region{}, errors.New("--area 使用 a4 或 x,y,width,height（毫米）")
	}
	var n [4]int
	for i, part := range parts {
		mm, err := strconv.ParseFloat(strings.TrimSpace(part), 64)
		units := math.Round(mm * 1000 / 25.4)
		if err != nil || math.IsNaN(units) || math.IsInf(units, 0) || mm < 0 || units > math.MaxInt32 {
			return region{}, errors.New("扫描区域必须是有限、非负且未超出范围的毫米数值")
		}
		n[i] = int(units)
	}
	if n[2] <= 0 || n[3] <= 0 {
		return region{}, errors.New("扫描区域宽高必须大于零")
	}
	return region{n[0], n[1], n[2], n[3]}, nil
}

func ticket(cap info, opts options) (string, error) {
	if cap.State != "Idle" {
		return "", fmt.Errorf("扫描器尚未就绪：%s", cap.State)
	}
	if !slices.Contains(cap.Formats, "jfif") {
		return "", errors.New("打印机未声明支持 JFIF/JPEG")
	}
	// The device advertises BlackAndWhite1, but its JPEG combination failed live testing.
	colors := map[string]string{"color": "RGB24", "gray": "Grayscale8"}
	color, ok := colors[opts.Mode]
	if !ok || !slices.Contains(cap.Colors, color) {
		return "", errors.New("JPEG 色彩模式无效或打印机不支持；使用 color 或 gray")
	}
	if !slices.Contains(cap.WidthsDPI, opts.DPI) || !slices.Contains(cap.HeightsDPI, opts.DPI) {
		return "", fmt.Errorf("打印机不支持 %d DPI", opts.DPI)
	}
	r := opts.Area
	if r.X < 0 || r.Y < 0 || r.Width < cap.MinSize.Width || r.Height < cap.MinSize.Height ||
		int64(r.X)+int64(r.Width) > int64(cap.MaxSize.Width) || int64(r.Y)+int64(r.Height) > int64(cap.MaxSize.Height) {
		return "", errors.New("扫描区域超出设备范围或小于最小尺寸")
	}
	return fmt.Sprintf(`<w:ScanTicket>
<w:JobDescription><w:JobName>GoScanner scan</w:JobName><w:JobOriginatingUserName>goscanner</w:JobOriginatingUserName><w:JobInformation>Single-page scan</w:JobInformation></w:JobDescription>
<w:DocumentParameters><w:Format>jfif</w:Format><w:ImagesToTransfer>1</w:ImagesToTransfer><w:ContentType>Mixed</w:ContentType>
<w:InputSize><w:InputMediaSize><w:Width>%d</w:Width><w:Height>%d</w:Height></w:InputMediaSize></w:InputSize><w:InputSource>Platen</w:InputSource>
<w:MediaSides><w:MediaFront><w:ColorProcessing>%s</w:ColorProcessing><w:Resolution><w:Width>%d</w:Width><w:Height>%d</w:Height></w:Resolution>
<w:ScanRegion><w:ScanRegionXOffset>%d</w:ScanRegionXOffset><w:ScanRegionYOffset>%d</w:ScanRegionYOffset><w:ScanRegionWidth>%d</w:ScanRegionWidth><w:ScanRegionHeight>%d</w:ScanRegionHeight></w:ScanRegion>
</w:MediaFront></w:MediaSides></w:DocumentParameters></w:ScanTicket>`, r.X+r.Width, r.Y+r.Height, color, opts.DPI, opts.DPI, r.X, r.Y, r.Width, r.Height), nil
}

func (c *client) cancel(ctx context.Context, id string) error {
	_, data, err := c.post(ctx, "CancelJob", `<w:CancelJobRequest><w:JobId>`+escape(id)+`</w:JobId></w:CancelJobRequest>`)
	if err != nil {
		return err
	}
	_, err = soapResponse(data, "CancelJobResponse")
	return err
}

func (c *client) scan(ctx context.Context, opts options, output string) (result scanResult, err error) {
	if ext := strings.ToLower(filepath.Ext(output)); ext != ".jpg" && ext != ".jpeg" {
		return result, errors.New("输出扩展名必须是 .jpg 或 .jpeg")
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
	scanTicket, err := ticket(cap, opts)
	if err != nil {
		return result, err
	}
	if err = os.MkdirAll(filepath.Dir(output), 0700); err != nil {
		return result, err
	}
	if _, err := os.Lstat(output); err == nil {
		return result, errors.New("输出文件已存在，不会覆盖")
	} else if !errors.Is(err, os.ErrNotExist) {
		return result, err
	}
	file, err := os.CreateTemp(filepath.Dir(output), ".goscanner-*.jpg.part")
	if err != nil {
		return result, fmt.Errorf("不能创建暂存文件：%w", err)
	}
	success, keepStaging := false, false
	jobID := ""
	defer func() {
		_ = file.Close()
		if !keepStaging {
			if removeErr := os.Remove(file.Name()); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
				err = errors.Join(err, fmt.Errorf("删除暂存文件失败：%w", removeErr))
			}
		}
		if !success {
			if jobID != "" {
				// The user's cancelled context must not prevent cleanup of our own job.
				cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				if cancelErr := c.cancel(cleanup, jobID); cancelErr != nil {
					err = errors.Join(err, fmt.Errorf("取消任务未确认：%w", cancelErr))
				}
			}
		}
	}()

	_, data, err := c.post(ctx, "CreateScanJob", "<w:CreateScanJobRequest>"+scanTicket+"</w:CreateScanJobRequest>")
	if err != nil {
		return result, fmt.Errorf("创建任务结果未确认；不要立即重复扫描：%w", err)
	}
	created, err := soapResponse(data, "CreateScanJobResponse")
	if err != nil {
		return result, err
	}
	id := created.path("JobId").text()
	if n, parseErr := strconv.ParseUint(id, 10, 32); parseErr == nil && n > 0 {
		jobID = id
	}
	token := ""
	if node := created.path("JobToken"); node != nil {
		token = node.Text // JobToken is opaque; preserve its whitespace.
	}
	if jobID == "" || strings.TrimSpace(token) == "" {
		return result, errors.New("创建响应缺少有效的 JobId 或 JobToken")
	}
	payload := `<w:RetrieveImageRequest><w:DocumentDescription><w:DocumentName>IMAGE000.JPG</w:DocumentName></w:DocumentDescription><w:JobId>` + escape(jobID) + `</w:JobId><w:JobToken>` + escape(token) + `</w:JobToken></w:RetrieveImageRequest>`
	contentType, data, err := c.post(ctx, "RetrieveImage", payload)
	if err != nil {
		return result, err
	}
	image, err := jpegFromResponse(contentType, data)
	if err != nil {
		return result, err
	}
	config, err := jpeg.DecodeConfig(bytes.NewReader(image))
	if err != nil || config.Width <= 0 || config.Height <= 0 || int64(config.Width)*int64(config.Height) > 40_000_000 {
		return result, errors.New("JPEG 尺寸无效或超过 4000 万像素")
	}
	if _, err = jpeg.Decode(bytes.NewReader(image)); err != nil {
		return result, errors.New("JPEG 无法完整解码")
	}
	if err = ctx.Err(); err != nil {
		return result, err
	}
	if _, err = file.Write(image); err != nil {
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
		return result, fmt.Errorf("发布文件失败；完整图像保留在 %s：%w", file.Name(), err)
	}
	success = true
	return scanResult{Bytes: len(image), Width: config.Width, Height: config.Height}, nil
}
