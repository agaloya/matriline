//go:build unix

package main

import (
	"path/filepath"
	"syscall"
)

// diskFree returns the bytes available to this user on the filesystem holding path (-1 if
// unknown).
func diskFree(path string) int64 {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return -1
	}
	return int64(st.Bavail) * int64(st.Bsize)
}

// diskTotal returns the size of the filesystem holding path (-1 if unknown).
func diskTotal(path string) int64 {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return -1
	}
	return int64(st.Blocks) * int64(st.Bsize)
}

// orcaExe is ORCA's main program in an installation directory.
func orcaExe(dir string) string { return filepath.Join(dir, "orca") }

// platformEnv: what the OS needs in a clean environment for ORCA (nothing on Unix).
func platformEnv(tmp string) []string { return nil }

// firewallHint: how to let clients through the system firewall (Windows only).
func firewallHint(port string) string { return "" }

const defaultEditor = "nano"
