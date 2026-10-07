//go:build !unix

package main

import "os"

func signaled(ps *os.ProcessState) bool { return false }

func rusageOf(ps *os.ProcessState) (float64, int64, bool) {
	return ps.UserTime().Seconds() + ps.SystemTime().Seconds(), 0, true
}
