package main

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"path"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
)

// checkedESCLEndpoint accepts only direct, credential-free connections to a unicast IP.
func checkedESCLEndpoint(raw string) (string, error) {
	if len(raw) > 4096 {
		return "", diagnosticf("The eSCL address exceeds the length limit.", "eSCL 地址过长")
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.Opaque != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.RawPath != "" || strings.ContainsAny(u.Path, "\\%") {
		return "", diagnosticf("Invalid eSCL address.", "无效的 eSCL 地址")
	}
	ip, err := netip.ParseAddr(u.Hostname())
	if err != nil || ip.Zone() != "" || ip.IsUnspecified() || ip.IsMulticast() {
		return "", diagnosticf("The eSCL address must use a valid unicast IP address.", "eSCL 地址必须使用有效的单播 IP")
	}
	port := u.Port()
	if port == "" {
		if u.Scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return "", diagnosticf("Invalid eSCL address port.", "eSCL 地址端口无效")
	}
	if u.Path == "" {
		u.Path = "/"
	}
	if !strings.HasPrefix(u.Path, "/") || path.Clean(u.Path) != strings.TrimSuffix(u.Path, "/") && u.Path != "/" {
		return "", diagnosticf("Invalid eSCL address path.", "eSCL 地址路径无效")
	}
	u.Host = net.JoinHostPort(ip.Unmap().String(), strconv.Itoa(n))
	return u.String(), nil
}

// checkedESCLJobLocation prevents a device from directing job requests outside its ScanJobs collection.
func checkedESCLJobLocation(endpoint, location string) (string, error) {
	base, err := checkedESCLEndpoint(endpoint)
	if err != nil || len(location) == 0 || len(location) > 4096 {
		return "", diagnosticf("Invalid eSCL job address.", "无效的 eSCL 任务地址")
	}
	root, _ := url.Parse(base)
	root.Path = strings.TrimRight(root.Path, "/") + "/ScanJobs/"
	ref, err := url.Parse(location)
	if err != nil || ref.User != nil || ref.RawQuery != "" || ref.ForceQuery || ref.Fragment != "" || ref.Opaque != "" || ref.RawPath != "" || strings.ContainsAny(ref.Path, "\\%") {
		return "", diagnosticf("Invalid eSCL job address.", "无效的 eSCL 任务地址")
	}
	if ref.Path != "" && path.Clean(ref.Path) != strings.TrimSuffix(ref.Path, "/") {
		return "", diagnosticf("Invalid eSCL job path.", "eSCL 任务路径无效")
	}
	for _, segment := range strings.Split(ref.Path, "/") {
		if segment == "." || segment == ".." {
			return "", diagnosticf("The eSCL job path contains relative directory components.", "eSCL 任务路径包含相对目录")
		}
	}
	resolved := root.ResolveReference(ref)
	checked, err := checkedESCLEndpoint(resolved.String())
	if err != nil {
		return "", err
	}
	job, _ := url.Parse(checked)
	if job.Scheme != root.Scheme || job.Host != root.Host || !strings.HasPrefix(job.Path, root.Path) {
		return "", diagnosticf("Refusing a Location outside the scanner address or scan job directory.", "拒绝跨地址或扫描任务目录的 Location")
	}
	id := strings.TrimPrefix(job.Path, root.Path)
	id = strings.TrimSuffix(id, "/")
	if id == "" || id == "." || id == ".." || strings.Contains(id, "/") {
		return "", diagnosticf("Invalid eSCL job ID.", "无效的 eSCL 任务编号")
	}
	return checked, nil
}

const (
	esclNS           = "http://schemas.hp.com/imaging/escl/2011/05/03"
	pwgNS            = "http://www.pwg.org/schemas/2010/12/sm"
	formatPNG        = "png"
	esclDocumentWait = 60 * time.Second
)

type esclResolutionRange struct{ min, max, step int }

func (r esclResolutionRange) contains(n int) bool {
	return n >= r.min && n <= r.max && (n-r.min)%r.step == 0
}

type esclResolution struct {
	x, y  esclResolutionRange
	color string
}

func (r esclResolution) supports(dpi int) bool {
	return r.x.contains(dpi) && r.y.contains(dpi)
}

type esclProfile struct {
	colors, formats []string
	resolutions     []esclResolution
	formatsExt      []string
}

func (p esclProfile) supports(color, format string, dpi int) bool {
	if !slices.Contains(p.colors, color) || !slices.Contains(p.formats, format) {
		return false
	}
	hasColorResolution := false
	for _, r := range p.resolutions {
		hasColorResolution = hasColorResolution || r.color == color
	}
	for _, r := range p.resolutions {
		if (r.color == color || r.color == "" && !hasColorResolution) && r.supports(dpi) {
			return true
		}
	}
	return false
}

type esclCapabilities struct {
	min, max  size // Native 1/300 inch units; info uses 1/1000 inch.
	maxOffset size
	profiles  []esclProfile
}

func esclInteger(node *element, allowZero bool) (int, error) {
	n, err := strconv.ParseInt(node.text(), 10, 32)
	if err != nil || n < 0 || n == 0 && !allowZero {
		return 0, diagnosticf("eSCL capabilities contain an invalid integer.", "eSCL 能力包含无效整数")
	}
	return int(n), nil
}

func parseESCLRange(node *element) (esclResolutionRange, error) {
	var r esclResolutionRange
	var err error
	r.min, err = esclInteger(node.child(esclNS, "Min"), false)
	if err != nil {
		return r, err
	}
	r.max, err = esclInteger(node.child(esclNS, "Max"), false)
	if err != nil || r.max < r.min {
		return r, diagnosticf("Invalid eSCL resolution range.", "eSCL 分辨率范围无效")
	}
	r.step = 1
	if step := node.child(esclNS, "Step"); step != nil {
		r.step, err = esclInteger(step, true)
		if err != nil {
			return r, err
		}
		if r.step == 0 {
			r.step = 1
		}
	}
	return r, nil
}

func esclFormat(mime string) string {
	switch mime {
	case "image/jpeg":
		return formatJPEG
	case "image/png":
		return formatPNG
	case "image/tiff":
		return formatTIFF
	}
	return ""
}

func esclMIME(format string) string {
	switch format {
	case formatJPEG:
		return "image/jpeg"
	case formatPNG:
		return "image/png"
	case formatTIFF:
		return "image/tiff"
	}
	return ""
}

func parseESCLProfile(node *element) (esclProfile, error) {
	var p esclProfile
	if colors := node.child(esclNS, "ColorModes"); colors != nil {
		for _, c := range colors.Children {
			if c.XMLName == (xml.Name{Space: esclNS, Local: "ColorMode"}) && (c.text() == "RGB24" || c.text() == "Grayscale8") {
				p.colors = append(p.colors, c.text())
			}
		}
	}
	if formats := node.child(esclNS, "DocumentFormats"); formats != nil {
		for _, f := range formats.Children {
			if f.XMLName == (xml.Name{Space: pwgNS, Local: "DocumentFormat"}) || f.XMLName == (xml.Name{Space: esclNS, Local: "DocumentFormatExt"}) {
				if format := esclFormat(f.text()); format != "" {
					p.formats = append(p.formats, format)
					if f.XMLName.Space == esclNS {
						p.formatsExt = append(p.formatsExt, format)
					}
				}
			}
		}
	}
	for _, supported := range node.Children {
		if supported.XMLName != (xml.Name{Space: esclNS, Local: "SupportedResolutions"}) {
			continue
		}
		color := supported.child(esclNS, "ColorMode").text()
		for _, res := range supported.Children {
			switch res.XMLName {
			case xml.Name{Space: esclNS, Local: "DiscreteResolutions"}:
				for _, discrete := range res.Children {
					if discrete.XMLName != (xml.Name{Space: esclNS, Local: "DiscreteResolution"}) {
						continue
					}
					x, err := esclInteger(discrete.child(esclNS, "XResolution"), false)
					if err != nil {
						return p, err
					}
					y, err := esclInteger(discrete.child(esclNS, "YResolution"), false)
					if err != nil {
						return p, err
					}
					p.resolutions = append(p.resolutions, esclResolution{esclResolutionRange{x, x, 1}, esclResolutionRange{y, y, 1}, color})
				}
			case xml.Name{Space: esclNS, Local: "ResolutionRange"}:
				x, err := parseESCLRange(res.child(esclNS, "XResolution"))
				if err != nil {
					return p, err
				}
				y, err := parseESCLRange(res.child(esclNS, "YResolution"))
				if err != nil {
					return p, err
				}
				p.resolutions = append(p.resolutions, esclResolution{x, y, color})
			}
		}
	}
	return p, nil
}

func parseESCLCapabilities(data []byte) (info, error) {
	root, err := parseXML(data)
	if err != nil {
		return info{}, err
	}
	if root.XMLName != (xml.Name{Space: esclNS, Local: "ScannerCapabilities"}) {
		return info{}, diagnosticf("Invalid eSCL ScannerCapabilities.", "无效的 eSCL ScannerCapabilities")
	}
	platen := root.child(esclNS, "Platen").child(esclNS, "PlatenInputCaps")
	if platen == nil {
		return info{}, diagnosticf("The eSCL scanner does not declare flatbed capabilities.", "eSCL 设备未声明平板能力")
	}
	cap := &esclCapabilities{}
	for _, field := range []struct {
		name string
		dst  *int
		zero bool
	}{
		{"MinWidth", &cap.min.Width, true}, {"MinHeight", &cap.min.Height, true},
		{"MaxWidth", &cap.max.Width, false}, {"MaxHeight", &cap.max.Height, false},
	} {
		*field.dst, err = esclInteger(platen.child(esclNS, field.name), field.zero)
		if err != nil {
			return info{}, err
		}
	}
	if cap.min.Width > cap.max.Width || cap.min.Height > cap.max.Height {
		return info{}, diagnosticf("Invalid eSCL flatbed dimensions.", "eSCL 平板尺寸无效")
	}
	cap.maxOffset = cap.max
	for _, field := range []struct {
		name string
		dst  *int
	}{{"MaxXOffset", &cap.maxOffset.Width}, {"MaxYOffset", &cap.maxOffset.Height}} {
		if node := platen.child(esclNS, field.name); node != nil {
			*field.dst, err = esclInteger(node, true)
			if err != nil {
				return info{}, err
			}
		}
	}
	profiles := platen.child(esclNS, "SettingProfiles")
	if profiles == nil {
		return info{}, diagnosticf("The eSCL flatbed has no SettingProfiles.", "eSCL 平板缺少 SettingProfiles")
	}
	result := info{escl: cap, SizeUnit: "1/1000 inch", UUID: root.child(esclNS, "UUID").text(), SerialNumber: root.child(pwgNS, "SerialNumber").text(), MakeAndModel: root.child(pwgNS, "MakeAndModel").text()}
	result.MinSize = size{int((int64(cap.min.Width)*10 + 2) / 3), int((int64(cap.min.Height)*10 + 2) / 3)}
	result.MaxSize = size{int(int64(cap.max.Width) * 10 / 3), int(int64(cap.max.Height) * 10 / 3)}
	for _, node := range profiles.Children {
		if node.XMLName != (xml.Name{Space: esclNS, Local: "SettingProfile"}) {
			continue
		}
		profile, err := parseESCLProfile(&node)
		if err != nil {
			return info{}, err
		}
		if len(profile.colors) == 0 || len(profile.formats) == 0 || len(profile.resolutions) == 0 {
			continue
		}
		cap.profiles = append(cap.profiles, profile)
		result.Colors = append(result.Colors, profile.colors...)
		result.Formats = append(result.Formats, profile.formats...)
		for _, res := range profile.resolutions {
			// shortcut: large range summaries show endpoints and common DPI; expose ranges if a future UI needs every value.
			candidates := []int{res.x.min, res.x.max, res.y.min, res.y.max, 75, 100, 150, 200, 300, 600, 1200, 2400}
			if (res.x.max-res.x.min)/res.x.step <= 512 {
				for dpi := int64(res.x.min); dpi <= int64(res.x.max); dpi += int64(res.x.step) {
					candidates = append(candidates, int(dpi))
				}
			}
			for _, dpi := range candidates {
				if res.supports(dpi) {
					result.WidthsDPI = append(result.WidthsDPI, dpi)
				}
			}
		}
	}
	if len(cap.profiles) == 0 {
		return info{}, diagnosticf("The eSCL flatbed has no supported parameter combinations.", "eSCL 平板没有支持的参数组合")
	}
	sort.Strings(result.Colors)
	result.Colors = slices.Compact(result.Colors)
	sort.Strings(result.Formats)
	result.Formats = slices.Compact(result.Formats)
	sort.Ints(result.WidthsDPI)
	result.WidthsDPI = slices.Compact(result.WidthsDPI)
	result.HeightsDPI = slices.Clone(result.WidthsDPI)
	return result, nil
}

func (c *client) esclRequest(ctx context.Context, method, target, body string, limit int64) (int, http.Header, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, method, target, strings.NewReader(body))
	if err != nil {
		return 0, nil, nil, err
	}
	if body != "" {
		req.Header.Set("Content-Type", "text/xml; charset=utf-8")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, nil, nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return resp.StatusCode, resp.Header, nil, err
	}
	if int64(len(data)) > limit {
		return resp.StatusCode, resp.Header, nil, diagnosticf("The eSCL response exceeds the size limit.", "eSCL 响应超过大小限制")
	}
	return resp.StatusCode, resp.Header, data, nil
}

func (c *client) esclInfo(ctx context.Context) (info, error) {
	base := strings.TrimRight(c.endpoint, "/")
	status, _, data, err := c.esclRequest(ctx, http.MethodGet, base+"/ScannerCapabilities", "", maxXML)
	if err != nil {
		return info{}, err
	}
	if status != http.StatusOK {
		return info{}, diagnosticf("ScannerCapabilities returned HTTP %d.", "ScannerCapabilities 返回 HTTP %d", status)
	}
	cap, err := parseESCLCapabilities(data)
	if err != nil {
		return info{}, err
	}
	status, _, data, err = c.esclRequest(ctx, http.MethodGet, base+"/ScannerStatus", "", maxXML)
	if err != nil {
		return info{}, err
	}
	if status != http.StatusOK {
		return info{}, diagnosticf("ScannerStatus returned HTTP %d.", "ScannerStatus 返回 HTTP %d", status)
	}
	root, err := parseXML(data)
	if err != nil {
		return info{}, err
	}
	if root.XMLName != (xml.Name{Space: esclNS, Local: "ScannerStatus"}) {
		return info{}, diagnosticf("Invalid eSCL ScannerStatus.", "无效的 eSCL ScannerStatus")
	}
	cap.State = root.child(pwgNS, "State").text()
	if cap.State == "" {
		return info{}, diagnosticf("eSCL ScannerStatus has no State.", "eSCL ScannerStatus 缺少 State")
	}
	return cap, nil
}

func esclTicket(cap info, opts options) (string, error) {
	if cap.State != "Idle" {
		return "", diagnosticf("The scanner is not ready. Device state: %q", "扫描器尚未就绪：%s", cap.State)
	}
	color := map[string]string{"color": "RGB24", "gray": "Grayscale8"}[opts.Mode]
	format := opts.Format
	if format == "" {
		format = formatJPEG
	}
	var profile *esclProfile
	for i := range cap.escl.profiles {
		if cap.escl.profiles[i].supports(color, format, opts.DPI) {
			profile = &cap.escl.profiles[i]
			break
		}
	}
	if profile == nil {
		return "", diagnosticf("eSCL does not support this combination of color mode, format, and DPI.", "eSCL 不支持此色彩、格式和 DPI 参数组合")
	}
	r := opts.Area
	if r == (region{}) {
		r = region{Width: cap.escl.max.Width, Height: cap.escl.max.Height}
	} else {
		for _, n := range []int{r.X, r.Y, r.Width, r.Height} {
			if n < 0 || int64(n) > math.MaxInt32 {
				return "", diagnosticf("Invalid scan area.", "扫描区域无效")
			}
		}
		// Round once in native units, then check native bounds (A4 rounds to 2480 x 3508).
		r = region{int((int64(r.X)*3 + 5) / 10), int((int64(r.Y)*3 + 5) / 10), int((int64(r.Width)*3 + 5) / 10), int((int64(r.Height)*3 + 5) / 10)}
	}
	if r.Width <= 0 || r.Height <= 0 || r.Width < cap.escl.min.Width || r.Height < cap.escl.min.Height || r.X > cap.escl.maxOffset.Width || r.Y > cap.escl.maxOffset.Height || int64(r.X)+int64(r.Width) > int64(cap.escl.max.Width) || int64(r.Y)+int64(r.Height) > int64(cap.escl.max.Height) {
		return "", diagnosticf("The scan area exceeds the eSCL flatbed bounds.", "扫描区域超出 eSCL 平板范围")
	}
	ext := ""
	if slices.Contains(profile.formatsExt, format) {
		ext = "<scan:DocumentFormatExt>" + esclMIME(format) + "</scan:DocumentFormatExt>"
	}
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?><scan:ScanSettings xmlns:scan="%s" xmlns:pwg="%s"><pwg:Version>2.0</pwg:Version><pwg:ScanRegions><pwg:ScanRegion><pwg:ContentRegionUnits>escl:ThreeHundredthsOfInches</pwg:ContentRegionUnits><pwg:XOffset>%d</pwg:XOffset><pwg:YOffset>%d</pwg:YOffset><pwg:Width>%d</pwg:Width><pwg:Height>%d</pwg:Height></pwg:ScanRegion></pwg:ScanRegions><pwg:InputSource>Platen</pwg:InputSource><scan:ColorMode>%s</scan:ColorMode><pwg:DocumentFormat>%s</pwg:DocumentFormat>%s<scan:XResolution>%d</scan:XResolution><scan:YResolution>%d</scan:YResolution></scan:ScanSettings>`, esclNS, pwgNS, r.X, r.Y, r.Width, r.Height, color, esclMIME(format), ext, opts.DPI, opts.DPI), nil
}

func (c *client) esclCreate(ctx context.Context, ticket string) (string, error) {
	status, headers, _, requestErr := c.esclRequest(ctx, http.MethodPost, strings.TrimRight(c.endpoint, "/")+"/ScanJobs", ticket, maxXML)
	if status != http.StatusCreated {
		if requestErr != nil {
			return "", requestErr
		}
		return "", diagnosticf("ScanJobs returned HTTP %d.", "ScanJobs 返回 HTTP %d", status)
	}
	if len(headers.Values("Location")) != 1 {
		return "", diagnosticf("The eSCL job creation response must contain exactly one Location.", "eSCL 创建响应必须包含唯一 Location")
	}
	job, err := checkedESCLJobLocation(c.endpoint, headers.Get("Location"))
	if err != nil {
		return "", err
	}
	// Keep the safe job URL for cancellation even if the response body was truncated.
	return job, requestErr
}

func (c *client) esclRetrieve(ctx context.Context, id string) ([]byte, error) {
	job, err := checkedESCLJobLocation(c.endpoint, id)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, esclDocumentWait)
	defer cancel()
	for {
		status, headers, data, err := c.esclRequest(ctx, http.MethodGet, strings.TrimRight(job, "/")+"/NextDocument", "", maxResponse)
		if err != nil {
			return nil, err
		}
		if status == http.StatusOK {
			return data, nil
		}
		if status != http.StatusServiceUnavailable {
			return nil, diagnosticf("NextDocument returned HTTP %d.", "NextDocument 返回 HTTP %d", status)
		}
		delay := 250 * time.Millisecond
		if seconds, err := strconv.Atoi(headers.Get("Retry-After")); err == nil && seconds > 0 {
			delay = time.Duration(min(seconds, 2)) * time.Second
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

func (c *client) esclCancel(ctx context.Context, id string) error {
	job, err := checkedESCLJobLocation(c.endpoint, id)
	if err != nil {
		return err
	}
	status, _, _, err := c.esclRequest(ctx, http.MethodDelete, job, "", maxXML)
	if err != nil {
		return err
	}
	switch status {
	case http.StatusOK, http.StatusAccepted, http.StatusNoContent, http.StatusNotFound, http.StatusGone:
		return nil
	}
	return diagnosticf("Cancelling the eSCL job returned HTTP %d.", "取消 eSCL 任务返回 HTTP %d", status)
}
