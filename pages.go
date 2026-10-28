package main

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"
)

func scanPages(ctx context.Context, c *client, opts options, output string, timeout time.Duration, wait func(context.Context) (bool, error), stdout, stderr io.Writer) error {
	for page := 1; ; page++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		path := output
		if wait != nil {
			ext := filepath.Ext(output)
			path = fmt.Sprintf("%s-%03d%s", strings.TrimSuffix(output, ext), page, ext)
		}
		fmt.Fprintln(stderr, "检查能力并扫描；请勿断电。Ctrl+C 可取消。")
		pageCtx, cancel := context.WithTimeout(ctx, timeout)
		result, err := c.scan(pageCtx, opts, path)
		cancel()
		if err != nil {
			return fmt.Errorf("扫描失败：%w\n未自动重试；若创建任务时断线，请先检查扫描器状态。", err)
		}
		absolute, _ := filepath.Abs(path)
		fmt.Fprintf(stdout, "已保存：%s\n尺寸：%d × %d 像素；%d 字节\n", absolute, result.Width, result.Height, result.Bytes)
		if wait == nil {
			return nil
		}
		more, err := wait(ctx)
		if err != nil {
			return fmt.Errorf("等待换页失败：%w", err)
		}
		if !more {
			fmt.Fprintf(stdout, "扫描结束，共保存 %d 页。\n", page)
			return nil
		}
	}
}
