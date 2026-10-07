// Package conhost stops a Windows program whose console host was killed.
//go:build windows

package conhost

import (
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"
)

// Watch: the scheduled task starts the client under "conhost.exe --headless",
// and ending the task (schtasks /End, Stop-ScheduledTask) kills only conhost. The client
// then went on without a console, and every ORCA it started died at once (0xC0000142,
// STATUS_DLL_INIT_FAILED), reported as ORCA errors until the server paused the client
// (Windows 11 chaos test). So a program started by conhost calls stop when conhost ends
// (the client and the server, which run the same way).
var procQueryFullProcessImageNameW = syscall.NewLazyDLL("kernel32.dll").NewProc("QueryFullProcessImageNameW")

func Watch(stop func()) {
	const queryLimited, synchronize = 0x1000, 0x00100000
	h, err := syscall.OpenProcess(queryLimited|synchronize, false, uint32(syscall.Getppid()))
	if err != nil {
		return
	}
	buf := make([]uint16, 1024)
	n := uint32(len(buf))
	if r, _, _ := procQueryFullProcessImageNameW.Call(uintptr(h), 0, uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&n))); r == 0 ||
		!strings.EqualFold(filepath.Base(syscall.UTF16ToString(buf[:n])), "conhost.exe") {
		syscall.CloseHandle(h)
		return
	}
	go func() {
		syscall.WaitForSingleObject(h, syscall.INFINITE)
		syscall.CloseHandle(h)
		stop()
	}()
}
