//go:build windows

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"
)

// Windows sandbox, first level: ORCA runs at LOW integrity (Mandatory Integrity Control,
// https://learn.microsoft.com/en-us/windows/win32/secauthz/mandatory-integrity-control).
// A low-integrity process cannot write to anything labelled medium or higher, i.e. the
// user's files, settings and other programs: only to its job directory, which the client
// labels low. It can still read and use the network, so it is reported as "low-integrity",
// not as isolation. Full isolation (AppContainer: no network, only the job and ORCA
// directories) is the next level; it needs an administrator once to let the container
// read ORCA's directory. Children (ORCA's modules, cmd.exe) inherit the integrity level,
// and the job object still ends the whole tree.

var (
	modAdvapi32                = syscall.NewLazyDLL("advapi32.dll")
	procDuplicateTokenEx       = modAdvapi32.NewProc("DuplicateTokenEx")
	procSetTokenInformation    = modAdvapi32.NewProc("SetTokenInformation")
	procConvertStringSidToSidW = modAdvapi32.NewProc("ConvertStringSidToSidW")
	procConvertStringSDToSDW   = modAdvapi32.NewProc("ConvertStringSecurityDescriptorToSecurityDescriptorW")
	procSetFileSecurityW       = modAdvapi32.NewProc("SetFileSecurityW")
	procGetFileSecurityW       = modAdvapi32.NewProc("GetFileSecurityW")
	procConvertSDToStringSDW   = modAdvapi32.NewProc("ConvertSecurityDescriptorToStringSecurityDescriptorW")
	procGetLengthSid           = modAdvapi32.NewProc("GetLengthSid")
)

const (
	tokenIntegrityLevel      = 25 // TOKEN_INFORMATION_CLASS TokenIntegrityLevel
	seGroupIntegrity         = 0x20
	securityImpersonation    = 2
	tokenPrimary             = 1
	labelSecurityInformation = 0x10
	daclSecurityInformation  = 0x04
	// enough to duplicate the token; asking for more (e.g. TOKEN_ADJUST_SESSIONID) fails
	// under a normal user's limited token, e.g. when started by the scheduled task
	tokenAllAccessForDuplicate = syscall.TOKEN_DUPLICATE | syscall.TOKEN_QUERY
)

// sandboxErr is why the last sandboxCommand returned nil (shown by the probe).
var sandboxErr string

// sandboxStrong: job directories are made for the AppContainer level (setSandboxStrong).
var sandboxStrong = true

func setSandboxStrong(strong bool) { sandboxStrong = strong }

// sandboxDesc names the level: AppContainer (strong) or low integrity.
func sandboxDesc(strong bool) string {
	if strong {
		return "appcontainer (no network)"
	}
	return "low-integrity"
}

// sandboxHint explains the one administrator step the AppContainer level needs: app
// containers must be able to read ORCA's folder and to see each folder above it (cmd.exe,
// which ORCA uses to start its modules, looks at every folder of the path; without the
// drive root it got "Access is denied" on Windows 11).
func sandboxHint(orcaDir string) string {
	cmds := []string{fmt.Sprintf(`icacls "%s" /grant "*S-1-15-2-1:(OI)(CI)RX"`, orcaDir)}
	for d := filepath.Dir(orcaDir); ; d = filepath.Dir(d) {
		cmds = append(cmds, fmt.Sprintf(`icacls "%s" /grant "*S-1-15-2-1:(RX)"`, d))
		if filepath.Dir(d) == d {
			break
		}
	}
	return "for full isolation (no network) Windows app containers must be able to read ORCA's folder; " +
		"an administrator runs once: " + strings.Join(cmds, " & ") +
		" (until then ORCA runs at low integrity: it cannot write outside its job, but it could use the network)"
}

// sandboxExecMain is the AppContainer helper: sandbox-exec <work> -- prog args...
func sandboxExecMain(args []string) {
	if len(args) < 3 || args[1] != "--" {
		fmt.Fprintln(os.Stderr, "sandbox-exec: bad arguments")
		os.Exit(125)
	}
	containerExec(args[0], args[2:])
}

// sandboxCommand runs prog with a low-integrity copy of this process's token, after
// labelling the work directory low (so ORCA can write there and nowhere else).
func sandboxCommand(self, work string, execDirs []string, fsizeMB int64, prog []string, useNS, loopback bool) *exec.Cmd {
	if !isLow(work) {
		sandboxErr = work + " was not created with the low label (makeWorkDir)"
		return nil
	}
	// a multi-core job (MS-MPI) runs at low integrity: MS-MPI's process manager (smpd)
	// cannot start in an app container (it dies with 0xC0000142 while loading its
	// libraries; tested with MS-MPI 10.1.1 on Windows 11), so such a job can write only in
	// its directory but is not cut off from the network
	if useNS && !loopback { // strong level: AppContainer through the helper
		sid := containerSIDString()
		if sid == "" {
			sandboxErr = "app containers are not available"
			return nil
		}
		// a job resumed after a restart runs in its old directory, which may come from the
		// low-integrity level (or an older build) and lack the container's entry: ORCA
		// would fail with "access denied", which looks like ORCA's fault (code review)
		if !strings.Contains(securityText(work, daclSecurityInformation), sid) {
			sandboxErr = work + " was made for the low-integrity level, not for the app container"
			return nil
		}
		return exec.Command(self, append([]string{"sandbox-exec", work, "--"}, prog...)...)
	}
	tok, err := lowIntegrityToken()
	if err != nil {
		sandboxErr = err.Error()
		return nil
	}
	cmd := exec.Command(prog[0], prog[1:]...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Token: tok}
	return cmd
}

// makeWorkDir creates a job directory with the low mandatory label, inherited by
// everything created in it (SDDL "S:(ML;OICI;NW;;;LW)": no-write-up, low). The label must be
// given at creation: changing it later needs WRITE_OWNER, which a normal user's (limited)
// token does not have on its own folders (only an elevated administrator's does; found
// when the scheduled task started the client).
// makeJobDir creates a job's directory for the level the job runs at: a multi-core job
// runs at low integrity, and the container's entry would stop it from writing there.
func makeJobDir(dir string, procs int) error {
	return makeWorkDirLevel(dir, sandboxStrong && procs <= 1)
}

// mpiSandboxDesc names the level a multi-core job runs at.
func mpiSandboxDesc() string { return "low-integrity (multi-core job: no network isolation)" }

func makeWorkDir(dir string) error { return makeWorkDirLevel(dir, sandboxStrong) }

func makeWorkDirLevel(dir string, strong bool) error {
	if err := os.MkdirAll(filepath.Dir(dir), 0o700); err != nil {
		return err
	}
	// DACL: this user, SYSTEM, Administrators and the job container (full control,
	// inherited), protected from the parent's entries; SACL: the low label
	// The container's entry only when the container is used: with it a low-integrity
	// process could not write in the directory any more (seen on Windows 11).
	sids := []string{currentUserSID(), "SY", "BA"}
	if strong {
		sids = append(sids, containerSIDString())
	}
	acl := "D:P"
	for _, sid := range sids {
		if sid != "" {
			acl += "(A;OICI;FA;;;" + sid + ")"
		}
	}
	sddl, _ := syscall.UTF16PtrFromString(acl + "S:(ML;OICI;NW;;;LW)")
	var sd uintptr
	if r, _, err := procConvertStringSDToSDW.Call(uintptr(unsafe.Pointer(sddl)), 1, uintptr(unsafe.Pointer(&sd)), 0); r == 0 {
		return fmt.Errorf("security descriptor: %v", err)
	}
	defer syscall.LocalFree(syscall.Handle(sd))
	sa := syscall.SecurityAttributes{Length: uint32(unsafe.Sizeof(syscall.SecurityAttributes{})), SecurityDescriptor: sd}
	p, _ := syscall.UTF16PtrFromString(dir)
	if err := syscall.CreateDirectory(p, &sa); err != nil && err != syscall.ERROR_ALREADY_EXISTS {
		return fmt.Errorf("creating %s: %v", dir, err)
	}
	return nil
}

// isLow reports whether dir carries the low mandatory label.
func isLow(dir string) bool {
	return strings.Contains(securityText(dir, labelSecurityInformation), ";;;LW)")
}

// securityText is part of dir's security descriptor (what: label or DACL) as SDDL text.
func securityText(dir string, what uint32) string {
	p, _ := syscall.UTF16PtrFromString(dir)
	var need uint32
	procGetFileSecurityW.Call(uintptr(unsafe.Pointer(p)), uintptr(what), 0, 0, uintptr(unsafe.Pointer(&need)))
	if need == 0 {
		return ""
	}
	buf := make([]byte, need)
	if r, _, _ := procGetFileSecurityW.Call(uintptr(unsafe.Pointer(p)), uintptr(what), uintptr(unsafe.Pointer(&buf[0])), uintptr(need), uintptr(unsafe.Pointer(&need))); r == 0 {
		return ""
	}
	var str *uint16
	if r, _, _ := procConvertSDToStringSDW.Call(uintptr(unsafe.Pointer(&buf[0])), 1, uintptr(what), uintptr(unsafe.Pointer(&str)), 0); r == 0 {
		return ""
	}
	defer syscall.LocalFree(syscall.Handle(unsafe.Pointer(str)))
	return utf16PtrString(str)
}

// lowIntegrityToken duplicates this process's token and lowers its integrity level.
func lowIntegrityToken() (syscall.Token, error) {
	var cur syscall.Token
	if err := syscall.OpenProcessToken(syscall.Handle(^uintptr(0)) /* current process */, tokenAllAccessForDuplicate, &cur); err != nil {
		return 0, fmt.Errorf("OpenProcessToken: %v", err)
	}
	defer cur.Close()
	var dup syscall.Token
	if r, _, err := procDuplicateTokenEx.Call(uintptr(cur), 0x02000000 /* MAXIMUM_ALLOWED */, 0, securityImpersonation, tokenPrimary, uintptr(unsafe.Pointer(&dup))); r == 0 {
		return 0, fmt.Errorf("DuplicateTokenEx: %v", err)
	}
	sidStr, _ := syscall.UTF16PtrFromString("S-1-16-4096") // low mandatory level
	var sid uintptr
	if r, _, err := procConvertStringSidToSidW.Call(uintptr(unsafe.Pointer(sidStr)), uintptr(unsafe.Pointer(&sid))); r == 0 {
		dup.Close()
		return 0, fmt.Errorf("ConvertStringSidToSid: %v", err)
	}
	defer syscall.LocalFree(syscall.Handle(sid))
	type sidAndAttributes struct {
		Sid        uintptr
		Attributes uint32
	}
	label := sidAndAttributes{Sid: sid, Attributes: seGroupIntegrity}
	n, _, _ := procGetLengthSid.Call(sid)
	if r, _, err := procSetTokenInformation.Call(uintptr(dup), tokenIntegrityLevel, uintptr(unsafe.Pointer(&label)), unsafe.Sizeof(label)+n); r == 0 {
		dup.Close()
		return 0, fmt.Errorf("SetTokenInformation: %v", err)
	}
	return dup, nil
}

func plainCommand(prog []string) *exec.Cmd { return exec.Command(prog[0], prog[1:]...) }

// systemShells: the shell ORCA uses for its own helper calls (cmd.exe).
func systemShells() []string {
	return []string{getenvDefault("SystemRoot", `C:\Windows`) + `\System32\cmd.exe`}
}

// killGroup lives in procgroup_windows.go (job objects).
