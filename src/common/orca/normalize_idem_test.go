package orca

import "testing"

// A clean restart normalizes an already normalized input again: it must not change.
func TestNormalizeIdempotent(t *testing.T) {
	in := "! r2SCAN-3c Opt Freq\n%pal nprocs 8 end\n* xyz 0 1\nH 0 0 0\nH 0 0 0.74\n*\n"
	r := Resources{MaxcoreMB: 768, Nprocs: 1}
	once := Normalize(in, r)
	if twice := Normalize(once, r); twice != once {
		t.Fatalf("not idempotent:\n%s\n---\n%s", once, twice)
	}
}
