//go:build linux

package main

import (
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"unsafe"
)

// cpuTempC returns the hottest CPU sensor reading. Sensor drivers are taken from the Linux
// hwmon documentation (k10temp: AMD, coretemp: Intel, cpu_thermal/soc_thermal: ARM boards,
// zenpower: out-of-tree AMD); acpitz is a last resort.
func cpuTempC() (float64, string) {
	if tempFile != "" {
		if v, err := strconv.ParseFloat(readTrim(tempFile), 64); err == nil {
			return v, ""
		}
		return 0, "temperature_file_unreadable"
	}
	pref := map[string]int{"k10temp": 3, "coretemp": 3, "zenpower": 3, "cpu_thermal": 2, "soc_thermal": 2, "acpitz": 1}
	best, bestRank := -1.0, 0
	dirs, _ := filepath.Glob("/sys/class/hwmon/hwmon*")
	for _, d := range dirs {
		name := readTrim(filepath.Join(d, "name"))
		rank := pref[name]
		if rank == 0 || rank < bestRank {
			continue
		}
		files, _ := filepath.Glob(filepath.Join(d, "temp*_input"))
		maxv := -1.0
		for _, f := range files {
			if v, err := strconv.ParseFloat(readTrim(f), 64); err == nil && v/1000 > maxv {
				maxv = v / 1000
			}
		}
		if maxv >= 0 && (rank > bestRank || maxv > best) {
			best, bestRank = maxv, rank
		}
	}
	if best < 0 {
		return 0, "no_sensor"
	}
	return best, ""
}

// cpuFreqMHz returns the mean current frequency over all CPUs.
func cpuFreqMHz() (float64, string) {
	files, _ := filepath.Glob("/sys/devices/system/cpu/cpu[0-9]*/cpufreq/scaling_cur_freq")
	if len(files) == 0 {
		return 0, "no_cpufreq"
	}
	sum, n := 0.0, 0
	for _, f := range files {
		if v, err := strconv.ParseFloat(readTrim(f), 64); err == nil {
			sum += v / 1000
			n++
		}
	}
	if n == 0 {
		return 0, "no_cpufreq"
	}
	return sum / float64(n), ""
}

func memInfo() (totalMB, availMB int64) {
	b, _ := os.ReadFile("/proc/meminfo")
	for _, ln := range strings.Split(string(b), "\n") {
		f := strings.Fields(ln)
		if len(f) < 2 {
			continue
		}
		v, _ := strconv.ParseInt(f[1], 10, 64)
		switch f[0] {
		case "MemTotal:":
			totalMB = v / 1024
		case "MemAvailable:":
			availMB = v / 1024
		}
	}
	return
}

func cpuModel() string {
	b, _ := os.ReadFile("/proc/cpuinfo")
	for _, ln := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(ln, "model name") {
			if _, v, ok := strings.Cut(ln, ":"); ok {
				return strings.TrimSpace(v)
			}
		}
	}
	return "unknown"
}

func osName() string {
	name := "Linux"
	b, _ := os.ReadFile("/etc/os-release")
	for _, ln := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(ln, "PRETTY_NAME=") {
			name = strings.Trim(strings.TrimPrefix(ln, "PRETTY_NAME="), `"`)
		}
	}
	return name
}

func kernelVersion() string { return readTrim("/proc/sys/kernel/osrelease") }

func loadAvg1() float64 {
	f := strings.Fields(readTrim("/proc/loadavg"))
	if len(f) == 0 {
		return -1
	}
	v, _ := strconv.ParseFloat(f[0], 64)
	return v
}

// cpuTimes returns (busy, total) jiffies from /proc/stat.
func cpuTimes() (uint64, uint64) {
	b, _ := os.ReadFile("/proc/stat")
	ln, _, _ := strings.Cut(string(b), "\n")
	f := strings.Fields(ln)
	var total, idle uint64
	for i, s := range f[1:] {
		v, _ := strconv.ParseUint(s, 10, 64)
		total += v
		if i == 3 || i == 4 { // idle, iowait
			idle += v
		}
	}
	return total - idle, total
}

// powerSupplyDirVar is a variable so tests can point it at a fake tree.
var powerSupplyDirVar = "/sys/class/power_supply"

// readPower reads dir (normally /sys/class/power_supply). Batteries with scope "Device"
// belong to peripherals (wireless mouse, keyboard, headset): before, one made a desktop
// without a "Mains" entry look like a laptop on battery, and it stopped taking tasks.
func readPower(dir string) power {
	dirs, _ := filepath.Glob(filepath.Join(dir, "*"))
	var now, full float64
	bats, mains, discharging := 0, false, false
	var pcts []float64
	for _, d := range dirs {
		switch readTrim(filepath.Join(d, "type")) {
		case "Battery":
			if readTrim(filepath.Join(d, "scope")) == "Device" || readTrim(filepath.Join(d, "present")) == "0" {
				continue
			}
			bats++
			if readTrim(filepath.Join(d, "status")) == "Discharging" {
				discharging = true
			}
			n, f := readNum(d, "energy_now"), readNum(d, "energy_full")
			if f <= 0 {
				n, f = readNum(d, "charge_now"), readNum(d, "charge_full")
			}
			if f > 0 {
				now, full = now+n, full+f
			} else if c := readNum(d, "capacity"); c >= 0 {
				pcts = append(pcts, c)
			}
		case "Mains", "USB", "USB_C", "USB_PD":
			if readTrim(filepath.Join(d, "online")) == "1" {
				mains = true
			}
		}
	}
	p := power{State: "none", Percent: -1}
	if bats == 0 {
		return p // desktops, servers, VMs, a laptop with its battery removed, UPS-only machines
	}
	switch {
	case full > 0:
		p.Percent = 100 * now / full
	case len(pcts) > 0:
		s := 0.0
		for _, c := range pcts {
			s += c
		}
		p.Percent = s / float64(len(pcts))
	}
	p.State = "ac"
	if discharging || !mains {
		p.State = "battery"
	}
	return p
}

func readNum(dir, name string) float64 {
	v, err := strconv.ParseFloat(readTrim(filepath.Join(dir, name)), 64)
	if err != nil {
		return -1
	}
	return v
}

// processTree returns root and all its descendants.
func processTree(root int) []procInfo {
	entries, _ := os.ReadDir("/proc")
	all := map[int]procInfo{}
	children := map[int][]int{}
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		st, err := os.ReadFile("/proc/" + e.Name() + "/stat")
		if err != nil {
			continue
		}
		s := string(st)
		i := strings.LastIndexByte(s, ')')
		if i < 0 {
			continue
		}
		f := strings.Fields(s[i+1:])
		if len(f) < 22 {
			continue
		}
		ppid, _ := strconv.Atoi(f[1])
		rssPages, _ := strconv.ParseInt(f[21], 10, 64)
		p := procInfo{pid: pid, ppid: ppid, rssKB: rssPages * int64(os.Getpagesize()) / 1024}
		all[pid] = p
		children[ppid] = append(children[ppid], pid)
	}
	var out []procInfo
	stack := []int{root}
	for len(stack) > 0 {
		pid := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		p, ok := all[pid]
		if !ok {
			continue
		}
		p.exe, _ = os.Readlink("/proc/" + strconv.Itoa(pid) + "/exe")
		out = append(out, p)
		stack = append(stack, children[pid]...)
	}
	return out
}

// tcpStats returns smoothed RTT (ms) and the total number of retransmitted segments of a TCP
// connection, read with getsockopt(TCP_INFO) (struct layout from <linux/tcp.h>, exposed by
// the standard library as syscall.TCPInfo).
func tcpStats(c net.Conn) (rttMs float64, retrans uint32, ok bool) {
	tc, isTCP := c.(*net.TCPConn)
	if !isTCP {
		return 0, 0, false
	}
	rc, err := tc.SyscallConn()
	if err != nil {
		return 0, 0, false
	}
	var info syscall.TCPInfo
	var serr syscall.Errno
	rc.Control(func(fd uintptr) {
		l := uint32(unsafe.Sizeof(info))
		_, _, serr = syscall.Syscall6(syscall.SYS_GETSOCKOPT, fd, syscall.IPPROTO_TCP, syscall.TCP_INFO,
			uintptr(unsafe.Pointer(&info)), uintptr(unsafe.Pointer(&l)), 0)
	})
	if serr != 0 {
		return 0, 0, false
	}
	return float64(info.Rtt) / 1000, info.Total_retrans, true
}

func readTrim(p string) string {
	b, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// physicalCores counts distinct (package, core) pairs in sysfs topology; 0 if unknown.
// Hybrid CPUs (P cores with two threads, E cores with one) are counted correctly.
func physicalCores() int {
	seen := map[string]bool{}
	dirs, _ := filepath.Glob("/sys/devices/system/cpu/cpu[0-9]*/topology")
	for _, d := range dirs {
		pkg, core := readTrim(filepath.Join(d, "physical_package_id")), readTrim(filepath.Join(d, "core_id"))
		if core != "" {
			seen[pkg+"/"+core] = true
		}
	}
	return len(seen)
}
