package main

import "syscall"

const (
	getTerminalMode = syscall.TCGETS
	setTerminalMode = syscall.TCSETS
)
