package main

// Late duplicates as free verifications. A task can run on two hosts at once: its first
// host was suspended or offline past tasks.task_timeout and the task was reassigned, then
// the first host came back and finished too (or the idle-host replication ran it twice).
// The first valid result is accepted (fileResult checks and stores under moveMu, so two
// near-simultaneous uploads can never both be stored or overwrite each other). Before,
// the second result was discarded unread; now, if it comes from an attempt the server
// really assigned for that task, its final energy is compared with the accepted one:
// agreement counts as a verification at no cost, disagreement schedules a replica of the
// accepted result on a third host (the usual check path then decides).

import (
	"fmt"
	"math"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/agaloya/matriline/common/ledger"
	"github.com/agaloya/matriline/common/orca"
	"github.com/agaloya/matriline/common/wire"
	"github.com/agaloya/matriline/common/xfer"
)

// doneTask remembers an accepted task for a while.
type doneTask struct {
	Task      string
	Producer  string
	ResultRel string
	Attempts  map[string]string // attempt id -> client id, every attempt the task had
	At        time.Time
}

const recentKeep = 72 * time.Hour

// rememberDone records an accepted task with all its attempts (call before they are
// cancelled).
func (s *Server) rememberDone(taskID, producer, resultRel string) {
	d := &doneTask{Task: taskID, Producer: producer, ResultRel: resultRel, Attempts: map[string]string{}, At: time.Now()}
	s.store.mu.Lock()
	for id, a := range s.store.Attempts {
		if a.TaskID == taskID {
			d.Attempts[id] = a.ClientID
		}
	}
	s.store.mu.Unlock()
	s.recentMu.Lock()
	defer s.recentMu.Unlock()
	if s.recent == nil {
		s.recent = map[string]*doneTask{}
	}
	if len(s.recent) > 20000 {
		for k, v := range s.recent {
			if time.Since(v.At) > recentKeep {
				delete(s.recent, k)
			}
		}
	}
	s.recent[opaqueID(taskID)] = d
	s.recent[legacyOpaqueID(taskID)] = d
}

// lateDuplicate handles a result for a task that is no longer queued. It returns false
// when the result is not a known late duplicate (the caller then discards it as before).
func (ss *Session) lateDuplicate(r wire.Result, opaque string, total int64) (bool, error) {
	s := ss.s
	s.recentMu.Lock()
	d := s.recent[opaque]
	s.recentMu.Unlock()
	if d == nil || d.Attempts[r.AttemptID] != ss.id || d.Producer == ss.id || time.Since(d.At) > recentKeep {
		return false, nil
	}
	top, _ := splitSpool(d.ResultRel)
	acceptedDir := filepath.Join(s.conf().Root, filepath.FromSlash(d.ResultRel))
	if top != dOutput || !fileExists(acceptedDir) {
		return false, nil // the accepted result was moved (weird/, deleted): nothing to compare
	}
	if !s.reserveDisk(total) {
		return false, nil
	}
	defer s.releaseDisk(total)
	stage := filepath.Join(s.conf().Root, dState, "staging", "dup-"+sanitize(r.AttemptID))
	os.RemoveAll(stage)
	defer os.RemoveAll(stage)
	if err := os.MkdirAll(stage, 0o750); err != nil {
		return true, err
	}
	for _, f := range r.Files {
		if err := xfer.ReceiveFile(ss.conn, f, stage); err != nil {
			return true, fmt.Errorf("receiving %s: %v", f.Name, err)
		}
	}
	stem := strings.TrimSuffix(path.Base(d.Task), path.Ext(d.Task))
	late, err1 := orca.ParseOutputFile(filepath.Join(stage, anonStem(r.AttemptID)+".out"))
	first, err2 := orca.ParseOutputFile(filepath.Join(acceptedDir, stem+".out"))
	verdict := "stale: task already completed (duplicate not comparable)"
	if r.Failure == "" && err1 == nil && err2 == nil && late.Terminated && len(late.FinalEnergies) > 0 && len(first.FinalEnergies) > 0 {
		e1, e2 := first.FinalEnergies[len(first.FinalEnergies)-1], late.FinalEnergies[len(late.FinalEnergies)-1]
		diff := math.Abs(e1 - e2)
		if diff <= s.conf().EnergyTolerance {
			s.ledger.Append(ledger.Entry{Kind: "verify", Task: d.Task, Client: d.Producer, Verdict: "passed",
				Note: fmt.Sprintf("free replica: late duplicate from %s agrees (%.1e Eh)", ss.id, diff)})
			_ = s.reg.update(d.Producer, func(c *ClientRec) { c.Verified++ })
			s.log.Infof("late duplicate of %s from %s agrees with the accepted result (%.1e Eh): counted as a verification", d.Task, ss.name, diff)
			verdict = "stale: task already completed; your result agreed and was used as a verification"
		} else {
			s.log.Warnf("late duplicate of %s from %s disagrees with the accepted result by %.2e Eh: replica on a third host", d.Task, ss.name, diff)
			s.alertf("verify_failed", "two results of %s disagree by %.2e Eh (accepted from %s, late duplicate from %s); a third host recomputes it", d.Task, diff, d.Producer, ss.name)
			if info, err := orca.ParseOutputFile(filepath.Join(acceptedDir, stem+".out")); err == nil {
				if _, err := s.createCheck(d.Task, d.ResultRel, stem, d.Producer, map[string]bool{"replica": true}, info, "", []string{ss.id}, nil); err != nil {
					s.log.Warnf("cannot plan the replica of %s: %v", d.Task, err)
				}
			}
			verdict = "stale: task already completed; your result disagreed, a third host decides"
		}
	}
	return true, ss.conn.Send(wire.TResultAck, wire.ResultAck{AttemptID: r.AttemptID, Verdict: verdict})
}

// knownLateAttempt reports whether attemptID of clientID ran the recently accepted task
// behind opaque (its finished result may then be uploaded and compared).
func (s *Server) knownLateAttempt(opaque, attemptID, clientID string) bool {
	s.recentMu.Lock()
	defer s.recentMu.Unlock()
	d := s.recent[opaque]
	return d != nil && d.Attempts[attemptID] == clientID && d.Producer != clientID && time.Since(d.At) < recentKeep
}

// settleDuplicates runs when a lost attempt comes back while the task was reassigned: two
// hosts now compute the same job. The one further ahead keeps it (progress = size of its
// output so far, comparable for the same input; ties keep the older attempt) and the other
// is cancelled, instead of both running until the first finishes. Deliberate replicas
// (duplicate_when_idle) are left alone.
func (s *Server) settleDuplicates(taskID string) {
	s.store.mu.Lock()
	var jobs []*Attempt
	for _, a := range s.store.Attempts {
		if a.TaskID == taskID && !a.Lost && a.Kind == "job" {
			jobs = append(jobs, a)
		}
	}
	if len(jobs) < 2 {
		s.store.mu.Unlock()
		return
	}
	best := jobs[0]
	for _, a := range jobs[1:] {
		if a.Progress > best.Progress || (a.Progress == best.Progress && a.Started.Before(best.Started)) {
			best = a
		}
	}
	type victim struct{ a Attempt }
	var cancel []victim
	kept := *best // read after the lock is released (a heartbeat may change best meanwhile)
	for _, a := range jobs {
		if a != best {
			cancel = append(cancel, victim{*a})
			delete(s.store.Attempts, a.ID)
			s.store.touch()
		}
	}
	s.store.mu.Unlock()
	for _, v := range cancel {
		s.log.Infof("task %s ran twice (a lost host came back): kept %s on %s (progress %d), cancelled %s on %s (progress %d)",
			taskID, kept.ID, kept.ClientID, kept.Progress, v.a.ID, v.a.ClientID, v.a.Progress)
		if ss := s.session(v.a.ClientID); ss != nil {
			_ = ss.conn.Send(wire.TCancel, wire.Cancel{AttemptID: v.a.ID, Reason: "another host is further ahead with this task"})
		}
	}
}
