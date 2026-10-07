//go:build !darwin

package main

// Gatekeeper's quarantine exists only on macOS (gatekeeper_darwin.go).

func quarantinedFiles(dir string) []string { return nil }

func quarantineHint(dir string) string { return "" }
