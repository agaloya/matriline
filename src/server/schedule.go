package main

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// A summary schedule in plain words (alerts.summary; user: "as versatile as a crontab but
// much easier to understand"):
//
//	off                       never
//	hourly                    at the start of every hour
//	every 6h | every 30m      at that interval from midnight (every 6h: 00, 06, 12, 18)
//	daily 08:00               every day at 08:00 (several times: daily 08:00 20:00)
//	weekdays 08:00            Monday to Friday; weekends 10:00: Saturday and Sunday
//	mon,thu 09:00             on those days; mon-fri 08:00 for a range of days
//	weekly mon 09:00          the same as "mon 09:00"
//
// Times are on this computer's clock.

type schedule struct {
	off   bool
	every time.Duration   // > 0: every interval from midnight
	days  [7]bool         // by time.Weekday
	times []time.Duration // since midnight
}

var dayNames = map[string]time.Weekday{"sun": 0, "mon": 1, "tue": 2, "wed": 3, "thu": 4, "fri": 5, "sat": 6}

func parseSchedule(s string) (*schedule, error) {
	f := strings.Fields(strings.ToLower(strings.TrimSpace(s)))
	sc := &schedule{}
	switch {
	case len(f) == 0 || f[0] == "off" || f[0] == "never":
		sc.off = true
		return sc, nil
	case len(f) == 1 && f[0] == "hourly":
		sc.every = time.Hour
		return sc, nil
	case f[0] == "every":
		if len(f) != 2 {
			return nil, fmt.Errorf("%q: e.g. every 6h", s)
		}
		d, err := time.ParseDuration(f[1])
		if err != nil || d < time.Minute || d > 24*time.Hour {
			return nil, fmt.Errorf("%q: an interval from 1m to 24h, e.g. every 6h", s)
		}
		sc.every = d
		return sc, nil
	}
	if f[0] == "weekly" {
		f = f[1:]
	}
	if len(f) < 2 {
		return nil, fmt.Errorf("%q: days and a time, e.g. daily 08:00 or mon,thu 09:00", s)
	}
	switch f[0] {
	case "daily":
		for i := range sc.days {
			sc.days[i] = true
		}
	case "weekdays":
		for d := 1; d <= 5; d++ {
			sc.days[d] = true
		}
	case "weekends":
		sc.days[0], sc.days[6] = true, true
	default:
		for _, part := range strings.Split(f[0], ",") {
			a, b, isRange := strings.Cut(part, "-")
			da, ok1 := dayNames[a]
			if !ok1 {
				return nil, fmt.Errorf("%q: unknown day %q (mon tue wed thu fri sat sun)", s, a)
			}
			if !isRange {
				sc.days[da] = true
				continue
			}
			db, ok2 := dayNames[b]
			if !ok2 {
				return nil, fmt.Errorf("%q: unknown day %q", s, b)
			}
			for d := da; ; d = (d + 1) % 7 {
				sc.days[d] = true
				if d == db {
					break
				}
			}
		}
	}
	for _, t := range f[1:] {
		h, m, ok := strings.Cut(t, ":")
		hh, e1 := strconv.Atoi(h)
		mm, e2 := strconv.Atoi(m)
		if !ok || e1 != nil || e2 != nil || hh < 0 || hh > 23 || mm < 0 || mm > 59 {
			return nil, fmt.Errorf("%q: time %q: use HH:MM, e.g. 08:00", s, t)
		}
		sc.times = append(sc.times, time.Duration(hh)*time.Hour+time.Duration(mm)*time.Minute)
	}
	return sc, nil
}

// next is the first scheduled moment strictly after t (zero if off).
func (sc *schedule) next(t time.Time) time.Time {
	if sc.off {
		return time.Time{}
	}
	midnight := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
	if sc.every > 0 {
		n := midnight
		for !n.After(t) {
			n = n.Add(sc.every)
		}
		if n.After(midnight.AddDate(0, 0, 1)) { // the interval restarts at midnight
			n = midnight.AddDate(0, 0, 1)
		}
		return n
	}
	for day := 0; day < 8; day++ {
		d := midnight.AddDate(0, 0, day)
		if !sc.days[d.Weekday()] {
			continue
		}
		var best time.Time
		for _, tt := range sc.times {
			// the clock time on that date (adding a duration to midnight is an hour off on
			// the days the clocks change)
			c := time.Date(d.Year(), d.Month(), d.Day(), int(tt/time.Hour), int(tt%time.Hour/time.Minute), 0, 0, d.Location())
			if c.After(t) && (best.IsZero() || c.Before(best)) {
				best = c
			}
		}
		if !best.IsZero() {
			return best
		}
	}
	return time.Time{}
}
