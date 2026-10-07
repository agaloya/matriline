package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/agaloya/matriline/common/i18n"
	"github.com/agaloya/matriline/common/ident"
	"github.com/agaloya/matriline/common/lock"
)

// The owner of the computer can stop lending it for a while without touching client.conf:
// 'pause [duration] [--now]' writes state/PAUSED, which the running service reads before
// taking work and at every heartbeat. Without --now running jobs finish; with --now they are
// stopped and returned to the server as a machine failure, so another host computes them
// (from this job's orbitals when the server keeps checkpoints).

type pauseState struct {
	Until time.Time `json:"until"` // zero: until 'resume'
	Now   bool      `json:"now"`   // stop running jobs too
	Set   time.Time `json:"set"`
}

func pausePath(cfg *Config) string { return filepath.Join(cfg.StateDir, "PAUSED") }

// readPause returns the pause in force, if any.
func readPause(cfg *Config) (pauseState, bool) {
	var p pauseState
	b, err := os.ReadFile(pausePath(cfg))
	if err != nil || json.Unmarshal(b, &p) != nil {
		return p, false
	}
	return p, p.Until.IsZero() || time.Now().Before(p.Until)
}

func (p pauseState) text() string {
	t := "paused by this computer's owner"
	if !p.Until.IsZero() {
		t += " until " + p.Until.Format("Jan 2 15:04 MST") // the zone: the admin may read it elsewhere
	}
	return t
}

// shown: the pause for this computer's owner, in general.language (text() goes to the
// log and the server, in English).
func (p pauseState) shown() string {
	if p.Until.IsZero() {
		return i18n.T("paused by this computer's owner")
	}
	return i18n.Tf("paused by this computer's owner until %s", p.Until.Format("Jan 2 15:04 MST"))
}

func cmdPause(cfgPath string, args []string) error {
	cfg, _, err := loadConfig(cfgPath)
	if err != nil {
		return err
	}
	p := pauseState{Set: time.Now()}
	for _, a := range args {
		switch {
		case a == "--now":
			p.Now = true
		case strings.HasPrefix(a, "-"):
			return errors.New(i18n.T("usage: pause [duration, e.g. 2h or 45m] [--now]"))
		default:
			d, err := time.ParseDuration(a)
			if err != nil || d <= 0 {
				return errors.New(i18n.Tf("duration %q: use e.g. 2h, 45m or 1h30m", a))
			}
			p.Until = time.Now().Add(d).Round(time.Second)
		}
	}
	if old, on := readPause(cfg); on && !old.Until.IsZero() && p.Until.IsZero() {
		fmt.Println(i18n.Tf("(it was paused until %s; now until 'resume')", old.Until.Format("Jan 2 15:04 MST")))
	}
	b, _ := json.Marshal(p)
	if err := os.MkdirAll(cfg.StateDir, 0o700); err != nil {
		return err
	}
	if err := ident.WriteFileAtomic(pausePath(cfg), b, 0o600); err != nil {
		return err
	}
	fmt.Println(i18n.Tf("%s: no new jobs are taken.", p.shown()))
	if p.Now {
		fmt.Println(i18n.T("Running jobs are stopped within seconds and returned to the server."))
	} else {
		fmt.Println(i18n.T("Running jobs finish first ('pause --now' stops them). 'resume' ends the pause."))
	}
	return nil
}

func cmdResume(cfgPath string) error {
	cfg, _, err := loadConfig(cfgPath)
	if err != nil {
		return err
	}
	if _, active := readPause(cfg); !active {
		os.Remove(pausePath(cfg)) // an expired one
		fmt.Println(i18n.T("not paused"))
		return nil
	}
	if err := os.Remove(pausePath(cfg)); err != nil && !os.IsNotExist(err) {
		return err
	}
	fmt.Println(i18n.T("lending again: new jobs are taken within half a minute"))
	return nil
}

// yieldJobs stops the running jobs for a pause --now; each is reported as a machine
// failure, not the input's fault.
func (a *Agent) yieldJobs(why string) {
	a.jobsMu.Lock()
	defer a.jobsMu.Unlock()
	for _, j := range a.jobs {
		if j.State == jsRunning {
			select {
			case j.yield <- why:
			default:
			}
		}
	}
}

// The running service writes state/connection.json so that 'status' (another process)
// can say whether it is connected, to which server and since when.
type connState struct {
	Connected bool      `json:"connected"`
	Server    string    `json:"server"`
	Status    string    `json:"status,omitempty"` // active, pending, quarantined...
	Since     time.Time `json:"since"`
	LastError string    `json:"last_error,omitempty"`
	PID       int       `json:"pid"`
}

func (a *Agent) noteConnection(up bool, status, lastErr string) {
	p := filepath.Join(a.cfg.StateDir, "connection.json")
	var old connState
	if b, err := os.ReadFile(p); err == nil {
		json.Unmarshal(b, &old)
	}
	st := connState{Connected: up, Server: a.cred.ServerAddress, Status: status, Since: time.Now(), LastError: lastErr, PID: os.Getpid()}
	if old.Connected == up && old.PID == st.PID && !old.Since.IsZero() {
		st.Since = old.Since // still down (or up) since then
	}
	if b, err := json.Marshal(st); err == nil {
		ident.WriteFileAtomic(p, b, 0o600)
	}
}

// connectionText describes the service's connection for 'status'.
func connectionText(cfg *Config) string {
	b, err := os.ReadFile(filepath.Join(cfg.StateDir, "connection.json"))
	var st connState
	if err != nil || json.Unmarshal(b, &st) != nil {
		if lock.Held(cfg.StateDir, "client.lock") {
			// running but not connected yet: the first start fingerprints every ORCA (user:
			// "not started yet" right after 'service install' looked like a failure)
			return i18n.T("service: running, starting up (the first start checks ORCA: a minute or two per installation; the log is state/client.log)")
		}
		return i18n.T("service: not started yet")
	}
	if !lock.Held(cfg.StateDir, "client.lock") {
		return i18n.T("service: not running (start it with 'matriline-client run')")
	}
	if !st.Connected && st.Status == "starting" {
		return i18n.T("service: running, starting up (the first start checks ORCA: a minute or two per installation; the log is state/client.log)")
	}
	ago := time.Since(st.Since).Round(time.Second)
	if st.Connected {
		return i18n.Tf("service: running, connected to %s since %s ago (status %s)", st.Server, ago, st.Status)
	}
	return i18n.Tf("service: running, NOT connected to %s for %s: %s", st.Server, ago, st.LastError)
}
