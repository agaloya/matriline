//go:build darwin

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// lsof samples captured on macOS 14 (VM) under Rosetta 2: an x86_64 /bin/sh (the shim has
// already run bash), and an Intel ORCA module started through "sh -c" whose ps name was "sh".
// The program is the first text file that is not Rosetta's or the system's.
func TestProgramsFromLsof(t *testing.T) {
	for file, want := range map[string]string{
		"lsof-rosetta-sh.txt":          "/bin/bash",
		"lsof-rosetta-orca-module.txt": "/Users/adrian/orca/orca_6_1_1_macosx_intel_openmpi411/orca_scfgrad",
	} {
		b, err := os.ReadFile(filepath.Join("testdata", file))
		if err != nil {
			t.Fatal(err)
		}
		got := programsFromLsof(string(b))
		if len(got) != 1 {
			t.Fatalf("%s: %v", file, got)
		}
		for _, exe := range got {
			if exe != want {
				t.Errorf("%s: %q, want %q", file, exe, want)
			}
		}
	}
	// several processes in one call; one with only system files stays unresolved
	got := programsFromLsof("p1\nftxt\nn/usr/lib/dyld\np2\nftxt\nn/usr/libexec/rosetta/runtime\nftxt\nn/bin/sh\n")
	if _, ok := got[1]; ok || got[2] != "/bin/sh" {
		t.Errorf("%v", got)
	}
}

// A real x86_64 shell under Rosetta: ps names it "sh"; processTree must give a path the audit
// accepts as the system shell.
func TestProcessTreeRosettaShell(t *testing.T) {
	if exec.Command("arch", "-x86_64", "/usr/bin/true").Run() != nil {
		t.Skip("Rosetta 2 is not installed")
	}
	cmd := exec.Command("arch", "-x86_64", "/bin/sh", "-c", "sleep 5; true")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer cmd.Process.Kill()
	time.Sleep(time.Second) // arch execs the shell in its own process
	shell := false
	for _, p := range processTree(cmd.Process.Pid) {
		t.Logf("pid %s: %s", strconv.Itoa(p.pid), p.exe)
		if p.exe == "/bin/sh" || p.exe == "/bin/bash" {
			shell = true
		}
		if p.exe != "" && !strings.HasPrefix(p.exe, "/") {
			t.Errorf("pid %d: %q has no path", p.pid, p.exe)
		}
	}
	if !shell {
		t.Error("the x86_64 shell was not found with its path")
	}
}

// A module's "sh" that has exited (gone, or a zombie) by the time lsof looks is no program
// run by the input; one still running stays unresolved, so the audit stops the job.
func TestParseRunning(t *testing.T) {
	got := parseRunning("  101 S\n  102 Z\n  103 R+\n  104 Z+\n")
	if !got[101] || got[102] || !got[103] || got[104] || got[105] {
		t.Errorf("got %v", got)
	}
}
