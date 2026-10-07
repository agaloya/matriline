package orca

import (
	"os"
	"path/filepath"
	"testing"
)

// The .run installer's top-level "setup" helper does not change the fingerprint: an
// installed build and the same build unpacked from its archive match.
func TestFingerprintIgnoresInstallerSetup(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()
	for _, d := range []string{a, b} {
		os.MkdirAll(filepath.Join(d, "lib"), 0o755)
		os.WriteFile(filepath.Join(d, "orca"), []byte("orca"), 0o755)
		os.WriteFile(filepath.Join(d, "lib", "setup"), []byte("not the helper"), 0o644)
	}
	os.WriteFile(filepath.Join(b, "setup"), []byte("installer helper"), 0o755)
	fa, err := FingerprintTree(a, "", true)
	if err != nil {
		t.Fatal(err)
	}
	fb, err := FingerprintTree(b, "", true)
	if err != nil {
		t.Fatal(err)
	}
	if fa.TreeHash != fb.TreeHash {
		t.Errorf("trees differ: %v vs %v", fa.Files, fb.Files)
	}
	if _, ok := fb.Files["lib/setup"]; !ok {
		t.Errorf("a 'setup' below the top folder must count: %v", fb.Files)
	}
}
