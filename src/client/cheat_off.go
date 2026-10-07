//go:build !cheat

package main

// Hooks used only by the lab's adversarial build (client/cheat_on.go, -tags cheat).
// In every normal build they do nothing and are inlined away.

func cheatBefore(j *Job, inPath string) bool { return false }
func cheatAfter(j *Job, stem string)         {}
