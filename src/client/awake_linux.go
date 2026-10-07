package main

import (
	"errors"
	"os/exec"
	"syscall"
	"time"
)

// preventSleep takes a systemd-logind inhibitor lock (systemd-inhibit(1)) against sleep and
// idle sleep for as long as the helper runs. When the sleep lock is refused (polkit asks
// for an administrator when nobody is logged in), it holds off idle sleep alone, which
// polkit allows (a laptop test).
func preventSleep() (func(), error) {
	if _, err := exec.LookPath("systemd-inhibit"); err != nil {
		return nil, errors.New("systemd-inhibit not found")
	}
	stop, err := inhibit("sleep:idle")
	if err != nil {
		if stop, err2 := inhibit("idle"); err2 == nil {
			return stop, nil
		}
	}
	return stop, err
}

func inhibit(what string) (func(), error) {
	cmd := exec.Command("systemd-inhibit", "--what="+what, "--who=Matriline",
		"--why=ORCA calculations running", "--mode=block", "sleep", "infinity")
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done: // refused (no permission to inhibit, no logind)
		if err == nil {
			err = errors.New("systemd-inhibit ended at once")
		}
		return nil, err
	case <-time.After(time.Second):
	}
	return func() { cmd.Process.Kill(); <-done }, nil
}
