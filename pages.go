package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func scanPages(ctx context.Context, c *client, opts options, output string, singlePage bool, timeout time.Duration, wait func(context.Context) (bool, error), stdout, stderr io.Writer) (err error) {
	if opts.Deskew || opts.Autocrop {
		if opts.processor == nil {
			factory := opts.newProcessor
			if factory == nil {
				factory = newImageProcessor
			}
			opts.processor, err = factory(opts.Deskew, opts.Autocrop)
			if err != nil {
				return err
			}
		}
		defer func() { err = errors.Join(err, opts.processor.Close()) }()
	}
	pdf := strings.EqualFold(filepath.Ext(output), ".pdf")
	var staging string
	var pages []string
	if pdf {
		if err := os.MkdirAll(filepath.Dir(output), 0700); err != nil {
			return err
		}
		staging, err = os.MkdirTemp(filepath.Dir(output), ".goscanner-pdf-*")
		if err != nil {
			return err
		}
		defer func() {
			// Finalize completed pages even when the next scan or user wait fails.
			if len(pages) > 0 {
				if saveErr := writePDF(output, pages, opts.DPI); saveErr != nil {
					err = errors.Join(err, fmt.Errorf("保存 PDF 失败；已完成图像保留在 %s：%w", staging, saveErr))
					return
				}
				absolute, _ := filepath.Abs(output)
				fmt.Fprintf(stdout, "已保存：%s\n共保存 %d 页。\n", absolute, len(pages))
				for _, path := range pages {
					err = errors.Join(err, os.Remove(path))
				}
			}
			// Never recursively remove a JPEG staging file retained by a failed scan.
			if removeErr := os.Remove(staging); removeErr != nil {
				err = errors.Join(err, fmt.Errorf("暂存图像保留在 %s：%w", staging, removeErr))
			}
		}()
	}
	for page := 1; ; page++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		path := output
		if wait != nil && (!pdf || singlePage) {
			ext := filepath.Ext(output)
			path = fmt.Sprintf("%s-%03d%s", strings.TrimSuffix(output, ext), page, ext)
		}
		scanPath := path
		if pdf {
			if _, err := os.Lstat(path); err == nil {
				return errors.New("输出文件已存在，不会覆盖")
			} else if !errors.Is(err, os.ErrNotExist) {
				return err
			}
			scanPath = filepath.Join(staging, fmt.Sprintf("%03d.jpg", page))
		}
		if opts.Deskew || opts.Autocrop {
			ext := strings.ToLower(filepath.Ext(output))
			if pdf {
				ext = ".jpg"
			} else if ext == ".png" {
				ext = ".tiff"
			}
			base := strings.TrimSuffix(output, filepath.Ext(output))
			if pdf || wait != nil {
				opts.originalPath = fmt.Sprintf("%s-original-%03d%s", base, page, ext)
			} else {
				opts.originalPath = base + "-original" + ext
			}
		}
		fmt.Fprintln(stderr, "检查能力并扫描；请勿断电。Ctrl+C 可取消。")
		pageCtx, cancel := context.WithTimeout(ctx, timeout)
		result, err := c.scan(pageCtx, opts, scanPath)
		cancel()
		if result.originalPath != "" {
			absolute, _ := filepath.Abs(result.originalPath)
			fmt.Fprintf(stdout, "原图保留：%s\n", absolute)
		}
		if result.warning != "" {
			fmt.Fprintf(stderr, "第 %d 页预处理警告：%s；保留原图\n", page, result.warning)
		}
		for _, warning := range result.processing.Warnings {
			fmt.Fprintf(stderr, "第 %d 页预处理警告：%s\n", page, warning)
		}
		if err != nil {
			return fmt.Errorf("扫描失败：%w\n未自动重试；若创建任务时断线，请先检查扫描器状态。", err)
		}
		if opts.processor != nil {
			fmt.Fprintf(stdout, "Detected: %t；Angle: %.2f°；Changed: %t；尺寸：%d × %d\n", result.processing.Detected, result.processing.Angle, result.processing.Changed, result.Width, result.Height)
		}
		if pdf && !singlePage {
			pages = append(pages, scanPath)
			fmt.Fprintf(stdout, "已扫描第 %d 页；结束时合并保存 PDF。\n", page)
		} else {
			if pdf {
				if err := writePDF(path, []string{scanPath}, opts.DPI); err != nil {
					return err
				}
				if err := os.Remove(scanPath); err != nil {
					return err
				}
			}
			absolute, _ := filepath.Abs(path)
			fmt.Fprintf(stdout, "已保存：%s\n尺寸：%d × %d 像素；图像 %d 字节\n", absolute, result.Width, result.Height, result.Bytes)
		}
		if wait == nil {
			return nil
		}
		more, err := wait(ctx)
		if err != nil {
			return fmt.Errorf("等待换页失败：%w", err)
		}
		if !more {
			if !pdf || singlePage {
				fmt.Fprintf(stdout, "扫描结束，共保存 %d 页。\n", page)
			}
			return nil
		}
	}
}
