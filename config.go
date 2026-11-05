package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const defaultScanTimeout = 2 * time.Minute

type config struct {
	Device deviceConfig `json:"device"`
	Scan   struct {
		DPI     int    `json:"dpi"`
		Mode    string `json:"mode"`
		Area    string `json:"area"`
		Timeout string `json:"timeout"`
	} `json:"scan"`
	Output struct {
		Directory string `json:"directory"`
		Format    string `json:"format"`
	} `json:"output"`
	Processing struct {
		Deskew   bool `json:"deskew"`
		Autocrop bool `json:"autocrop"`
	} `json:"processing"`
}

func defaultConfig() config {
	var c config
	c.Device.Port, c.Device.Path = 8018, "/wsd/scan"
	c.Scan.DPI, c.Scan.Mode, c.Scan.Area = 150, "color", "a4"
	c.Scan.Timeout = defaultScanTimeout.String()
	c.Output.Directory, c.Output.Format = ".", "jpg"
	return c
}

func configPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", diagnosticf("Cannot locate the configuration file: %w", "定位配置文件：%w", err)
	}
	return filepath.Join(home, ".config", "goscanner", "config.json"), nil
}

func loadConfig() (config, error) {
	path, err := configPath()
	if err != nil {
		return defaultConfig(), err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return defaultConfig(), nil
	}
	if err != nil {
		return defaultConfig(), diagnosticf("Cannot read the configuration file %q: %w", "读取配置文件 %s：%w", path, err)
	}
	return decodeConfig(data, path)
}

func decodeConfig(data []byte, path string) (config, error) {
	c := defaultConfig()
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	parsed := &c
	err := decoder.Decode(&parsed)
	if err == nil && parsed == nil {
		err = diagnosticf("The top-level value must be a JSON object.", "顶层必须是 JSON 对象")
	}
	if err == nil {
		var extra any
		if trailing := decoder.Decode(&extra); trailing != io.EOF {
			err = diagnosticf("The configuration file must contain exactly one JSON object.", "配置文件只能包含一个 JSON 对象")
		}
	}
	if err != nil {
		return c, diagnosticf("Cannot parse the configuration file %q: %w", "解析配置文件 %s：%w", path, err)
	}
	var raw struct {
		Device map[string]json.RawMessage `json:"device"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return c, err
	}
	if value, present := raw.Device["record_id"]; present {
		var id string
		if err := json.Unmarshal(value, &id); err != nil || !validRecordID(id) {
			return c, diagnosticf("Invalid configuration device.record_id.", "配置 device.record_id 格式无效")
		}
		if len(raw.Device) != 1 {
			return c, diagnosticf("device.record_id must not be combined with legacy address fields.", "device.record_id 不能与旧地址字段混用")
		}
		c.Device = deviceConfig{RecordID: id}
		return c, nil
	}
	if protocolName(c.Device.Protocol) == "escl" {
		var fields struct {
			Device map[string]json.RawMessage `json:"device"`
		}
		if err := json.Unmarshal(data, &fields); err != nil {
			return c, err
		}
		if _, ok := fields.Device["port"]; !ok {
			c.Device.Port = 80
			if c.Device.Scheme == "https" {
				c.Device.Port = 443
			}
		}
		if _, ok := fields.Device["path"]; !ok {
			c.Device.Path = "/eSCL"
		}
	}
	return c, nil
}

func saveDefaultDevice(device deviceConfig) error {
	if device.RecordID != "" {
		if !validRecordID(device.RecordID) || device.Host != "" || device.Port != 0 || device.Path != "" || device.Protocol != "" || device.Scheme != "" {
			return diagnosticf("The default scanner reference is invalid or combined with address fields.", "默认设备引用无效或与地址混用")
		}
		devices, err := loadDrives()
		if err != nil {
			return err
		}
		if _, err := lookupRecord(devices, device.RecordID); err != nil {
			return err
		}
	} else if _, err := device.endpoint(); err != nil {
		return err
	}
	if protocolName(device.Protocol) == "wsd" && (device.Scheme == "" || device.Scheme == "http") {
		device.Protocol = ""
		device.Scheme = ""
	}
	path, err := configPath()
	if err != nil {
		return err
	}
	if info, err := os.Lstat(path); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return diagnosticf("The configuration file is a symbolic link. Set device manually. The link will not be automatically replaced.", "配置文件是符号链接；请手动设置 device，拒绝自动替换链接")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	// 选择设备可能耗时；重新读取配置，仅更新 device，保留期间修改的其他设置。
	data, err := os.ReadFile(path)
	fields := make(map[string]json.RawMessage)
	if err == nil {
		if _, err := decodeConfig(data, path); err != nil {
			return err
		}
		if err := json.Unmarshal(data, &fields); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	fields["device"], err = json.Marshal(device)
	if err != nil {
		return err
	}
	data, err = json.MarshalIndent(fields, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".config-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	defer file.Close()
	if _, err := file.Write(append(data, '\n')); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), path)
}

func (c config) apply(flags *flag.FlagSet, command string) error {
	explicit := make(map[string]bool)
	flags.Visit(func(f *flag.Flag) { explicit[f.Name] = true })
	type setting struct{ name, key, value string }
	values := []setting{}
	if command != "discover" && !explicit["endpoint"] && c.Device.RecordID == "" {
		values = append(values,
			setting{"host", "device.host", c.Device.Host},
			setting{"protocol", "device.protocol", c.Device.Protocol},
			setting{"scheme", "device.scheme", c.Device.Scheme},
			setting{"port", "device.port", strconv.Itoa(c.Device.Port)},
			setting{"path", "device.path", c.Device.Path},
		)
	}
	if command == "scan" {
		values = append(values,
			setting{"dpi", "scan.dpi", strconv.Itoa(c.Scan.DPI)},
			setting{"mode", "scan.mode", c.Scan.Mode},
			setting{"area", "scan.area", c.Scan.Area},
			setting{"timeout", "scan.timeout", c.Scan.Timeout},
			setting{"deskew", "processing.deskew", strconv.FormatBool(c.Processing.Deskew)},
			setting{"autocrop", "processing.autocrop", strconv.FormatBool(c.Processing.Autocrop)},
		)
	}
	for _, v := range values {
		if !explicit[v.name] {
			if err := flags.Set(v.name, v.value); err != nil {
				return diagnosticf("Invalid configuration %s: %w", "配置 %s：%w", v.key, err)
			}
		}
	}
	return nil
}

func (c config) outputPath() (string, error) {
	switch c.Output.Format {
	case "jpg", "tiff", "png", "pdf":
	default:
		return "", diagnosticf("Configuration output.format must be jpg, tiff, png, or pdf.", "配置 output.format 必须是 jpg、tiff、png 或 pdf")
	}
	dir := c.Output.Directory
	if dir == "" {
		return "", diagnosticf("Configuration output.directory must not be empty. Use . for the current working directory.", "配置 output.directory 不能为空；使用 . 表示当前工作目录")
	}
	if dir == "~" || strings.HasPrefix(dir, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", diagnosticf("Cannot expand output.directory: %w", "展开 output.directory：%w", err)
		}
		dir = filepath.Join(home, strings.TrimPrefix(dir, "~"))
	} else if strings.HasPrefix(dir, "~") {
		return "", diagnosticf("Configuration output.directory does not support ~username. Use an absolute path or ~/.", "配置 output.directory 不支持 ~用户名；请使用绝对路径或 ~/")
	}
	return filepath.Join(dir, "scan-"+time.Now().Format("20060102-150405")+"."+c.Output.Format), nil
}
