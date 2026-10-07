package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"unsafe"
)

// The test binary doubles as the sandbox helper (like "matriline-client sandbox-exec").
func TestMain(m *testing.M) {
	if os.Getenv("ML_TEST_HELPER") == "1" && len(os.Args) > 1 && os.Args[1] == "sandbox-exec" {
		if os.Getenv("ML_TEST_CHURN") == "1" {
			// busy goroutines that make the scheduler move the helper's goroutine between threads
			for range 2 * runtime.NumCPU() {
				go func() {
					for {
						runtime.Gosched()
					}
				}()
			}
		}
		sandboxExecMain(os.Args[2:])
		return
	}
	if os.Getenv("ML_TEST_PROBE") == "1" { // the program the helper starts: report what it got
		var sig int32
		syscall.RawSyscall(syscall.SYS_PRCTL, syscall.PR_GET_PDEATHSIG, uintptr(unsafe.Pointer(&sig)), 0)
		b, _ := os.ReadFile("/proc/self/status")
		fmt.Printf("PDeathSig: %d\n", sig)
		for _, ln := range strings.Split(string(b), "\n") {
			if strings.HasPrefix(ln, "Cap") || strings.HasPrefix(ln, "NoNewPrivs") {
				fmt.Println(ln)
			}
		}
		return
	}
	os.Exit(m.Run())
}

// The helper may hold CAP_NET_ADMIN (to bring up loopback for MPI jobs) but the program it
// starts must have no capability at all.
func TestSandboxDropsCapabilities(t *testing.T) {
	if sandboxLevel() == "none" || landlockABI() < 1 {
		t.Skip("no landlock")
	}
	work := t.TempDir()
	prog := []string{"/bin/sh", "-c", `while read k v; do case $k in Cap*) echo "$k $v";; esac; done < /proc/self/status`}
	for _, ns := range []bool{true, false} {
		cmd := sandboxCommand(os.Args[0], work, []string{work}, 0, prog, ns, false)
		cmd.Env = []string{"ML_TEST_HELPER=1", "PATH=/usr/bin:/bin"}
		out, err := cmd.CombinedOutput()
		if err != nil {
			if ns && strings.Contains(string(out)+err.Error(), "operation not permitted") {
				t.Logf("user namespaces unavailable here: %v", err)
				continue
			}
			t.Fatalf("namespaces=%v: %v\n%s", ns, err, out)
		}
		for _, ln := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			f := strings.Fields(ln)
			if len(f) != 2 || (f[0] != "CapBnd:" && strings.Trim(f[1], "0") != "") {
				t.Errorf("namespaces=%v: %s", ns, ln)
			}
		}
		t.Logf("namespaces=%v:\n%s", ns, out)
	}
}

// Every restriction the helper sets is per thread (capabilities, no_new_privs, Landlock) and
// the parent-death signal belongs to the thread the client started; Go may move a goroutine
// to another thread at any time, and execve keeps only the calling thread's state. ORCA must
// get all of them on every start, also when the helper's scheduler is busy (an ORCA once
// kept CAP_NET_ADMIN and outlived its stopped client).
func TestSandboxExecKeepsThreadState(t *testing.T) {
	if sandboxLevel() == "none" || landlockABI() < 1 {
		t.Skip("no landlock")
	}
	self, err := filepath.Abs(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	work := t.TempDir()
	runs := 200
	if testing.Short() {
		runs = 20
	}
	for _, ns := range []bool{true, false} {
		for i := range runs {
			cmd := sandboxCommand(self, work, []string{filepath.Dir(self)}, 0, []string{self}, ns, false)
			cmd.Env = []string{"ML_TEST_HELPER=1", "ML_TEST_CHURN=1", "ML_TEST_PROBE=1", "GOMAXPROCS=8"}
			out, err := cmd.CombinedOutput()
			if err != nil {
				if ns && strings.Contains(string(out)+err.Error(), "operation not permitted") {
					t.Logf("user namespaces unavailable here: %v", err)
					break
				}
				t.Fatalf("namespaces=%v run %d: %v\n%s", ns, i, err, out)
			}
			for _, ln := range strings.Split(strings.TrimSpace(string(out)), "\n") {
				k, v, _ := strings.Cut(ln, ":")
				v = strings.TrimSpace(v)
				bad := false
				switch {
				case k == "PDeathSig":
					bad = v != fmt.Sprint(int(syscall.SIGKILL))
				case k == "NoNewPrivs":
					bad = v != "1"
				case k != "CapBnd":
					bad = strings.Trim(v, "0") != ""
				}
				if bad {
					t.Fatalf("namespaces=%v run %d: %s\n%s", ns, i, ln, out)
				}
			}
		}
	}
}
