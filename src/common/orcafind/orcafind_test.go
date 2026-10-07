package orcafind

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// fakeOrca makes a directory that looks like an ORCA installation.
func fakeOrca(t *testing.T, dir string) {
	t.Helper()
	exe := "orca"
	if runtime.GOOS == "windows" {
		exe = "orca.exe"
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{exe, "orca_scfgrad"} {
		if err := os.WriteFile(filepath.Join(dir, n), nil, 0o755); err != nil {
			t.Fatal(err)
		}
	}
}

// ORCA is found in the home directory, directly (~/orca_6_1_1) or inside a folder that
// collects versions (~/orca/orca_6_1_1_...); a folder without ORCA's modules is not ORCA.
func TestDirsHome(t *testing.T) {
	home := t.TempDir()
	if r, err := filepath.EvalSymlinks(home); err == nil {
		home = r // Dirs returns resolved paths (macOS: /var is /private/var)
	}
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("PATH", "")
	direct := filepath.Join(home, "orca_6_1_1")
	nested := filepath.Join(home, "orca", "orca_6_1_1_macosx_arm64_openmpi411")
	apps := filepath.Join(home, "Applications", "orca-6.1.1")
	fakeOrca(t, direct)
	fakeOrca(t, nested)
	fakeOrca(t, apps)
	os.MkdirAll(filepath.Join(home, "orca_notes"), 0o755)
	got := map[string]bool{}
	for _, d := range Dirs() {
		got[d] = true
	}
	for _, want := range []string{direct, nested, apps} {
		if !got[want] {
			t.Errorf("%s not found; got %v", want, got)
		}
	}
	if got[filepath.Join(home, "orca")] || got[filepath.Join(home, "orca_notes")] {
		t.Errorf("a folder without ORCA was listed: %v", got)
	}
}

// A program called orca that is not ORCA (GNOME's screen reader) earlier in the PATH does
// not hide a real ORCA later in it.
func TestDirsLooksPastAnotherOrcaInPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix PATH layout")
	}
	reader, real := t.TempDir(), t.TempDir()
	os.WriteFile(filepath.Join(reader, "orca"), []byte("#!/bin/sh\necho screen reader\n"), 0o755)
	os.WriteFile(filepath.Join(real, "orca"), []byte("\x7fELF"), 0o755)
	os.WriteFile(filepath.Join(real, "orca_scf"), []byte("\x7fELF"), 0o755)
	t.Setenv("PATH", reader+string(os.PathListSeparator)+real)
	t.Setenv("HOME", t.TempDir())
	want, _ := filepath.EvalSymlinks(real)
	for _, d := range Dirs() {
		if d == reader {
			t.Errorf("the screen reader's folder was taken for ORCA")
		}
		if d == want {
			return
		}
	}
	t.Errorf("the real ORCA later in the PATH was not found: %v", Dirs())
}
