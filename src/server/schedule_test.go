package main

import (
	"testing"
	"time"
)

func TestSchedule(t *testing.T) {
	// Tuesday 2026-10-06 10:30 local
	now := time.Date(2026, 10, 6, 10, 30, 0, 0, time.Local)
	at := func(day, h, m int) time.Time { return time.Date(2026, 10, day, h, m, 0, 0, time.Local) }
	for spec, want := range map[string]time.Time{
		"hourly":            at(6, 11, 0),
		"every 6h":          at(6, 12, 0),
		"every 30m":         at(6, 11, 0),
		"daily 08:00":       at(7, 8, 0),
		"daily 08:00 20:00": at(6, 20, 0),
		"weekdays 08:00":    at(7, 8, 0),
		"weekends 10:00":    at(10, 10, 0),
		"mon,thu 09:00":     at(8, 9, 0),
		"mon-wed 11:00":     at(6, 11, 0),
		"fri-mon 07:00":     at(9, 7, 0),
		"weekly mon 09:00":  at(12, 9, 0),
		"  Daily   08:00  ": at(7, 8, 0),
	} {
		sc, err := parseSchedule(spec)
		if err != nil {
			t.Errorf("%q: %v", spec, err)
			continue
		}
		if got := sc.next(now); !got.Equal(want) {
			t.Errorf("%q: next %s, want %s", spec, got.Format("Mon 02 15:04"), want.Format("Mon 02 15:04"))
		}
	}
	if sc, _ := parseSchedule("off"); !sc.next(now).IsZero() {
		t.Error("off has a next time")
	}
	for _, bad := range []string{"daily", "every", "every 5s", "monday 08:00", "daily 25:00", "daily 8"} {
		if _, err := parseSchedule(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

// On the day the clocks change, "daily 08:00" is still 08:00 on the clock (code review).
func TestScheduleDST(t *testing.T) {
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skip("no time zone data")
	}
	sc, _ := parseSchedule("daily 08:00")
	got := sc.next(time.Date(2026, 3, 8, 0, 30, 0, 0, ny)) // clocks jump 02:00 -> 03:00
	if got.Hour() != 8 || got.Minute() != 0 || got.Day() != 8 {
		t.Errorf("next on the DST day: %s", got)
	}
}
