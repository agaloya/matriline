package main

import "testing"

func TestReserveDiskKeepsMinFree(t *testing.T) {
	dir := t.TempDir()
	free := diskFree(dir)
	if free <= 0 {
		t.Skip("free space unknown on this system")
	}
	s := &Server{cfg: &Config{Root: dir, StorageMinFree: free / 2}}
	third := free / 3
	if !s.reserveDisk(third) {
		t.Fatal("first third refused although it leaves 2/3 free")
	}
	if s.reserveDisk(third) {
		t.Fatal("second third accepted while the first is still in flight: only 1/3 would stay free")
	}
	s.releaseDisk(third)
	if s.diskInflight != 0 {
		t.Fatalf("in-flight bytes left over: %d", s.diskInflight)
	}
	s.cfg.StorageMinFree = 0 // disabled
	if !s.reserveDisk(free * 10) {
		t.Fatal("min_free = 0 must not refuse anything")
	}
}

func TestMinFreeAuto(t *testing.T) {
	dir := t.TempDir()
	total := diskTotal(dir)
	if total <= 0 {
		t.Skip("disk size unknown")
	}
	s := &Server{cfg: &Config{Root: dir, StorageMinFree: minFreeAuto}}
	want := min(total/20, 5<<30)
	if got := s.minFree(); got != want {
		t.Fatalf("auto min_free %d, want %d (5 %% of %d, at most 5 GB)", got, want, total)
	}
	s.cfg.StorageMinFree = 0
	if s.minFree() != 0 {
		t.Fatal("min_free = 0 must disable the reserve")
	}
}
