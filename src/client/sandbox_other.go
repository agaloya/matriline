//go:build !linux && !windows && !darwin

package main

import (
	"fmt"
	"os"
	"os/exec"
)

// Other systems (FreeBSD, ...) have no ORCA sandbox yet (Linux, Windows and macOS do):
// the client refuses to run unless the user disables the sandbox
// explicitly (security.sandbox = false), so nobody is unprotected by accident.

var sandboxErr = "no sandbox on this OS"

func sandboxDesc(strong bool) string { return "none (not implemented on this OS)" }

func sandboxExecMain(args []string) {
	fmt.Fprintln(os.Stderr, "sandbox-exec is only available on Linux")
	os.Exit(125)
}

func sandboxCommand(self, work string, execDirs []string, fsizeMB int64, prog []string, useNS, loopback bool) *exec.Cmd {
	return nil
}

func plainCommand(prog []string) *exec.Cmd { return exec.Command(prog[0], prog[1:]...) }

// systemShells: no shell needs allowing here.
func systemShells() []string { return nil }

func killGroup(cmd *exec.Cmd) {
	if cmd.Process != nil {
		cmd.Process.Kill()
	}
}
