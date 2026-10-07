//go:build windows

package main

import (
	"os"
	"syscall"
	"unsafe"
)

// enableVT turns on escape sequences (clear screen) in a Windows console.
func enableVT() {
	k := syscall.NewLazyDLL("kernel32.dll")
	get, set := k.NewProc("GetConsoleMode"), k.NewProc("SetConsoleMode")
	var mode uint32
	h := os.Stdout.Fd()
	if r, _, _ := get.Call(h, uintptr(unsafe.Pointer(&mode))); r != 0 {
		set.Call(h, uintptr(mode|0x0004)) // ENABLE_VIRTUAL_TERMINAL_PROCESSING
	}
}
