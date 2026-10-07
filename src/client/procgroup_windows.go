//go:build windows

package main

// ORCA starts helper programs (orca_scf, orca_mdci, ...). On Windows killing the parent
// leaves them running, so every job's process is put in a Job Object that is closed with
// "kill on close": stopping the job (cancel, timeout, disable) ends the whole tree.
// https://learn.microsoft.com/en-us/windows/win32/procthread/job-objects
// Children started in the few microseconds between Start and the assignment would escape;
// ORCA's first child starts much later (after reading the input).

import (
	"os/exec"
	"sync"
	"syscall"
	"unsafe"
)

var (
	procCreateJobObjectW        = kernel32.NewProc("CreateJobObjectW")
	procSetInformationJobObject = kernel32.NewProc("SetInformationJobObject")
	procAssignProcessToJob      = kernel32.NewProc("AssignProcessToJobObject")
	procTerminateJobObject      = kernel32.NewProc("TerminateJobObject")
	procQueryInformationJob     = kernel32.NewProc("QueryInformationJobObject")
	jobsMu                      sync.Mutex
	jobHandles                  = map[int]syscall.Handle{}
)

const (
	jobObjectExtendedLimitInformation = 9
	jobObjectLimitKillOnJobClose      = 0x2000
	processSetQuota                   = 0x0100
	processTerminate                  = 0x0001
)

type jobBasicLimit struct {
	PerProcessUserTimeLimit int64
	PerJobUserTimeLimit     int64
	LimitFlags              uint32
	MinimumWorkingSetSize   uintptr
	MaximumWorkingSetSize   uintptr
	ActiveProcessLimit      uint32
	Affinity                uintptr
	PriorityClass           uint32
	SchedulingClass         uint32
}

type jobExtendedLimit struct {
	Basic                 jobBasicLimit
	IoInfo                [6]uint64
	ProcessMemoryLimit    uintptr
	JobMemoryLimit        uintptr
	PeakProcessMemoryUsed uintptr
	PeakJobMemoryUsed     uintptr
}

// jobPIDs lists the processes in the job object of the job whose first process is pid
// (ok = false when it has none). This is the job's exact membership: following parent
// process ids is not, because Windows reuses process ids. A server started at boot whose
// parent had ended counted as a child of a job's ORCA that got that parent's old id, and
// the audit stopped the job (Windows 11).
func jobPIDs(pid int) ([]int, bool) {
	jobsMu.Lock()
	job, ok := jobHandles[pid]
	jobsMu.Unlock()
	if !ok {
		return nil, false
	}
	const jobObjectBasicProcessIDList = 3
	for n := 64; n <= 1<<16; n *= 4 {
		buf := make([]uintptr, 1+n) // two DWORD counters, then the ids
		r, _, err := procQueryInformationJob.Call(uintptr(job), jobObjectBasicProcessIDList,
			uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf))*unsafe.Sizeof(buf[0]), 0)
		if r == 0 && err == syscall.ERROR_MORE_DATA {
			continue
		}
		if r == 0 {
			return nil, false
		}
		inList := int(*(*uint32)(unsafe.Add(unsafe.Pointer(&buf[0]), 4)))
		out := make([]int, 0, inList)
		for i := 0; i < inList && i < n; i++ {
			out = append(out, int(buf[1+i]))
		}
		return out, true
	}
	return nil, false
}

// trackGroup puts a started process in a new kill-on-close job object.
func trackGroup(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	h, _, _ := procCreateJobObjectW.Call(0, 0)
	if h == 0 {
		return
	}
	job := syscall.Handle(h)
	var info jobExtendedLimit
	info.Basic.LimitFlags = jobObjectLimitKillOnJobClose
	procSetInformationJobObject.Call(uintptr(job), jobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&info)), unsafe.Sizeof(info))
	ph, err := syscall.OpenProcess(processSetQuota|processTerminate, false, uint32(cmd.Process.Pid))
	if err != nil {
		syscall.CloseHandle(job)
		return
	}
	defer syscall.CloseHandle(ph)
	if r, _, _ := procAssignProcessToJob.Call(uintptr(job), uintptr(ph)); r == 0 {
		syscall.CloseHandle(job)
		return
	}
	jobsMu.Lock()
	jobHandles[cmd.Process.Pid] = job
	jobsMu.Unlock()
}

// killGroup ends the whole process tree of cmd.
func killGroup(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	jobsMu.Lock()
	job, ok := jobHandles[cmd.Process.Pid]
	jobsMu.Unlock()
	if ok {
		procTerminateJobObject.Call(uintptr(job), 1)
		return
	}
	cmd.Process.Kill()
}

// releaseGroup closes the job object after the process ended (kill on close also stops
// any helper left behind).
func releaseGroup(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	jobsMu.Lock()
	job, ok := jobHandles[cmd.Process.Pid]
	delete(jobHandles, cmd.Process.Pid)
	jobsMu.Unlock()
	if ok {
		syscall.CloseHandle(job)
	}
}

// extraEnv: Windows programs need SystemRoot (and windir) to load system DLLs; TEMP/TMP
// point into the job directory like TMPDIR on Linux.
func extraEnv(work string) []string {
	// ComSpec: ORCA starts its modules through system(), which runs %ComSpec% (cmd.exe);
	// without it every job ended in "error termination in Startup" (Windows 11 VM test)
	root := getenvDefault("SystemRoot", `C:\Windows`)
	env := []string{"SystemRoot=" + root, "windir=" + getenvDefault("windir", `C:\Windows`),
		"ComSpec=" + getenvDefault("ComSpec", root+`\system32\cmd.exe`), "TEMP=" + work, "TMP=" + work}
	// creating a process in an AppContainer needs the user's profile folders (else
	// CreateProcess fails with ERROR_ENVVAR_NOT_FOUND; found on Windows 11); only paths
	for _, k := range []string{"SystemDrive", "USERPROFILE", "LOCALAPPDATA", "APPDATA", "HOMEDRIVE", "HOMEPATH"} {
		if v, ok := syscall.Getenv(k); ok && v != "" {
			env = append(env, k+"="+v)
		}
	}
	return env
}

func getenvDefault(k, def string) string {
	if v, ok := syscall.Getenv(k); ok && v != "" {
		return v
	}
	return def
}
