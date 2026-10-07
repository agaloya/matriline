//go:build windows

package main

import "os/exec"

// ownGroup: on Windows the child itself is ended at its time limit; WaitDelay (runChild)
// keeps a leftover grandchild from holding the server.
func ownGroup(cmd *exec.Cmd) {}
