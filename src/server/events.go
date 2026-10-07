package main

import (
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/agaloya/matriline/common/ledger"
)

// Events log and task check (user, 2026-10-05). state/events.log is the short story of the
// project for its admin: one line per admin action ("admin ...") and per thing the system
// noticed ("system ..."): results, alerts, inputs removed by hand, findings of the task
// check. About 70 bytes per task, so 100000 molecules stay under 10 MB. The details stay in
// server.log (technical) and the hash-chained ledger (the integrity record).

var eventsMu sync.Mutex

// event appends one line to state/events.log; who is "admin" or "system".
func (s *Server) event(who, f string, a ...any) {
	msg := strings.ReplaceAll(fmt.Sprintf(f, a...), "\n", " ")
	if len(msg) > 300 {
		msg = msg[:300] + "..."
	}
	line := time.Now().Format("2006-01-02 15:04:05") + " " + who + " " + msg + "\n"
	eventsMu.Lock()
	defer eventsMu.Unlock()
	fl, err := os.OpenFile(filepath.Join(s.conf().Root, dState, "events.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		s.log.Errorf("events log: %v", err)
		return
	}
	fl.WriteString(line)
	fl.Close()
}

// readOnlyCommands change nothing: not written to the events log.
var readOnlyCommands = map[string]bool{"status": true, "stats": true, "review": true, "history": true,
	"model": true, "verify": true, "version": true, "doctor": true, "check": true, "help": true, "events": true, "live": true, "molxyz": true}

func adminChanges(cmd string, a []string) bool {
	if readOnlyCommands[cmd] {
		return false
	}
	sub := ""
	if len(a) > 0 {
		sub = a[0]
	}
	switch cmd {
	case "clients":
		return sub != "list" && sub != ""
	case "config":
		return sub != "show" && sub != "validate"
	case "bans":
		return sub == "lift"
	case "update":
		return sub == "apply"
	case "clean":
		return sub != "--dry-run"
	}
	return true
}

// adminEvent records an admin command that changes something, with its outcome.
func (s *Server) adminEvent(cmd string, a []string, out string, err error) {
	if !adminChanges(cmd, a) {
		return
	}
	res := "ok"
	if err != nil {
		res = "failed: " + err.Error()
	} else if first, _, _ := strings.Cut(strings.TrimSpace(out), "\n"); first != "" {
		res = first
	}
	s.event("admin", "%s: %s", strings.TrimSpace(cmd+" "+strings.Join(a, " ")), res)
}

// checkLoop runs the task check a minute after start (the first scans have settled) and
// then every hour.
func (s *Server) checkLoop() {
	defer s.wg.Done()
	wait := time.Minute
	for {
		select {
		case <-s.stop:
			return
		case <-time.After(wait):
		}
		wait = time.Hour
		if _, err := s.checkTasks(true); err != nil {
			s.log.Errorf("task check: %v", err)
		}
	}
}

// reCopy matches the ".2", ".3"... a repeated name gets in completed/ and result directories.
var reCopy = regexp.MustCompile(`\.[0-9]+$`)

// checkTasks checks that every task the ledger knows is still somewhere in the working
// directory, and that no input name is in two places at once. Allowed together: an input
// in completed/ and its result(s) in output/ (or weird/, errors/); completed/ and input/
// while a recompute is pending (the task's last ledger entry is a requeue or a weird retry).
// A lost task is reported once (a "lost" ledger entry), duplicates on every check until
// they are solved. record = write findings to the events log and alert.
func (s *Server) checkTasks(record bool) ([]string, error) {
	s.moveMu.Lock() // a move in progress would look like a lost or duplicated task
	defer s.moveMu.Unlock()
	cfg := s.conf()
	entries, err := ledger.Read(filepath.Join(cfg.Root, dState, "ledger.log"), s.key.Pub)
	if err != nil {
		return nil, err
	}
	known := map[string]bool{}
	last := map[string]string{}
	for _, e := range entries {
		if e.Task == "" || strings.HasPrefix(e.Task, "~") {
			continue
		}
		if e.Kind == "queued" {
			known[e.Task] = true
		}
		if e.Kind != "verify" && e.Kind != "verify_planned" {
			last[e.Task] = e.Kind
		}
	}
	inputs := map[string][]string{} // task -> input/, paused/, cancelled/, completed/
	found := map[string]bool{}      // task -> found anywhere (inputs or result directories)
	for _, top := range []string{dInput, dPaused, dCancelled, dCompleted} {
		base := filepath.Join(cfg.Root, top)
		filepath.WalkDir(base, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			rel, _ := filepath.Rel(base, p)
			id := filepath.ToSlash(rel)
			if top == dCompleted {
				id = reCopy.ReplaceAllString(id, "")
			}
			if !hasExt(id, cfg.InputExt) {
				return nil
			}
			if n := len(inputs[id]); n == 0 || inputs[id][n-1] != top {
				inputs[id] = append(inputs[id], top)
			}
			found[id] = true
			return nil
		})
	}
	// result directories hold a copy of their input: <top>/<sub>/<stem>[.N]/<stem>.inp
	for _, top := range []string{dOutput, dWeird, dErrors, dOutdated, "other-versions"} {
		base := filepath.Join(cfg.Root, top)
		filepath.WalkDir(base, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !hasExt(d.Name(), cfg.InputExt) {
				return nil
			}
			rel, _ := filepath.Rel(base, filepath.Dir(filepath.Dir(p)))
			sub := filepath.ToSlash(rel)
			if top == "other-versions" || strings.HasPrefix(sub, "other-versions/") {
				// other-versions/<version>/<kind>/<sub> and errors/other-versions/<version>/<sub>
				if parts := strings.Split(sub, "/"); len(parts) >= 2 {
					sub = path.Join(append([]string{"."}, parts[2:]...)...)
				}
			}
			found[path.Join(sub, d.Name())] = true
			return nil
		})
	}
	var report []string
	ids := make([]string, 0, len(inputs))
	for id := range inputs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		places := inputs[id]
		if len(places) < 2 {
			continue
		}
		if len(places) == 2 && places[0] == dInput && places[1] == dCompleted && (last[id] == "requeue" || last[id] == "retry_weird") {
			continue
		}
		report = append(report, fmt.Sprintf("duplicate: %s is in %s at once", id, strings.Join(places, "/, ")+"/"))
	}
	var lost []string
	for id := range known {
		if !found[id] && last[id] != "lost" {
			lost = append(lost, id)
		}
	}
	sort.Strings(lost)
	for _, id := range lost {
		report = append(report, fmt.Sprintf("lost: %s is nowhere in the working directory (last record: %s)", id, last[id]))
		if record {
			s.ledger.Append(ledger.Entry{Kind: "lost", Task: id, Note: "found by the task check; last record: " + last[id]})
		}
	}
	if record {
		for _, r := range report {
			s.event("system", "task check: %s", r)
		}
		if len(report) > 0 {
			s.alertf("tasks", "task check: %d finding(s), e.g. %s (see state/events.log)", len(report), report[0])
		}
	}
	return report, nil
}

// cmdEvents shows the last n lines of state/events.log.
func (s *Server) cmdEvents(n int) (string, error) {
	b, err := os.ReadFile(filepath.Join(s.conf().Root, dState, "events.log"))
	if os.IsNotExist(err) {
		return "no events yet", nil
	}
	if err != nil {
		return "", err
	}
	lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n"), nil
}

func (s *Server) cmdCheck() (string, error) {
	report, err := s.checkTasks(true)
	if err != nil {
		return "", err
	}
	if len(report) == 0 {
		return "task check: every known task is in exactly one place", nil
	}
	return strings.Join(report, "\n") + "\n(lost tasks are recorded once in the ledger and reported only this time)", nil
}
