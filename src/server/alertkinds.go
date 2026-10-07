package main

import (
	"strings"
	"time"
)

// Every alert, with when it is sent by default ([alerts] in server.conf; user: list them
// and let the admin enable the ones of interest). Modes: off, now (at once, the same
// alert at most once per alerts.now_limit), summary (in the next scheduled summary).
var alertKinds = []struct{ Kind, Mode, Help string }{
	{"storage", "now", "the disk is nearly full, or results are refused for lack of space"},
	{"verify_failed", "now", "a result failed a check (it went to weird/)"},
	{"quarantine", "now", "a client was quarantined (every result of it is checked)"},
	{"client_lost", "now", "a client stopped reporting the calculations it was running"},
	{"tasks", "now", "a task was lost or duplicated ('check')"},
	{"campaign_done", "now", "every input is done: nothing queued, running or being checked"},
	{"update", "now", "a new Matriline version, or a problem installing it"},
	{"errors", "summary", "a task went to errors/ (ORCA failed), or a client was paused after errors"},
	{"orca_version", "summary", "a client with another ORCA version than this campaign's"},
	{"enrolled", "summary", "a new client joined with its credential"},
	{"ban", "summary", "an address was banned (port scanners, failed logins)"},
}

func alertModeDefault(kind string) string {
	for _, a := range alertKinds {
		if a.Kind == kind {
			return a.Mode
		}
	}
	return "now"
}

// alertConfig reads the [alerts] modes and the summary schedule. Files written before
// the modes ("events = ..." and "digest = ...") keep their meaning: the listed events now
// (or in a summary every digest interval), the rest off.
func alertConfig(c *Config, has func(string) bool, str func(string, string) string, dur func(string, time.Duration) time.Duration) []string {
	var w []string
	c.AlertModes = map[string]string{}
	old := has("alerts.events") && !has("alerts.storage")
	listed := map[string]bool{}
	digest := time.Duration(0)
	if old {
		for _, e := range strings.Split(str("alerts.events", ""), ",") {
			listed[strings.TrimSpace(e)] = true
		}
		digest = dur("alerts.digest", 0)
	}
	for _, a := range alertKinds {
		m := strings.ToLower(str("alerts."+a.Kind, a.Mode))
		if old {
			switch {
			case a.Kind == "update" || listed[a.Kind] && digest == 0:
				m = "now"
			case listed[a.Kind]:
				m = "summary"
			default:
				m = "off"
			}
		}
		switch m {
		case "off", "now", "summary":
		default:
			w = append(w, "ERROR: alerts."+a.Kind+" must be off, now or summary")
		}
		c.AlertModes[a.Kind] = m
	}
	c.AlertNowLimit = dur("alerts.now_limit", time.Hour)
	if c.AlertNowLimit < time.Minute { // events a client can cause must not flood the channels
		w = append(w, "WARNING: alerts.now_limit is at least 1m; 1m is used")
		c.AlertNowLimit = time.Minute
	}
	spec := str("alerts.summary", "daily 08:00")
	if old && digest > 0 {
		spec = "every " + digest.String()
	}
	sc, err := parseSchedule(spec)
	if err != nil {
		w = append(w, "ERROR: alerts.summary: "+err.Error())
		sc, _ = parseSchedule("off")
	}
	c.AlertSummary, c.AlertSummarySpec = sc, spec
	if sc.off {
		for k, m := range c.AlertModes {
			if m == "summary" {
				w = append(w, "WARNING: alerts."+k+" = summary, but alerts.summary = off: those alerts are never sent")
				break
			}
		}
	}
	return w
}
