package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	soapNS      = "http://www.w3.org/2003/05/soap-envelope"
	addrNS      = "http://schemas.xmlsoap.org/ws/2004/08/addressing"
	scanNS      = "http://schemas.microsoft.com/windows/2006/08/wdp/scan"
	xopNS       = "http://www.w3.org/2004/08/xop/include"
	maxResponse = 64 << 20
	maxXML      = 1 << 20
)

type element struct {
	XMLName  xml.Name
	Text     string     `xml:",chardata"`
	Attrs    []xml.Attr `xml:",any,attr"`
	Children []element  `xml:",any"`
}

func (e *element) child(ns, name string) *element {
	if e != nil {
		for i := range e.Children {
			c := &e.Children[i]
			if c.XMLName.Space == ns && c.XMLName.Local == name {
				return c
			}
		}
	}
	return nil
}

func (e *element) path(names ...string) *element {
	for _, name := range names {
		e = e.child(scanNS, name)
	}
	return e
}

func (e *element) text() string {
	if e == nil {
		return ""
	}
	return strings.TrimSpace(e.Text)
}

func (e *element) attr(name string) string {
	if e != nil {
		for _, a := range e.Attrs {
			if a.Name.Local == name {
				return a.Value
			}
		}
	}
	return ""
}

func escape(s string) string {
	var b bytes.Buffer
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

func soapResponse(data []byte, name string) (*element, error) {
	if len(data) > maxXML || bytes.Contains(bytes.ToUpper(data), []byte("<!DOCTYPE")) {
		return nil, errors.New("SOAP XML 超出限制或包含 DOCTYPE")
	}
	var root element
	d := xml.NewDecoder(bytes.NewReader(data))
	if err := d.Decode(&root); err != nil {
		return nil, errors.New("无效的 SOAP XML")
	}
	for {
		token, err := d.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, errors.New("SOAP XML 结尾无效")
		}
		if text, ok := token.(xml.CharData); !ok || len(bytes.TrimSpace(text)) != 0 {
			return nil, errors.New("SOAP XML 包含多余内容")
		}
	}
	if root.XMLName != (xml.Name{Space: soapNS, Local: "Envelope"}) {
		return nil, errors.New("SOAP Envelope 命名空间无效")
	}
	body := root.child(soapNS, "Body")
	if fault := body.child(soapNS, "Fault"); fault != nil {
		reason := fault.child(soapNS, "Reason").child(soapNS, "Text").text()
		code := fault.child(soapNS, "Code").child(soapNS, "Subcode").child(soapNS, "Value").text()
		message := strings.Join(strings.Fields(code+" "+reason), " ")
		if len(message) > 512 {
			message = message[:512]
		}
		return nil, fmt.Errorf("WSD 错误：%s", message)
	}
	response := body.child(scanNS, name)
	if response == nil {
		return nil, fmt.Errorf("缺少 SOAP 响应 %s", name)
	}
	return response, nil
}

type client struct {
	endpoint string
	http     *http.Client
}

func newClient(host string) (*client, error) {
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return nil, errors.New("--host 必须是 IP 地址，不是 URL 或主机名")
	}
	transport := &http.Transport{
		Proxy:                  nil, // Keep LAN scan jobs out of HTTP_PROXY/HTTPS_PROXY.
		DialContext:            (&net.Dialer{Timeout: 5 * time.Second}).DialContext,
		DisableKeepAlives:      true,
		MaxResponseHeaderBytes: 1 << 20,
	}
	endpoint := url.URL{Scheme: "http", Host: net.JoinHostPort(ip.String(), "8018"), Path: "/wsd/scan"}
	return &client{
		endpoint: endpoint.String(),
		http: &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		}},
	}, nil
}

func (c *client) post(ctx context.Context, action, payload string) (string, []byte, error) {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return "", nil, err
	}
	id[6] = (id[6] & 0x0f) | 0x40
	id[8] = (id[8] & 0x3f) | 0x80
	messageID := fmt.Sprintf("urn:uuid:%x-%x-%x-%x-%x", id[:4], id[4:6], id[6:8], id[8:10], id[10:])
	body := fmt.Sprintf(`<?xml version="1.0" encoding="utf-8"?>
<s:Envelope xmlns:s="%s" xmlns:a="%s" xmlns:w="%s">
<s:Header><a:MessageID>%s</a:MessageID><a:To>%s</a:To>
<a:ReplyTo><a:Address>%s/role/anonymous</a:Address></a:ReplyTo>
<a:Action>%s/%s</a:Action></s:Header><s:Body>%s</s:Body></s:Envelope>`,
		soapNS, addrNS, scanNS, messageID, escape(c.endpoint), addrNS, scanNS, action, payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, strings.NewReader(body))
	if err != nil {
		return "", nil, err
	}
	req.Header.Set("Content-Type", "application/soap+xml; charset=utf-8")
	req.Header.Set("User-Agent", "WSDAPI")
	resp, err := c.http.Do(req)
	if err != nil {
		return "", nil, fmt.Errorf("%s 请求失败：%w", action, err)
	}
	defer resp.Body.Close()
	limit := int64(maxXML)
	if action == "RetrieveImage" {
		limit = maxResponse
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return "", nil, fmt.Errorf("%s 读取失败：%w", action, err)
	}
	if int64(len(data)) > limit {
		return "", nil, errors.New("打印机响应超过大小限制")
	}
	if resp.StatusCode != http.StatusOK {
		if _, fault := soapResponse(data, action+"Response"); fault != nil && strings.HasPrefix(fault.Error(), "WSD 错误：") {
			return "", nil, fault
		}
		return "", nil, fmt.Errorf("%s 返回 HTTP %d", action, resp.StatusCode)
	}
	return resp.Header.Get("Content-Type"), data, nil
}

type size struct {
	Width  int `json:"width"`
	Height int `json:"height"`
}

type info struct {
	State      string   `json:"state"`
	Formats    []string `json:"formats"`
	Colors     []string `json:"colors"`
	WidthsDPI  []int    `json:"widths_dpi"`
	HeightsDPI []int    `json:"heights_dpi"`
	MinSize    size     `json:"min_size"`
	MaxSize    size     `json:"max_size"`
	SizeUnit   string   `json:"size_unit"`
}

func positive(node *element) (int, error) {
	n, err := strconv.ParseInt(node.text(), 10, 32)
	if err != nil || n <= 0 {
		return 0, errors.New("扫描能力包含缺失或无效的正整数")
	}
	return int(n), nil
}

func values(node *element, name string) []string {
	var out []string
	if node != nil {
		for i := range node.Children {
			child := &node.Children[i]
			if child.XMLName.Space == scanNS && child.XMLName.Local == name {
				out = append(out, child.text())
			}
		}
	}
	return out
}

func parseInfo(data []byte) (info, error) {
	response, err := soapResponse(data, "GetScannerElementsResponse")
	if err != nil {
		return info{}, err
	}
	var config, status *element
	entries := response.path("ScannerElements")
	if entries != nil {
		for i := range entries.Children {
			e := &entries.Children[i]
			if e.XMLName != (xml.Name{Space: scanNS, Local: "ElementData"}) || e.attr("Valid") != "true" {
				continue
			}
			if v := e.path("ScannerConfiguration"); v != nil {
				config = v
			}
			if v := e.path("ScannerStatus"); v != nil {
				status = v
			}
		}
	}
	platen := config.path("Platen")
	result := info{
		State:    status.path("ScannerState").text(),
		Formats:  values(config.path("DeviceSettings", "FormatsSupported"), "FormatValue"),
		Colors:   values(platen.path("PlatenColor"), "ColorEntry"),
		SizeUnit: "1/1000 inch",
	}
	if platen == nil || result.State == "" || len(result.Formats) == 0 || len(result.Colors) == 0 {
		return info{}, errors.New("扫描能力或状态不完整，或设备没有平板扫描器")
	}
	for _, axis := range []struct {
		container, name string
		dst             *[]int
	}{
		{"Widths", "Width", &result.WidthsDPI}, {"Heights", "Height", &result.HeightsDPI},
	} {
		for _, value := range values(platen.path("PlatenResolutions", axis.container), axis.name) {
			n, err := positive(&element{Text: value})
			if err != nil {
				return info{}, err
			}
			*axis.dst = append(*axis.dst, n)
		}
		if len(*axis.dst) == 0 {
			return info{}, errors.New("扫描能力缺少分辨率")
		}
	}
	for _, field := range []struct {
		path []string
		dst  *int
	}{
		{[]string{"PlatenMinimumSize", "Width"}, &result.MinSize.Width},
		{[]string{"PlatenMinimumSize", "Height"}, &result.MinSize.Height},
		{[]string{"PlatenMaximumSize", "Width"}, &result.MaxSize.Width},
		{[]string{"PlatenMaximumSize", "Height"}, &result.MaxSize.Height},
	} {
		n, err := positive(platen.path(field.path...))
		if err != nil {
			return info{}, err
		}
		*field.dst = n
	}
	if result.MinSize.Width > result.MaxSize.Width || result.MinSize.Height > result.MaxSize.Height {
		return info{}, errors.New("扫描尺寸范围无效")
	}
	return result, nil
}

func (c *client) info(ctx context.Context) (info, error) {
	_, data, err := c.post(ctx, "GetScannerElements", `<w:GetScannerElementsRequest><w:RequestedElements><w:Name>w:ScannerConfiguration</w:Name><w:Name>w:ScannerStatus</w:Name></w:RequestedElements></w:GetScannerElementsRequest>`)
	if err != nil {
		return info{}, err
	}
	return parseInfo(data)
}
