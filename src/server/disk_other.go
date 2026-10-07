//go:build !unix && !windows

package main

import "path/filepath"

func diskFree(string) int64 { return -1 }

func diskTotal(string) int64 { return -1 }

func orcaExe(dir string) string { return filepath.Join(dir, "orca") }

func platformEnv(tmp string) []string { return nil }

// firewallHint: how to let clients through the system firewall (Windows only).
func firewallHint(port string) string { return "" }

const defaultEditor = "nano"
