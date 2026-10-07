//go:build !linux

package main

import (
	"net"
	"os"
	"strconv"
	"strings"
)

// Readings only Linux provides without extra privileges: NA:<reason> (D17).

func cpuTempC() (float64, string) {
	if tempFile != "" {
		if b, err := os.ReadFile(tempFile); err == nil {
			if v, err := strconv.ParseFloat(strings.TrimSpace(string(b)), 64); err == nil {
				return v, ""
			}
		}
		return 0, "temperature_file_unreadable"
	}
	return 0, "unsupported_os"
}

func cpuFreqMHz() (float64, string)             { return 0, "unsupported_os" }
func loadAvg1() float64                         { return -1 }
func cpuTimes() (uint64, uint64)                { return 0, 0 }
func tcpStats(net.Conn) (float64, uint32, bool) { return 0, 0, false }
