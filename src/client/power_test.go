//go:build linux

package main

import (
	"os"
	"path/filepath"
	"testing"
)

func fakeSupply(t *testing.T, root, name string, files map[string]string) {
	t.Helper()
	d := filepath.Join(root, name)
	os.MkdirAll(d, 0o755)
	for k, v := range files {
		os.WriteFile(filepath.Join(d, k), []byte(v+"\n"), 0o644)
	}
}

func TestReadPower(t *testing.T) {
	// desktop with a wireless mouse: the mouse battery must not count
	d := t.TempDir()
	fakeSupply(t, d, "hidpp_battery_0", map[string]string{"type": "Battery", "scope": "Device", "status": "Discharging", "capacity": "40"})
	if p := readPower(d); p.State != "none" || p.Percent != -1 {
		t.Fatalf("desktop with a mouse battery: %+v", p)
	}
	// laptop on battery: energy_now/energy_full
	l := t.TempDir()
	fakeSupply(t, l, "BAT0", map[string]string{"type": "Battery", "status": "Discharging", "energy_now": "38950000", "energy_full": "67000000"})
	fakeSupply(t, l, "AC", map[string]string{"type": "Mains", "online": "0"})
	if p := readPower(l); p.State != "battery" || p.Percent < 58 || p.Percent > 58.2 {
		t.Fatalf("laptop on battery: %+v", p)
	}
	// plugged in and charging
	fakeSupply(t, l, "AC", map[string]string{"online": "1"})
	fakeSupply(t, l, "BAT0", map[string]string{"status": "Charging"})
	if p := readPower(l); p.State != "ac" {
		t.Fatalf("laptop on mains: %+v", p)
	}
	// no power_supply entries at all (VM, server)
	if p := readPower(t.TempDir()); p.State != "none" {
		t.Fatalf("VM: %+v", p)
	}
}

func TestBatteryHysteresis(t *testing.T) {
	l := t.TempDir()
	fakeSupply(t, l, "BAT0", map[string]string{"type": "Battery", "status": "Discharging", "capacity": "49"})
	old := powerSupplyDirVar
	powerSupplyDirVar = l
	defer func() { powerSupplyDirVar = old }()
	a := &Agent{cfg: &Config{MinBattery: 50, BatteryResume: 5}}
	if ok, _ := a.accepting(); ok {
		t.Fatal("49 % < 50 %: should pause")
	}
	fakeSupply(t, l, "BAT0", map[string]string{"capacity": "52"})
	if ok, _ := a.accepting(); ok {
		t.Fatal("52 % is inside the resume margin: still paused")
	}
	fakeSupply(t, l, "BAT0", map[string]string{"capacity": "55"})
	if ok, why := a.accepting(); !ok {
		t.Fatalf("55 %% reaches 50+5: should resume (%s)", why)
	}
}
