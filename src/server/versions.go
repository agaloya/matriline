package main

import (
	"fmt"
	"path"
	"strings"
	"time"

	"github.com/agaloya/matriline/common/wire"
)

// Clients whose ORCA is not the campaign's version (orca.version), orca.other_versions:
// refuse (default) gives them no task and tells them which version to install.

// hasCampaignOrca reports whether the client offered the campaign's ORCA version.
func (ss *Session) hasCampaignOrca() bool {
	v := ss.s.conf().OrcaVersion
	for _, o := range ss.offer.Orca {
		if o.Version == v {
			return true
		}
	}
	return false
}

// noteOrcaVersions tells a client without the campaign's version what to do (once per
// connection) and alerts the admin.
func (ss *Session) noteOrcaVersions() {
	s, cfg := ss.s, ss.s.conf()
	if ss.hasCampaignOrca() || len(ss.offer.Orca) == 0 {
		ss.outdated = ""
		return
	}
	var have []string
	for _, o := range ss.offer.Orca {
		have = append(have, o.Version)
	}
	ss.outdated = strings.Join(have, ", ")
	msg := fmt.Sprintf("this campaign uses ORCA %s and this computer has %s", cfg.OrcaVersion, ss.outdated)
	switch cfg.OtherVersions {
	case "refuse":
		msg += fmt.Sprintf(": no tasks until ORCA %s is installed (then restart the client)", cfg.OrcaVersion)
	default:
		msg += ": its results are kept apart from the campaign's (orca.other_versions = " + cfg.OtherVersions + ")"
	}
	ss.conn.Send(wireNotice("orca_version", msg))
	s.log.Warnf("client %s: %s", ss.name, msg)
	s.alertf("orca_version", "client %s: %s", ss.name, msg)
}

// clientVersion is the ORCA version an outdated client runs (its newest).
func (ss *Session) clientVersion() string {
	best := ""
	for _, o := range ss.offer.Orca {
		if naturalLess(best, o.Version) {
			best = o.Version
		}
	}
	return best
}

// fileOtherVersion files a result computed with another ORCA version than the campaign's.
// separate: kept as the task's result in other-versions/<version>/output (weird and errors
// alike), the input completed; errors: kept for reference in errors/other-versions/<version>
// and the input stays queued for a client with the campaign's version. Never cross-checked
// (a check by another version would compare two different programs) and never in output/.
func (s *Server) fileOtherVersion(ss *Session, r wire.Result, v *Verdict, stage string, task *TaskState) (filed, error) {
	cfg := s.conf()
	kind := dOutput
	switch {
	case r.Failure != "":
		kind = dErrors
	case !v.OK:
		kind = dWeird
	}
	top := path.Join("other-versions", v.OtherVersion, kind)
	if cfg.OtherVersions == "errors" {
		top = path.Join(dErrors, "other-versions", v.OtherVersion)
	}
	if kind == dWeird {
		s.clientFault(ss.id, true, "failed verification of "+r.TaskID)
	}
	// written into the verdict kept with the result: only the passive checks ran
	v.Checks = append(v.Checks, "NOT cross-checked: ORCA "+v.OtherVersion+" is not the campaign's version")
	writeVerdict(stage, v)
	dest, err := s.placeResult(top, r.TaskID, stage, kind != dOutput)
	if err != nil {
		return filed{}, err
	}
	h := s.recordResult("result", r, ss, v, dest, "other version "+v.OtherVersion)
	if cfg.OtherVersions == "separate" && kind == dOutput {
		s.finishTask(r.TaskID, dCompleted)
		s.cancelAttemptsOf(r.TaskID, "completed by another host", r.AttemptID)
		s.log.Infof("result %s from %s (ORCA %s, not the campaign's %s) -> %s", r.TaskID, ss.name, v.OtherVersion, cfg.OrcaVersion, dest)
		return filed{h, "accepted (other ORCA version, kept apart, not cross-checked)"}, nil
	}
	s.store.mu.Lock()
	delete(s.store.Attempts, r.AttemptID)
	task.avoid(ss.id, 30*24*time.Hour) // the input still needs the campaign's version
	if task.Attempts > 0 {
		task.Attempts--
	}
	s.store.touch()
	s.store.mu.Unlock()
	s.log.Infof("result %s from %s (ORCA %s) kept apart in %s; the input stays queued for ORCA %s", r.TaskID, ss.name, v.OtherVersion, dest, cfg.OrcaVersion)
	return filed{h, "kept apart (other ORCA version); task requeued"}, nil
}
