//go:build windows

package main

import (
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

// Windows sandbox, strong level: ORCA runs in an AppContainer
// (https://learn.microsoft.com/en-us/windows/win32/secauthz/appcontainer-isolation) with no
// capabilities, so it has no network at all, and it can open only what grants access to
// its container: its job directory (created so) and ORCA's directory (an administrator
// grants read access to all app containers once: sandboxHint). Windows itself (System32,
// cmd.exe) is readable by app containers. The client starts itself as a helper
// ("matriline-client sandbox-exec <work> -- prog...") inside the job object; the helper
// creates ORCA in the container (CreateProcessW with SECURITY_CAPABILITIES, which os/exec
// cannot pass) and exits with ORCA's exit code. ORCA's children stay in the container and
// in the job object.

const containerName = "matriline.orca"

var (
	modUserenv                       = syscall.NewLazyDLL("userenv.dll")
	procCreateAppContainerProfile    = modUserenv.NewProc("CreateAppContainerProfile")
	procDeriveAppContainerSidFromAcn = modUserenv.NewProc("DeriveAppContainerSidFromAppContainerName")
	procConvertSidToStringSidW       = modAdvapi32.NewProc("ConvertSidToStringSidW")
	procFreeSid                      = modAdvapi32.NewProc("FreeSid")
	procInitProcThreadAttributeList  = kernel32.NewProc("InitializeProcThreadAttributeList")
	procUpdateProcThreadAttribute    = kernel32.NewProc("UpdateProcThreadAttribute")
	procDeleteProcThreadAttributeLst = kernel32.NewProc("DeleteProcThreadAttributeList")
	procCreateProcessW               = kernel32.NewProc("CreateProcessW")
)

const (
	procThreadAttributeSecurityCapabilities = 0x00020009
	procThreadAttributeHandleList           = 0x00020002
	extendedStartupinfoPresent              = 0x00080000
	hresultAlreadyExists                    = 0x800700B7 // HRESULT_FROM_WIN32(ERROR_ALREADY_EXISTS)
)

// containerSID returns the container's SID (creating its profile the first time); the
// caller frees it with FreeSid.
func containerSID() (uintptr, error) {
	name, _ := syscall.UTF16PtrFromString(containerName)
	display, _ := syscall.UTF16PtrFromString("Matriline ORCA jobs")
	var sid uintptr
	r, _, _ := procCreateAppContainerProfile.Call(uintptr(unsafe.Pointer(name)), uintptr(unsafe.Pointer(display)), uintptr(unsafe.Pointer(display)), 0, 0, uintptr(unsafe.Pointer(&sid)))
	if r == 0 {
		return sid, nil
	}
	if uint32(r) != hresultAlreadyExists {
		return 0, fmt.Errorf("CreateAppContainerProfile: 0x%x", uint32(r))
	}
	if r, _, _ := procDeriveAppContainerSidFromAcn.Call(uintptr(unsafe.Pointer(name)), uintptr(unsafe.Pointer(&sid))); r != 0 {
		return 0, fmt.Errorf("DeriveAppContainerSidFromAppContainerName: 0x%x", uint32(r))
	}
	return sid, nil
}

func sidString(sid uintptr) string {
	var s *uint16
	if r, _, _ := procConvertSidToStringSidW.Call(sid, uintptr(unsafe.Pointer(&s))); r == 0 {
		return ""
	}
	defer syscall.LocalFree(syscall.Handle(unsafe.Pointer(s)))
	return utf16PtrString(s)
}

// utf16PtrString reads a NUL-terminated UTF-16 string returned by Windows.
func utf16PtrString(p *uint16) string {
	var u []uint16
	for ptr := unsafe.Pointer(p); *(*uint16)(ptr) != 0; ptr = unsafe.Add(ptr, 2) {
		u = append(u, *(*uint16)(ptr))
	}
	return syscall.UTF16ToString(u)
}

var containerSIDOnce struct {
	sync.Once
	s string
}

// containerSIDString is the container's SID as text ("" when containers are unavailable),
// computed once: the client asks for it for every job.
func containerSIDString() string {
	containerSIDOnce.Do(func() {
		sid, err := containerSID()
		if err != nil {
			return
		}
		containerSIDOnce.s = sidString(sid)
		procFreeSid.Call(sid)
	})
	return containerSIDOnce.s
}

func currentUserSID() string {
	tok, err := syscall.OpenCurrentProcessToken()
	if err != nil {
		return ""
	}
	defer tok.Close()
	u, err := tok.GetTokenUser()
	if err != nil {
		return ""
	}
	s, _ := u.User.Sid.String()
	return s
}

// containerExec is the helper: run prog in the container, wait, exit with its code.
func containerExec(work string, prog []string) {
	sid, err := containerSID()
	if err != nil {
		fail(126, "%v", err)
	}
	defer procFreeSid.Call(sid)
	type securityCapabilities struct {
		AppContainerSid uintptr
		Capabilities    uintptr // none: no network, no libraries, no devices
		CapabilityCount uint32
		Reserved        uint32
	}
	caps := securityCapabilities{AppContainerSid: sid}
	var size uintptr
	procInitProcThreadAttributeList.Call(0, 2, 0, uintptr(unsafe.Pointer(&size)))
	attrs := make([]byte, size)
	if r, _, err := procInitProcThreadAttributeList.Call(uintptr(unsafe.Pointer(&attrs[0])), 2, 0, uintptr(unsafe.Pointer(&size))); r == 0 {
		fail(126, "InitializeProcThreadAttributeList: %v", err)
	}
	defer procDeleteProcThreadAttributeLst.Call(uintptr(unsafe.Pointer(&attrs[0])))
	if r, _, err := procUpdateProcThreadAttribute.Call(uintptr(unsafe.Pointer(&attrs[0])), 0, procThreadAttributeSecurityCapabilities,
		uintptr(unsafe.Pointer(&caps)), unsafe.Sizeof(caps), 0, 0); r == 0 {
		fail(126, "UpdateProcThreadAttribute: %v", err)
	}
	type startupInfoEx struct {
		syscall.StartupInfo
		AttributeList uintptr
	}
	var si startupInfoEx
	si.Cb = uint32(unsafe.Sizeof(si))
	si.Flags = syscall.STARTF_USESTDHANDLES
	// ORCA writes into pipes that this helper copies to the client's files: with the files
	// themselves as its stdout/stderr, ORCA's system() calls failed inside the container
	// although the programs had worked (r2SCAN-3c: "Calculation of the gCP correction
	// failed"; with pipes it works; Windows 11)
	var pipes [2][2]syscall.Handle // [stdout, stderr][read, write]
	sa := syscall.SecurityAttributes{Length: uint32(unsafe.Sizeof(syscall.SecurityAttributes{})), InheritHandle: 1}
	for i := range pipes {
		if err := syscall.CreatePipe(&pipes[i][0], &pipes[i][1], &sa, 0); err != nil {
			fail(126, "CreatePipe: %v", err)
		}
		syscall.SetHandleInformation(pipes[i][0], syscall.HANDLE_FLAG_INHERIT, 0) // read end stays here
	}
	// stdin: NUL; and ORCA inherits exactly these three handles (a handle list, as os/exec
	// does): an inherited handle bypasses the container's access checks, so nothing else
	// the helper holds (its own stdout/stderr files, anything added later) may leak in
	// (code review)
	nul, err := syscall.Open("NUL", syscall.O_RDONLY, 0)
	if err != nil {
		fail(126, "NUL: %v", err)
	}
	syscall.SetHandleInformation(nul, syscall.HANDLE_FLAG_INHERIT, syscall.HANDLE_FLAG_INHERIT)
	si.StdInput = nul
	si.StdOutput, si.StdErr = pipes[0][1], pipes[1][1]
	inherit := []syscall.Handle{nul, pipes[0][1], pipes[1][1]}
	if r, _, err := procUpdateProcThreadAttribute.Call(uintptr(unsafe.Pointer(&attrs[0])), 0, procThreadAttributeHandleList,
		uintptr(unsafe.Pointer(&inherit[0])), uintptr(len(inherit))*unsafe.Sizeof(inherit[0]), 0, 0); r == 0 {
		fail(126, "UpdateProcThreadAttribute (handles): %v", err)
	}
	si.AttributeList = uintptr(unsafe.Pointer(&attrs[0]))
	quoted := make([]string, len(prog))
	for i, a := range prog {
		quoted[i] = syscall.EscapeArg(a)
	}
	cmdline, _ := syscall.UTF16PtrFromString(strings.Join(quoted, " "))
	dir, _ := syscall.UTF16PtrFromString(work)
	var pi syscall.ProcessInformation
	// environment: inherited (the client gave this helper ORCA's clean environment)
	if r, _, err := procCreateProcessW.Call(0, uintptr(unsafe.Pointer(cmdline)), 0, 0, 1, extendedStartupinfoPresent, 0,
		uintptr(unsafe.Pointer(dir)), uintptr(unsafe.Pointer(&si)), uintptr(unsafe.Pointer(&pi))); r == 0 {
		fail(127, "CreateProcess in the container: %v", err)
	}
	// the attribute list holds only addresses of attrs, caps and inherit (uintptrs, no Go
	// references): keep them alive until CreateProcessW has read them (code review)
	runtime.KeepAlive(attrs)
	runtime.KeepAlive(&caps)
	runtime.KeepAlive(inherit)
	syscall.CloseHandle(pi.Thread)
	syscall.CloseHandle(nul)
	// the write ends belong to ORCA now; copy until every program of the job closed them
	done := make(chan struct{}, 2)
	for i, dst := range []*os.File{os.Stdout, os.Stderr} {
		syscall.CloseHandle(pipes[i][1])
		src := os.NewFile(uintptr(pipes[i][0]), "pipe")
		go func() { io.Copy(dst, src); src.Close(); done <- struct{}{} }()
	}
	syscall.WaitForSingleObject(pi.Process, syscall.INFINITE)
	// a program ORCA started and left running keeps the pipes open: wait for it a little,
	// then exit anyway (the client's job object ends it; code review)
	grace := time.After(30 * time.Second)
	for n := 0; n < 2; n++ {
		select {
		case <-done:
		case <-grace:
			fmt.Fprintln(os.Stderr, "sandbox-exec: a program of the job still holds its output 30 s after ORCA ended")
			n = 2
		}
	}
	var code uint32
	syscall.GetExitCodeProcess(pi.Process, &code)
	syscall.CloseHandle(pi.Process)
	os.Exit(int(code))
}

// fail ends the helper with code after printing the reason.
func fail(code int, format string, a ...any) {
	fmt.Fprintf(os.Stderr, "sandbox-exec: "+format+"\n", a...)
	os.Exit(code)
}
