//go:build windows

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// A program run through the sandbox can write in its job directory but not in the user's
// files (here: a new file in the user's profile).
func TestLowIntegritySandbox(t *testing.T) {
	setSandboxStrong(false)
	defer setSandboxStrong(true)
	work := filepath.Join(t.TempDir(), "work")
	if err := makeWorkDir(work); err != nil {
		t.Fatal(err)
	}
	home, _ := os.UserHomeDir()
	outside := filepath.Join(home, "matriline-sandbox-probe.txt")
	os.Remove(outside)
	defer os.Remove(outside)
	inside := filepath.Join(work, "inside.txt")
	shell := systemShells()[0]
	cmd := sandboxCommand("", work, nil, 0, []string{shell, "/c", "echo x>" + outside + " & echo y>" + inside}, false, false)
	if cmd == nil {
		t.Fatal("no sandbox")
	}
	cmd.Dir = work
	out, _ := cmd.CombinedOutput()
	if _, err := os.Stat(inside); err != nil {
		t.Errorf("could not write in the job directory: %v (%s)", err, out)
	}
	if _, err := os.Stat(outside); err == nil {
		t.Errorf("wrote %s outside the job directory", outside)
	}
	t.Logf("output: %s", out)
}

// The test binary doubles as the AppContainer helper.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "sandbox-exec" {
		sandboxExecMain(os.Args[2:])
		return
	}
	os.Exit(m.Run())
}

// AppContainer: no writing outside the job, and no network at all. MATRILINE_TEST_URL is a
// web address reachable from this computer (outside the container curl gets it).
func TestAppContainerSandbox(t *testing.T) {
	url := os.Getenv("MATRILINE_TEST_URL")
	if url == "" {
		t.Skip("set MATRILINE_TEST_URL to a reachable address")
	}
	work := filepath.Join(t.TempDir(), "work")
	if err := makeWorkDir(work); err != nil {
		t.Fatal(err)
	}
	home, _ := os.UserHomeDir()
	outside := filepath.Join(home, "matriline-ac-probe.txt")
	os.Remove(outside)
	defer os.Remove(outside)
	inside := filepath.Join(work, "inside.txt")
	cmd := sandboxCommand(os.Args[0], work, nil, 0, []string{systemShells()[0], "/c", "echo x>" + outside + " & echo y>" + inside}, true, false)
	if cmd == nil {
		t.Fatalf("no AppContainer: %s", sandboxErr)
	}
	cmd.Dir = work
	out, _ := cmd.CombinedOutput()
	t.Logf("output: %s", out)
	if _, err := os.Stat(inside); err != nil {
		t.Errorf("could not write in the job directory: %v", err)
	}
	if _, err := os.Stat(outside); err == nil {
		t.Errorf("wrote %s outside the job directory", outside)
	}
	// network: curl run directly (cmd would expand the % of -w); outside the container
	// it reaches url, inside it must not (exit 7: could not connect)
	curl := filepath.Join(os.Getenv("SystemRoot"), "System32", "curl.exe")
	args := []string{curl, "-s", "-m", "5", "-o", "NUL", url}
	if err := exec.Command(args[0], args[1:]...).Run(); err != nil {
		t.Fatalf("control: %s is not reachable outside the container either: %v", url, err)
	}
	cmd = sandboxCommand(os.Args[0], work, nil, 0, args, true, false)
	cmd.Dir = work
	if err := cmd.Run(); err == nil {
		t.Errorf("the container reached %s: it has network", url)
	} else {
		t.Logf("in the container: %v (no network, as wanted)", err)
	}
}
