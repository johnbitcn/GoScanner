package main

import (
	_ "embed"
	"encoding/json"
)

//go:embed package.json
var packageMetadata []byte

func applicationVersion() (string, error) {
	var metadata struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(packageMetadata, &metadata); err != nil {
		return "", diagnosticf("Cannot read the embedded application version: %w", "无法读取内置版本号：%w", err)
	}
	if metadata.Version == "" {
		return "", diagnosticf("The embedded application version is missing.", "内置版本号缺失")
	}
	return metadata.Version, nil
}
