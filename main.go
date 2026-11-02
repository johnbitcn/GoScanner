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

const usage = `GoScanner — WSD 局域网扫描工具

用法：
  goscanner discover [--timeout 10s]
  goscanner info [--host IP] [--port 8018] [--path /wsd/scan] [--timeout 2m]
  goscanner scan [--host IP] [--port 8018] [--path /wsd/scan] [--dpi 150] [--mode color]
                 [--area a4] [-o output.jpg|output.tiff|output.png|output.pdf] [--timeout 30s]
                 [--manual] [--single-page] [--deskew] [--autocrop]

info/scan 未指定 host 时自动发现并列出设备；即使只有一台，也须输入编号确认。
确认后将 host、port、path 保存为默认设备；取消或无法保存时不启动扫描。
discover 仅输出设备列表，不选择设备、不写配置。
--host：扫描仪 IP；--port 默认 8018，--path 默认 /wsd/scan。
兼容 --endpoint URL；它覆盖整组设备配置，不能与 --host、--port、--path 同时使用。
配置文件：~/.config/goscanner/config.json；显式命令行参数 > 配置文件 > 内置默认值。
--deskew / --autocrop：可选 AI 纠偏 / 裁切；默认关闭，启用时保留原图。
可用 --deskew=false / --autocrop=false 临时关闭配置中启用的处理。
--mode：color、gray
--area：a4（默认）、full（完整平板）或 x,y,width,height（毫米，原点为平板左上角）
discover/info 不启动扫描；scan 在设备已指定或确认选择后启动扫描。已有文件不会被覆盖。
--manual：手动连续扫描；换页后按任意键继续，单独按 Esc 结束。
-o：支持 JPEG、TIFF、PNG、PDF；默认在当前工作目录生成带时间戳的 JPEG。
连续扫描默认合并为一个 PDF，图像格式按页编号保存。
TIFF 使用设备原生未压缩 TIFF；PNG 由原生 TIFF 无损转换，不经过 JPEG。
--single-page：仅用于 PDF 输出（由配置或 -o 选择），每页独立保存；连续扫描时文件名带序号。
PDF 在结束、中断或后续页失败时保存已完成的页面，不覆盖已有文件。
设备选择和手动模式需要交互终端；--timeout 不包含设备选择或换页等待。
Ctrl+C 会尝试取消本客户端创建的任务；不会自动重试扫描任务。
`

func run(parent context.Context, args []string, stdout, stderr io.Writer) int {
	return runWithDiscovery(parent, args, stdout, stderr, discover)
}

func runWithDiscovery(parent context.Context, args []string, stdout, stderr io.Writer, find func(context.Context) ([]scannerDevice, []string, error)) int {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		fmt.Fprint(stdout, usage)
		return 0
	}
	command := args[0]
	if command != "info" && command != "scan" && command != "discover" {
		fmt.Fprintln(stderr, "未知命令：使用 discover、info 或 scan")
		return 2
	}
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	flags.SetOutput(stderr)
	defaults := defaultConfig()
	defaultTimeout := 2 * time.Minute
	if command == "scan" {
		defaultTimeout = defaultScanTimeout
	}
	if command == "discover" {
		defaultTimeout = 10 * time.Second
	}
	timeout := flags.Duration("timeout", defaultTimeout, "任务超时；手动模式按发现/每页分别计时；失败清理最多额外等待 10 秒")
	device := defaults.Device
	var endpoint string
	if command != "discover" {
		flags.StringVar(&device.Host, "host", "", "扫描仪 IP；未设置时发现并确认选择设备")
		flags.IntVar(&device.Port, "port", defaults.Device.Port, "扫描服务端口")
		flags.StringVar(&device.Path, "path", defaults.Device.Path, "扫描服务路径，以 / 开头")
		flags.StringVar(&endpoint, "endpoint", "", "兼容完整 HTTP 地址；与 --host、--port、--path 互斥")
	}
	var dpi *int
	var mode, area, output *string
	var manual, singlePage, deskew, autocrop bool
	if command == "scan" {
		flags.BoolVar(&deskew, "deskew", false, "AI 纠偏")
		flags.BoolVar(&autocrop, "autocrop", false, "AI 裁切")
		flags.BoolVar(&manual, "manual", false, "手动连续扫描：换页后按任意键继续，Esc 结束")
		flags.BoolVar(&singlePage, "single-page", false, "PDF 每页保存为独立文档；由配置或 -o 选择 PDF")
		dpi = flags.Int("dpi", defaults.Scan.DPI, "扫描分辨率；必须是设备声明的值")
		mode = flags.String("mode", defaults.Scan.Mode, "color 或 gray")
		area = flags.String("area", defaults.Scan.Area, "a4（默认）、full（完整平板）或 x,y,width,height（毫米）")
		output = flags.String("o", "", "新 JPEG、TIFF、PNG 或 PDF 文件；默认 ./scan-时间戳.jpg")
	}
	if err := flags.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	badAddress, addressParts := false, false
	flags.Visit(func(f *flag.Flag) {
		if f.Name == "host" || f.Name == "port" || f.Name == "path" {
			addressParts = true
		}
		if (f.Name == "host" || f.Name == "endpoint") && f.Value.String() == "" {
			badAddress = true
		}
	})
	if flags.NArg() != 0 || badAddress || (endpoint != "" && addressParts) {
		fmt.Fprintln(stderr, "地址不能空置；--endpoint 与 --host、--port、--path 互斥；不能使用多余的位置参数")
		return 2
	}
	cfg, err := loadConfig()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	if err := cfg.apply(flags, command); err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	if *timeout <= 0 {
		fmt.Fprintln(stderr, "--timeout / scan.timeout 必须大于零")
		return 2
	}
	var opts options
	if command == "scan" {
		r, err := parseArea(*area)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 2
		}
		if *dpi <= 0 || (*mode != "color" && *mode != "gray") {
			fmt.Fprintln(stderr, "--dpi 必须大于零；扫描模式目前只开放 color、gray")
			return 2
		}
		if *output == "" {
			*output, err = cfg.outputPath()
			if err != nil {
				fmt.Fprintln(stderr, err)
				return 2
			}
		}
		ext := strings.ToLower(filepath.Ext(*output))
		if ext != ".jpg" && ext != ".jpeg" && ext != ".tif" && ext != ".tiff" && ext != ".png" && ext != ".pdf" {
			fmt.Fprintln(stderr, "输出扩展名必须是 .jpg、.jpeg、.tif、.tiff、.png 或 .pdf")
			return 1
		}
		if singlePage && ext != ".pdf" {
			fmt.Fprintln(stderr, "--single-page 仅适用于 PDF 输出；请用 -o 或配置 output.format 选择 PDF")
			return 2
		}
		opts = options{DPI: *dpi, Mode: *mode, Area: r, Deskew: deskew, Autocrop: autocrop}
	}
	ctx, cancel := context.WithTimeout(parent, *timeout)
	defer cancel()
	var c *client
	if endpoint == "" && device.Host != "" {
		endpoint, err = device.endpoint()
	}
	if err == nil && endpoint != "" {
		c, err = newEndpointClient(endpoint)
	}
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	var keys *keyInput
	if manual {
		keys, err = openKeyInput()
		if err != nil {
			fmt.Fprintln(stderr, "手动连续扫描需要交互终端：", err)
			return 1
		}
		defer keys.file.Close()
	}
	if c == nil {
		devices, warnings, findErr := find(ctx)
		cancel()
		for _, warning := range warnings {
			fmt.Fprintf(stderr, "发现警告：%q\n", warning)
		}
		if findErr != nil {
			fmt.Fprintln(stderr, "发现失败：", findErr)
			return exitCode(parent)
		}
		if command == "discover" {
			encoder := json.NewEncoder(stdout)
			encoder.SetIndent("", "  ")
			if err := encoder.Encode(devices); err != nil {
				fmt.Fprintln(stderr, err)
				return 1
			}
			return 0
		}
		endpoint, err = chooseDevice(parent, devices, stderr)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return exitCode(parent)
		}
		selected, err := deviceFromEndpoint(endpoint)
		if err == nil {
			err = saveDefaultDevice(selected)
		}
		if err != nil {
			fmt.Fprintln(stderr, "登记默认设备失败；未启动扫描：", err)
			return exitCode(parent)
		}
		c, err = newEndpointClient(endpoint)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return exitCode(parent)
		}
		fmt.Fprintln(stderr, "已登记默认设备：", endpoint)
		ctx, cancel = context.WithTimeout(parent, *timeout)
		defer cancel()
	}
	if command == "info" {
		cap, err := c.info(ctx)
		if err == nil {
			encoder := json.NewEncoder(stdout)
			encoder.SetIndent("", "  ")
			err = encoder.Encode(cap)
		}
		if err != nil {
			fmt.Fprintln(stderr, "读取失败：", err)
			return exitCode(parent)
		}
		return 0
	}
	var wait func(context.Context) (bool, error)
	if manual {
		ctx = parent // 换页等待不使用发现阶段的超时。
		wait = func(ctx context.Context) (bool, error) { return keys.wait(ctx, stderr) }
	}
	if err := scanPages(ctx, c, opts, *output, singlePage, *timeout, wait, stdout, stderr); err != nil {
		fmt.Fprintln(stderr, err)
		return exitCode(parent)
	}
	return 0
}

func exitCode(parent context.Context) int {
	if parent.Err() != nil {
		return 130
	}
	return 1
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}
