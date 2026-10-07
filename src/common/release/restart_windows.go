//go:build windows

package release

import (
	"os"
	"os/exec"
)

// Restart starts the program at exe with the same arguments, attached to the same console
// (the scheduled task's console host stays alive while it is attached), and ends this
// process. Only returns on error.
func Restart(exe string) error {
	cmd := exec.Command(exe, os.Args[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	cmd.Env = append(os.Environ(), Updated+"=1")
	if err := cmd.Start(); err != nil {
		return err
	}
	os.Exit(0)
	return nil
}
