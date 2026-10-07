//go:build windows

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// A 2-process ORCA job (MS-MPI) through the sandbox: MATRILINE_TEST_ORCA = ORCA's folder,
// MATRILINE_TEST_MSMPI = MS-MPI's Bin folder (holds mpiexec.exe and smpd.exe). STRONG=0
// runs it at low integrity instead of in the AppContainer.
func TestMSMPIJob(t *testing.T) {
	orcaDir, mpiBin := os.Getenv("MATRILINE_TEST_ORCA"), os.Getenv("MATRILINE_TEST_MSMPI")
	if orcaDir == "" || mpiBin == "" {
		t.Skip("set MATRILINE_TEST_ORCA and MATRILINE_TEST_MSMPI")
	}
	strong := os.Getenv("STRONG") != "0"
	setSandboxStrong(strong)
	defer setSandboxStrong(true)
	work := filepath.Join(t.TempDir(), "work")
	if err := makeWorkDir(work); err != nil {
		t.Fatal(err)
	}
	in := "%pal nprocs 2 end\n! HF def2-SVP\n* xyz 0 1\nO 0 0 0\nH 0 0.757 0.587\nH 0 -0.757 0.587\n*\n"
	if err := os.WriteFile(filepath.Join(work, "w.inp"), []byte(in), 0o644); err != nil {
		t.Fatal(err)
	}
	prog := []string{filepath.Join(orcaDir, "orca.exe"), "w.inp"}
	cmd := sandboxCommand(os.Args[0], work, []string{orcaDir, mpiBin}, 0, prog, strong, false)
	if cmd == nil {
		t.Fatalf("no sandbox: %s", sandboxErr)
	}
	cmd.Dir = work
	cmd.Env = append(orcaEnv(orcaDir, work), "PATH="+orcaDir+";"+mpiBin+";"+os.Getenv("SystemRoot")+`\System32`)
	if extra := os.Getenv("MATRILINE_TEST_ENV"); extra != "" {
		cmd.Env = append(cmd.Env, strings.Split(extra, ";;")...)
	}
	out, err := cmd.CombinedOutput()
	b, _ := os.ReadFile(filepath.Join(work, "w.out"))
	t.Logf("exit: %v\nstdout/stderr tail: %s", err, tail(string(out), 1500))
	t.Logf("w.out tail: %s", tail(string(b), 1500))
	all := string(out) + string(b)
	if !strings.Contains(all, "ORCA TERMINATED NORMALLY") {
		t.Errorf("the 2-process job did not finish (strong=%v)", strong)
	}
	if !strings.Contains(all, "running with 2 parallel MPI-processes") {
		t.Errorf("ORCA did not run 2 MPI processes (strong=%v)", strong)
	}
	for _, ln := range strings.Split(all, "\n") {
		if strings.Contains(ln, "MPI-processes") || strings.Contains(ln, "mpiexec") || strings.Contains(ln, "MPI") && strings.Contains(ln, "rror") {
			t.Logf("> %s", strings.TrimSpace(ln))
		}
	}
	_ = exec.Command
}

// Any command in the sandbox, for experiments: MATRILINE_TEST_CMD = program and arguments
// separated by ";;", MATRILINE_TEST_PATH = extra PATH entries. STRONG=0: low integrity.
func TestContainerRun(t *testing.T) {
	line := os.Getenv("MATRILINE_TEST_CMD")
	if line == "" {
		t.Skip("set MATRILINE_TEST_CMD")
	}
	strong := os.Getenv("STRONG") != "0"
	setSandboxStrong(strong)
	defer setSandboxStrong(true)
	work := filepath.Join(t.TempDir(), "work")
	if err := makeWorkDir(work); err != nil {
		t.Fatal(err)
	}
	cmd := sandboxCommand(os.Args[0], work, nil, 0, strings.Split(line, ";;"), strong, false)
	if cmd == nil {
		t.Fatalf("no sandbox: %s", sandboxErr)
	}
	cmd.Dir = work
	cmd.Env = append(orcaEnv(os.Getenv("MATRILINE_TEST_ORCA"), work), "PATH="+os.Getenv("MATRILINE_TEST_PATH")+";"+os.Getenv("SystemRoot")+`\System32`)
	if extra := os.Getenv("MATRILINE_TEST_ENV"); extra != "" {
		cmd.Env = append(cmd.Env, strings.Split(extra, ";;")...)
	}
	out, err := cmd.CombinedOutput()
	t.Logf("exit: %v\n%s", err, tail(string(out), 3000))
}
