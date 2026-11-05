package main

import (
	"bytes"
	"encoding/base64"
	"io"
	"mime"
	"mime/multipart"
	"net/url"
	"regexp"
	"strings"
)

var hpDelimiter = regexp.MustCompile(`--_DPWS_v1\.0_MimeBoundary_WSD(?:--)?(?:\r?\n|$)`)

func repairHPFraming(data []byte) []byte {
	var out bytes.Buffer
	last := 0
	for _, match := range hpDelimiter.FindAllIndex(data, -1) {
		start := match[0]
		if start > 0 && data[start-1] != '\n' {
			out.Write(data[last:start])
			out.WriteString("\r\n")
			last = start
		}
	}
	if last == 0 {
		return data
	}
	out.Write(data[last:])
	return out.Bytes()
}

type mimePart struct {
	id   string
	data []byte
}

func imageFromResponse(contentType string, data []byte, format string) ([]byte, error) {
	kind, params, err := mime.ParseMediaType(contentType)
	if err != nil {
		return nil, diagnosticf("Invalid response Content-Type.", "无效的响应 Content-Type")
	}
	if kind != "multipart/related" {
		if _, err := soapResponse(data, "RetrieveImageResponse"); err != nil {
			return nil, err
		}
		return nil, diagnosticf("The image response is not multipart/related.", "图像响应不是 multipart/related")
	}
	boundary := params["boundary"]
	if boundary == "" || len(boundary) > 70 {
		return nil, diagnosticf("The MIME boundary is missing or invalid.", "MIME 边界缺失或无效")
	}
	if boundary == "_DPWS_v1.0_MimeBoundary_WSD" {
		// This HP firmware omits CRLF immediately before MIME delimiters.
		data = repairHPFraming(data)
	}
	reader := multipart.NewReader(bytes.NewReader(data), boundary)
	var parts []mimePart
	seen := make(map[string]bool)
	for {
		part, err := reader.NextRawPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, diagnosticf("The MIME response is incomplete or malformed.", "MIME 响应不完整或格式异常")
		}
		if len(parts) >= 8 {
			return nil, diagnosticf("The MIME part count exceeds the limit.", "MIME 部件数量超过限制")
		}
		payload, err := io.ReadAll(io.LimitReader(part, maxResponse+1))
		if err != nil || len(payload) > maxResponse {
			return nil, diagnosticf("A MIME part is incomplete or too large.", "MIME 部件不完整或过大")
		}
		switch strings.ToLower(strings.TrimSpace(part.Header.Get("Content-Transfer-Encoding"))) {
		case "", "binary", "8bit", "7bit":
		case "base64":
			payload, err = io.ReadAll(base64.NewDecoder(base64.StdEncoding, bytes.NewReader(payload)))
			if err != nil {
				return nil, diagnosticf("Invalid base64 MIME part.", "无效的 base64 MIME 部件")
			}
		default:
			return nil, diagnosticf("Unsupported MIME transfer encoding.", "不支持的 MIME 传输编码")
		}
		id := strings.Trim(part.Header.Get("Content-ID"), "<>")
		if id != "" && seen[id] {
			return nil, diagnosticf("Duplicate MIME Content-ID.", "MIME Content-ID 重复")
		}
		seen[id] = true
		parts = append(parts, mimePart{id: id, data: payload})
	}
	if len(parts) == 0 {
		return nil, diagnosticf("The MIME response contains no parts.", "MIME 响应没有部件")
	}
	root := &parts[0]
	if start := params["start"]; start != "" {
		root = nil
		for i := range parts {
			if parts[i].id == strings.Trim(start, "<>") {
				root = &parts[i]
			}
		}
		if root == nil {
			return nil, diagnosticf("The MTOM root part does not exist.", "MTOM 根部件不存在")
		}
	}
	response, err := soapResponse(root.data, "RetrieveImageResponse")
	if err != nil {
		return nil, err
	}
	var includes []*element
	var visit func(*element)
	visit = func(e *element) {
		if e.XMLName.Space == xopNS && e.XMLName.Local == "Include" {
			includes = append(includes, e)
		}
		for i := range e.Children {
			visit(&e.Children[i])
		}
	}
	visit(response)
	if len(includes) != 1 {
		return nil, diagnosticf("Expected exactly one XOP image reference.", "预期唯一的 XOP 图像引用")
	}
	href := includes[0].attr("href")
	if !strings.HasPrefix(href, "cid:") {
		return nil, diagnosticf("The XOP image reference is not a cid reference. External URLs will not be accessed.", "XOP 图像引用不是 cid；不会访问外部 URL")
	}
	id, err := url.PathUnescape(strings.TrimPrefix(href, "cid:"))
	if err != nil || id == "" {
		return nil, diagnosticf("Invalid XOP cid reference.", "无效的 XOP cid 引用")
	}
	for _, part := range parts {
		if part.id == id {
			switch format {
			case formatJPEG:
				if !bytes.HasPrefix(part.data, []byte{0xff, 0xd8, 0xff}) || !bytes.HasSuffix(part.data, []byte{0xff, 0xd9}) {
					return nil, diagnosticf("The attachment is not a complete JPEG image.", "附件不是完整的 JPEG")
				}
			case formatTIFF:
				if !bytes.HasPrefix(part.data, []byte{'I', 'I', 42, 0}) && !bytes.HasPrefix(part.data, []byte{'M', 'M', 0, 42}) {
					return nil, diagnosticf("The attachment is not a TIFF image.", "附件不是 TIFF")
				}
			default:
				return nil, diagnosticf("Unsupported image format.", "不支持的图像格式")
			}
			return part.data, nil
		}
	}
	return nil, diagnosticf("The image part referenced by XOP was not found.", "没有找到 XOP 引用的图像部件")
}
