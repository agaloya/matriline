//go:build darwin

package main

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/agaloya/matriline/common/orcafind"
)

// The test binary doubles as the sandbox helper (like "matriline-client sandbox-exec") and as
// the program run inside the sandbox (ML_TEST_PROBE: one action, its outcome on stdout).
func TestMain(m *testing.M) {
	if os.Getenv("ML_TEST_HELPER") == "1" && len(os.Args) > 1 && os.Args[1] == "sandbox-exec" {
		sandboxExecMain(os.Args[2:])
		return
	}
	if p := os.Getenv("ML_TEST_PROBE"); p != "" {
		probeAction(p)
		return
	}
	os.Exit(m.Run())
}

// probeAction does "connect <addr>", "write <file>" or "read <file>" and prints "ok" or
// "denied: <error>".
func probeAction(p string) {
	verb, arg, _ := strings.Cut(p, " ")
	var err error
	switch verb {
	case "connect":
		var c net.Conn
		if c, err = net.DialTimeout("tcp", arg, 3*time.Second); err == nil {
			c.Close()
		}
	case "write":
		err = os.WriteFile(arg, []byte("x\n"), 0o644)
	case "read":
		_, err = os.ReadFile(arg)
	default:
		err = fmt.Errorf("unknown probe %q", verb)
	}
	if err != nil {
		fmt.Println("denied:", err)
		return
	}
	fmt.Println("ok")
}

// sandboxed runs the test binary inside the sandbox of a job whose directory is work and
// returns what the probe printed.
func sandboxed(t *testing.T, work, probe string) string {
	t.Helper()
	self, err := filepath.Abs(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	cmd := sandboxCommand(self, work, []string{filepath.Dir(self)}, 0, []string{self}, true, false)
	if cmd == nil {
		t.Fatalf("no sandbox: %s", sandboxErr)
	}
	cmd.Dir = work
	cmd.Env = []string{"ML_TEST_HELPER=1", "ML_TEST_PROBE=" + probe, "HOME=" + work, "TMPDIR=" + work}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s: %v\n%s", probe, err, out)
	}
	return strings.TrimSpace(string(out))
}

// jobDir makes <tmp>/<attempt>/work, the layout of a real job (the profile goes next to work).
func jobDir(t *testing.T) string {
	work := filepath.Join(t.TempDir(), "attempt", "work")
	if err := makeWorkDir(work); err != nil {
		t.Fatal(err)
	}
	return work
}

// The sandboxed program cannot open a TCP connection, not even to 127.0.0.1.
func TestSandboxNoNetwork(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	if out := sandboxed(t, jobDir(t), "connect "+ln.Addr().String()); !strings.HasPrefix(out, "denied:") {
		t.Errorf("connected to %s from the sandbox: %s", ln.Addr(), out)
	}
	// the same probe outside the sandbox does connect: the refusal above is the sandbox's
	c, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("control connection: %v", err)
	}
	c.Close()
}

// The sandboxed program can write in its job directory and nowhere else (here a directory
// standing for the user's home).
func TestSandboxWritesOnlyInJob(t *testing.T) {
	work := jobDir(t)
	home := t.TempDir()
	if out := sandboxed(t, work, "write "+filepath.Join(work, "inside.txt")); out != "ok" {
		t.Errorf("cannot write in the job directory: %s", out)
	}
	outside := filepath.Join(home, "outside.txt")
	if out := sandboxed(t, work, "write "+outside); !strings.HasPrefix(out, "denied:") {
		t.Errorf("wrote outside the job directory: %s", out)
	}
	if _, err := os.Stat(outside); err == nil {
		t.Errorf("%s exists", outside)
	}
	// nor next to it: the profile lives in the job's parent directory
	if out := sandboxed(t, work, "write "+filepath.Join(filepath.Dir(work), "sandbox.sb")); !strings.HasPrefix(out, "denied:") {
		t.Errorf("rewrote its own profile: %s", out)
	}
}

// The sandboxed program cannot read the user's files (a stand-in for ~/.ssh/id_ed25519).
func TestSandboxCannotReadHome(t *testing.T) {
	work := jobDir(t)
	ssh := filepath.Join(t.TempDir(), ".ssh")
	os.MkdirAll(ssh, 0o700)
	key := filepath.Join(ssh, "id_ed25519")
	if err := os.WriteFile(key, []byte("not a real key\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if out := sandboxed(t, work, "read "+key); !strings.HasPrefix(out, "denied:") {
		t.Errorf("read %s from the sandbox: %s", key, out)
	}
	if out := sandboxed(t, work, "read "+filepath.Join(filepath.Dir(work), "sandbox.sb")); !strings.HasPrefix(out, "denied:") {
		t.Errorf("read its profile: %s", out)
	}
}

// A real ORCA job (HF/STO-3G water) runs inside the sandbox. ORCA comes from ML_TEST_ORCA or
// the usual install places; the test is skipped without one.
func TestSandboxRunsOrca(t *testing.T) {
	dir := os.Getenv("ML_TEST_ORCA")
	if dir == "" {
		if d := orcafind.Dirs(); len(d) > 0 {
			dir = d[0]
		}
	}
	if dir == "" {
		t.Skip("no ORCA on this computer (set ML_TEST_ORCA)")
	}
	if q := quarantinedFiles(dir); len(q) > 0 {
		t.Skipf("ORCA at %s is quarantined by Gatekeeper (it would hang): %s", dir, quarantineHint(dir))
	}
	work := jobDir(t)
	inp := "! HF STO-3G\n* xyz 0 1\nO 0 0 0.1173\nH 0 0.7572 -0.4692\nH 0 -0.7572 -0.4692\n*\n"
	if err := os.WriteFile(filepath.Join(work, "w.inp"), []byte(inp), 0o644); err != nil {
		t.Fatal(err)
	}
	self, _ := filepath.Abs(os.Args[0])
	cmd := sandboxCommand(self, work, []string{dir}, 0, []string{orcaExe(dir), "w.inp"}, true, false)
	if cmd == nil {
		t.Fatalf("no sandbox: %s", sandboxErr)
	}
	cmd.Dir = work
	cmd.Env = append(orcaEnv(dir, work), "ML_TEST_HELPER=1")
	done := make(chan struct{})
	timer := time.AfterFunc(5*time.Minute, func() { killGroup(cmd); close(done) })
	out, err := cmd.CombinedOutput()
	timer.Stop()
	select {
	case <-done:
		t.Fatal("ORCA did not finish in 5 minutes")
	default:
	}
	if err != nil || !strings.Contains(string(out), "ORCA TERMINATED NORMALLY") {
		tail := string(out)
		if len(tail) > 2000 {
			tail = tail[len(tail)-2000:]
		}
		t.Fatalf("ORCA in the sandbox: %v\n%s", err, tail)
	}
	for _, ln := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(ln, "FINAL SINGLE POINT ENERGY") {
			t.Log(strings.TrimSpace(ln))
		}
	}
}
