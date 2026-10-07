//go:build darwin

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// An ORCA directory whose programs carry com.apple.quarantine is reported, with the command
// that clears it; after that command it is not.
func TestQuarantinedFiles(t *testing.T) {
	dir := t.TempDir()
	for _, n := range []string{"orca", "orca_startup"} {
		if err := os.WriteFile(filepath.Join(dir, n), nil, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if q := quarantinedFiles(dir); len(q) != 0 {
		t.Fatalf("fresh files reported as quarantined: %v", q)
	}
	// the value Safari/curl-style downloads get: flags;time;agent;UUID
	if out, err := exec.Command("/usr/bin/xattr", "-w", "com.apple.quarantine", "0083;6ac59cd9;Safari;", filepath.Join(dir, "orca")).CombinedOutput(); err != nil {
		t.Fatalf("xattr -w: %v %s", err, out)
	}
	q := quarantinedFiles(dir)
	if len(q) != 1 || q[0] != filepath.Join(dir, "orca") {
		t.Fatalf("quarantined: %v", q)
	}
	hint := quarantineHint(dir)
	if !strings.Contains(hint, "xattr -dr com.apple.quarantine '"+dir+"'") {
		t.Errorf("hint without the command: %s", hint)
	}
	if out, err := exec.Command("/usr/bin/xattr", "-dr", "com.apple.quarantine", dir).CombinedOutput(); err != nil {
		t.Fatalf("xattr -dr: %v %s", err, out)
	}
	if q := quarantinedFiles(dir); len(q) != 0 {
		t.Errorf("still quarantined after xattr -dr: %v", q)
	}
}
