package main

import (
	"bytes"
	"encoding/base64"
	"errors"
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

func jpegFromResponse(contentType string, data []byte) ([]byte, error) {
	kind, params, err := mime.ParseMediaType(contentType)
	if err != nil {
		return nil, errors.New("无效的响应 Content-Type")
	}
	if kind != "multipart/related" {
		if _, err := soapResponse(data, "RetrieveImageResponse"); err != nil {
			return nil, err
		}
		return nil, errors.New("图像响应不是 multipart/related")
	}
	boundary := params["boundary"]
	if boundary == "" || len(boundary) > 70 {
		return nil, errors.New("MIME 边界缺失或无效")
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
			return nil, errors.New("MIME 响应不完整或格式异常")
		}
		if len(parts) >= 8 {
			return nil, errors.New("MIME 部件数量超过限制")
		}
		payload, err := io.ReadAll(io.LimitReader(part, maxResponse+1))
		if err != nil || len(payload) > maxResponse {
			return nil, errors.New("MIME 部件不完整或过大")
		}
		switch strings.ToLower(strings.TrimSpace(part.Header.Get("Content-Transfer-Encoding"))) {
		case "", "binary", "8bit", "7bit":
		case "base64":
			payload, err = io.ReadAll(base64.NewDecoder(base64.StdEncoding, bytes.NewReader(payload)))
			if err != nil {
				return nil, errors.New("无效的 base64 MIME 部件")
			}
		default:
			return nil, errors.New("不支持的 MIME 传输编码")
		}
		id := strings.Trim(part.Header.Get("Content-ID"), "<>")
		if id != "" && seen[id] {
			return nil, errors.New("MIME Content-ID 重复")
		}
		seen[id] = true
		parts = append(parts, mimePart{id: id, data: payload})
	}
	if len(parts) == 0 {
		return nil, errors.New("MIME 响应没有部件")
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
			return nil, errors.New("MTOM 根部件不存在")
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
		return nil, errors.New("预期唯一的 XOP 图像引用")
	}
	href := includes[0].attr("href")
	if !strings.HasPrefix(href, "cid:") {
		return nil, errors.New("XOP 图像引用不是 cid；不会访问外部 URL")
	}
	id, err := url.PathUnescape(strings.TrimPrefix(href, "cid:"))
	if err != nil || id == "" {
		return nil, errors.New("无效的 XOP cid 引用")
	}
	for _, part := range parts {
		if part.id == id {
			if !bytes.HasPrefix(part.data, []byte{0xff, 0xd8, 0xff}) || !bytes.HasSuffix(part.data, []byte{0xff, 0xd9}) {
				return nil, errors.New("附件不是完整的 JPEG")
			}
			return part.data, nil
		}
	}
	return nil, errors.New("没有找到 XOP 引用的图像部件")
}
