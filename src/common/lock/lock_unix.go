//go:build unix

// Package lock gives a process exclusive use of a directory (one client per state
// directory, one server per spool). flock(2) locks are released by the kernel when the
// process dies, so a crash or a power cut never leaves a stale lock behind.
package lock

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
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
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
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
