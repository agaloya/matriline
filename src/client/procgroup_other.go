//go:build !windows

package main

import (
	"os"
	"os/exec"
)

// Outside Windows there is nothing to track: on Linux the process group (Setpgid, killGroup
// in sandbox_linux.go) already covers the whole ORCA tree. The sandbox helpers below are the
// same on every OS but Windows.

func trackGroup(*exec.Cmd)     {}
func releaseGroup(*exec.Cmd)   {}
func extraEnv(string) []string { return nil }

func sandboxHint(orcaDir string) string { return "" }

func setSandboxStrong(bool) {}

func makeWorkDir(dir string) error { return os.MkdirAll(dir, 0o700) }

// makeJobDir: the same directory whatever the job's number of processes.
func makeJobDir(dir string, procs int) error { return makeWorkDir(dir) }

// mpiSandboxDesc: multi-core jobs run at the same level as the others ("" = unchanged).
func mpiSandboxDesc() string { return "" }
