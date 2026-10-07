//go:build !windows

package main

import (
	"os/exec"
	"syscall"
)

// ownGroup runs the child in its own process group, ended as a whole when its time is up
// (a hook that starts background programs cannot outlive its limit).
func ownGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
}
