package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/agaloya/matriline/common/ledger"
	"github.com/agaloya/matriline/common/manifest"
	"github.com/agaloya/matriline/common/orca"
	"github.com/agaloya/matriline/common/wire"
	"github.com/agaloya/matriline/common/xfer"
)

const (
	manifestFile = "matriline.manifest"
	reportFile   = "matriline.report"
	verdictFile  = "matriline.verdict"
	namesFile    = "matriline.names"
)

// intake receives a RESULT and its files and files it into the spool.
func (ss *Session) intake(r wire.Result) error {
	s := ss.s
	cfg := s.conf()
	s.store.mu.Lock()
	if a := s.store.Attempts[r.AttemptID]; a != nil && a.ClientID == ss.id {
		a.Phase = "uploading" // the live view shows it while the files arrive
	}
	s.store.mu.Unlock()
	drain := func() error {
		for range r.Files {
			if err := xfer.Drain(ss.conn); err != nil {
				return err
			}
		}
		return nil
	}
	// --- cheap checks before accepting any data
	var total int64
	for _, f := range r.Files {
		if err := xfer.SafeName(f.Name); err != nil || strings.Contains(f.Name, "/") {
			s.log.Warnf("client %s sent unsafe file name %q: dropping connection", ss.name, f.Name)
			return fmt.Errorf("unsafe file name in result")
		}
		total += f.Size
	}
	if cfg.MaxResultBytes > 0 && total > cfg.MaxResultBytes {
		s.log.Warnf("result of %s from %s is %d bytes (limit %d): refused", r.TaskID, ss.name, total, cfg.MaxResultBytes)
		if err := drain(); err != nil {
			return err
		}
		s.dropAttempt(r.AttemptID, r.TaskID, ss.id, "result too large", true)
		return ss.conn.Send(wire.TResultAck, wire.ResultAck{AttemptID: r.AttemptID, Verdict: "refused: too large"})
	}
	if s.storageFull(total) {
		s.alertf("storage", "storage limit reached: results are being refused (clients keep them and retry)")
		if err := drain(); err != nil {
			return err
		}
		return ss.conn.Send(wire.TNotice, wire.Notice{Kind: "warn", Message: "server storage full, retry later"})
	}
	// map the opaque task id the client knows to the real task
	opaque := r.TaskID
	s.store.mu.Lock()
	att := s.store.Attempts[r.AttemptID]
	realID, found := "", false
	if att != nil && att.ClientID == ss.id && (opaqueID(att.TaskID) == opaque || legacyOpaqueID(att.TaskID) == opaque) {
		realID, found = att.TaskID, true
	} else {
		realID, found = s.store.resolveOpaque(opaque)
	}
	task, queued := s.store.Tasks[realID]
	queued = queued && found
	var inputSHA string
	if queued {
		inputSHA = task.InputSHA
	}
	s.store.mu.Unlock()
	r.TaskID = realID
	if !queued {
		if handled, err := ss.lateDuplicate(r, opaque, total); handled || err != nil {
			return err
		}
		if err := drain(); err != nil {
			return err
		}
		return ss.conn.Send(wire.TResultAck, wire.ResultAck{AttemptID: r.AttemptID, Verdict: "stale: task no longer queued"})
	}
	if att != nil && att.ClientID != ss.id {
		s.log.Warnf("client %s sent a result for attempt %s owned by another client", ss.name, r.AttemptID)
		if err := drain(); err != nil {
			return err
		}
		return ss.conn.Send(wire.TResultAck, wire.ResultAck{AttemptID: r.AttemptID, Verdict: "refused: not your attempt"})
	}
	// --- receive into staging (sizes were announced and ReceiveFile enforces them, so the
	// booking covers the whole upload)
	if !s.reserveDisk(total) {
		s.log.Warnf("result of %s from %s (%d bytes) refused: the disk would drop below storage.min_free", r.TaskID, ss.name, total)
		s.alertf("storage", "free disk below storage.min_free (%d MB): results are being refused (clients keep them and retry)", s.minFree()>>20)
		if err := drain(); err != nil {
			return err
		}
		return ss.conn.Send(wire.TNotice, wire.Notice{Kind: "warn", Message: "server disk almost full, retry later"})
	}
	stage := filepath.Join(cfg.Root, dState, "staging", sanitize(r.AttemptID))
	os.RemoveAll(stage)
	if err := os.MkdirAll(stage, 0o750); err != nil {
		s.releaseDisk(total)
		return err
	}
	for _, f := range r.Files {
		if err := xfer.ReceiveFile(ss.conn, f, stage); err != nil {
			s.releaseDisk(total)
			os.RemoveAll(stage)
			return fmt.Errorf("receiving %s: %v", f.Name, err)
		}
	}
	s.releaseDisk(total) // the files are on disk now: statfs sees them
	// --- verify and file
	v := s.verifyResult(ss, r, stage, inputSHA, opaque)
	if task.Internal {
		err := s.internalResult(ss, r, v, stage, task)
		os.RemoveAll(stage)
		if err != nil {
			return err
		}
		return ss.conn.Send(wire.TResultAck, wire.ResultAck{AttemptID: r.AttemptID, Verdict: "received"})
	}
	dest, err := s.fileResult(ss, r, v, stage)
	if err != nil {
		s.log.Errorf("filing result %s: %v", r.TaskID, err)
		os.RemoveAll(stage)
		return ss.conn.Send(wire.TNotice, wire.Notice{Kind: "warn", Message: "server could not store the result, retry later"})
	}
	os.RemoveAll(stage)
	return ss.conn.Send(wire.TResultAck, wire.ResultAck{AttemptID: r.AttemptID, Ledger: dest.ledger, Verdict: dest.verdict})
}

// Verdict is the outcome of the verification layers.
type Verdict struct {
	OK       bool
	Reasons  []string
	Manifest *manifest.Manifest
	Info     *orca.OutputInfo
	Checks   []string // names of layers applied
	// OtherVersion: computed with this ORCA version, not the campaign's
	// (orca.other_versions = separate | errors); filed apart, never cross-checked
	OtherVersion string
}

func (v *Verdict) fail(format string, a ...any) {
	v.OK = false
	v.Reasons = append(v.Reasons, fmt.Sprintf(format, a...))
}

// verifyResult applies the passive verification layers (D44).
func (s *Server) verifyResult(ss *Session, r wire.Result, stage, inputSHA, opaque string) *Verdict {
	cfg := s.conf()
	v := &Verdict{OK: true}
	// signature layer: the manifest must be signed by this client and describe exactly the
	// files received (names, sizes, hashes) and the task that was assigned.
	m, err := manifest.Verify(r.Manifest, ss.conn.PeerPub)
	v.Checks = append(v.Checks, "signature")
	if err != nil {
		v.fail("manifest: %v", err)
		return v
	}
	v.Manifest = m
	if err := m.MatchFiles(r.Files); err != nil {
		v.fail("manifest: %v", err)
	}
	if m.TaskID != opaque || m.AttemptID != r.AttemptID || m.ServerID != s.key.ID() {
		v.fail("manifest describes another task/attempt/server")
	}
	// the checks below read <input name>.out, and the result is filed from the files named
	// after the input the server sent: the manifest must name that same input, or one
	// output could be checked and another filed (found by code review)
	if want := anonStem(r.AttemptID) + ".inp"; m.InputName != want {
		v.fail("manifest names input %q, not the %q this attempt was sent", m.InputName, want)
	}
	if m.InputSHA256 != inputSHA {
		v.fail("manifest input hash does not match the queued input")
	}
	if m.OrcaVersion != cfg.OrcaVersion && cfg.OtherVersions != "refuse" && ss.outdated != "" {
		v.OtherVersion = m.OrcaVersion
	}
	if !cfg.VerifyFingerprints && cfg.OrcaCheck == "version" && m.OrcaVersion != cfg.OrcaVersion && v.OtherVersion == "" {
		v.fail("ORCA version %s, the campaign uses %s", m.OrcaVersion, cfg.OrcaVersion)
	}
	if cfg.VerifyFingerprints && v.OtherVersion == "" {
		v.Checks = append(v.Checks, "fingerprints")
		s.refMu.RLock()
		if len(s.refTrees) > 0 && !s.refTrees[m.OrcaTree] {
			v.fail("ORCA installation fingerprint %s is not an accepted one", short(m.OrcaTree))
		}
		if m.OrcaVersion != s.refVersion {
			v.fail("ORCA version %s, expected %s", m.OrcaVersion, s.refVersion)
		}
		if len(s.refGit) > 0 && !s.refGit[m.OrcaGit] && !(m.OrcaGit == "" && s.refNoGit[m.OrcaTree]) {
			v.fail("ORCA build %s is not an accepted build", m.OrcaGit)
		}
		orcaRan := false
		for _, e := range m.Execs {
			orcaRan = orcaRan || (!e.MPI && !e.System)
		}
		if !orcaRan && r.Failure == "" {
			v.fail("no executed ORCA programs recorded")
		}
		for _, e := range m.Execs {
			if e.MPI {
				// OpenMPI's launcher (mpirun/orterun/orted) of a multi-core job: allowed only
				// when the job really ran with several processes; the ORCA programs it
				// started are still checked against the reference below
				if m.Nprocs < 2 || m.MPITree == "" || !(strings.HasPrefix(m.MPIVersion, "4.") || strings.HasPrefix(m.MPIVersion, "msmpi-10.")) {
					v.fail("MPI program %s in a job that is not a multi-core one", e.Path)
				}
				// the server cannot check OpenMPI's files against a reference, so only its
				// launcher programs may appear (a client could otherwise mark any program
				// as "OpenMPI" to get it past this check)
				if !mpiLaunchers[strings.TrimSuffix(strings.ToLower(path.Base(strings.ReplaceAll(e.Path, `\`, "/"))), ".exe")] {
					v.fail("program %s is not an MPI launcher", e.Path)
				}
				continue
			}
			if e.System {
				if !manifest.IsSystemShell(e.Path) {
					v.fail("program %s outside ORCA declared as system shell", e.Path)
				}
				continue
			}
			if len(s.refFileSHAs) > 0 && !s.refListed[m.OrcaTree] && !s.refFileSHAs[e.SHA256] {
				v.fail("executed program %s (%s) is not part of the reference ORCA", e.Path, short(e.SHA256))
			}
		}
		s.refMu.RUnlock()
	}
	if r.Failure == "" && cfg.VerifyConsistency {
		v.Checks = append(v.Checks, "output_consistency")
		s.checkConsistency(v, m, stage, r.TaskID)
	}
	if r.Failure == "" && cfg.VerifyTiming {
		v.Checks = append(v.Checks, "timing")
		s.checkTiming(v, m, stage, r.AttemptID)
	}
	return v
}

func short(h string) string {
	if len(h) > 12 {
		return h[:12]
	}
	return h
}

// mpiLaunchers: the MPI programs a multi-core job runs. OpenMPI (Linux, macOS): mpirun and
// mpiexec are links to orterun, orted is the per-node daemon. Microsoft MPI (Windows):
// mpiexec.exe and its process manager smpd.exe.
var mpiLaunchers = map[string]bool{"mpirun": true, "mpiexec": true, "orterun": true, "orted": true, "smpd": true}

// checkConsistency parses the main output and compares it with the task input.
func (s *Server) checkConsistency(v *Verdict, m *manifest.Manifest, stage, taskID string) {
	stem := strings.TrimSuffix(m.InputName, path.Ext(m.InputName))
	info, err := orca.ParseOutputFile(filepath.Join(stage, stem+".out"))
	if err != nil {
		v.fail("main output %s.out missing or unreadable", stem)
		return
	}
	v.Info = info
	s.refMu.RLock()
	gitOK := len(s.refGit) == 0 || s.refGit[info.Git] || (info.Git == "" && s.refNoGit[m.OrcaTree])
	ver := s.refVersion
	s.refMu.RUnlock()
	if v.OtherVersion != "" { // another version on purpose (orca.other_versions): itself
		ver, gitOK = v.OtherVersion, true
	}
	if info.Version != ver || !gitOK {
		v.fail("output was written by ORCA %s (%s), expected %s", info.Version, info.Git, ver)
	}
	if info.Git != m.OrcaGit {
		v.fail("output build %s differs from the build declared in the manifest (%s)", info.Git, m.OrcaGit)
	}
	if !info.Terminated || info.ErrorTerm {
		v.fail("ORCA did not terminate normally")
	}
	// always compare with the server's own copy of the input; the client's copy is untrusted
	// and is never used as a reference
	in, err := s.taskInput(taskID)
	if err != nil {
		v.fail("server copy of the input not found: cannot check the echoed input")
	} else {
		norm := orca.Normalize(string(in), orca.Resources{MaxcoreMB: m.MaxcoreMB, Nprocs: m.Nprocs})
		if ok, why := orca.EchoMatches(norm, info.EchoedInput); !ok {
			v.fail("echoed input mismatch: %s", why)
		}
		if info.EchoGap {
			v.fail("echoed input line numbers are not consecutive: the output was edited")
		}
		// the output must start from THIS input's geometry, and end with the same one (single
		// point) or the same bonds (optimization): a real output of another input, such as
		// an isomer, fails here even when its echoed input was edited
		if want, ok := orca.InputAtoms(string(in)); ok {
			stem := strings.TrimSuffix(m.InputName, path.Ext(m.InputName))
			if g, err := orca.ParseSCF(filepath.Join(stage, stem+".out")); err == nil && g.First != nil {
				if same, why := orca.SameGeometry(want, g.First, 1e-3); !same {
					v.fail("geometry mismatch: the output did not start from the input geometry (%s)", why)
				} else if info.OptSteps == 0 {
					if same, why := orca.SameGeometry(want, g.Final, 1e-3); !same {
						v.fail("geometry mismatch: single point geometry changed (%s)", why)
					}
				} else if same, why := orca.SameConnectivity(want, g.Final); !same {
					v.fail("connectivity changed during the optimization (%s): another isomer?", why)
				} else if same, why := orca.SameStereo(want, g.Final); !same {
					v.fail("stereochemistry changed during the optimization: %s", why)
				} else if mirrored, why := orca.MirrorMatch(want, g.Final); mirrored {
					v.fail("stereochemistry changed during the optimization: %s", why)
				}
			}
		}
	}
	if len(info.Modules) == 0 {
		v.fail("no ORCA module banners found in the output")
	}
}

type filed struct {
	ledger  string
	verdict string
}

// fileResult moves the staged result to its final place and records it in the ledger.
func (s *Server) fileResult(ss *Session, r wire.Result, v *Verdict, stage string) (filed, error) {
	s.moveMu.Lock()
	defer s.moveMu.Unlock()
	cfg := s.conf()
	// the task may have finished through another attempt while we were receiving
	s.store.mu.Lock()
	task, queued := s.store.Tasks[r.TaskID]
	s.store.mu.Unlock()
	if !queued {
		return filed{verdict: "stale: already completed by another host"}, nil
	}
	if err := os.WriteFile(filepath.Join(stage, manifestFile), r.Manifest, 0o644); err != nil {
		return filed{}, err
	}
	// restore the real base name of every file named after the anonymous input
	if err := restoreNames(stage, anonStem(r.AttemptID), strings.TrimSuffix(path.Base(r.TaskID), path.Ext(r.TaskID))); err != nil {
		return filed{}, err
	}
	// ORCA prints "TERMINATED NORMALLY" (exit 0) even when a geometry optimization hit its
	// cycle limit, and with Opt Freq it then silently skips the frequencies (verified with
	// ORCA 6.1.1). Such a result is not usable: errors/, with the reason, no retry (another
	// host would give the same answer).
	if r.Failure == "" && v.OK {
		stem := strings.TrimSuffix(path.Base(r.TaskID), path.Ext(r.TaskID))
		if info, err := orca.ParseOutputFile(filepath.Join(stage, stem+".out")); err == nil && info.OptSteps > 0 && !info.OptConverged {
			why := fmt.Sprintf("geometry optimization did not converge (%d cycles; ORCA still reported normal termination)", info.OptSteps)
			if strings.Contains(strings.ToLower(strings.Join(info.EchoedInput, " ")), "freq") && !info.HasFreq {
				why += "; the requested frequencies were skipped"
			}
			writeVerdict(stage, &Verdict{Reasons: []string{why}})
			dest, err := s.placeResult(dErrors, r.TaskID, stage, true)
			if err != nil {
				return filed{}, err
			}
			h := s.recordResult("result", r, ss, v, dest, "error")
			s.finishTask(r.TaskID, "")
			s.cancelAttemptsOf(r.TaskID, "task failed")
			s.log.Warnf("task %s: %s -> %s", r.TaskID, why, dest)
			s.alertf("errors", "task %s moved to errors/: %s", r.TaskID, why)
			return filed{h, "error"}, nil
		}
	}
	// a computed result (or ORCA's own error) of another ORCA version is filed apart; a job
	// the computer could not finish (returned, machine failure, safety stop) takes the
	// usual way back to the queue, without blaming the task (found by code review)
	if v.OtherVersion != "" && (r.Failure == "" || r.Failure == "orca") {
		return s.fileOtherVersion(ss, r, v, stage, task)
	}
	switch {
	case r.Failure == "" && v.OK:
		dest, err := s.placeResult(dOutput, r.TaskID, stage, false)
		if err != nil {
			return filed{}, err
		}
		h := s.recordResult("result", r, ss, v, dest, "accepted")
		s.rememberDone(r.TaskID, ss.id, dest)
		s.learnFrom(task, v.Manifest)
		s.finishTask(r.TaskID, dCompleted)
		_ = s.reg.update(ss.id, func(c *ClientRec) { c.Results++; c.ErrStreak = 0 })
		s.cancelAttemptsOf(r.TaskID, "completed by another host", r.AttemptID) // not the attempt that produced it
		s.log.Infof("result %s from %s accepted -> %s", r.TaskID, ss.name, dest)
		s.planChecks(ss, r, v, dest)
		// one result per task: earlier weird results go to outdated/ once this one is final,
		// i.e. now if no check was planned for it, otherwise when its check passes (it may
		// still fail and the earlier one be the honest result; code review)
		if !s.checked(dest) {
			s.retireWeirdLocked(r.TaskID) // (fileResult holds moveMu)
		}
		ss.machineFails, ss.healthy = 0, true
		return filed{h, "accepted"}, nil

	case r.Failure == "":
		// verification failed (D44): result goes to weird/, input copy included
		writeVerdict(stage, v)
		dest, err := s.placeResult(dWeird, r.TaskID, stage, true)
		if err != nil {
			return filed{}, err
		}
		h := s.recordResult("result", r, ss, v, dest, "weird: "+strings.Join(v.Reasons, "; "))
		s.finishTask(r.TaskID, "")
		s.clientFault(ss.id, true, "failed verification of "+r.TaskID)
		s.cancelAttemptsOf(r.TaskID, "result under review")
		s.log.Warnf("result %s from %s FAILED verification -> %s: %s", r.TaskID, ss.name, dest, strings.Join(v.Reasons, "; "))
		s.alertf("verify_failed", "result %s from client %s failed verification: %s", r.TaskID, ss.name, strings.Join(v.Reasons, "; "))
		if cfg.FailurePolicy == "quarantine" {
			s.quarantine(ss.id, "failed verification of "+r.TaskID)
		}
		s.retryWeird(r.TaskID, ss.id, dest)
		return filed{h, "weird"}, nil

	case r.Failure == "orca" && s.memoryShortage(ss, r, stage, task):
		return filed{verdict: "the job needs more memory per core than this client lends; requeued for a client with more"}, nil

	case r.Failure == "orca" && s.startedFromCheckpoint(r.AttemptID) != "":
		// ORCA failed on a host that started from orbitals kept from another host's
		// failed attempt. They may be the cause (a poisoned .gbw could make every next
		// host fail and send a good input to errors/), so: the orbitals were used once and
		// are gone, this failure does not count against the input nor this host, and the
		// donor is named in the log.
		donor := s.startedFromCheckpoint(r.AttemptID)
		s.store.mu.Lock()
		delete(s.store.Attempts, r.AttemptID)
		if task.Attempts > 0 {
			task.Attempts--
		}
		task.LastError = "ORCA error after starting from kept orbitals (discarded)"
		s.store.touch()
		s.store.mu.Unlock()
		dn := donor
		if c := s.reg.get(donor); c != nil {
			dn = c.Name
		}
		s.log.Warnf("ORCA failed for %s on %s, which started from the orbitals kept from %s's failed attempt: orbitals discarded, the next host starts from scratch; not counted as an ORCA error", r.TaskID, ss.name, dn)
		return filed{verdict: "orca error after reusing kept orbitals; requeued from scratch"}, nil

	case r.Failure == "orca":
		s.store.mu.Lock()
		if task.OrcaFailOn == nil {
			task.OrcaFailOn = map[string]bool{}
		}
		task.OrcaFailOn[ss.id] = true
		task.avoid(ss.id, 30*24*time.Hour) // same input, same program: it would fail again
		task.LastError = "ORCA error on " + ss.name
		stem := strings.TrimSuffix(path.Base(r.TaskID), path.Ext(r.TaskID))
		if info, err := orca.ParseOutputFile(filepath.Join(stage, stem+".out")); err == nil && info.ErrorText != "" {
			task.LastError += ": " + info.ErrorText // kept for diagnosis; the files are not
		}
		n := len(task.OrcaFailOn)
		delete(s.store.Attempts, r.AttemptID)
		s.store.touch()
		s.store.mu.Unlock()
		_ = s.reg.update(ss.id, func(c *ClientRec) { c.OrcaFails++ })
		s.clientFault(ss.id, false, "ORCA error on "+r.TaskID)
		threshold := s.orcaFailThreshold()
		if n >= threshold {
			writeVerdict(stage, &Verdict{Reasons: []string{"ORCA failed on " + plural(n, "computer")}})
			dest, err := s.placeResult(dErrors, r.TaskID, stage, true)
			if err != nil {
				return filed{}, err
			}
			h := s.recordResult("result", r, ss, v, dest, "error")
			s.finishTask(r.TaskID, "")
			s.cancelAttemptsOf(r.TaskID, "task failed")
			s.log.Warnf("task %s failed with ORCA errors on %d hosts -> %s", r.TaskID, n, dest)
			s.alertf("errors", "task %s moved to errors/ (ORCA failed on %s)", r.TaskID, plural(n, "computer"))
			return filed{h, "error"}, nil
		}
		s.log.Infof("ORCA error for %s on %s (%d/%d hosts); requeued: %s", r.TaskID, ss.name, n, threshold, task.LastError)
		return filed{verdict: "orca error recorded; task requeued"}, nil

	case r.Failure == "time_limit" && s.timeLimitReached(r.AttemptID):
		// the project's tasks.max_job_time: the input is too long for this project's limit,
		// whoever computes it, so it stops for good; the orbitals so far stay with it in
		// errors/ and 'retry' continues from them (user, 2026-10-07)
		stem := strings.TrimSuffix(path.Base(r.TaskID), path.Ext(r.TaskID))
		gbw := stem + ".gbw"
		if info, err := os.Stat(filepath.Join(stage, gbw)); err != nil || info.Size() == 0 {
			gbw = ""
		}
		dest, err := s.placeResult(dErrors, r.TaskID, stage, true)
		if err != nil {
			return filed{}, err
		}
		reasons := []string{"stopped: it ran for the project's tasks.max_job_time (" + cfg.MaxJobTime.String() + ") on " + ss.name}
		if gbw != "" {
			reasons = append(reasons, "the orbitals reached so far are in "+gbw+"; to continue from them, raise tasks.max_job_time (0 = no limit) and run 'matriline-server retry "+dest+"'")
		} else {
			reasons = append(reasons, "no orbitals came back (results.include/exclude?); 'matriline-server retry "+dest+"' starts it again from the beginning")
		}
		writeVerdict(filepath.Join(cfg.Root, filepath.FromSlash(dest)), &Verdict{Reasons: reasons})
		h := s.recordResult("result", r, ss, v, dest, "error")
		s.finishTask(r.TaskID, "")
		s.cancelAttemptsOf(r.TaskID, "task stopped by tasks.max_job_time")
		s.log.Warnf("task %s reached tasks.max_job_time (%s) on %s -> %s", r.TaskID, cfg.MaxJobTime, ss.name, dest)
		s.alertf("errors", "task %s moved to errors/: it reached tasks.max_job_time (%s); 'retry' continues from its orbitals", r.TaskID, cfg.MaxJobTime)
		return filed{h, "error"}, nil

	case r.Failure == "returned":
		// the computer's owner paused it ('pause --now'): nobody's fault, not even the
		// host's; the task goes back to the queue, starting from this job's orbitals
		s.keepCheckpoint(ss, r, stage)
		s.dropAttempt(r.AttemptID, r.TaskID, ss.id, "returned: the host was paused by its owner", false)
		s.log.Infof("task %s returned by %s (paused by its owner); requeued", r.TaskID, ss.name)
		return filed{verdict: "returned; task requeued"}, nil

	default:
		// The host failed, not the input: the task goes to another host (avoid this one for
		// it) and a host that keeps failing gets a pause. Before, one broken host retried
		// the same task 6 times in 12 s and sent a healthy input to errors/ (lab).
		//
		// But an input that fails at once on several different computers is the input's
		// fault whatever they call it (user's real test: clients older than the fix of ORCA's
		// exit 126 reported an unreadable input as a sandbox failure, and it waited forever).
		if dest, ok := s.quickFailEverywhere(ss, r, stage, task); ok {
			h := s.recordResult("result", r, ss, v, dest, "error")
			return filed{h, "error"}, nil
		}
		s.keepCheckpoint(ss, r, stage)
		s.dropAttempt(r.AttemptID, r.TaskID, ss.id, "machine failure: "+r.Failure, true)
		ss.machineFails++
		ss.healthy = false
		s.clientFault(ss.id, false, "machine failure ("+r.Failure+") on "+r.TaskID)
		if ss.machineFails >= 3 {
			ss.coolUntil = time.Now().Add(5 * time.Minute)
			ss.machineFails = 0
			s.log.Warnf("client %s: 3 machine failures in a row; no tasks for 5 minutes", ss.name)
		}
		return filed{verdict: "machine failure recorded; task requeued"}, nil
	}
}

func writeVerdict(stage string, v *Verdict) {
	var b strings.Builder
	b.WriteString("# matriline verification verdict\n")
	fmt.Fprintf(&b, "time=%s\nchecks=%s\n", time.Now().UTC().Format(time.RFC3339), strings.Join(v.Checks, ","))
	for i, r := range v.Reasons {
		fmt.Fprintf(&b, "reason.%d=%s\n", i+1, r)
	}
	os.WriteFile(filepath.Join(stage, verdictFile), []byte(b.String()), 0o644)
}

// placeResult moves stage to <top>/<stem> (unique), optionally with a copy of the input.
func (s *Server) placeResult(top, taskID, stage string, withInput bool) (string, error) {
	root := s.conf().Root
	if withInput {
		src := filepath.Join(root, dInput, filepath.FromSlash(taskID))
		dst := filepath.Join(stage, path.Base(taskID))
		if _, err := os.Stat(dst); err != nil {
			if err := copyFile(src, dst); err != nil {
				return "", err
			}
		}
	}
	rel := freeName(root, resultDir(top, taskID))
	if err := moveFile(stage, filepath.Join(root, filepath.FromSlash(rel))); err != nil {
		return "", err
	}
	return rel, nil
}

// finishTask removes a task from the queue; if to != "" the input file moves there
// (completed/), otherwise it is deleted from input/ (a copy lives in weird/ or errors/).
func (s *Server) finishTask(taskID, to string) {
	root := s.conf().Root
	os.Remove(s.checkpointPath(taskID)) // a kept checkpoint is not needed any more
	src := filepath.Join(root, dInput, filepath.FromSlash(taskID))
	if to != "" {
		dst := filepath.Join(root, filepath.FromSlash(freeName(root, path.Join(to, taskID))))
		if err := moveFile(src, dst); err != nil {
			s.log.Errorf("moving input %s: %v", taskID, err)
		}
	} else {
		os.Remove(src)
	}
	s.store.mu.Lock()
	delete(s.store.Tasks, taskID)
	s.store.touch()
	s.store.mu.Unlock()
	s.pruneSpool()
}

// dropAttempt forgets an attempt; the task goes back to the queue.
func (s *Server) dropAttempt(attemptID, taskID, clientID, why string, avoid bool) {
	// the task goes to another host for a while; with no other host it would wait 6 h
	// for the only one (a macOS test): then only for the 5-minute cool-down
	wait := 6 * time.Hour
	if avoid && s.activeClients() <= 1 {
		wait = 5 * time.Minute
	}
	s.store.mu.Lock()
	delete(s.store.Attempts, attemptID)
	if t := s.store.Tasks[taskID]; t != nil {
		t.LastError = why
		if avoid {
			t.avoid(clientID, wait)
		}
	}
	s.store.touch()
	s.store.mu.Unlock()
	s.log.Infof("attempt %s of %s dropped: %s", attemptID, taskID, why)
}

// failTask moves a queued task to errors/ without a result (e.g. too many attempts).
func (s *Server) failTask(taskID, why string) {
	s.moveMu.Lock()
	defer s.moveMu.Unlock()
	root := s.conf().Root
	dir := filepath.Join(root, filepath.FromSlash(resultDir(dErrors, taskID)))
	if err := os.MkdirAll(dir, 0o750); err == nil {
		copyFile(filepath.Join(root, dInput, filepath.FromSlash(taskID)), filepath.Join(dir, path.Base(taskID)))
		writeVerdict(dir, &Verdict{Reasons: []string{why}})
	}
	s.ledger.Append(ledger.Entry{Kind: "error", Task: taskID, Note: why})
	s.finishTask(taskID, "")
	s.cancelAttemptsOf(taskID, why)
	s.alertf("errors", "task %s moved to errors/: %s", taskID, why)
}

// recordResult appends the ledger entry with the hashes of the stored files.
func (s *Server) recordResult(kind string, r wire.Result, ss *Session, v *Verdict, dest, verdict string) string {
	if !strings.HasPrefix(r.TaskID, "~") { // verification sub-tasks: only in server.log
		s.event("system", "result %s from %s: %s -> %s", r.TaskID, ss.name, verdict, dest)
	}
	mh := sha256.Sum256(r.Manifest)
	files, from := dirHashes(s.conf().Root, dest)
	h, err := s.ledger.Append(ledger.Entry{Kind: kind, Task: r.TaskID, Attempt: r.AttemptID, Client: ss.id,
		Manifest: hex.EncodeToString(mh[:]), Verdict: verdict, Files: files, FromManifest: from})
	if err != nil {
		s.log.Errorf("ledger append: %v", err)
	}
	s.hookResult(r.TaskID, dest)
	return h
}

// recoverStaging removes half-received results left by a crash (clients resend them,
// because they only delete a result after RESULT_ACK).
func (s *Server) recoverStaging() {
	dir := filepath.Join(s.conf().Root, dState, "staging")
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		os.RemoveAll(filepath.Join(dir, e.Name()))
	}
	if len(entries) > 0 {
		s.log.Infof("removed %d incomplete staged result(s) from a previous run", len(entries))
	}
}

// noAutoRelease: the client's quarantine waits for the admin.
func (s *Server) noAutoRelease(id string) bool {
	c := s.reg.get(id)
	return c != nil && c.NoAutoRelease
}

func (s *Server) quarantine(id, why string) {
	cfg := s.conf()
	_ = s.reg.update(id, func(c *ClientRec) {
		c.Status, c.Note = stQuarantined, why
		c.NoAutoRelease = !c.AutoReleased.IsZero() && time.Since(c.AutoReleased) < cfg.QuarantineWindow
		if c.NoAutoRelease {
			c.Note = why + "; again within " + cfg.QuarantineWindow.String() + " of an automatic release: only the admin releases it"
			why = c.Note
		}
	})
	s.ledger.Append(ledger.Entry{Kind: "quarantine", Client: id, Note: why})
	s.alertf("quarantine", "client %s quarantined: %s", id, why)
	if ss := s.session(id); ss != nil {
		_ = ss.conn.Send(wire.TNotice, wire.Notice{Kind: "warn", Message: "this client has been quarantined (every result will be verified): " + why})
	}
	go s.recheckClient(id, 1)
}

// recheckClient re-verifies past accepted results of a quarantined client on independent
// hosts, with the cheapest check that applies (SCF when orbitals were returned, otherwise
// a replica). Round 1: the verify.quarantine_recheck most recent ones. Round 2 (only if
// round 1 found a bad one): a sample (verify.quarantine_escalate_fraction) of ALL of them.
// Failures follow the usual path (weird/ + automatic retry); recheckDone decides release.
func (s *Server) recheckClient(id string, round int) {
	cfg := s.conf()
	es, err := ledger.Read(filepath.Join(cfg.Root, dState, "ledger.log"), s.key.Pub)
	if err != nil {
		return
	}
	n, limit := 0, cfg.QuarantineRecheck
	if round > 1 {
		limit = 500 // a sample of everything, bounded
	}
	for i := len(es) - 1; i >= 0 && n < limit; i-- {
		e := es[i]
		if e.Kind != "result" || e.Client != id || e.Verdict != "accepted" || len(e.Files) == 0 {
			continue
		}
		if round > 1 && secureFloat() >= cfg.QuarantineEscalate {
			continue
		}
		dest := path.Dir(e.Files[0].Path)
		stem := strings.TrimSuffix(path.Base(e.Task), path.Ext(e.Task))
		dir := filepath.Join(cfg.Root, filepath.FromSlash(dest))
		info, err := orca.ParseOutputFile(filepath.Join(dir, stem+".out"))
		if err != nil {
			continue // moved or deleted since
		}
		want := map[string]bool{"replica": true}
		if fileExists(filepath.Join(dir, stem+".gbw")) {
			want = map[string]bool{"scf": true}
		}
		if ck, err := s.createCheck(e.Task, dest, stem, id, want, info, "", nil, nil); err == nil && ck != nil {
			s.store.mu.Lock()
			ck.Recheck = true // counted by recheckDone
			s.store.touch()
			s.store.mu.Unlock()
			n++
		}
	}
	fresh := 0
	if n == 0 {
		// Nothing of its own to re-check (e.g. a host that only lied as a verifier):
		// releasing it at once would let it repeat. Its next results decide instead (a
		// quarantined client keeps computing and every result it returns is checked).
		fresh = max(cfg.QuarantineRecheck, 1)
	}
	_ = s.reg.update(id, func(c *ClientRec) {
		c.RecheckPending, c.RecheckFailed, c.RecheckRound, c.RecheckFresh = n+fresh, 0, round, fresh
	})
	if fresh > 0 {
		s.log.Warnf("quarantined client %s: no past result to re-check; its next %d result(s) decide", id, fresh)
	} else {
		s.log.Warnf("quarantined client %s: round %d, %d past result(s) scheduled for re-verification", id, round, n)
	}
}

// recheckDone counts finished re-verifications. All clean -> automatic release (be
// forgiving: detection can be wrong and the admin should not have to unblock clients by
// hand); a bad one in round 1 -> round 2 on a sample of everything; still bad -> the
// client stays quarantined and the admin is alerted.
func (s *Server) recheckDone(id string, failed bool) {
	cfg := s.conf()
	var pending, bad, round int
	var name string
	_ = s.reg.update(id, func(c *ClientRec) {
		if c.RecheckPending > 0 {
			c.RecheckPending--
		}
		if failed {
			c.RecheckFailed++
		}
		pending, bad, round, name = c.RecheckPending, c.RecheckFailed, c.RecheckRound, c.Name
	})
	if pending > 0 {
		return
	}
	switch {
	case bad == 0 && cfg.QuarantineAutoRelease && !s.noAutoRelease(id):
		_ = s.reg.update(id, func(c *ClientRec) {
			c.FailTimes = nil
			c.RecheckRound = 0
			c.RecheckFresh = 0
			c.AutoReleased = time.Now()
		})
		_, _ = s.setClientStatus(id, stActive, "released automatically: its re-checked results were all correct")
		s.alertf("quarantine", "client %s released automatically: round %d re-checks were all correct", name, round)
	case bad == 0 && s.noAutoRelease(id):
		s.alertf("quarantine", "client %s: its re-checked results were correct, but it was quarantined again soon after an automatic release; it stays quarantined until you release it ('clients release %s')", name, name)
	case bad > 0 && round == 1 && cfg.QuarantineEscalate > 0:
		s.alertf("quarantine", "client %s: %d bad result(s) among its recent ones; re-checking a %.0f %% sample of all its results", name, bad, 100*cfg.QuarantineEscalate)
		go s.recheckClient(id, 2)
	default:
		s.alertf("quarantine", "client %s stays quarantined: %d bad result(s) in re-check round %d; review it ('clients release %s' to resume)", name, bad, round, name)
	}
}

// restoreNames renames "<anon>.*" files to "<orig>.*" and records the mapping in
// matriline.names so the signed manifest can still be checked later.
func restoreNames(dir, anon, orig string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	var b strings.Builder
	b.WriteString("# anonymous name used by the client = name in this directory\n")
	for _, e := range entries {
		n := e.Name()
		if !strings.HasPrefix(n, anon+".") && !strings.HasPrefix(n, anon+"_") {
			continue
		}
		nn := orig + strings.TrimPrefix(n, anon)
		if err := os.Rename(filepath.Join(dir, n), filepath.Join(dir, nn)); err != nil {
			return err
		}
		fmt.Fprintf(&b, "%s=%s\n", n, nn)
	}
	return os.WriteFile(filepath.Join(dir, namesFile), []byte(b.String()), 0o644)
}

// readNames loads matriline.names (anon -> real).
func readNames(dir string) map[string]string {
	out := map[string]string{}
	b, err := os.ReadFile(filepath.Join(dir, namesFile))
	if err != nil {
		return out
	}
	for _, ln := range strings.Split(string(b), "\n") {
		if a, r, ok := strings.Cut(ln, "="); ok && !strings.HasPrefix(ln, "#") {
			out[a] = r
		}
	}
	return out
}

// taskInput returns the server's own copy of a task input (input/, completed/ or internal).
func (s *Server) taskInput(taskID string) ([]byte, error) {
	root := s.conf().Root
	if strings.HasPrefix(taskID, "~check/") {
		parts := strings.Split(taskID, "/") // ~check/<id>/<purpose>.inp
		if len(parts) == 3 {
			p := strings.TrimSuffix(parts[2], ".inp")
			return os.ReadFile(filepath.Join(root, dState, "internal", sanitize(parts[1]), sanitize(p), sanitize(p)+".inp"))
		}
	}
	if b, err := os.ReadFile(filepath.Join(root, dInput, filepath.FromSlash(taskID))); err == nil {
		return b, nil
	}
	return os.ReadFile(filepath.Join(root, dCompleted, filepath.FromSlash(taskID)))
}

// checkTiming flags results that claim more computation than physically possible since the
// assignment (e.g. an output copied from somewhere else).
func (s *Server) checkTiming(v *Verdict, m *manifest.Manifest, stage, attemptID string) {
	s.store.mu.Lock()
	a := s.store.Attempts[attemptID]
	s.store.mu.Unlock()
	if a == nil || a.Adopted {
		// unknown, or re-adopted after the server lost track of it: its start time is the
		// re-adoption, so an honest long job would look impossibly fast (found by review)
		return
	}
	elapsed := time.Since(a.Started).Seconds()
	slack := 120.0
	if m.EndedUnix-m.StartedUnix > int64(elapsed+slack) {
		v.fail("client claims %ds of computation but the task was assigned %.0fs ago", m.EndedUnix-m.StartedUnix, elapsed)
	}
	stem := strings.TrimSuffix(m.InputName, path.Ext(m.InputName))
	if info, err := orca.ParseOutputFile(filepath.Join(stage, stem+".out")); err == nil && info.TotalRunSec > elapsed+slack {
		v.fail("ORCA output reports %.0fs of run time but the task was assigned %.0fs ago", info.TotalRunSec, elapsed)
	}
}

// retryWeird puts the input of a result that went to weird/ back at the END of the queue
// (priority -1), away from the client that produced it, so the task still gets a trusted
// result while the suspicious one waits in weird/ for review. Once per task: a second
// weird result only raises an alert. verify.retry_weird = false keeps the old behaviour
// and alerts that the input is not scheduled.
func (s *Server) retryWeird(taskID, producer, weirdRel string) {
	cfg := s.conf()
	if !cfg.RetryWeird {
		s.alertf("verify_failed", "task %s is in weird/ and NOT rescheduled (verify.retry_weird = false)", taskID)
		return
	}
	if es, err := ledger.Read(filepath.Join(cfg.Root, dState, "ledger.log"), s.key.Pub); err == nil {
		for _, e := range es {
			if e.Kind == "retry_weird" && e.Task == taskID {
				s.alertf("verify_failed", "task %s went to weird/ again after its automatic retry; left for review", taskID)
				return
			}
		}
	}
	dst := filepath.Join(cfg.Root, dInput, filepath.FromSlash(taskID))
	if fileExists(dst) {
		return
	}
	if len(s.taskResultDirs(dOutput, taskID)) > 0 {
		return // the task has a result in output/ already (e.g. accepted by the admin)
	}
	// the input is in completed/ (accepted, then condemned by an active check) or inside
	// the weird/ result itself (rejected on arrival by a passive check)
	src := filepath.Join(cfg.Root, dCompleted, filepath.FromSlash(taskID))
	if !fileExists(src) {
		src = filepath.Join(cfg.Root, filepath.FromSlash(weirdRel), path.Base(taskID))
	}
	if err := copyFile(src, dst); err != nil {
		s.log.Warnf("retry of weird task %s: %v", taskID, err)
		return
	}
	s.store.markPlaced([]string{taskID})
	s.rescan()
	s.store.mu.Lock()
	if t := s.store.Tasks[taskID]; t != nil {
		t.Priority = -1
		t.avoid(producer, avoidForever)
		s.store.orderOK = false
		s.store.touch()
	}
	s.store.mu.Unlock()
	s.ledger.Append(ledger.Entry{Kind: "retry_weird", Task: taskID, Client: producer, Note: "requeued at the end of the queue, producer excluded"})
	s.log.Infof("task %s requeued at the end of the queue after a weird result (producer excluded)", taskID)
}

// startedFromCheckpoint returns the donor of the kept orbitals an attempt started from.
func (s *Server) startedFromCheckpoint(attemptID string) string {
	s.store.mu.Lock()
	defer s.store.mu.Unlock()
	if a := s.store.Attempts[attemptID]; a != nil {
		return a.FromCheckpoint
	}
	return ""
}

// checkpointName is the bundle name of a kept .gbw; serveTask renames it after the input.
const checkpointName = "matriline-checkpoint.gbw"

func (s *Server) checkpointPath(taskID string) string {
	return filepath.Join(s.conf().Root, dState, "checkpoints", sanitize(taskID)+".gbw")
}

// quickFailEverywhere records a machine failure that came within minutes of the start on a
// healthy computer (an accepted result since its last machine failure); when
// the task has failed so on at least two different computers (and on as many as
// tasks.orca_error_hosts asks, when fewer computers exist), it goes to errors/ with the
// last message and true is returned.
func (s *Server) quickFailEverywhere(ss *Session, r wire.Result, stage string, task *TaskState) (string, bool) {
	s.store.mu.Lock()
	a := s.store.Attempts[r.AttemptID]
	quick := a != nil && !a.Adopted && time.Since(a.Started) < 2*time.Minute && ss.healthy
	n := 0
	if quick {
		if task.QuickFailOn == nil {
			task.QuickFailOn = map[string]bool{}
		}
		task.QuickFailOn[ss.id] = true
		s.store.touch()
	}
	n = len(task.QuickFailOn)
	s.store.mu.Unlock()
	if !quick || n < max(2, s.orcaFailThreshold()) {
		return "", false
	}
	msg := "failed at once on " + plural(n, "computer")
	if r.Failure != "" && r.Failure != "machine" {
		msg += " (" + r.Failure + ")"
	}
	writeVerdict(stage, &Verdict{Reasons: []string{msg + ": the same input failed within minutes on every computer that tried it, so it is most likely the input (see the output and matriline.stderr here)"}})
	dest, err := s.placeResult(dErrors, r.TaskID, stage, true)
	if err != nil {
		return "", false
	}
	s.finishTask(r.TaskID, "")
	s.cancelAttemptsOf(r.TaskID, "task failed on every computer")
	s.log.Warnf("task %s failed at once on %d computers -> %s", r.TaskID, n, dest)
	s.alertf("errors", "task %s moved to errors/ (it failed at once on %s)", r.TaskID, plural(n, "computer"))
	return dest, true
}

// timeLimitReached: a "time_limit" result is believed only when the attempt really ran for
// about tasks.max_job_time (code review: otherwise any host could send any task to
// errors/ at once, with orbitals of its choosing for the retry). Otherwise it is handled as
// a machine failure: the task goes to another host.
func (s *Server) timeLimitReached(attemptID string) bool {
	limit := s.conf().MaxJobTime
	s.store.mu.Lock()
	a := s.store.Attempts[attemptID]
	s.store.mu.Unlock()
	return limit > 0 && a != nil && !a.Adopted && time.Since(a.Started) >= limit*95/100
}

// checkpointTrusted: orbitals are kept for another host only from hosts the server already
// trusts (active, past probation, no failures).
func (s *Server) checkpointTrusted(clientID string) bool {
	cfg := s.conf()
	c := s.reg.get(clientID)
	return c != nil && c.Status == stActive && c.Results > cfg.ProbationResults && c.Failures == 0
}

// keepCheckpoint saves the orbitals of a failed attempt (tasks.reuse_checkpoints).
func (s *Server) keepCheckpoint(ss *Session, r wire.Result, stage string) {
	cfg := s.conf()
	if !cfg.ReuseCheckpoints || (r.Failure != "machine" && r.Failure != "timeout" && r.Failure != "returned") {
		return
	}
	if !s.checkpointTrusted(ss.id) {
		return // only from hosts the server already trusts
	}
	stem := strings.TrimSuffix(path.Base(r.TaskID), path.Ext(r.TaskID))
	src := filepath.Join(stage, stem+".gbw")
	if info, err := os.Stat(src); err != nil || info.Size() == 0 {
		return
	}
	dst := s.checkpointPath(r.TaskID)
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err == nil && copyFile(src, dst) == nil {
		s.store.mu.Lock()
		if t := s.store.Tasks[r.TaskID]; t != nil {
			t.CheckpointFrom = ss.id
			s.store.touch()
		}
		s.store.mu.Unlock()
		s.log.Infof("kept the orbitals of the failed attempt of %s from %s: the next host starts from them", r.TaskID, ss.name)
	}
}

var reMaxcoreLine = regexp.MustCompile(`(?i)%maxcore\s+(\d+)`)

// memoryShortage handles an ORCA error that is only a lack of memory ("Please increase
// MaxCore - by at least (X MB)", e.g. DLPNO-CCSD(T) of a radical at 768 MB; or "MemNeeded X MB >
// MemAvailable Y MB" for the triples; or no amount at all, then twice the MaxCore): not the input's
// fault, so it does not count towards errors/. The task records the memory per core it
// needs (client maxcore is 3/4 of what it lends, plus 25 % margin) and is offered only to
// clients lending that much. Found in the BDE campaign: the phenoxyl DLPNO failure blamed on
// the disk was this.
func (s *Server) memoryShortage(ss *Session, r wire.Result, stage string, task *TaskState) bool {
	stem := strings.TrimSuffix(path.Base(r.TaskID), path.Ext(r.TaskID))
	info, err := orca.ParseOutputFile(filepath.Join(stage, stem+".out"))
	if err != nil || !info.MaxcoreAsked {
		return false
	}
	used := 0
	for _, ln := range info.EchoedInput {
		if m := reMaxcoreLine.FindStringSubmatch(ln); m != nil {
			used, _ = strconv.Atoi(m[1])
		}
	}
	if used == 0 {
		used = ss.offer.MemPerSlotMB * 3 / 4
	}
	short := info.MaxcoreShortMB
	switch {
	case short > 0:
	case info.MemNeededMB > info.MemAvailMB && info.MemAvailMB > 0:
		// only part of MaxCore is available to the step: scale MaxCore by the same ratio
		short = float64(used) * (info.MemNeededMB/info.MemAvailMB - 1)
	default:
		short = float64(used) // no amount given: ask for twice as much
	}
	need := int(math.Ceil((float64(used) + short) * 1.25 / 0.75))
	s.store.mu.Lock()
	if need > task.MemMB {
		task.MemMB = need
	}
	task.LastError = fmt.Sprintf("needs about %d MB per core (ORCA asked for %.0f MB more MaxCore on %s)", task.MemMB, short, ss.name)
	if task.Attempts > 0 {
		task.Attempts-- // not the input's fault, not this host's either
	}
	s.store.touch()
	memMB := task.MemMB
	s.store.mu.Unlock()
	s.dropAttempt(r.AttemptID, r.TaskID, ss.id, "not enough memory per core", false)
	s.log.Warnf("task %s: ORCA needs more memory than %s lends; it now needs %d MB per core", r.TaskID, ss.name, memMB)
	fits := 0
	for _, c := range s.reg.list() {
		if c.Status != stActive {
			continue
		}
		if o := s.session(c.ID); o != nil && o.offer.MemPerSlotMB >= memMB {
			fits++
		}
	}
	if fits == 0 {
		s.alertf("errors", "task %s needs about %d MB of memory per core and no connected client lends that much (raise resources.memory_per_core on a client)", r.TaskID, memMB)
	}
	return true
}

// plural: "1 computer", "3 computers".
func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
}
