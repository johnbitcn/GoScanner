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
	"syscall"
	"time"
)

const usage = `GoScanner — HP Laser MFP 1188w 的 WSD 扫描工具

用法：
  goscanner info [--host 192.168.1.8] [--timeout 2m]
  goscanner scan [--host 192.168.1.8] [--dpi 300] [--mode color]
                 [--area a4] [-o scans/output.jpg] [--timeout 2m]

--mode：color、gray
--area：a4 或 x,y,width,height（毫米，原点为平板左上角）
info 只读取能力；scan 会直接启动一次扫描。已有文件不会被覆盖。
Ctrl+C 会尝试取消本客户端创建的任务；不会自动重试。
`

func run(parent context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		fmt.Fprint(stdout, usage)
		return 0
	}
	command := args[0]
	if command != "info" && command != "scan" {
		fmt.Fprintln(stderr, "未知命令：使用 info 或 scan")
		return 2
	}
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	flags.SetOutput(stderr)
	host := flags.String("host", "192.168.1.8", "打印机 IP 地址")
	timeout := flags.Duration("timeout", 2*time.Minute, "整体任务超时；失败清理最多额外等待 10 秒")
	var dpi *int
	var mode, area, output *string
	if command == "scan" {
		dpi = flags.Int("dpi", 300, "扫描分辨率；必须是设备声明的值")
		mode = flags.String("mode", "color", "color 或 gray")
		area = flags.String("area", "a4", "a4 或 x,y,width,height（毫米）")
		output = flags.String("o", "", "新 JPEG 文件；默认 scans/scan-时间戳.jpg")
	}
	if err := flags.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() != 0 || *timeout <= 0 {
		fmt.Fprintln(stderr, "不能使用多余的位置参数；--timeout 必须大于零")
		return 2
	}
	c, err := newClient(*host)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	ctx, cancel := context.WithTimeout(parent, *timeout)
	defer cancel()
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
	r, err := parseArea(*area)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	if *dpi <= 0 || (*mode != "color" && *mode != "gray") {
		fmt.Fprintln(stderr, "--dpi 必须大于零；JPEG 模式目前只开放 color、gray")
		return 2
	}
	if *output == "" {
		*output = filepath.Join("scans", "scan-"+time.Now().Format("20060102-150405.000000000")+".jpg")
	}
	fmt.Fprintln(stderr, "检查能力并扫描；请勿断电。Ctrl+C 可取消。")
	result, err := c.scan(ctx, options{DPI: *dpi, Mode: *mode, Area: r}, *output)
	if err != nil {
		fmt.Fprintln(stderr, "扫描失败：", err)
		fmt.Fprintln(stderr, "未自动重试；若创建任务时断线，请先检查扫描器状态。")
		return exitCode(parent)
	}
	path, _ := filepath.Abs(*output)
	fmt.Fprintf(stdout, "已保存：%s\n尺寸：%d × %d 像素；%d 字节\n", path, result.Width, result.Height, result.Bytes)
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
