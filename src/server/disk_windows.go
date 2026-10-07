//go:build windows

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"unsafe"
)

// Windows: free/total disk (GetDiskFreeSpaceExW), orca.exe, the variables ORCA needs in
// a clean environment (the client found them on Windows 11: without ComSpec ORCA cannot
// start its modules), Notepad as the default editor.

var procGetDiskFreeSpaceExW = syscall.NewLazyDLL("kernel32.dll").NewProc("GetDiskFreeSpaceExW")

func diskSpace(path string) (avail, total int64) {
	p, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return -1, -1
	}
	var a, t, f uint64
	if r, _, _ := procGetDiskFreeSpaceExW.Call(uintptr(unsafe.Pointer(p)), uintptr(unsafe.Pointer(&a)), uintptr(unsafe.Pointer(&t)), uintptr(unsafe.Pointer(&f))); r == 0 {
		return -1, -1
	}
	return int64(a), int64(t)
}

func diskFree(path string) int64  { a, _ := diskSpace(path); return a }
func diskTotal(path string) int64 { _, t := diskSpace(path); return t }

func orcaExe(dir string) string { return filepath.Join(dir, "orca.exe") }

func platformEnv(tmp string) []string {
	root := os.Getenv("SystemRoot")
	if root == "" {
		root = `C:\Windows`
	}
	comspec := os.Getenv("ComSpec")
	if comspec == "" {
		comspec = root + `\system32\cmd.exe`
	}
	return []string{"SystemRoot=" + root, "windir=" + root, "ComSpec=" + comspec, "TEMP=" + tmp, "TMP=" + tmp}
}

// firewallHint: Windows Firewall blocks a new program's incoming connections, and when nobody
// answers its prompt (a scheduled task, a dismissed window) it adds Block rules named after
// the program, which win over any Allow rule (seen on Windows 11, "Public" network).
func firewallHint(port string) string {
	return fmt.Sprintf(`; Windows Firewall: an administrator allows clients in once with: netsh advfirewall firewall delete rule name="matriline-server" dir=in & netsh advfirewall firewall add rule name="Matriline server port %s" dir=in action=allow protocol=TCP localport=%s`, port, port)
}

const defaultEditor = "notepad"
