//go:build unix

package main

import (
	"os"
	"syscall"
)

// signaled reports whether the process was killed by a signal (e.g. the OOM killer).
func signaled(ps *os.ProcessState) bool {
	if ws, ok := ps.Sys().(syscall.WaitStatus); ok {
		return ws.Signaled()
	}
	return false
}

// rusageOf returns CPU seconds (user+system, including waited-for descendants on Linux)
// and the maximum resident set size in KiB of the largest process.
func rusageOf(ps *os.ProcessState) (float64, int64, bool) {
	ru, ok := ps.SysUsage().(*syscall.Rusage)
	if !ok || ru == nil {
		return 0, 0, false
	}
	cpu := float64(ru.Utime.Sec+ru.Stime.Sec) + float64(ru.Utime.Usec+ru.Stime.Usec)/1e6
	return cpu, int64(ru.Maxrss), true
}
