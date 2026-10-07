//go:build windows

package lock

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"unsafe"
)

// LockFileEx (kernel32): an exclusive byte-range lock that Windows releases when the
// process exits, like flock(2) on Unix, so a crash never leaves a stale lock.
// https://learn.microsoft.com/en-us/windows/win32/api/fileapi/nf-fileapi-lockfileex
var procLockFileEx = syscall.NewLazyDLL("kernel32.dll").NewProc("LockFileEx")

const (
	lockfileExclusiveLock   = 0x2
	lockfileFailImmediately = 0x1
)

// Acquire locks dir/name or reports which process holds it. Keep the file open (do not
// close it) for as long as the lock is needed.
func Acquire(dir, name string) (*os.File, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	p := filepath.Join(dir, name)
	f, err := os.OpenFile(p, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	var ol syscall.Overlapped
	// lock one byte far beyond the end, so the PID written at the start stays readable
	ol.Offset, ol.OffsetHigh = 0, 0x7fffffff
	r, _, _ := procLockFileEx.Call(f.Fd(), lockfileExclusiveLock|lockfileFailImmediately, 0, 1, 0, uintptr(unsafe.Pointer(&ol)))
	if r == 0 {
		b, _ := os.ReadFile(p)
		f.Close()
		holder := strings.TrimSpace(string(b))
		if _, e := strconv.Atoi(holder); e != nil {
			holder = "?"
		}
		return nil, fmt.Errorf("another instance is already running here (pid %s, lock %s)", holder, p)
	}
	f.Truncate(0)
	f.WriteAt([]byte(strconv.Itoa(os.Getpid())+"\n"), 0)
	return f, nil
}
