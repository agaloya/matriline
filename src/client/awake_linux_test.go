package main

import (
	"os/exec"
	"strings"
	"testing"
)

// The inhibitor lock is taken and appears in systemd-inhibit --list, and is released.
func TestPreventSleepLinux(t *testing.T) {
	if _, err := exec.LookPath("systemd-inhibit"); err != nil {
		t.Skip("no systemd-inhibit")
	}
	stop, err := preventSleep()
	if err != nil {
		t.Skipf("no inhibitor here (%v)", err)
	}
	out, _ := exec.Command("systemd-inhibit", "--list").CombinedOutput()
	stop()
	if !strings.Contains(string(out), "Matriline") {
		t.Errorf("lock not listed:\n%s", out)
	}
	after, _ := exec.Command("systemd-inhibit", "--list").CombinedOutput()
	if strings.Contains(string(after), "Matriline") {
		t.Errorf("lock not released:\n%s", after)
	}
}
