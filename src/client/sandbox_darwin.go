//go:build darwin

package main

// ORCA sandbox for macOS.
//
// The client writes a sandbox profile (SBPL, the Scheme-like language of Apple's Seatbelt) for
// each job next to its work directory and starts itself as a helper ("matriline-client
// sandbox-exec ..."), which lowers the resource limits (no core dumps, maximum file size) and
// execs /usr/bin/sandbox-exec -f <profile> <orca ...>. The kernel then enforces the profile on
// ORCA and every program it starts:
//   - everything is denied by default ("deny default"), the network included: there is no
//     network rule at all, so ORCA cannot open any socket, not even a unix one (it tries
//     /private/var/run/syslog and is refused);
//   - ORCA's directory (and OpenMPI's) may be read and executed, plus /bin/sh and the shell it
//     forwards to, because ORCA starts its modules through system(3);
//   - the job directory is the only place that can be written (and /dev/null);
//   - what dyld, libSystem, the time zone and Rosetta 2 need is readable, nothing else: no
//     home directory, no /Users, no /tmp.
// The rules were found by running ORCA 6.1.1 (arm64, and x86-64 under Rosetta 2) under
// "deny default" and adding only what the kernel's denial log proved was needed
// (log stream --predicate 'sender == "Sandbox"'); refusals that ORCA survives (sysctl kern.*,
// syslog, /dev/tty, /dev/dtracehelper, mach-lookup of the notification center) stay denied.
//
// sandbox-exec and the profile language are deprecated by Apple but still work, and are what
// Apple's own daemons use; the alternative, the App Sandbox, needs a signed app bundle.

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

const sandboxExecBin = "/usr/bin/sandbox-exec"

// sandboxErr is why the last sandboxCommand returned nil (shown by the probe).
var sandboxErr string

func sandboxDesc(strong bool) string { return "sandbox-exec (no network)" }

// sandboxExecMain is the helper: sandbox-exec <profile> <fsizeMB> -- prog args...
func sandboxExecMain(args []string) {
	if len(args) < 4 || args[2] != "--" {
		fmt.Fprintln(os.Stderr, "sandbox-exec: bad arguments")
		os.Exit(125)
	}
	profile, prog := args[0], args[3:]
	fsizeMB, _ := strconv.ParseInt(args[1], 10, 64)
	syscall.Setrlimit(syscall.RLIMIT_CORE, &syscall.Rlimit{Cur: 0, Max: 0})
	if fsizeMB > 0 {
		lim := uint64(fsizeMB) << 20
		syscall.Setrlimit(syscall.RLIMIT_FSIZE, &syscall.Rlimit{Cur: lim, Max: lim})
	}
	argv := append([]string{"sandbox-exec", "-f", profile}, prog...)
	err := syscall.Exec(sandboxExecBin, argv, os.Environ())
	fmt.Fprintf(os.Stderr, "sandbox-exec: exec %s: %v\n", sandboxExecBin, err)
	os.Exit(127)
}

// sandboxCommand writes the job's profile next to work (out of ORCA's reach) and wraps prog
// in the helper. There is a single level on macOS, so useNS is ignored. A multi-core job
// (loopback) is refused: OpenMPI needs a TCP listener, and no profile rule confines one to
// the loopback (docs/PORTING.md, "Multi-core jobs on macOS").
func sandboxCommand(self, work string, execDirs []string, fsizeMB int64, prog []string, useNS, loopback bool) *exec.Cmd {
	if loopback {
		sandboxErr = "multi-core jobs are not supported by the macOS sandbox yet"
		return nil
	}
	if _, err := os.Stat(sandboxExecBin); err != nil {
		sandboxErr = sandboxExecBin + " not found"
		return nil
	}
	profile := filepath.Join(filepath.Dir(work), "sandbox.sb")
	text, err := sandboxProfile(work, execDirs)
	if err == nil {
		err = os.WriteFile(profile, []byte(text), 0o600)
	}
	if err != nil {
		sandboxErr = "writing the sandbox profile: " + err.Error()
		return nil
	}
	args := append([]string{"sandbox-exec", profile, strconv.FormatInt(fsizeMB, 10), "--"}, prog...)
	cmd := exec.Command(self, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	return cmd
}

// sandboxProfile is the SBPL text for one job. The kernel matches resolved paths
// (/var is /private/var, /tmp is /private/tmp), so every path is resolved first.
func sandboxProfile(work string, execDirs []string) (string, error) {
	w, err := filepath.EvalSymlinks(work)
	if err != nil {
		return "", err
	}
	var execs []string // directories whose programs may run
	for _, d := range execDirs {
		r, err := filepath.EvalSymlinks(d)
		if err != nil {
			return "", err
		}
		execs = append(execs, r)
	}
	var b strings.Builder
	b.WriteString("(version 1)\n(deny default)\n(allow process-fork)\n")
	b.WriteString("(allow process-exec")
	for _, d := range execs {
		b.WriteString(" (subpath " + sbplString(d) + ")")
	}
	for _, s := range systemShells() {
		b.WriteString(" (literal " + sbplString(s) + ")")
	}
	b.WriteString(")\n")
	b.WriteString("(allow file-read*")
	for _, d := range append(execs, w) {
		b.WriteString(" (subpath " + sbplString(d) + ")")
	}
	for _, s := range systemShells() {
		b.WriteString(" (literal " + sbplString(s) + ")")
	}
	b.WriteString(`
  (literal "/private/var/select/sh")
  (literal "/")
  (subpath "/usr/lib")
  (subpath "/System")
  (subpath "/private/var/db/dyld")
  (literal "/dev/null") (literal "/dev/zero") (literal "/dev/random") (literal "/dev/urandom")
  (literal "/private/etc/localtime")
  (subpath "/private/var/db/timezone")
  (subpath "/usr/share/zoneinfo")
  (subpath "/usr/share/zoneinfo.default")
  (subpath "/Library/Apple/usr/libexec/oah")
  (subpath "/private/var/db/oah"))
(allow file-read-metadata
  (literal "/var") (literal "/etc") (literal "/tmp") (literal "/private") (literal "/private/var")
  (literal "/private/var/select") (literal "/usr") (literal "/bin") (literal "/etc/localtime")
  (literal "/usr/libexec/rosetta/runtime"))
(allow sysctl-read (sysctl-name-prefix "hw."))
`)
	b.WriteString("(allow file-write* (subpath " + sbplString(w) + `) (literal "/dev/null"))` + "\n")
	return b.String(), nil
}

// sbplString quotes s as an SBPL string literal.
func sbplString(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

// plainCommand runs ORCA without sandbox (sandbox disabled by the user), in its own process
// group so that killGroup ends its modules too.
func plainCommand(prog []string) *exec.Cmd {
	cmd := exec.Command(prog[0], prog[1:]...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	return cmd
}

// killGroup kills the whole process group of a job.
func killGroup(cmd *exec.Cmd) {
	if cmd.Process != nil {
		syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}

// systemShells returns /bin/sh and the shell it forwards to: on macOS /bin/sh is a small
// program that runs the shell named by the link /private/var/select/sh (bash by default).
func systemShells() []string {
	out := []string{"/bin/sh"}
	if t, err := os.Readlink("/private/var/select/sh"); err == nil && filepath.IsAbs(t) {
		if r, err := filepath.EvalSymlinks(t); err == nil {
			out = append(out, r)
		}
	}
	return out
}
