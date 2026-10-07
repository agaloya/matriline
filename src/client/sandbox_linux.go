//go:build linux

package main

// ORCA sandbox for Linux (D43, layer 2).
//
// The client starts itself as a helper ("matriline-client sandbox-exec ...") inside new user
// and network namespaces (no network interfaces except loopback) when the kernel allows it.
// The helper then
//   1. sets PR_SET_NO_NEW_PRIVS,
//   2. lowers resource limits (no core dumps, maximum file size),
//   3. installs a Landlock ruleset: read-only access to system libraries, read+execute on the
//      ORCA installation and the dynamic loader only, full access to the job directory only,
//      no TCP bind/connect (ABI >= 4) and no signals / abstract sockets outside (ABI >= 6),
//   4. execs ORCA. All restrictions are inherited by every ORCA sub-program.
//
// Landlock usage follows the kernel documentation (Documentation/userspace-api/landlock.rst)
// and the reference sample samples/landlock/sandboxer.c by Mickaël Salaün (Linux kernel,
// GPL-2.0); the code below is an independent Go re-implementation of the same syscall
// sequence (create_ruleset -> add_rule(path_beneath) -> restrict_self), with ABI handling as
// described in that document. Syscall numbers 444/445/446 are the generic ones.

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"unsafe"
)

const (
	sysLandlockCreateRuleset = 444
	sysLandlockAddRule       = 445
	sysLandlockRestrictSelf  = 446

	llCreateRulesetVersion = 1
	llRulePathBeneath      = 1

	llExecute    = 1 << 0
	llWriteFile  = 1 << 1
	llReadFile   = 1 << 2
	llReadDir    = 1 << 3
	llRemoveDir  = 1 << 4
	llRemoveFile = 1 << 5
	llMakeChar   = 1 << 6
	llMakeDir    = 1 << 7
	llMakeReg    = 1 << 8
	llMakeSock   = 1 << 9
	llMakeFifo   = 1 << 10
	llMakeBlock  = 1 << 11
	llMakeSym    = 1 << 12
	llRefer      = 1 << 13 // ABI 2
	llTruncate   = 1 << 14 // ABI 3
	llIoctlDev   = 1 << 15 // ABI 5

	llNetBindTCP    = 1 << 0 // ABI 4
	llNetConnectTCP = 1 << 1

	llScopeAbstractUnix = 1 << 0 // ABI 6
	llScopeSignal       = 1 << 1

	prSetNoNewPrivs = 38
	oPath           = 0x200000 // O_PATH (not exported by package syscall); same value on amd64 and arm64
)

type llRulesetAttr struct {
	fs, net, scoped uint64
}

// llPathBeneath mirrors the packed struct landlock_path_beneath_attr (12 bytes).
type llPathBeneath struct {
	allowed uint64
	fd      int32
}

func landlockABI() int {
	r, _, e := syscall.Syscall(sysLandlockCreateRuleset, 0, 0, llCreateRulesetVersion)
	if e != 0 {
		return 0
	}
	return int(r)
}

// sandboxLevel describes what isolation is available (reported in the run report).
func sandboxLevel() string {
	abi := landlockABI()
	if abi == 0 {
		return "none (Landlock unavailable)"
	}
	return fmt.Sprintf("landlock-abi%d", abi)
}

// applyLandlock restricts the current process. workDir gets full access; roExec paths get
// read+execute; roPaths read-only; devFiles read/write (e.g. /dev/null).
func applyLandlock(workDir string, roExec, roPaths, devFiles []string, allowTCP bool) error {
	abi := landlockABI()
	if abi <= 0 {
		return errors.New("landlock not supported by this kernel")
	}
	fsAll := uint64(llExecute | llWriteFile | llReadFile | llReadDir | llRemoveDir | llRemoveFile |
		llMakeChar | llMakeDir | llMakeReg | llMakeSock | llMakeFifo | llMakeBlock | llMakeSym)
	if abi >= 2 {
		fsAll |= llRefer
	}
	if abi >= 3 {
		fsAll |= llTruncate
	}
	if abi >= 5 {
		fsAll |= llIoctlDev
	}
	attr := llRulesetAttr{fs: fsAll}
	size := uintptr(8)
	if abi >= 4 {
		size = 16
		if !allowTCP {
			attr.net = llNetBindTCP | llNetConnectTCP // handled, no rule -> denied
		}
	}
	if abi >= 6 {
		attr.scoped = llScopeAbstractUnix | llScopeSignal
		size = 24
	}
	rfd, _, e := syscall.Syscall(sysLandlockCreateRuleset, uintptr(unsafe.Pointer(&attr)), size, 0)
	if e != 0 {
		return fmt.Errorf("landlock_create_ruleset: %v", e)
	}
	defer syscall.Close(int(rfd))
	fileRights := uint64(llExecute|llWriteFile|llReadFile) | (fsAll & (llTruncate | llIoctlDev))
	add := func(p string, rights uint64) error {
		fd, err := syscall.Open(p, oPath|syscall.O_CLOEXEC, 0)
		if err != nil {
			return nil // missing optional path: nothing to allow
		}
		defer syscall.Close(fd)
		var st syscall.Stat_t
		if syscall.Fstat(fd, &st) == nil && st.Mode&syscall.S_IFMT != syscall.S_IFDIR {
			rights &= fileRights // only file rights are valid on files
		}
		rights &= fsAll
		rule := llPathBeneath{allowed: rights, fd: int32(fd)}
		_, _, e := syscall.Syscall6(sysLandlockAddRule, rfd, llRulePathBeneath, uintptr(unsafe.Pointer(&rule)), 0, 0, 0)
		if e != 0 {
			return fmt.Errorf("landlock_add_rule %s: %v", p, e)
		}
		return nil
	}
	if err := add(workDir, fsAll); err != nil {
		return err
	}
	for _, p := range roExec {
		if err := add(p, llExecute|llReadFile|llReadDir); err != nil {
			return err
		}
	}
	for _, p := range roPaths {
		if err := add(p, llReadFile|llReadDir); err != nil {
			return err
		}
	}
	for _, p := range devFiles {
		if err := add(p, llReadFile|llWriteFile); err != nil {
			return err
		}
	}
	if _, _, e := syscall.Syscall6(syscall.SYS_PRCTL, prSetNoNewPrivs, 1, 0, 0, 0, 0); e != 0 {
		return fmt.Errorf("prctl(NO_NEW_PRIVS): %v", e)
	}
	if _, _, e := syscall.Syscall(sysLandlockRestrictSelf, rfd, 0, 0); e != 0 {
		return fmt.Errorf("landlock_restrict_self: %v", e)
	}
	return nil
}

// The helper must run on the thread the client started: the parent-death signal (SIGKILL
// when the client ends) is set on that thread only, and capabilities, no_new_privs and the
// Landlock domain are per thread too; execve keeps only the state of the thread that calls
// it. Go moves goroutines between threads, so without this ORCA sometimes started without
// the parent-death signal or with CAP_NET_ADMIN (an ORCA outlived its stopped client).
// LockOSThread in init pins main to the main thread (runtime.LockOSThread documentation).
func init() {
	if len(os.Args) > 1 && os.Args[1] == "sandbox-exec" {
		runtime.LockOSThread()
	}
}

// sandboxExecMain is the helper entry point:
//
//	sandbox-exec <work> <exec dirs> <fsizeMB> [loopback] -- prog args...
//
// "loopback" (multi-core jobs) lets the job use TCP, which MPI needs between the processes
// of one job; it is honoured only inside a network namespace whose only interface is lo.
func sandboxExecMain(args []string) {
	loopback := len(args) > 3 && args[3] == "loopback"
	if loopback {
		args = append(args[:3:3], args[4:]...)
	}
	if len(args) < 5 || args[3] != "--" {
		fmt.Fprintln(os.Stderr, "sandbox-exec: bad arguments")
		os.Exit(125)
	}
	// args[1]: the directories whose programs may run (ORCA; OpenMPI for a multi-core job),
	// separated by the OS path-list separator
	work, execDirs := args[0], filepath.SplitList(args[1])
	fsizeMB, _ := strconv.ParseInt(args[2], 10, 64)
	prog := args[4:]
	syscall.Setrlimit(syscall.RLIMIT_CORE, &syscall.Rlimit{Cur: 0, Max: 0})
	if fsizeMB > 0 {
		lim := uint64(fsizeMB) << 20
		syscall.Setrlimit(syscall.RLIMIT_FSIZE, &syscall.Rlimit{Cur: lim, Max: lim})
	}
	// the dynamic loader must be executable for ORCA's (dynamically linked) programs
	var loaders []string
	for _, l := range []string{"/lib64/ld-linux-x86-64.so.2", "/lib/ld-linux-aarch64.so.1", "/lib/ld-linux-riscv64-lp64d.so.1"} {
		if r, err := filepath.EvalSymlinks(l); err == nil {
			loaders = append(loaders, r, l)
		}
	}
	// ORCA starts some of its sub-programs through system(3), i.e. /bin/sh -c "...", so the
	// system shell must be executable too (found while testing ORCA 6.1.1 in this sandbox).
	roExec := append(append(append([]string{}, execDirs...), loaders...), systemShells()...)
	for _, d := range execDirs {
		if r, err := filepath.EvalSymlinks(d); err == nil && r != d {
			roExec = append(roExec, r)
		}
	}
	// a new network namespace has only "lo", and down: MPI processes of one job talk over
	// loopback (and shared memory in the work directory). No other interface exists, so
	// this opens nothing to the outside. Outside a namespace (lo already up) it is a no-op.
	bringLoopbackUp()
	if err := dropCapabilities(); err != nil {
		fmt.Fprintf(os.Stderr, "sandbox-exec: %v\n", err)
		os.Exit(126)
	}
	roPaths := []string{"/usr/lib", "/usr/lib64", "/lib", "/lib64", "/usr/share/zoneinfo",
		"/etc/ld.so.cache", "/etc/ld.so.conf", "/etc/ld.so.conf.d", "/etc/localtime",
		"/etc/nsswitch.conf", "/etc/passwd", "/etc/group", "/etc/hosts",
		"/proc", "/sys/devices/system/cpu", "/sys/fs/cgroup", "/dev/urandom", "/dev/random"}
	devFiles := []string{"/dev/null", "/dev/zero", "/dev/shm"}
	if loopback && !onlyLoopback() {
		fmt.Fprintln(os.Stderr, "sandbox-exec: a multi-core job needs its own network namespace (only lo); refusing to allow TCP here")
		os.Exit(126)
	}
	if err := applyLandlock(work, roExec, roPaths, devFiles, loopback); err != nil {
		fmt.Fprintf(os.Stderr, "sandbox-exec: %v\n", err)
		os.Exit(126)
	}
	err := syscall.Exec(prog[0], prog, os.Environ())
	fmt.Fprintf(os.Stderr, "sandbox-exec: exec %s: %v\n", prog[0], err)
	os.Exit(127)
}

func sandboxDesc(strong bool) string {
	if strong {
		return sandboxLevel() + "+userns+netns"
	}
	return sandboxLevel()
}

// sandboxErr: on Linux sandboxCommand never returns nil (the helper reports its errors).
var sandboxErr = ""

// sandboxCommand wraps ORCA in the helper. useNS requests user+network namespaces.
func sandboxCommand(self, work string, execDirs []string, fsizeMB int64, prog []string, useNS, loopback bool) *exec.Cmd {
	args := []string{"sandbox-exec", work, strings.Join(execDirs, string(filepath.ListSeparator)), strconv.FormatInt(fsizeMB, 10)}
	if loopback {
		args = append(args, "loopback")
	}
	args = append(append(args, "--"), prog...)
	cmd := exec.Command(self, args...)
	attr := &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGKILL}
	if useNS {
		uid, gid := os.Getuid(), os.Getgid()
		// CLONE_NEWPID: ORCA (exec'd by the helper) becomes PID 1 of its own namespace, so
		// when it exits or is killed (also through Pdeathsig when the client dies) the
		// kernel kills every descendant. Without it, orca_* grandchildren survived a client
		// restart and kept writing into the work directory while the resumed run started a
		// second ORCA there (seen in the lab). See pid_namespaces(7).
		attr.Cloneflags = syscall.CLONE_NEWUSER | syscall.CLONE_NEWNET | syscall.CLONE_NEWPID
		attr.UidMappings = []syscall.SysProcIDMap{{ContainerID: uid, HostID: uid, Size: 1}}
		attr.GidMappings = []syscall.SysProcIDMap{{ContainerID: gid, HostID: gid, Size: 1}}
		attr.GidMappingsEnableSetgroups = false
		// the helper keeps CAP_NET_ADMIN (inside its own user namespace only) just to bring
		// up the loopback interface of its new network namespace; it drops every capability
		// before it starts ORCA (dropCapabilities)
		attr.AmbientCaps = []uintptr{capNetAdmin}
	}
	cmd.SysProcAttr = attr
	return cmd
}

// onlyLoopback reports whether the current network namespace has no interface but lo
// (/proc/net/dev lists them; proc(5)).
func onlyLoopback() bool {
	b, err := os.ReadFile("/proc/net/dev")
	if err != nil {
		return false
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(lines) < 3 {
		return false
	}
	for _, ln := range lines[2:] {
		name, _, _ := strings.Cut(strings.TrimSpace(ln), ":")
		if name != "lo" {
			return false
		}
	}
	return true
}

// bringLoopbackUp sets IFF_UP on "lo" (SIOCGIFFLAGS/SIOCSIFFLAGS, netdevice(7)); errors
// are ignored: without it a multi-core job fails visibly and single-core jobs need nothing.
func bringLoopbackUp() {
	fd, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_DGRAM, 0)
	if err != nil {
		return
	}
	defer syscall.Close(fd)
	var ifr [40]byte // struct ifreq: name[16], then a union whose first short is ifr_flags
	copy(ifr[:], "lo")
	if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), syscall.SIOCGIFFLAGS, uintptr(unsafe.Pointer(&ifr[0]))); e != 0 {
		return
	}
	flags := *(*uint16)(unsafe.Pointer(&ifr[16])) | syscall.IFF_UP
	*(*uint16)(unsafe.Pointer(&ifr[16])) = flags
	syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), syscall.SIOCSIFFLAGS, uintptr(unsafe.Pointer(&ifr[0])))
}

const (
	capNetAdmin          = 12 // CAP_NET_ADMIN, linux/capability.h
	prCapAmbient         = 47 // PR_CAP_AMBIENT, linux/prctl.h
	prCapAmbientClearAll = 4
	linuxCapabilityVer3  = 0x20080522
)

// dropCapabilities clears the ambient set and the effective, permitted and inheritable
// sets (capset(2), version 3), so ORCA runs with no capability at all, as before.
func dropCapabilities() error {
	if _, _, e := syscall.RawSyscall6(syscall.SYS_PRCTL, prCapAmbient, prCapAmbientClearAll, 0, 0, 0, 0); e != 0 && e != syscall.EINVAL {
		return fmt.Errorf("clearing ambient capabilities: %v", e)
	}
	hdr := struct {
		version uint32
		pid     int32
	}{linuxCapabilityVer3, 0}
	var data [2]struct{ effective, permitted, inheritable uint32 }
	if _, _, e := syscall.RawSyscall(syscall.SYS_CAPSET, uintptr(unsafe.Pointer(&hdr)), uintptr(unsafe.Pointer(&data[0])), 0); e != 0 {
		return fmt.Errorf("dropping capabilities: %v", e)
	}
	return nil
}

// plainCommand runs ORCA without sandbox (sandbox disabled by the user).
func plainCommand(prog []string) *exec.Cmd {
	cmd := exec.Command(prog[0], prog[1:]...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGKILL}
	return cmd
}

// killGroup kills the whole process group of a job.
func killGroup(cmd *exec.Cmd) {
	if cmd.Process != nil {
		syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}

// systemShells returns /bin/sh and /usr/bin/sh plus their resolved targets.
func systemShells() []string {
	var out []string
	for _, p := range []string{"/bin/sh", "/usr/bin/sh"} {
		if r, err := filepath.EvalSymlinks(p); err == nil {
			out = append(out, p, r)
		}
	}
	return out
}
