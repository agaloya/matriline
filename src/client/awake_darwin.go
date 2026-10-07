package main

import (
	"os"
	"os/exec"
	"strconv"
)

// preventSleep runs caffeinate(8): -i against idle sleep, -s against system sleep on mains
// power, -w ends it with this program.
func preventSleep() (func(), error) {
	cmd := exec.Command("caffeinate", "-i", "-s", "-w", strconv.Itoa(os.Getpid()))
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	return func() { cmd.Process.Kill(); <-done }, nil
}
