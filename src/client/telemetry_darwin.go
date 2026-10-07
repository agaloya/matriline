//go:build darwin

package main

// macOS telemetry: sysctl (hw.memsize, hw.physicalcpu, machdep.cpu.brand_string,
// kern.osproductversion), statfs for free disk, `pmset -g batt` for the battery and
// `ps` for the job's process tree (full paths for the audit). Standard library only.
// CPU temperature and frequency need root (powermetrics): NA (D17).

import (
	"encoding/binary"
	"os/exec"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
)

func sysctlUint64(name string) uint64 {
	s, err := syscall.Sysctl(name)
	if err != nil {
		return 0
	}
	b := []byte(s)
	for len(b) < 8 {
		b = append(b, 0) // Sysctl drops the trailing zero byte of the raw value
	}
	return binary.LittleEndian.Uint64(b[:8])
}

// memInfo returns total memory in MB; "available" is not cheap to read here: 0.
func memInfo() (int64, int64) { return int64(sysctlUint64("hw.memsize") >> 20), 0 }

func cpuModel() string {
	if s, err := syscall.Sysctl("machdep.cpu.brand_string"); err == nil && s != "" {
		return s
	}
	return runtime.GOARCH
}

func osName() string {
	if v, err := syscall.Sysctl("kern.osproductversion"); err == nil && v != "" {
		return "macOS " + v
	}
	return "macOS"
}

func kernelVersion() string {
	v, _ := syscall.Sysctl("kern.osrelease")
	return v
}

var powerSupplyDirVar = ""

var rePct = regexp.MustCompile(`(\d+)%`)

// readPower parses `pmset -g batt`: "Now drawing from 'AC Power'" or "'Battery Power'",
// then a line with the charge percentage; no "InternalBattery" line = no battery.
func readPower(string) power {
	out, err := exec.Command("pmset", "-g", "batt").Output()
	if err != nil || !strings.Contains(string(out), "InternalBattery") {
		return power{State: "none", Percent: -1}
	}
	p := power{State: "ac", Percent: -1}
	if strings.Contains(string(out), "'Battery Power'") {
		p.State = "battery"
	}
	if m := rePct.FindStringSubmatch(string(out)); m != nil {
		p.Percent, _ = strconv.ParseFloat(m[1], 64)
	}
	return p
}

// processTree lists root and its descendants with `ps` (comm = the executable's path).
func processTree(root int) []procInfo {
	out, err := exec.Command("ps", "-axo", "pid=,ppid=,rss=,comm=").Output()
	if err != nil {
		return []procInfo{{pid: root}}
	}
	all := map[int]procInfo{}
	parent := map[int]int{}
	for _, ln := range strings.Split(string(out), "\n") {
		f := strings.Fields(ln)
		if len(f) < 4 {
			continue
		}
		pid, _ := strconv.Atoi(f[0])
		ppid, _ := strconv.Atoi(f[1])
		rss, _ := strconv.ParseInt(f[2], 10, 64)
		all[pid] = procInfo{pid: pid, ppid: ppid, rssKB: rss, exe: strings.Join(f[3:], " ")}
		parent[pid] = ppid
	}
	var res []procInfo
	var unnamed []string
	for pid := range descendants(root, parent) {
		if p, ok := all[pid]; ok {
			res = append(res, p)
			if isBareName(p.exe) {
				unnamed = append(unnamed, strconv.Itoa(pid))
			}
		} else {
			res = append(res, procInfo{pid: pid})
		}
	}
	if len(unnamed) > 0 {
		resolveBareNames(res, unnamed)
	}
	var relative []string
	for _, p := range res {
		if p.exe != "" && !strings.Contains(p.exe, "/") {
			relative = append(relative, strconv.Itoa(p.pid))
		}
	}
	if len(relative) > 0 {
		resolveRelativeNames(res, relative)
	}
	return res
}

// lsofSkip are the files every process maps besides its own program: under Rosetta 2 its
// runtime and the translated (AOT) copies, and dyld, libSystem and the shared cache.
var lsofSkip = []string{"/usr/libexec/rosetta/", "/Library/Apple/usr/libexec/oah/", "/private/var/db/oah/",
	"/usr/lib/", "/System/", "/private/var/db/dyld/"}

// resolveRelativeNames: for a process started by a program running under Rosetta 2 (the Intel
// ORCA's "sh -c ..." modules) ps prints the bare name, "sh", not "/bin/sh", and the audit
// stopped every job that ran a module through the shell. lsof lists the files each process
// maps as its text; the first one that is not Rosetta's or the system's is the program. One
// lsof call for all of them, and only when a bare name appears. A process that has exited
// before lsof saw it (a module's short "sh -c": a macOS test stopped every ethanol
// r2SCAN-3c Opt this way) is left without an executable, like a vanished "(name)"; a name it
// cannot resolve while the process still runs stays as it is, so the audit stops the job.
func resolveRelativeNames(res []procInfo, pids []string) {
	out, _ := exec.Command("/usr/sbin/lsof", "-a", "-p", strings.Join(pids, ","), "-d", "txt", "-Fpn").Output()
	exes := programsFromLsof(string(out))
	var missing []string
	for i := range res {
		if strings.Contains(res[i].exe, "/") || res[i].exe == "" {
			continue
		}
		if exe, ok := exes[res[i].pid]; ok {
			res[i].exe = exe
		} else {
			missing = append(missing, strconv.Itoa(res[i].pid))
		}
	}
	if len(missing) == 0 {
		return
	}
	alive := runningPIDs(missing)
	for i := range res {
		if res[i].exe != "" && !strings.Contains(res[i].exe, "/") && !alive[res[i].pid] {
			res[i].exe = ""
		}
	}
}

// runningPIDs: which of pids still run (ps lists only existing ones; a zombie, state Z, has
// exited and maps no program, so it does not count).
func runningPIDs(pids []string) map[int]bool {
	out, _ := exec.Command("ps", "-o", "pid=,stat=", "-p", strings.Join(pids, ",")).Output()
	return parseRunning(string(out))
}

func parseRunning(out string) map[int]bool {
	alive := map[int]bool{}
	for _, ln := range strings.Split(out, "\n") {
		f := strings.Fields(ln)
		if len(f) < 2 || strings.HasPrefix(f[1], "Z") {
			continue
		}
		if pid, err := strconv.Atoi(f[0]); err == nil {
			alive[pid] = true
		}
	}
	return alive
}

// programsFromLsof reads lsof -Fpn output ("p<pid>", then "f..."/"n<path>" lines per file)
// and returns each process's first text file outside lsofSkip.
func programsFromLsof(out string) map[int]string {
	exes := map[int]string{}
	pid := 0
	for _, ln := range strings.Split(out, "\n") {
		switch {
		case strings.HasPrefix(ln, "p"):
			pid, _ = strconv.Atoi(ln[1:])
		case strings.HasPrefix(ln, "n") && pid > 0:
			if _, done := exes[pid]; done {
				continue
			}
			path := ln[1:]
			skip := false
			for _, s := range lsofSkip {
				if strings.HasPrefix(path, s) {
					skip = true
					break
				}
			}
			if !skip {
				exes[pid] = path
			}
		}
	}
	return exes
}

// isBareName: ps prints "(name)" instead of the executable's path while it cannot read the
// process's arguments, which happens for a few milliseconds around exec and exit (seen for
// every ORCA module, in states R, S and U, not only for zombies). Taken as a path, "(orca)"
// is outside the ORCA installation and stopped every job.
func isBareName(exe string) bool {
	return strings.HasPrefix(exe, "(") && strings.HasSuffix(exe, ")")
}

// resolveBareNames asks ps again, a moment later, for the processes that had no path yet. A
// process that has exited meanwhile is left without an executable (exe ""), like one that
// vanished between two samples; one that still shows "(name)" keeps it, so the audit stops
// the job. (The sandbox only lets ORCA's directory and the shell be executed anyway.)
func resolveBareNames(res []procInfo, pids []string) {
	time.Sleep(50 * time.Millisecond)
	out, _ := exec.Command("ps", "-o", "pid=,comm=", "-p", strings.Join(pids, ",")).Output()
	now := map[int]string{}
	for _, ln := range strings.Split(string(out), "\n") {
		f := strings.Fields(ln)
		if len(f) < 2 {
			continue
		}
		pid, _ := strconv.Atoi(f[0])
		now[pid] = strings.Join(f[1:], " ")
	}
	for i := range res {
		if !isBareName(res[i].exe) {
			continue
		}
		if exe, ok := now[res[i].pid]; ok {
			res[i].exe = exe
		} else {
			res[i].exe = ""
		}
	}
}

func physicalCores() int { return int(sysctlUint64("hw.physicalcpu") & 0xffffffff) }
