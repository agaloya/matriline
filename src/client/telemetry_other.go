//go:build !linux && !windows && !darwin

package main

import (
	"runtime"
)

// Portable fallbacks: values that cannot be measured are reported as NA:<reason> (D17).
// Native implementations for macOS and Windows are planned.

func memInfo() (int64, int64) { return 0, 0 }
func cpuModel() string        { return runtime.GOARCH }
func osName() string          { return runtime.GOOS }
func kernelVersion() string   { return "unknown" }

func readPower(string) power { return power{State: "none", Percent: -1} }

var powerSupplyDirVar = ""

func diskFree(string) int64 { return -1 }
func diskSize(string) int64 { return -1 }

func processTree(root int) []procInfo { return []procInfo{{pid: root}} }

// physicalCores is unknown here: the logical CPU count is used.
func physicalCores() int { return 0 }
