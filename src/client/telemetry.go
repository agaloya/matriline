package main

// Telemetry declarations shared by every OS; the readings are in telemetry_<os>.go.

// tempFile, if set (limits.cpu_temperature_file), is the temperature source (on Linux it
// replaces the hwmon sensors).
var tempFile string

// power describes the computer's power source (Linux power_supply class:
// https://www.kernel.org/doc/html/latest/power/power_supply_class.html).
type power struct {
	State   string  // "ac" (mains, or battery charging/full), "battery", "none" (no system battery)
	Percent float64 // system batteries combined; -1 if none
}

// onBattery is true when a system battery exists and no mains supply is online.
func onBattery() bool { return readPower(powerSupplyDirVar).State == "battery" }

// procInfo describes one process of a job's process tree.
type procInfo struct {
	pid   int
	ppid  int
	exe   string
	rssKB int64
}

// descendants follows parent process ids from root (without a job object to ask).
func descendants(root int, parent map[int]int) map[int]bool {
	in := map[int]bool{root: true}
	for changed := true; changed; {
		changed = false
		for pid, pp := range parent {
			if !in[pid] && in[pp] && pid != pp {
				in[pid], changed = true, true
			}
		}
	}
	return in
}
