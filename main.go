package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

type discoveryFunc func(context.Context) ([]scannerDevice, []diagnostic, error)

var errOutputFormat = diagnosticf("The output extension must be .jpg, .jpeg, .tif, .tiff, .png, or .pdf.", "输出扩展名必须是 .jpg、.jpeg、.tif、.tiff、.png 或 .pdf")

type commandRequest struct {
	command, endpoint, protocol, host, area, output string
	recordID                                        string
	language                                        language
	device                                          *scannerDevice
	registerDefault                                 bool
	timeout                                         time.Duration
	opts                                            options
	manual, singlePage                              bool
	protocolExplicit                                bool
}

func (r *commandRequest) validateScan() error {
	area, err := parseArea(r.area)
	if err != nil {
		return err
	}
	if r.opts.DPI <= 0 || (r.opts.Mode != "color" && r.opts.Mode != "gray") {
		return diagnosticf("--dpi must be greater than zero. The scan mode must be color or gray.", "--dpi 必须大于零；扫描模式目前只开放 color、gray")
	}
	switch strings.ToLower(filepath.Ext(r.output)) {
	case ".jpg", ".jpeg", ".tif", ".tiff", ".png", ".pdf":
	default:
		return errOutputFormat
	}
	if r.singlePage && !strings.EqualFold(filepath.Ext(r.output), ".pdf") {
		return diagnosticf("--single-page requires PDF output. Select PDF with -o or output.format.", "--single-page 仅适用于 PDF 输出；请用 -o 或配置 output.format 选择 PDF")
	}
	r.opts.Area = area
	return nil
}

func parseRequest(args []string, manualDefault bool, lang language) (r commandRequest, help string, failure *commandError, code int) {
	r.language = lang
	invalid := func(err error) (commandRequest, string, *commandError, int) {
		return r, "", newCommandError("invalid_arguments", err), 2
	}
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		return r, renderHelp("", 80, false, lang), nil, 0
	}
	r.command = args[0]
	if r.command != "info" && r.command != "scan" && r.command != "discover" {
		return invalid(diagnosticf("Unknown command. Use discover, info, or scan.", "未知命令：使用 discover、info 或 scan"))
	}
	flags := flag.NewFlagSet(r.command, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.Usage = func() {}
	defaults := defaultConfig()
	var languageErr error
	flags.Func("lang", "Human interface language: auto, en, or zh-CN", func(value string) error {
		selected, err := selectLanguage(value)
		if err != nil {
			languageErr = err
			return err
		}
		r.language = selected
		return nil
	})
	r.timeout = 2 * time.Minute
	if r.command == "scan" {
		r.timeout = defaultScanTimeout
	} else if r.command == "discover" {
		r.timeout = 10 * time.Second
	}
	flags.DurationVar(&r.timeout, "timeout", r.timeout, "任务超时；每页分别计时，不包含交互等待；失败清理最多额外等待 10 秒")
	device := defaults.Device
	if r.command != "discover" {
		flags.StringVar(&device.Protocol, "protocol", "", "可选协议：wsd 或 escl；仅覆盖本次操作，默认使用设备列表首选协议")
		flags.StringVar(&device.Scheme, "scheme", "", "服务 scheme：http 或 https；默认 http")
		flags.StringVar(&device.Host, "host", "", "扫描仪 IP；只给 IP 时使用设备表或发现结果解析服务地址")
		flags.IntVar(&device.Port, "port", device.Port, "扫描服务端口")
		flags.StringVar(&device.Path, "path", device.Path, "扫描服务路径，以 / 开头")
		flags.StringVar(&r.endpoint, "endpoint", "", "完整 HTTP/HTTPS 地址；已登记设备默认使用首选协议，与拆分地址参数互斥")
	}
	if r.command == "scan" {
		flags.BoolVar(&r.opts.Deskew, "deskew", false, "AI 纠偏")
		flags.BoolVar(&r.opts.Autocrop, "autocrop", false, "AI 裁切")
		flags.BoolVar(&r.manual, "manual", manualDefault, "手动连续扫描")
		flags.BoolVar(&r.singlePage, "single-page", false, "PDF 每页保存为独立文档")
		flags.IntVar(&r.opts.DPI, "dpi", defaults.Scan.DPI, "扫描分辨率；必须是设备声明的值")
		flags.StringVar(&r.opts.Mode, "mode", defaults.Scan.Mode, "color 或 gray")
		flags.StringVar(&r.area, "area", defaults.Scan.Area, "a4、full 或 x,y,width,height（毫米）")
		flags.StringVar(&r.output, "o", "", "新 JPEG、TIFF、PNG 或 PDF 文件")
	}
	parseErr := flags.Parse(args[1:])
	if languageErr != nil {
		return invalid(languageErr)
	}
	if parseErr != nil {
		if errors.Is(parseErr, flag.ErrHelp) {
			return r, renderHelp(r.command, 80, false, r.language), nil, 0
		}
		return invalid(parseErr)
	}
	badAddress, addressParts := false, false
	explicit := map[string]bool{}
	flags.Visit(func(f *flag.Flag) {
		explicit[f.Name] = true
		if f.Name == "host" || f.Name == "port" || f.Name == "path" || f.Name == "scheme" {
			addressParts = true
		}
		if (f.Name == "host" || f.Name == "endpoint") && f.Value.String() == "" {
			badAddress = true
		}
	})
	if flags.NArg() != 0 || badAddress || (r.endpoint != "" && addressParts) {
		return invalid(diagnosticf("The address must not be empty. Do not combine --endpoint with --host, --port, or --path. Extra positional arguments are not supported.", "地址不能空置；--endpoint 与 --host、--port、--path 互斥；不能使用多余的位置参数"))
	}
	cfg, err := loadConfig()
	if err == nil {
		err = cfg.apply(flags, r.command)
	}
	if err != nil {
		return r, "", newCommandError("invalid_config", err), 2
	}
	if r.timeout <= 0 {
		return invalid(diagnosticf("--timeout / scan.timeout must be greater than zero.", "--timeout / scan.timeout 必须大于零"))
	}
	if r.command == "scan" {
		if r.output == "" {
			r.output, err = cfg.outputPath()
			if err != nil {
				return invalid(err)
			}
		}
		if err := r.validateScan(); err != nil {
			// Preserve the existing unsupported-format exit code.
			if errors.Is(err, errOutputFormat) {
				return r, "", newCommandError("invalid_arguments", err), 1
			}
			return invalid(err)
		}
	}
	r.protocol = protocolName(device.Protocol)
	r.protocolExplicit = explicit["protocol"]
	if cfg.Device.RecordID != "" && !explicit["host"] && !explicit["endpoint"] {
		r.recordID = cfg.Device.RecordID
		if explicit["port"] || explicit["path"] || explicit["scheme"] {
			return invalid(diagnosticf("Specify --host when overriding a saved scanner record. Port, path, or scheme alone cannot override a record.", "记录引用模式下请同时提供 --host，不能仅覆盖端口、路径或 scheme"))
		}
	}
	if r.protocol != "wsd" && r.protocol != "escl" {
		return invalid(diagnosticf("--protocol / device.protocol must be wsd or escl.", "--protocol / device.protocol 必须是 wsd 或 escl"))
	}
	if r.protocol != protocolName(cfg.Device.Protocol) {
		if !explicit["scheme"] {
			device.Scheme = "http"
		}
		if !explicit["port"] {
			device.Port = 8018
			if r.protocol == "escl" {
				device.Port = 80
				if device.Scheme == "https" {
					device.Port = 443
				}
			}
		}
		if !explicit["path"] {
			device.Path = "/wsd/scan"
			if r.protocol == "escl" {
				device.Path = "/eSCL"
			}
		}
	}
	if (explicit["host"] || (explicit["protocol"] && r.protocol != protocolName(cfg.Device.Protocol) && !explicit["endpoint"])) && !explicit["port"] && !explicit["path"] && !explicit["scheme"] && device.Host != "" {
		_, err = discoveryIP(device.Host)
		r.host = device.Host
	} else if r.endpoint == "" && device.Host != "" {
		r.endpoint, err = device.endpoint()
	}
	if err == nil && r.endpoint != "" {
		if strings.HasPrefix(r.endpoint, "https://") {
			_, err = checkedESCLEndpoint(r.endpoint)
		} else {
			_, err = newProtocolClient(r.endpoint, r.protocol)
		}
	}
	if err != nil {
		return invalid(err)
	}
	return r, "", nil, 0
}

func run(parent context.Context, args []string, stdout, stderr io.Writer) int {
	return runWithDiscovery(parent, args, stdout, stderr, discover)
}

func runWithDiscovery(parent context.Context, args []string, stdout, stderr io.Writer, find discoveryFunc) int {
	agent := len(args) > 0 && args[0] == "--agent"
	if agent {
		args = args[1:]
	}
	args, lang, languageErr := leadingLanguage(args)
	if languageErr != nil {
		result := commandResult{Status: "ok"}
		result.fail("invalid_arguments", languageErr)
		return writeResult(stdout, stderr, result, 2, agent, lang)
	}
	humanTUI := !agent && interactiveOutput(stdout)
	if len(args) == 0 && humanTUI {
		args = []string{"scan"}
	}
	result := commandResult{Status: "ok"}
	if agent && len(args) == 0 {
		result.fail("invalid_arguments", diagnosticf("--agent requires a discover, info, or scan subcommand.", "--agent 必须指定 discover、info 或 scan 子命令"))
		return writeResult(stdout, stderr, result, 2, agent, lang)
	}
	r, help, failure, code := parseRequest(args, humanTUI, lang)
	lang = r.language
	if failure != nil {
		result.Status, result.Error = "error", failure
	} else if help != "" {
		result.Help = help
		if agent {
			result.Help = renderAgentHelp(r.command)
		} else {
			width, colored := helpDisplay(stdout)
			result.Help = renderHelp(r.command, width, colored, lang)
		}
	} else if agent && r.manual {
		result.fail("unsupported_interaction", diagnosticf("Agent mode does not support --manual. Run scan separately for each acquisition.", "Agent 模式不支持 --manual；请逐次执行 scan"))
		code = 2
	} else if humanTUI {
		return runTUI(parent, r, stdout, stderr, find)
	} else {
		hooks := commandHooks{agent: agent}
		if !agent {
			hooks.choose = func(ctx context.Context, devices []scannerDevice) (string, error) {
				return chooseDevice(ctx, devices, stderr, lang)
			}
			if r.manual {
				keys, err := openKeyInput()
				if err != nil {
					result.fail("terminal_required", diagnosticf("Manual scanning requires an interactive terminal: %w", "手动连续扫描需要交互终端：%w", err))
					return writeResult(stdout, stderr, result, 1, agent, lang)
				}
				defer keys.file.Close()
				hooks.wait = func(ctx context.Context) (bool, error) { return keys.wait(ctx, stderr, lang) }
			}
		}
		out, diagnostics := stdout, stderr
		if agent {
			out, diagnostics = io.Discard, io.Discard
		}
		result, code = executeCommand(parent, r, find, hooks, out, diagnostics)
	}
	return writeResult(stdout, stderr, result, code, agent, lang)
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

func exitCode(ctx context.Context) int {
	if ctx.Err() != nil {
		return 130
	}
	return 1
}

// Keep encoding errors observable even when the command itself succeeded.
func writeResult(stdout, stderr io.Writer, result commandResult, code int, agent bool, lang language) int {
	var err error
	if agent {
		err = json.NewEncoder(stdout).Encode(result)
	} else if result.Help != "" {
		_, err = fmt.Fprint(stdout, result.Help)
	} else if result.Error != nil {
		_, err = fmt.Fprintln(stderr, lang.commandError(result.Error))
	}
	if err != nil {
		failure := diagnosticf("Cannot write the command result: %w", "输出结果失败： %w", err)
		if agent {
			fmt.Fprintln(stderr, agentError(failure))
		} else {
			fmt.Fprintln(stderr, lang.error(failure))
		}
		return 1
	}
	return code
}
