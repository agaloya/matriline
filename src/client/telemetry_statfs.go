//go:build linux || darwin

package main

import "syscall"

func diskFree(path string) int64 {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return -1
	}
	return int64(st.Bavail) * int64(st.Bsize)
}

// diskSize is the size of the filesystem holding path (-1 unknown).
func diskSize(path string) int64 {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return -1
	}
	return int64(st.Blocks) * int64(st.Bsize)
}
