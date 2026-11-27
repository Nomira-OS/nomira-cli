package main

import (
	"os"
	"syscall"
	"unsafe"
)

// isTerminalFile reports whether f is a terminal: /dev/null is a character
// device too, so the mode alone does not tell.
func isTerminalFile(f *os.File) bool {
	var t syscall.Termios
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), syscall.TCGETS, uintptr(unsafe.Pointer(&t)))
	return errno == 0
}
