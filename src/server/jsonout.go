package main

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/agaloya/matriline/common/ledger"
)

// --json: status, live, clients, review, history and events as JSON, for scripts (user:
// "interactive like an API"). The text output stays the default.

var jsonCommands = map[string]bool{"status": true, "live": true, "clients": true, "review": true, "history": true, "events": true}

// withoutJSON takes "--json" out of the arguments.
func withoutJSON(a []string) ([]string, bool) {
	var out []string
	found := false
	for _, w := range a {
		if w == "--json" {
			found = true
		} else {
			out = append(out, w)
		}
	}
	return out, found
}

func jsonText(v any) (string, error) {
	b, err := json.MarshalIndent(v, "", "  ")
	return string(b), err
}

func (s *Server) cmdJSON(cmd string, a []string) (string, error) {
	switch cmd {
	case "status":
		return s.statusJSON()
	case "live":
		var rows []map[string]any
		for _, r := range s.liveRows() {
			rows = append(rows, map[string]any{"n": r.n, "client": r.client, "task": r.label, "subtask": r.task,
				"kind": r.kind, "started": r.started.UTC().Format(time.RFC3339), "seconds": int(time.Since(r.started).Seconds()),
				"output_bytes": r.progress, "lost": r.lost, "tail": r.tail, "phase": r.phase})
		}
		return jsonText(map[string]any{"running": rows})
	case "clients":
		var cs []map[string]any
		for _, c := range s.reg.list() {
			cs = append(cs, map[string]any{"name": c.Name, "id": c.ID, "host": c.Hostname, "status": c.Status,
				"results": c.Results, "failed_checks": c.Failures, "orca_errors": c.OrcaFails,
				"last_seen": timeOrEmpty(c.LastSeen), "address": c.LastAddr, "connected": s.session(c.ID) != nil,
				"version": c.Agent, "platform": c.Platform})
		}
		return jsonText(map[string]any{"clients": cs})
	case "review":
		cfg := s.conf()
		var ws []map[string]any
		filepath.WalkDir(filepath.Join(cfg.Root, dWeird), func(p string, d fs.DirEntry, err error) error {
			if err == nil && d.Name() == verdictFile {
				rel, _ := filepath.Rel(cfg.Root, filepath.Dir(p))
				b, _ := os.ReadFile(p)
				var reasons []string
				for _, l := range strings.Split(string(b), "\n") {
					if k, v, ok := strings.Cut(l, "="); ok && strings.HasPrefix(k, "reason.") {
						reasons = append(reasons, v)
					}
				}
				ws = append(ws, map[string]any{"path": filepath.ToSlash(rel), "reasons": reasons})
			}
			return nil
		})
		return jsonText(map[string]any{"weird": ws})
	case "history":
		if len(a) == 0 {
			return "", fmt.Errorf("usage: history <task> --json")
		}
		_, rest := splitSpool(a[0])
		rest = strings.TrimSuffix(rest, "/")
		es, err := ledger.Read(filepath.Join(s.conf().Root, dState, "ledger.log"), s.key.Pub)
		if err != nil {
			return "", err
		}
		var hs []map[string]any
		for _, e := range es {
			if e.Task == "" || !(e.Task == rest || strings.HasPrefix(e.Task, rest) || strings.TrimSuffix(e.Task, path.Ext(e.Task)) == rest) {
				continue
			}
			name := e.Client
			if c := s.reg.get(e.Client); c != nil {
				name = c.Name
			}
			hs = append(hs, map[string]any{"time": time.Unix(e.Time, 0).UTC().Format(time.RFC3339), "kind": e.Kind,
				"task": e.Task, "client": name, "verdict": e.Verdict, "note": e.Note})
		}
		return jsonText(map[string]any{"history": hs})
	case "events":
		n := 50
		if len(a) > 0 {
			if v, err := strconv.Atoi(a[0]); err == nil && v > 0 {
				n = v
			}
		}
		out, err := s.cmdEvents(n)
		if err != nil {
			return "", err
		}
		var evs []map[string]any
		for _, l := range strings.Split(out, "\n") {
			f := strings.SplitN(l, " ", 4) // date time who text
			if len(f) == 4 {
				evs = append(evs, map[string]any{"time": f[0] + " " + f[1], "by": f[2], "text": f[3]})
			}
		}
		return jsonText(map[string]any{"events": evs})
	}
	return "", fmt.Errorf("--json is available for: status, live, clients, review, history, events")
}

func timeOrEmpty(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

func (s *Server) statusJSON() (string, error) {
	cfg := s.conf()
	counts := s.spoolCounts("")
	s.store.mu.Lock()
	running, lost, queued, internal := 0, 0, 0, 0
	for _, at := range s.store.Attempts {
		if at.Lost {
			lost++
		} else {
			running++
		}
	}
	for _, t := range s.store.Tasks {
		if t.Internal {
			internal++
		} else {
			queued++
		}
	}
	s.store.mu.Unlock()
	s.sessMu.Lock()
	ids := make([]string, 0, len(s.sessions))
	for id := range s.sessions {
		ids = append(ids, id)
	}
	s.sessMu.Unlock()
	sort.Strings(ids)
	var clients []map[string]any
	for _, id := range ids {
		ss := s.session(id)
		if ss == nil {
			continue
		}
		n := 0
		s.store.mu.Lock()
		for _, at := range s.store.Attempts {
			if at.ClientID == id && !at.Lost {
				n++
			}
		}
		s.store.mu.Unlock()
		acc := ss.offer.Accepting
		if !ss.lastBeat.IsZero() {
			acc = ss.hbAccept
		}
		clients = append(clients, map[string]any{"name": ss.name, "id": id, "slots": ss.offer.Slots, "running": n,
			"accepting": acc, "last_heartbeat": timeOrEmpty(ss.lastBeat), "power": ss.powerText()})
	}
	// one entry per top-level sub-directory (project folder) of the spool
	projects := []map[string]any{}
	for _, sub := range s.topSubdirs() {
		q, r, c := s.subCounts(sub)
		projects = append(projects, map[string]any{"name": sub, "queued": max(q-r, 0), "running": r,
			"output": c[dOutput], "weird": c[dWeird], "errors": c[dErrors], "paused": c[dPaused],
			"cancelled": c[dCancelled], "other_versions": c["other-versions"]})
	}
	return jsonText(map[string]any{
		"server": s.key.ID(), "spool": cfg.Root, "version": versionText(),
		"queued": queued, "running": running, "lost": lost, "verification_subtasks": internal,
		"results": map[string]int{"output": counts[dOutput], "weird": counts[dWeird], "errors": counts[dErrors],
			"outdated": counts[dOutdated], "other_versions": counts["other-versions"]},
		"inputs":  map[string]int{"completed": counts[dCompleted], "paused": counts[dPaused], "cancelled": counts[dCancelled]},
		"clients": clients, "projects": projects,
	})
}
