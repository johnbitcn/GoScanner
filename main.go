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
  goscanner info [--host IP | --endpoint URL] [--timeout 2m]
  goscanner scan [--host IP | --endpoint URL] [--dpi 300] [--mode color]
                 [--area a4] [-o scans/output.jpg|output.tiff|output.png|output.pdf] [--timeout 2m]
                 [--manual] [--single-page]

未指定设备时自动发现：只有一个扫描端点且发现过程完整时才使用它；多项时停止并列出地址。
--host：显式使用该 IP 的 http://IP:8018/wsd/scan，保留原用法。
--endpoint：显式使用 discover 返回的完整 HTTP 扫描地址。
--mode：color、gray
--area：a4（默认）、full（完整平板）或 x,y,width,height（毫米，原点为平板左上角）
discover/info 不启动扫描；scan 会直接启动一次扫描。已有文件不会被覆盖。
--manual：手动连续扫描；换页后按任意键继续，单独按 Esc 结束。
-o：支持 JPEG、TIFF、PNG、PDF；连续扫描默认合并为一个 PDF，图像格式按页编号保存。
TIFF 使用设备原生未压缩 TIFF；PNG 由原生 TIFF 无损转换，不经过 JPEG。
--single-page：仅用于 PDF，每页保存为独立文档；连续扫描时文件名带序号。
PDF 在结束、中断或后续页失败时保存已完成的页面，不覆盖已有文件。
手动模式需要交互终端；--timeout 分别限制发现和每页扫描，不包含换页等待。
Ctrl+C 会尝试取消本客户端创建的任务；不会自动重试扫描任务。
`

func run(parent context.Context, args []string, stdout, stderr io.Writer) int {
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
	defaultTimeout := 2 * time.Minute
	if command == "discover" {
		defaultTimeout = 10 * time.Second
	}
	timeout := flags.Duration("timeout", defaultTimeout, "任务超时；手动模式按发现/每页分别计时；失败清理最多额外等待 10 秒")
	var host, endpoint string
	if command != "discover" {
		flags.StringVar(&host, "host", "", "显式指定打印机 IP，端口固定 8018；与 --endpoint 互斥")
		flags.StringVar(&endpoint, "endpoint", "", "完整 HTTP 扫描地址；未指定设备时自动发现")
	}
	var dpi *int
	var mode, area, output *string
	var manual, singlePage bool
	if command == "scan" {
		flags.BoolVar(&manual, "manual", false, "手动连续扫描：换页后按任意键继续，Esc 结束")
		flags.BoolVar(&singlePage, "single-page", false, "PDF 每页保存为独立文档；须用 -o 指定 .pdf")
		dpi = flags.Int("dpi", 300, "扫描分辨率；必须是设备声明的值")
		mode = flags.String("mode", "color", "color 或 gray")
		area = flags.String("area", "a4", "a4（默认）、full（完整平板）或 x,y,width,height（毫米）")
		output = flags.String("o", "", "新 JPEG、TIFF、PNG 或 PDF 文件；默认 scans/scan-时间戳.jpg")
	}
	if err := flags.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	badAddress := false
	flags.Visit(func(f *flag.Flag) {
		if (f.Name == "host" || f.Name == "endpoint") && f.Value.String() == "" {
			badAddress = true
		}
	})
	if flags.NArg() != 0 || *timeout <= 0 || (host != "" && endpoint != "") || badAddress {
		fmt.Fprintln(stderr, "地址不能空置；--host 与 --endpoint 互斥；不能使用多余的位置参数；--timeout 必须大于零")
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
			*output = filepath.Join("scans", "scan-"+time.Now().Format("20060102-150405.000000000")+".jpg")
		}
		ext := strings.ToLower(filepath.Ext(*output))
		if ext != ".jpg" && ext != ".jpeg" && ext != ".tif" && ext != ".tiff" && ext != ".png" && ext != ".pdf" {
			fmt.Fprintln(stderr, "输出扩展名必须是 .jpg、.jpeg、.tif、.tiff、.png 或 .pdf")
			return 1
		}
		if singlePage && ext != ".pdf" {
			fmt.Fprintln(stderr, "--single-page 须用 -o 指定 .pdf 文件")
			return 2
		}
		opts = options{DPI: *dpi, Mode: *mode, Area: r}
	}
	ctx, cancel := context.WithTimeout(parent, *timeout)
	defer cancel()
	var c *client
	var err error
	if host != "" {
		c, err = newClient(host)
	} else if endpoint != "" {
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
		devices, warnings, findErr := discover(ctx)
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
		endpoint, err = chooseDevice(devices, warnings)
		if err != nil {
			for _, device := range devices {
				fmt.Fprintf(stderr, "%q  %s\n", device.Name, device.Endpoint)
			}
			fmt.Fprintln(stderr, err)
			return 1
		}
		c, err = newEndpointClient(endpoint)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		fmt.Fprintln(stderr, "自动选择：", endpoint)
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
