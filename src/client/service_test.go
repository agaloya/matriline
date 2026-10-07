package main

import (
	"os"
	"path/filepath"
	"testing"
)

// 'service remove' falls back to the old fixed unit name only for the same configuration:
// the lab's services test removed the real server's unit through it.
func TestLegacyUnitOnlyForSameConfig(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, ".config", "systemd", "user")
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, serviceName+".service"), []byte("ExecStart=/x/matriline-client -c /home/u/real/client.conf run\n"), 0o644)
	if _, ok := legacyUnit(home, "/lab/A/client.conf"); ok {
		t.Error("another configuration's unit would be removed")
	}
	if _, ok := legacyUnit(home, "/home/u/real/client.conf"); !ok {
		t.Error("the configuration's own old unit not found")
	}
}
