package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDiskClass(t *testing.T) {
	for in, want := range map[string]string{
		"! wB97M-V def2-TZVPP RIJCOSX def2/J\n* xyz 0 1\n": classLight,
		"! DLPNO-CCSD(T) cc-pVTZ cc-pVTZ/C TightPNO\n":     classHeavy,
		"! RI-MP2 def2-SVP def2-SVP/C\n":                   classHeavy,
		"! r2SCAN-3c Opt Freq\n# a comment about MP2\n":    classLight,
	} {
		if got := diskClass(in); got != want {
			t.Errorf("%q: %s, want %s", in, got, want)
		}
	}
}

func TestDiskNeedPerClass(t *testing.T) {
	dir := t.TempDir()
	a := &Agent{cfg: &Config{StateDir: dir}, jobs: map[string]*Job{}, diskPeaks: map[string][]int64{}}
	a.notePeak(classHeavy, 8<<30, false) // one DLPNO job
	a.notePeak(classLight, 300<<20, false)
	if n := a.diskNeed(classLight); n != 300<<20 {
		t.Fatalf("a light job is estimated at %d MB after a heavy one", n>>20)
	}
	// four light jobs running, each already using 100 MB: they may grow to 300 MB each
	for _, id := range []string{"a", "b", "c", "d"} {
		a.jobs[id] = &Job{State: jsRunning, DiskClass: classLight, DiskNow: 100 << 20}
	}
	if n := a.diskNeed(classLight); n != (300+4*200)<<20 {
		t.Fatalf("need %d MB, want %d", n>>20, 300+4*200)
	}
	a.notePeak(classHeavy, 6<<30, true) // killed by a full disk at 6 GB: needs more
	if p := a.classPeak(classHeavy); p != 12<<30 {
		t.Fatalf("disk-full job counted as %d MB, want 12288", p>>20)
	}
	// persisted and restored
	b := &Agent{cfg: &Config{StateDir: dir}}
	b.loadPeaks()
	if b.classPeak(classHeavy) != 12<<30 || b.classPeak(classLight) != 300<<20 {
		t.Fatal("peaks not restored")
	}
	// old format: one list, read as heavy
	os.WriteFile(filepath.Join(dir, "diskpeaks.json"), []byte("[7000000000]"), 0o600)
	c := &Agent{cfg: &Config{StateDir: dir}}
	c.loadPeaks()
	if c.classPeak(classHeavy) != 7000000000 || c.classPeak(classLight) != 256<<20 {
		t.Fatalf("old diskpeaks.json not migrated: %+v", c.diskPeaks)
	}
}

func TestOldPeaksSplitBySize(t *testing.T) {
	dir := t.TempDir()
	// an old lab list: mostly DFT jobs plus one DLPNO end-of-job size
	os.WriteFile(filepath.Join(dir, "diskpeaks.json"), []byte("[300000000,900000000,6800000000]"), 0o600)
	a := &Agent{cfg: &Config{StateDir: dir}}
	a.loadPeaks()
	if a.classPeak(classLight) != 900000000 || a.classPeak(classHeavy) != 6800000000 {
		t.Fatalf("light %d heavy %d", a.classPeak(classLight), a.classPeak(classHeavy))
	}
	b := &Agent{cfg: &Config{StateDir: t.TempDir()}, diskPeaks: map[string][]int64{}}
	if b.classPeak(classHeavy) != 2<<30 {
		t.Fatal("a heavy job with no history must be estimated at 2 GB, not 256 MB")
	}
}

// ORCA exits with 126 on an unreadable input, like a sandbox helper that could not start
// it: only the helper's own message makes it this computer's fault.
func TestHelperFailed(t *testing.T) {
	for _, tc := range []struct {
		stderr, out string
		want        bool
	}{
		{"", "", true},
		{"", "ERROR: expect a '$', '!', '%', '*' or '[' in the input\n", false},
		{"sandbox-exec: landlock_restrict_self: operation not permitted\n", "", true},
		{"/bin/sh: orca: cannot execute binary file\n", "", true},
		{"ERROR: expect a '$', '!', '%', '*' or '[' in the input\n       Line 4 of job.inp (C)\n", "", false},
		{"sh: 1: /opt/orca/orca_scf: not found\n", "", true},
		{"bash: orca: command not found\n", "", true},
		{"sandbox-exec: exec /opt/orca/orca: permission denied\n" + strings.Repeat("[mpi] noise\n", 100), "", true},
		{"ERROR: basis set def2-QZVPPX not found\n", "", false},
		{"FATAL ERROR ENCOUNTERED: file job.xyz not found\n", "", false},
	} {
		w := t.TempDir()
		if tc.stderr != "" {
			os.WriteFile(filepath.Join(w, "matriline.stderr"), []byte(tc.stderr), 0o644)
		}
		os.WriteFile(filepath.Join(w, "job.out"), []byte(tc.out), 0o644)
		if got := helperFailed(w, "job"); got != tc.want {
			t.Errorf("stderr %q, out %q: helperFailed = %v, want %v", tc.stderr, tc.out, got, tc.want)
		}
	}
}
