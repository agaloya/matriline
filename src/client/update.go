package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/agaloya/matriline/common/release"
	"github.com/agaloya/matriline/common/wire"
)

// Self-update (D66): the server relays a signed release; the client checks the signature
// with the key built into it, downloads its own binary in the background from its own
// update_url (never an address the server chooses), checks the SHA-256 against the
// signed list, runs it once ("version") and, when no job is left, puts it in place and
// restarts into it. A failed download waits longer each time; a re-offer of the same
// version changes nothing; a version that does not run here is never tried again.

type pendingUpdate struct {
	m     *release.Manifest
	fails int
	after time.Time // next download attempt
	busy  bool      // a download runs
	file  string    // downloaded and checked, waiting for no job to be left
}

// restartErr: the new program is in place; cmdRun restarts into it.
type restartErr struct{ exe, version string }

func (r *restartErr) Error() string { return "restarting into Matriline " + r.version }

// selfExe is the program an update replaces (a variable for the tests).
var selfExe = release.Executable

// canUpdate is what the client tells the server when it connects.
func canUpdate(cfg *Config) bool {
	return cfg.UpdateAllow && release.Enabled() && release.Writable()
}

func (a *Agent) offerUpdate(msg string) {
	if !canUpdate(a.cfg) {
		a.log.Infof("the server offers a new version; this client does not install updates (security.updates = false, no release key built in, or its folder is read-only)")
		return
	}
	var o wire.UpdateOffer
	if err := json.Unmarshal([]byte(msg), &o); err != nil {
		a.log.Warnf("update offer: %v", err)
		return
	}
	m, err := release.Verify(o.Manifest, o.Sig)
	if err != nil {
		a.log.Warnf("update refused: %v", err)
		return
	}
	if !release.Newer(m.Version, release.VersionOf(agentVersion)) {
		return
	}
	if _, ok := m.Files[release.AssetName("client", runtime.GOOS, runtime.GOARCH)]; !ok {
		a.log.Warnf("Matriline %s has no client for %s: not updated", m.Version, release.Platform())
		return
	}
	if b, err := os.ReadFile(refusedPath(a.cfg)); err == nil && strings.HasPrefix(string(b), m.Version+": ") {
		// it did not run here: say so again (the earlier notice may have been lost)
		a.updateMu.Lock()
		a.refusedNote = string(b)
		a.updateMu.Unlock()
		return
	}
	a.updateMu.Lock()
	defer a.updateMu.Unlock()
	if a.update != nil && !release.Newer(m.Version, a.update.m.Version) {
		return // already known: keeps its download state and back-off
	}
	if a.update != nil && a.update.file != "" && !a.update.busy {
		os.Remove(a.update.file) // an older download, replaced (one being checked is left to its owner)
	}
	a.update = &pendingUpdate{m: m}
	a.log.Infof("Matriline %s offered by the server (signature OK); it is installed when no job is left", m.Version)
}

func (a *Agent) pendingUpdate() *pendingUpdate {
	a.updateMu.Lock()
	defer a.updateMu.Unlock()
	return a.update
}

// tryUpdate downloads the pending release in the background and installs it once no job
// is left; a restartErr means "restart".
func (a *Agent) tryUpdate(ctx context.Context) error {
	a.updateMu.Lock()
	u := a.update
	if u == nil || u.busy {
		a.updateMu.Unlock()
		return nil
	}
	if u.file == "" {
		if time.Now().After(u.after) {
			u.busy = true
			go a.downloadUpdate(ctx, u)
		}
		a.updateMu.Unlock()
		return nil
	}
	a.updateMu.Unlock()
	a.jobsMu.Lock() // never both locks at once
	n := len(a.jobs)
	a.jobsMu.Unlock()
	if n > 0 {
		return nil
	}
	a.updateMu.Lock()
	if u.busy || u.file == "" || a.update != u {
		a.updateMu.Unlock()
		return nil
	}
	u.busy = true // the file is ours until installed or dropped
	a.updateMu.Unlock()
	done := func(keep bool) {
		a.updateMu.Lock()
		u.busy = false
		if !keep {
			os.Remove(u.file)
			u.file = ""
		}
		a.updateMu.Unlock()
	}
	exe, err := selfExe()
	name := release.AssetName("client", runtime.GOOS, runtime.GOARCH)
	if err == nil {
		err = release.Preflight(ctx, u.file, u.m, name)
		if errors.Is(err, release.ErrDoesNotRun) {
			// a verdict: never installed, never retried on this computer; the server is told
			a.log.Warnf("update to %s refused: %v (the server is told)", u.m.Version, err)
			note := u.m.Version + ": " + err.Error()
			os.WriteFile(refusedPath(a.cfg), []byte(note), 0o600)
			done(false)
			a.updateMu.Lock()
			a.refusedNote = note
			if a.update == u {
				a.update = nil
			}
			a.updateMu.Unlock()
			return nil
		}
		if err == nil {
			a.updateMu.Lock()
			replaced := a.update != u
			a.updateMu.Unlock()
			if replaced {
				done(false) // a newer release was offered meanwhile
				return nil
			}
			err = release.Install(u.file, exe)
		}
	}
	if err != nil {
		// not a verdict on the release (could not start it, timed out, file gone...): later
		a.log.Warnf("update to %s: %v", u.m.Version, err)
		done(false)
		a.updateMu.Lock()
		a.backoff(u)
		a.updateMu.Unlock()
		return nil
	}
	a.log.Infof("Matriline %s installed (the previous program stays as %s.previous); restarting", u.m.Version, filepath.Base(exe))
	return &restartErr{exe: exe, version: u.m.Version}
}

func (a *Agent) downloadUpdate(ctx context.Context, u *pendingUpdate) {
	exe, err := selfExe()
	var file string
	if err == nil {
		file, err = release.Download(ctx, a.cfg.UpdateURL, u.m, release.AssetName("client", runtime.GOOS, runtime.GOARCH), filepath.Dir(exe))
	}
	a.updateMu.Lock()
	defer a.updateMu.Unlock()
	u.busy = false
	if a.update != u { // replaced by a newer offer meanwhile
		if err == nil {
			os.Remove(file)
		}
		return
	}
	if err != nil {
		a.backoff(u)
		a.log.Warnf("update to %s: %v (next try %s)", u.m.Version, err, u.after.Format("Jan 2 15:04 MST"))
		return
	}
	u.file = file
}

// refusal is the refusal to report to the server; refusalSent clears it once sent.
func (a *Agent) refusal() string {
	a.updateMu.Lock()
	defer a.updateMu.Unlock()
	return a.refusedNote
}

func (a *Agent) refusalSent(n string) {
	a.updateMu.Lock()
	if a.refusedNote == n {
		a.refusedNote = ""
	}
	a.updateMu.Unlock()
}

// refusedPath records a release that did not run on this computer (Preflight).
func refusedPath(cfg *Config) string { return filepath.Join(cfg.StateDir, "update-refused") }

// backoff: 1 h, 2 h, 4 h ... up to a day between attempts. Caller holds updateMu.
func (a *Agent) backoff(u *pendingUpdate) {
	u.fails++
	u.after = time.Now().Add(min(time.Hour<<min(u.fails-1, 5), 24*time.Hour))
}
