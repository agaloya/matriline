//go:build !windows

package release

import (
	"os"
	"syscall"
)

// Restart replaces the running process with the program at exe and the same arguments
// (same process id: a service manager does not notice). Only returns on error.
func Restart(exe string) error {
	return syscall.Exec(exe, os.Args, append(os.Environ(), Updated+"=1"))
}
