package main

import "syscall"

const (
	getTerminalMode = syscall.TIOCGETA
	setTerminalMode = syscall.TIOCSETA
)
