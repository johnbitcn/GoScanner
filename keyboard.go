//go:build darwin || linux

package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"syscall"
	"unsafe"
)

type keyInput struct {
	file     *os.File
	original syscall.Termios
}

func terminalMode(file *os.File, request uintptr, mode *syscall.Termios) error {
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, file.Fd(), request, uintptr(unsafe.Pointer(mode)))
	if errno != 0 {
		return errno
	}
	return nil
}

func openKeyInput() (*keyInput, error) {
	file, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return nil, err
	}
	keys := &keyInput{file: file}
	if err := terminalMode(file, getTerminalMode, &keys.original); err != nil {
		file.Close()
		return nil, err
	}
	return keys, nil
}

func (keys *keyInput) wait(ctx context.Context, prompt io.Writer) (more bool, err error) {
	mode := keys.original
	mode.Lflag &^= syscall.ICANON | syscall.ECHO | syscall.IEXTEN
	mode.Iflag &^= syscall.IXON
	mode.Cc[syscall.VMIN], mode.Cc[syscall.VTIME] = 0, 0
	// 保留 ISIG 以支持 Ctrl+C；立即设置模式，不等待终端输出排空。
	if err = terminalMode(keys.file, setTerminalMode, &mode); err != nil {
		return false, err
	}
	defer func() {
		err = errors.Join(err, terminalMode(keys.file, setTerminalMode, &keys.original))
	}()
	var input [64]byte
	// 丢弃扫描期间的按键，避免旧输入触发下一页。
	for {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		n, err := syscall.Read(int(keys.file.Fd()), input[:])
		if err == syscall.EINTR {
			continue
		}
		if err != nil {
			return false, err
		}
		if n == 0 {
			break
		}
	}
	mode.Cc[syscall.VTIME] = 1
	if err := terminalMode(keys.file, setTerminalMode, &mode); err != nil {
		return false, err
	}
	fmt.Fprintln(prompt, "请更换扫描页，按任意键继续扫描；单独按 Esc 结束。")
	for {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		// 直接读取以保留 VTIME 的 0.1 秒超时，让等待也能响应 context 取消。
		n, err := syscall.Read(int(keys.file.Fd()), input[:])
		if err == syscall.EINTR {
			continue
		}
		if err != nil {
			return false, err
		}
		if n == 0 {
			continue
		}
		if input[0] != 0x1b || n > 1 {
			return true, nil
		}
		// 方向键等以 Esc 开头；等待一个读取周期，区分按键序列与单独 Esc。
		n, err = syscall.Read(int(keys.file.Fd()), input[:])
		if ctx.Err() != nil {
			return false, ctx.Err()
		}
		if err == syscall.EINTR {
			continue
		}
		return n > 0, err
	}
}
