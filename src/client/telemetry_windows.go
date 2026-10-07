//go:build windows

package main

// Windows telemetry through kernel32/ntdll (standard library syscall only, no cgo):
// GlobalMemoryStatusEx, GetDiskFreeSpaceExW, GetSystemPowerStatus, RtlGetVersion
// (https://learn.microsoft.com/en-us/windows/win32/api/). Values Windows does not expose
// without extra privileges (CPU temperature, frequency) are reported as NA (D17).

import (
	"os"
	"runtime"
	"strconv"
	"syscall"
	"unsafe"
)

var (
	kernel32                 = syscall.NewLazyDLL("kernel32.dll")
	ntdll                    = syscall.NewLazyDLL("ntdll.dll")
	procGlobalMemoryStatusEx = kernel32.NewProc("GlobalMemoryStatusEx")
	procGetDiskFreeSpaceExW  = kernel32.NewProc("GetDiskFreeSpaceExW")
	procGetSystemPowerStatus = kernel32.NewProc("GetSystemPowerStatus")
	procRtlGetVersion        = ntdll.NewProc("RtlGetVersion")
)

// memInfo returns total and available physical memory in MB.
func memInfo() (int64, int64) {
	var m struct {
		Length               uint32
		MemoryLoad           uint32
		TotalPhys, AvailPhys uint64
		TotalPageFile        uint64
		AvailPageFile        uint64
		TotalVirtual         uint64
		AvailVirtual         uint64
		AvailExtendedVirtual uint64
	}
	m.Length = uint32(unsafe.Sizeof(m))
	if r, _, _ := procGlobalMemoryStatusEx.Call(uintptr(unsafe.Pointer(&m))); r == 0 {
		return 0, 0
	}
	return int64(m.TotalPhys >> 20), int64(m.AvailPhys >> 20)
}

func cpuModel() string {
	if v := os.Getenv("PROCESSOR_IDENTIFIER"); v != "" {
		return v
	}
	return runtime.GOARCH
}

// osName is e.g. "Windows 10.0 build 22631" (Windows 11 reports 10.0 with build >= 22000).
func osName() string {
	var v struct {
		Size, Major, Minor, Build, Platform uint32
		CSD                                 [128]uint16
	}
	v.Size = uint32(unsafe.Sizeof(v))
	if r, _, _ := procRtlGetVersion.Call(uintptr(unsafe.Pointer(&v))); r != 0 {
		return "windows"
	}
	name := "Windows 10"
	if v.Build >= 22000 {
		name = "Windows 11"
	}
	return name + " (" + strconv.Itoa(int(v.Major)) + "." + strconv.Itoa(int(v.Minor)) + " build " + strconv.Itoa(int(v.Build)) + ")"
}

func kernelVersion() string { return osName() }

var powerSupplyDirVar = ""

// readPower uses GetSystemPowerStatus: ACLineStatus 0 = battery, 1 = mains; BatteryFlag 128
// = no system battery; BatteryLifePercent 255 = unknown.
func readPower(string) power {
	var st struct {
		ACLineStatus, BatteryFlag, BatteryLifePercent, SystemStatusFlag uint8
		BatteryLifeTime, BatteryFullLifeTime                            uint32
	}
	if r, _, _ := procGetSystemPowerStatus.Call(uintptr(unsafe.Pointer(&st))); r == 0 || st.BatteryFlag == 128 || st.BatteryFlag == 255 {
		return power{State: "none", Percent: -1}
	}
	p := power{State: "ac", Percent: -1}
	if st.BatteryLifePercent <= 100 {
		p.Percent = float64(st.BatteryLifePercent)
	}
	if st.ACLineStatus == 0 {
		p.State = "battery"
	}
	return p
}

// diskSpace returns the bytes available to this user and the size of the volume holding
// path (-1, -1 unknown).
func diskSpace(path string) (avail, total int64) {
	p, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return -1, -1
	}
	var a, t, f uint64
	if r, _, _ := procGetDiskFreeSpaceExW.Call(uintptr(unsafe.Pointer(p)), uintptr(unsafe.Pointer(&a)),
		uintptr(unsafe.Pointer(&t)), uintptr(unsafe.Pointer(&f))); r == 0 {
		return -1, -1
	}
	return int64(a), int64(t)
}

func diskFree(path string) int64 { a, _ := diskSpace(path); return a }
func diskSize(path string) int64 { _, t := diskSpace(path); return t }

var (
	procQueryFullProcessImageNameW = kernel32.NewProc("QueryFullProcessImageNameW")
	procK32GetProcessMemoryInfo    = kernel32.NewProc("K32GetProcessMemoryInfo")
	procGetLogicalProcessorInfo    = kernel32.NewProc("GetLogicalProcessorInformation")
)

// processTree returns root and all its descendants (Toolhelp snapshot) with their full
// executable paths, for the process audit (no programs outside the ORCA installation).
func processTree(root int) []procInfo {
	snap, err := syscall.CreateToolhelp32Snapshot(syscall.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return []procInfo{{pid: root}}
	}
	defer syscall.CloseHandle(snap)
	parent := map[int]int{}
	var e syscall.ProcessEntry32
	e.Size = uint32(unsafe.Sizeof(e))
	for err := syscall.Process32First(snap, &e); err == nil; err = syscall.Process32Next(snap, &e) {
		parent[int(e.ProcessID)] = int(e.ParentProcessID)
	}
	in := map[int]bool{root: true}
	if pids, ok := jobPIDs(root); ok {
		for _, p := range pids {
			in[p] = true
		}
	} else {
		in = descendants(root, parent)
	}
	var out []procInfo
	for pid := range in {
		p := procInfo{pid: pid, ppid: parent[pid]}
		if h, err := syscall.OpenProcess(0x1000|0x0010, false, uint32(pid)); err == nil { // QUERY_LIMITED_INFORMATION | VM_READ
			buf := make([]uint16, 1024)
			n := uint32(len(buf))
			if r, _, _ := procQueryFullProcessImageNameW.Call(uintptr(h), 0, uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&n))); r != 0 {
				p.exe = syscall.UTF16ToString(buf[:n])
			}
			var mc struct {
				Cb                         uint32
				PageFaultCount             uint32
				PeakWorkingSetSize         uintptr
				WorkingSetSize             uintptr
				QuotaPeakPagedPoolUsage    uintptr
				QuotaPagedPoolUsage        uintptr
				QuotaPeakNonPagedPoolUsage uintptr
				QuotaNonPagedPoolUsage     uintptr
				PagefileUsage              uintptr
				PeakPagefileUsage          uintptr
			}
			mc.Cb = uint32(unsafe.Sizeof(mc))
			if r, _, _ := procK32GetProcessMemoryInfo.Call(uintptr(h), uintptr(unsafe.Pointer(&mc)), uintptr(mc.Cb)); r != 0 {
				p.rssKB = int64(mc.WorkingSetSize >> 10)
			}
			syscall.CloseHandle(h)
		}
		out = append(out, p)
	}
	return out
}

// physicalCores counts processor cores (RelationProcessorCore entries of
// GetLogicalProcessorInformation); 0 if unknown.
func physicalCores() int {
	type slpi struct {
		Mask         uintptr
		Relationship uint32
		_            [4]byte
		_            [16]byte
	}
	var n uint32
	procGetLogicalProcessorInfo.Call(0, uintptr(unsafe.Pointer(&n)))
	size := uint32(unsafe.Sizeof(slpi{}))
	if n == 0 || n%size != 0 {
		return 0
	}
	buf := make([]slpi, n/size)
	if r, _, _ := procGetLogicalProcessorInfo.Call(uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&n))); r == 0 {
		return 0
	}
	cores := 0
	for _, e := range buf[:n/size] {
		if e.Relationship == 0 { // RelationProcessorCore
			cores++
		}
	}
	return cores
}
