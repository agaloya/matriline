package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/agaloya/matriline/common/ident"
	"github.com/agaloya/matriline/common/ledger"
	"github.com/agaloya/matriline/common/release"
	"github.com/agaloya/matriline/common/wire"
)

// Updates (D66). With update.mode = alert or auto the server looks for a new signed
// release every update.check_interval (a month). alert: the admin is told and runs
// 'update apply' when it suits. auto, or 'update apply': only if every client can update
// (it said so, a binary exists for its platform), connected clients get the release (each
// one checks the signature itself and installs it when no job is left); once none of the
// connected clients runs the old version, the server installs its own and restarts. A
// client offline meanwhile gets it when it comes back. Nothing unsigned is ever installed.
// A client on which the new program does not run (its preflight) refuses it: that client
// is not offered it again and the server waits for it, telling the admin, who can go on
// with 'update apply --force' (that client keeps the old version). A single client never
// cancels a release for everyone (code review).

type updateState struct {
	LastCheck time.Time `json:"last_check"`
	Alerted   string    `json:"alerted,omitempty"` // the version the admin was told about
	Rollout   string    `json:"rollout,omitempty"` // the version being installed
	Since     time.Time `json:"since,omitempty"`   // rollout start
	Warned    bool      `json:"warned,omitempty"`  // told that clients take long
	Force     bool      `json:"force,omitempty"`   // update apply --force: do not wait for every client
	Failed    string    `json:"failed,omitempty"`  // a version that did not run on this server: not tried again unless --force
	// Refused: version -> client id -> why its preflight failed there
	Refused map[string]map[string]string `json:"refused,omitempty"`
	Fails   int                          `json:"fails,omitempty"` // failed downloads of the server's own program
	NextTry time.Time                    `json:"next_try,omitempty"`
}

// selfExe is the program an update replaces (a variable for the tests).
var selfExe = release.Executable

func (s *Server) updateDir() string { return filepath.Join(s.conf().Root, dState, "update") }

// updateState changes the stored state under one lock: load, change, save. Nothing slow
// (downloads) happens inside f.
func (s *Server) updateState(f func(st *updateState)) updateState {
	s.updMu.Lock()
	defer s.updMu.Unlock()
	st := s.loadUpdateState()
	f(&st)
	s.saveUpdateState(st)
	return st
}

func (s *Server) loadUpdateState() updateState {
	var st updateState
	if b, err := os.ReadFile(filepath.Join(s.updateDir(), "state.json")); err == nil {
		json.Unmarshal(b, &st)
	}
	return st
}

func (s *Server) saveUpdateState(st updateState) {
	os.MkdirAll(s.updateDir(), 0o700)
	b, _ := json.Marshal(st)
	if err := ident.WriteFileAtomic(filepath.Join(s.updateDir(), "state.json"), b, 0o600); err != nil {
		s.log.Errorf("update state: %v", err)
	}
}

// knownRelease is the newest verified release stored (state/update/release.json + .sig).
func (s *Server) knownRelease() (body, sig []byte, m *release.Manifest) {
	body, err1 := os.ReadFile(filepath.Join(s.updateDir(), release.ManifestName))
	sig, err2 := os.ReadFile(filepath.Join(s.updateDir(), release.SigName))
	if err1 != nil || err2 != nil {
		return nil, nil, nil
	}
	m, err := release.Verify(body, sig)
	if err != nil {
		return nil, nil, nil
	}
	return body, sig, m
}

func (s *Server) updateLoop() {
	defer s.wg.Done()
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-s.stop:
			return
		case <-t.C:
		}
		cfg := s.conf()
		st := s.loadUpdateState()
		if st.Rollout != "" {
			s.advanceRollout()
			continue
		}
		if cfg.UpdateMode != "off" && time.Since(st.LastCheck) >= cfg.UpdateInterval {
			if out, err := s.checkUpdate(context.Background(), cfg.UpdateMode == "auto"); err != nil {
				s.log.Warnf("update check: %v", err)
			} else {
				s.log.Infof("update check: %s", out)
			}
		}
	}
}

// checkUpdate fetches the latest release; apply starts installing it.
func (s *Server) checkUpdate(ctx context.Context, apply bool) (string, error) {
	cfg := s.conf()
	if !release.Enabled() {
		return "", fmt.Errorf("this server was built without a release key: it cannot verify releases")
	}
	if err := release.URLOK(cfg.UpdateURL); err != nil {
		return "", err
	}
	st := s.updateState(func(st *updateState) { st.LastCheck = time.Now() })
	body, sig, m, err := release.Fetch(ctx, cfg.UpdateURL)
	if err != nil {
		s.event("system", "update check failed: %v", err)
		return "", err
	}
	own := release.VersionOf(agentVersion)
	if !release.Newer(m.Version, own) {
		return fmt.Sprintf("up to date (%s; latest release %s)", own, m.Version), nil
	}
	if m.Version == st.Failed && !apply {
		return fmt.Sprintf("Matriline %s did not run on this server: waiting for a newer release ('update apply --force' tries it again)", m.Version), nil
	}
	os.MkdirAll(s.updateDir(), 0o700)
	if err := ident.WriteFileAtomic(filepath.Join(s.updateDir(), release.ManifestName), body, 0o600); err != nil {
		return "", err
	}
	if err := ident.WriteFileAtomic(filepath.Join(s.updateDir(), release.SigName), sig, 0o600); err != nil {
		return "", err
	}
	if !apply {
		tell := false
		s.updateState(func(st *updateState) { tell, st.Alerted = st.Alerted != m.Version, m.Version })
		if tell {
			s.alertf("update", "Matriline %s is out (commit %s, %s; signature OK). This server runs %s. %s Install it with 'matriline-server update apply'.",
				m.Version, m.Commit, m.Date, own, s.blockersText(m))
		}
		return fmt.Sprintf("Matriline %s is available (this server runs %s); 'update apply' installs it", m.Version, own), nil
	}
	return s.startRollout(m, false)
}

// updateBlockers lists why the clients cannot all move to release m.
func (s *Server) updateBlockers(m *release.Manifest) []string {
	var out []string
	for _, c := range s.reg.list() {
		if (c.Status != stActive && c.Status != stDraining) || time.Since(c.LastSeen) > 30*24*time.Hour {
			continue // gone, pending approval or quarantined: does not hold the others back
		}
		switch {
		case c.Platform == "":
			out = append(out, c.Name+" (its version cannot update itself)")
		case !c.CanUpdate:
			out = append(out, c.Name+" (updates off, or its folder is read-only)")
		default:
			os, arch, _ := strings.Cut(c.Platform, "-")
			if _, ok := m.Files[release.AssetName("client", os, arch)]; !ok {
				out = append(out, c.Name+" (no client for "+c.Platform+" in the release)")
			}
		}
	}
	if _, ok := m.Files[release.AssetName("server", runtime.GOOS, runtime.GOARCH)]; !ok {
		out = append(out, "this server (no server for "+release.Platform()+" in the release)")
	} else if !release.Writable() {
		out = append(out, "this server (its program's folder is read-only)")
	}
	sort.Strings(out)
	return out
}

func (s *Server) blockersText(m *release.Manifest) string {
	if b := s.updateBlockers(m); len(b) > 0 {
		return "It cannot be installed yet: " + strings.Join(b, ", ") + "."
	}
	return "Every client can update."
}

func (s *Server) startRollout(m *release.Manifest, force bool) (string, error) {
	if b := s.updateBlockers(m); len(b) > 0 && !force {
		tell := false
		s.updateState(func(st *updateState) { tell, st.Alerted = st.Alerted != "blocked "+m.Version, "blocked "+m.Version })
		if tell {
			s.alertf("update", "Matriline %s not installed: not everyone can update (%s)", m.Version, strings.Join(b, ", "))
		}
		return "", fmt.Errorf("Matriline %s not installed: not everyone can update: %s", m.Version, strings.Join(b, ", "))
	}
	var failed bool
	s.updateState(func(st *updateState) {
		if failed = st.Failed == m.Version && !force; failed {
			return
		}
		if force { // the admin decides: try again everywhere
			st.Failed = ""
			delete(st.Refused, m.Version)
		}
		st.Rollout, st.Since, st.Warned, st.Force, st.Fails, st.NextTry = m.Version, time.Now(), false, force, 0, time.Time{}
	})
	if failed {
		return "", fmt.Errorf("Matriline %s did not run on this server: 'update apply --force' tries it again", m.Version)
	}
	s.ledger.Append(ledger.Entry{Kind: "update", Note: "installing Matriline " + m.Version + " (commit " + m.Commit + ")"})
	s.event("system", "update to %s started: clients first, then this server", m.Version)
	n := 0
	s.sessMu.Lock()
	for _, ss := range s.sessions {
		if s.offerUpdate(ss) {
			n++
		}
	}
	s.sessMu.Unlock()
	return fmt.Sprintf("installing Matriline %s: offered to %d connected client(s); the server follows when they run it", m.Version, n), nil
}

// offerUpdate sends the known release to a client that runs an older version, during a
// rollout or once this server runs that release itself (clients that were offline).
func (s *Server) offerUpdate(ss *Session) bool {
	body, sig, m := s.knownRelease()
	if m == nil || !release.Newer(m.Version, release.VersionOf(ss.agent)) {
		return false
	}
	st := s.loadUpdateState()
	if st.Rollout != m.Version && m.Version != release.VersionOf(agentVersion) {
		return false
	}
	if _, refused := st.Refused[m.Version][ss.id]; refused {
		return false // it does not run there; the admin was told
	}
	msg, _ := json.Marshal(wire.UpdateOffer{Manifest: body, Sig: sig})
	if err := ss.conn.Send(wireNotice("update", string(msg))); err != nil {
		return false
	}
	ss.offered.Store(m.Version)
	s.log.Infof("offered Matriline %s to %s (runs %s)", m.Version, ss.name, ss.agent)
	return true
}

// advanceRollout installs the server's own release once no connected client runs an
// older version (or at once with --force).
func (s *Server) advanceRollout() {
	st := s.loadUpdateState()
	_, _, m := s.knownRelease()
	if m == nil {
		s.updateState(func(st *updateState) { st.Rollout = "" })
		s.alertf("update", "the update stopped: its signed release can no longer be installed (expired: the maintainer signs a new one; 'update check' finds it)")
		return
	}
	if m.Version != st.Rollout || !release.Newer(m.Version, release.VersionOf(agentVersion)) {
		s.updateState(func(st *updateState) { st.Rollout = "" }) // replaced, or this server already runs it (or newer: never go back)
		return
	}
	var old []string
	s.sessMu.Lock()
	for _, ss := range s.sessions {
		if release.Newer(m.Version, release.VersionOf(ss.agent)) {
			old = append(old, ss.name)
		}
	}
	s.sessMu.Unlock()
	if len(old) > 0 && !st.Force {
		if !st.Warned && time.Since(st.Since) > s.conf().UpdateWait {
			s.updateState(func(st *updateState) { st.Warned = true })
			sort.Strings(old)
			s.alertf("update", "update to %s: after %s these clients still run the old version (they install it when their jobs are done; 'update apply --force' installs the server's anyway): %s",
				m.Version, humanDuration(s.conf().UpdateWait), strings.Join(old, ", "))
		}
		return
	}
	if time.Since(st.Since) < 2*time.Minute || time.Now().Before(st.NextTry) {
		return // the clients that just restarted come back first; or a failed download waits
	}
	exe, err := selfExe()
	name := release.AssetName("server", runtime.GOOS, runtime.GOARCH)
	var tmp string
	if err == nil {
		tmp, err = release.Download(context.Background(), s.conf().UpdateURL, m, name, filepath.Dir(exe))
	}
	if err == nil {
		if err = release.Preflight(context.Background(), tmp, m, name); errors.Is(err, release.ErrDoesNotRun) {
			os.Remove(tmp)
			s.updateState(func(st *updateState) { st.Rollout, st.Failed = "", m.Version })
			s.alertf("update", "Matriline %s was not installed on this server: %v. This server stays on %s ('update apply --force' tries it again)", m.Version, err, agentVersion)
			return
		}
		if err == nil {
			err = release.Install(tmp, exe)
		}
		if err != nil {
			os.Remove(tmp)
		}
	}
	if err != nil {
		st = s.updateState(func(st *updateState) {
			st.Fails++
			st.NextTry = time.Now().Add(min(time.Hour<<min(st.Fails-1, 5), 24*time.Hour))
		})
		if st.Fails == 1 {
			s.alertf("update", "the clients run %s, but this server could not install it yet: %v (tried again after 1 h, 2 h, 4 h ...)", m.Version, err)
		}
		return
	}
	s.updateState(func(st *updateState) { st.Rollout, st.Fails, st.NextTry = "", 0, time.Time{} })
	s.ledger.Append(ledger.Entry{Kind: "update", Note: "server program replaced by Matriline " + m.Version + "; restarting"})
	s.event("system", "Matriline %s installed (the previous program stays as %s.previous); restarting", m.Version, filepath.Base(exe))
	s.setRestart(exe)
}

// setRestart stops the server and has cmdRun start exe (the same program, or an update).
func (s *Server) setRestart(exe string) {
	s.updMu.Lock()
	s.restartExe = exe
	s.updMu.Unlock()
	select {
	case s.stopReq <- struct{}{}:
	default:
	}
}

func (s *Server) restartTo() string {
	s.updMu.Lock()
	defer s.updMu.Unlock()
	return s.restartExe
}

// updateRefused: the new program does not run on this client (its preflight). Counted only
// from a client that was offered that version; it is not offered to that client again,
// the rollout waits for it and the admin decides ('update apply --force').
func (s *Server) updateRefused(ss *Session, msg string) {
	version, why, _ := strings.Cut(msg, ": ")
	if offered, _ := ss.offered.Load().(string); offered == "" || offered != version {
		return // not offered to it: nothing to refuse
	}
	first := false
	s.updateState(func(st *updateState) {
		if st.Rollout != version {
			return
		}
		if st.Refused == nil {
			st.Refused = map[string]map[string]string{}
		}
		if st.Refused[version] == nil {
			st.Refused[version] = map[string]string{}
		}
		_, seen := st.Refused[version][ss.id]
		first = !seen
		st.Refused[version][ss.id] = why
	})
	if first {
		platform := "?"
		if c := s.reg.get(ss.id); c != nil && c.Platform != "" {
			platform = c.Platform
		}
		s.alertf("update", "Matriline %s does not run on client %s (%s, %s): %s. It keeps its version and the update waits for it; 'update apply --force' goes on without it",
			version, ss.name, platform, ss.agent, why)
	}
}

// cmdUpdate: update [status|check|apply].
func (s *Server) cmdUpdate(a []string) (string, error) {
	sub := "status"
	if len(a) > 0 {
		sub = a[0]
	}
	cfg := s.conf()
	switch sub {
	case "status":
		st := s.loadUpdateState()
		var b strings.Builder
		fmt.Fprintf(&b, "this server: %s; update.mode = %s, every %s, from %s\n", agentVersion, cfg.UpdateMode, humanDuration(cfg.UpdateInterval), cfg.UpdateURL)
		if !release.Enabled() {
			b.WriteString("built without a release key: it never updates itself\n")
		}
		if !st.LastCheck.IsZero() {
			fmt.Fprintf(&b, "last check: %s\n", st.LastCheck.Format("2006-01-02 15:04"))
		}
		if _, _, m := s.knownRelease(); m != nil && release.Newer(m.Version, release.VersionOf(agentVersion)) {
			fmt.Fprintf(&b, "available: %s (commit %s, %s). %s\n", m.Version, m.Commit, m.Date, s.blockersText(m))
		}
		if st.Rollout != "" {
			fmt.Fprintf(&b, "installing %s since %s\n", st.Rollout, st.Since.Format("2006-01-02 15:04"))
		}
		if st.Failed != "" {
			fmt.Fprintf(&b, "%s did not run on this server ('update apply --force' tries it again)\n", st.Failed)
		}
		for v, cs := range st.Refused {
			for id, why := range cs {
				name := id
				if c := s.reg.get(id); c != nil {
					name = c.Name
				}
				fmt.Fprintf(&b, "%s does not run on client %s: %s\n", v, name, why)
			}
		}
		return strings.TrimSpace(b.String()), nil
	case "check":
		return s.checkUpdate(context.Background(), false)
	case "apply":
		force := len(a) > 1 && a[1] == "--force"
		if _, err := s.checkUpdate(context.Background(), false); err != nil {
			return "", err
		}
		_, _, m := s.knownRelease()
		if m == nil || !release.Newer(m.Version, release.VersionOf(agentVersion)) {
			return "already up to date", nil
		}
		return s.startRollout(m, force)
	}
	return "", fmt.Errorf("usage: update [status|check|apply [--force]]")
}
