package main

import (
	"errors"
	"fmt"
	"github.com/agaloya/matriline/common/build"
	"io/fs"
	"net"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"time"

	"github.com/agaloya/matriline/common/ledger"
	"github.com/agaloya/matriline/common/orca"
	"github.com/agaloya/matriline/common/wire"
	"github.com/agaloya/matriline/common/xfer"
)

// Session is one connected client.
type Session struct {
	s        *Server
	conn     *wire.Conn
	id       string
	name     string
	offer    wire.OfferRes
	lastBeat time.Time
	hbAccept bool         // what the last heartbeat said (the client may pause itself)
	outdated string       // its ORCA versions when it lacks the campaign's (orca.other_versions)
	agent    string       // program and version it reported ("matriline-client/0.1.0")
	offered  atomic.Value // string: the release version offered to it (update.go)
	rttMs    float64
	// power source from heartbeats; battery drain measured over >= 10 min of discharge
	power      string
	battPct    float64
	battRefT   time.Time
	battRefPct float64
	drainPerH  float64 // percentage points per hour, 0 = unknown
	// consecutive machine failures and the pause they trigger (see intake)
	machineFails int
	// healthy: an accepted result since its last machine failure. Only such a computer's
	// quick failure counts against the input (quickFailEverywhere): two computers whose
	// sandbox cannot start would otherwise send every input to errors/ (code review)
	healthy   bool
	coolUntil time.Time
}

func (s *Server) handleConn(raw net.Conn, direct bool) {
	cfg := s.conf()
	host, _, _ := net.SplitHostPort(raw.RemoteAddr().String())
	for _, b := range cfg.BlockedIPs {
		if b == host {
			s.log.Infof("refused blocked address %s", host)
			raw.Close()
			return
		}
	}
	if direct && s.banned(host) {
		s.log.Debugf("refused banned address %s", host)
		raw.Close()
		return
	}
	s.sessMu.Lock()
	n := len(s.sessions)
	s.sessMu.Unlock()
	if n >= cfg.MaxSessions {
		s.log.Warnf("session limit reached, refusing %s", host)
		raw.Close()
		return
	}
	acc := &wire.Acceptor{Key: s.key, Mode: cfg.Mode}
	c, err := acc.Accept(raw)
	if err != nil {
		s.log.Debugf("handshake with %s failed: %v", raw.RemoteAddr(), err)
		raw.Close()
		if direct {
			s.connFailed(host, !acc.SawHello, "handshake: "+err.Error())
		}
		return
	}
	defer c.Close()
	ss := &Session{s: s, conn: c, id: c.PeerID()}
	if err := ss.authorize(); err != nil {
		s.log.Infof("client %s (%s) not admitted: %v", ss.id, host, err)
		_ = c.Send(wire.TError, wire.ErrorMsg{Message: err.Error()})
		if direct && !errors.Is(err, errPending) {
			s.connFailed(host, false, "not admitted: "+err.Error())
		}
		return
	}
	if direct {
		s.connOK(host)
	}
	s.sessMu.Lock()
	if old := s.sessions[ss.id]; old != nil {
		old.conn.Close() // newest connection wins (client reconnected)
		oldHost, _, _ := net.SplitHostPort(old.conn.RemoteAddr().String())
		if time.Since(old.lastBeat) < 2*time.Minute && oldHost != host {
			go s.noteDisplaced(ss.id, ss.name, oldHost, host)
		}
	}
	s.sessions[ss.id] = ss
	s.sessMu.Unlock()
	defer func() {
		s.sessMu.Lock()
		if s.sessions[ss.id] == ss {
			delete(s.sessions, ss.id)
		}
		s.sessMu.Unlock()
	}()
	s.log.Infof("client %s (%s) connected from %s", ss.name, ss.id, host)
	err = ss.loop()
	s.log.Infof("client %s disconnected: %v", ss.name, err)
}

// authorize reads AUTH, applies the enrollment policy and sends WELCOME.
func (ss *Session) authorize() error {
	s := ss.s
	cfg := s.conf()
	_ = ss.conn.SetReadDeadline(time.Now().Add(30 * time.Second))
	var a wire.Auth
	if err := ss.conn.Expect(wire.TAuth, &a); err != nil {
		return err
	}
	rec := s.reg.get(ss.id)
	host, _, _ := net.SplitHostPort(ss.conn.RemoteAddr().String())
	if rec == nil && strings.HasPrefix(a.JoinToken, "mle-") {
		// one-time credential from "keys issue": bind this device key to the issued name
		e, ok := s.reg.useEnrollToken(a.JoinToken)
		if !ok {
			return fmt.Errorf("this credential was already used or has expired: ask the server admin for a new one")
		}
		rec = &ClientRec{ID: ss.id, Name: e.Name, PubKey: ss.conn.PeerPub, Status: stActive, Created: time.Now()}
		if err := s.reg.add(rec); err != nil {
			return err
		}
		s.ledger.Append(ledger.Entry{Kind: "key_enrolled", Client: ss.id, Note: e.Name + " from " + host})
		s.alertf("enrolled", "client %s enrolled its device key %s from %s (one-time credential used up)", e.Name, ss.id, host)
	}
	if rec == nil {
		switch cfg.Enrollment {
		case "open":
			rec = &ClientRec{ID: ss.id, Name: nameOr(a.Name, ss.id), PubKey: ss.conn.PeerPub, Status: stActive, Created: time.Now()}
		case "register":
			if a.JoinToken == "" || !s.reg.useToken(a.JoinToken) {
				return fmt.Errorf("unknown client and no valid join token")
			}
			rec = &ClientRec{ID: ss.id, Name: nameOr(a.Name, ss.id), PubKey: ss.conn.PeerPub, Status: stPending, Created: time.Now()}
		default:
			return fmt.Errorf("unknown client (not issued by this server)")
		}
		if err := s.reg.add(rec); err != nil {
			return err
		}
		s.log.Infof("enrolled client %s (%s) as %s", rec.Name, rec.ID, rec.Status)
	}
	if rec.Status == stRevoked {
		return fmt.Errorf("access revoked")
	}
	if rec.Status == stDisabled { // was offline when the admin disabled it: tell it now
		return errors.New(disabledText(rec.Note))
	}
	ss.name, ss.agent = rec.Name, cleanWord(a.Agent)
	_ = s.reg.update(ss.id, func(c *ClientRec) {
		c.LastSeen, c.LastAddr = time.Now(), host
		c.Agent, c.Platform, c.CanUpdate = ss.agent, cleanWord(a.Platform), a.CanUpdate
	})
	w := s.welcome(rec.Status)
	if err := ss.conn.Send(wire.TWelcome, w); err != nil {
		return err
	}
	if rec.Status == stPending {
		return errPending
	}
	_ = ss.conn.SetReadDeadline(time.Time{})
	s.offerUpdate(ss) // a newer signed release this server is installing or runs (D66)
	return nil
}

// errPending: a registered client waiting for approval (not a failed attempt).
var errPending = errors.New("pending approval by the server admin")

func nameOr(n, id string) string {
	n = sanitize(n)
	if n == "" {
		return id[:12]
	}
	if len(n) > 64 {
		n = n[:64]
	}
	return n
}

func (s *Server) welcome(status string) wire.Welcome {
	cfg := s.conf()
	s.refMu.RLock()
	defer s.refMu.RUnlock()
	w := wire.Welcome{ServerID: s.key.ID(), Status: status, HeartbeatSec: cfg.HeartbeatSec,
		TaskTimeoutSec: int(cfg.TaskTimeout.Seconds()), ReturnFiles: cfg.ReturnFiles,
		ReturnExclude: cfg.ReturnExclude, MaxResultBytes: cfg.MaxResultBytes,
		MetadataFields: cfg.MetaFields, Percentiles: cfg.Percentiles, SampleSec: cfg.SampleSec,
		CompressLevel: cfg.CompressLevel,
		KeepGBW:       cfg.VerifySCF > 0, ServerUnixMs: time.Now().UnixMilli()}
	for g := range s.refGit {
		w.OrcaVersions = append(w.OrcaVersions, s.refVersion+"@"+g)
	}
	if len(w.OrcaVersions) == 0 || len(s.refNoGit) > 0 {
		w.OrcaVersions = append(w.OrcaVersions, cfg.OrcaVersion) // builds without a GIT hash
	}
	for t := range s.refTrees {
		w.OrcaTreeHashes = append(w.OrcaTreeHashes, t)
	}
	switch status {
	case stPending:
		w.Message = "registered; waiting for the admin to approve this client"
	case stQuarantined:
		w.Message = "this client is quarantined: it still gets tasks, and every result it returns is verified"
	}
	return w
}

// loop processes client messages until the connection ends.
func (ss *Session) loop() error {
	s := ss.s
	hb := time.Duration(s.conf().HeartbeatSec) * time.Second
	// a silent client is dropped after 3 missed heartbeats (also applies to every frame of a
	// file upload); its attempts stay valid until task_timeout (they keep running offline)
	ss.conn.ReadIdle = 3*hb + 10*time.Second
	for {
		t, p, err := ss.conn.ReadFrame()
		if err != nil {
			return err
		}
		switch t {
		case wire.TOfferRes:
			if err := wire.Decode(p, &ss.offer); err != nil {
				return err
			}
			s.log.Infof("client %s (host %s) offers %d slot(s), %d MB/slot, ORCA %v", ss.name, nz(ss.offer.Hostname, "?"), ss.offer.Slots, ss.offer.MemPerSlotMB, orcaList(ss.offer.Orca))
			if h := sanitize(ss.offer.Hostname); h != "" && len(h) <= 253 {
				s.noteHostname(ss, h)
			}
			ss.noteOrcaVersions()
			if ss.offer.NoSecurity && !s.conf().AcceptNoSecurity && !build.NoSecurity {
				msg := "this client was built without security (no sandbox, no process audit); this server does not give it tasks (network.accept_nosecurity_clients = false)"
				ss.conn.Send(wireNotice("security", msg))
				s.log.Warnf("client %s: %s", ss.name, msg)
			}
		case wire.TReconcile:
			var r wire.Reconcile
			if err := wire.Decode(p, &r); err != nil {
				return err
			}
			ss.reconcile(r.Attempts)
		case wire.THeartbeat:
			var h wire.Heartbeat
			if err := wire.Decode(p, &h); err != nil {
				return err
			}
			ss.heartbeat(h)
		case wire.TGetTask:
			var g wire.GetTask
			if err := wire.Decode(p, &g); err != nil {
				return err
			}
			if err := ss.serveTask(g.Free, g.FreeMemMB); err != nil {
				return err
			}
		case wire.TTaskReject:
			var r wire.TaskReply
			if err := wire.Decode(p, &r); err != nil {
				return err
			}
			ss.rejected(r)
		case wire.TTaskAccept:
			// nothing to do: the attempt already exists
		case wire.TResult:
			var r wire.Result
			if err := wire.Decode(p, &r); err != nil {
				return err
			}
			if err := ss.intake(r); err != nil {
				return err
			}
		case wire.TNotice:
			var n wire.Notice
			_ = wire.Decode(p, &n)
			n.Kind, n.Message = cleanWord(n.Kind), cleanTail(n.Message)
			s.log.Infof("notice from %s: [%s] %s", ss.name, n.Kind, n.Message)
			if n.Kind == "update_refused" {
				s.updateRefused(ss, n.Message)
			}
		case wire.TBye:
			return fmt.Errorf("client said bye")
		default:
			return fmt.Errorf("unexpected message %v", t)
		}
	}
}

func orcaList(in []wire.OrcaInstall) []string {
	var out []string
	for _, o := range in {
		h := o.TreeHash
		if len(h) > 12 {
			h = h[:12]
		}
		out = append(out, fmt.Sprintf("%s@%s(%s)", o.Version, o.Git, h))
	}
	return out
}

func (ss *Session) heartbeat(h wire.Heartbeat) {
	s := ss.s
	now := time.Now()
	ss.lastBeat = now
	ss.hbAccept = h.Accepting
	ss.notePower(h.Power, h.BatteryPct, now)
	if h.SentUnix > 0 {
		// rough one-way delay + clock offset indicator (logged at debug level)
		ss.rttMs = float64(now.UnixMilli() - h.SentUnix)
	}
	s.store.mu.Lock()
	var revived []string
	for _, st := range h.Attempts {
		if a, ok := s.store.Attempts[st.AttemptID]; ok && a.ClientID == ss.id {
			a.LastSeen, a.Progress, a.Tail = now, st.Progress, cleanTail(st.Tail)
			if a.Phase != "uploading" {
				a.Phase = cleanWord(st.State)
			}
			if a.Lost {
				a.Lost = false
				revived = append(revived, a.TaskID)
				s.log.Infof("attempt %s of %s is alive again on %s", a.ID, a.TaskID, ss.name)
			}
			s.store.touch()
		}
	}
	s.store.mu.Unlock()
	for _, t := range revived {
		s.settleDuplicates(t)
	}
	_ = s.reg.update(ss.id, func(c *ClientRec) { c.LastSeen = now })
}

// reconcile re-adopts attempts after a reconnect or a server restart (D30).
func (ss *Session) reconcile(list []wire.AttemptState) {
	s := ss.s
	now := time.Now()
	var revived []string
	for _, st := range list {
		opaque := st.TaskID
		s.store.mu.Lock()
		a, known := s.store.Attempts[st.AttemptID]
		realID, queued := s.store.resolveOpaque(st.TaskID)
		if !queued && known && a.ClientID == ss.id && legacyOpaqueID(a.TaskID) == st.TaskID {
			realID, queued = a.TaskID, s.store.Tasks[a.TaskID] != nil // id from before 29ad9d6
		}
		st.TaskID = realID
		switch {
		case known && a.ClientID == ss.id:
			if a.Lost {
				revived = append(revived, a.TaskID)
			}
			a.LastSeen, a.Lost = now, false
			a.Progress = st.Progress
			s.store.touch()
		case !known && queued && st.State == "running":
			// server lost track (e.g. power cut before the state flush): adopt it
			s.store.Attempts[st.AttemptID] = &Attempt{ID: st.AttemptID, TaskID: st.TaskID,
				ClientID: ss.id, Kind: "job", Started: now, LastSeen: now, Adopted: true}
			s.store.touch()
			s.log.Infof("re-adopted attempt %s of %s on %s", st.AttemptID, st.TaskID, ss.name)
		case !queued:
			s.store.mu.Unlock()
			if st.State == "finished" && s.knownLateAttempt(opaque, st.AttemptID, ss.id) {
				continue // already computed: let it upload, it is compared as a free verification
			}
			// still running: the task was completed elsewhere, so stop wasting this CPU
			_ = ss.conn.Send(wire.TCancel, wire.Cancel{AttemptID: st.AttemptID, Reason: "task no longer queued (completed by another host, or cancelled or paused by the admin)"})
			continue
		}
		s.store.mu.Unlock()
	}
	// the opposite case: the server believes this client runs an attempt that the client
	// does not have (it delivered the result and the server lost it, e.g. killed before
	// saving its state; Windows chaos test). Without this the attempt waited for
	// tasks.task_timeout (1 h); now its task is requeued at once. A result arriving
	// later is still accepted while the task is queued.
	listed := map[string]bool{}
	for _, st := range list {
		listed[st.AttemptID] = true
	}
	s.store.mu.Lock()
	for id, a := range s.store.Attempts {
		if a.ClientID == ss.id && !a.Lost && !listed[id] && a.Started.Before(now) {
			a.Lost = true
			s.store.touch()
			s.log.Warnf("attempt %s of %s: %s reconnected without it; task requeued", id, a.TaskID, ss.name)
		}
	}
	s.store.mu.Unlock()
	for _, t := range revived {
		s.settleDuplicates(t)
	}
}

func (ss *Session) rejected(r wire.TaskReply) {
	s := ss.s
	s.store.mu.Lock()
	a, ok := s.store.Attempts[r.AttemptID]
	if ok && a.ClientID == ss.id {
		delete(s.store.Attempts, r.AttemptID)
		if t := s.store.Tasks[a.TaskID]; t != nil {
			// the client's local policy may change later; a lack of scratch disk is usually
			// temporary (another job finishes), so it is asked again sooner
			wait := 6 * time.Hour
			if strings.Contains(r.Reason, "scratch disk") {
				wait = 30 * time.Minute
			}
			t.avoid(ss.id, wait)
			if t.Attempts > 0 {
				t.Attempts-- // a refusal is not a computing attempt (max_attempts)
			}
			t.LastError = "rejected by " + ss.name + ": " + r.Reason
		}
		s.store.touch()
	}
	s.store.mu.Unlock()
	if ok {
		s.log.Infof("client %s rejected %s: %s", ss.name, a.TaskID, r.Reason)
		if strings.Contains(r.Reason, "scratch disk") {
			s.noteDiskRefusal(a.TaskID, ss.id, r.Reason)
		}
	}
}

// noteDiskRefusal records that a client's scratch disk is too small for a task, and alerts
// once when every active client has refused it: the task would otherwise wait forever
// (or, before clients refused, cycle through hosts filling their disks: a DLPNO replica
// failed on six hosts in the BDE campaign).
func (s *Server) noteDiskRefusal(taskID, clientID, reason string) {
	s.store.mu.Lock()
	t := s.store.Tasks[taskID]
	if t == nil {
		s.store.mu.Unlock()
		return
	}
	if t.DiskRefused == nil {
		t.DiskRefused = map[string]bool{}
	}
	t.DiskRefused[clientID] = true
	refused := t.DiskRefused
	alerted := t.DiskAlerted
	s.store.touch()
	s.store.mu.Unlock()
	if alerted {
		return
	}
	active := 0
	for _, c := range s.reg.list() {
		if c.Status == stActive || c.Status == stQuarantined {
			active++
			if !refused[c.ID] {
				return // some client has not refused it (yet)
			}
		}
	}
	if active == 0 {
		return
	}
	s.store.mu.Lock()
	if t := s.store.Tasks[taskID]; t != nil {
		t.DiskAlerted = true
		s.store.touch()
	}
	s.store.mu.Unlock()
	s.alertf("storage", "task %s does not fit on the scratch disk of any of the %d client(s) (last: %s); each client is asked again every 30 min (pause the input, or free disk on a client)", taskID, active, reason)
}

// serveTask assigns up to one task (the client asks again for each free slot).
func (ss *Session) serveTask(free, freeMem int) error {
	s := ss.s
	rec := s.reg.get(ss.id)
	// quarantined clients keep computing (every result is then checked); only draining,
	// pending and revoked clients get no tasks. A client asks only when it accepts work:
	// the Accepting of its offer is only how things were at connection time (testing
	// 'pause' found that a client connecting on battery or paused got no task until it
	// reconnected).
	ss.hbAccept = true
	if rec == nil || (rec.Status != stActive && rec.Status != stQuarantined) || free <= 0 || time.Now().Before(ss.coolUntil) ||
		(ss.outdated != "" && s.conf().OtherVersions == "refuse") ||
		(ss.offer.NoSecurity && !s.conf().AcceptNoSecurity && !build.NoSecurity) {
		return ss.conn.Send(wire.TNoTask, struct{}{})
	}
	cfg := s.conf()
	s.store.mu.Lock()
	idle := true
	for _, a := range s.store.Attempts {
		if a.ClientID == ss.id && !a.Lost {
			idle = false // only fully idle clients receive duplicates
			break
		}
	}
	fit := 1 // cores the job may take
	if ss.offer.MaxProcs > 1 {
		fit = min(free, ss.offer.MaxProcs)
	}
	if ss.offer.MemTotalMB <= 0 {
		freeMem = 0 // fixed memory per core: the per-core check applies
	}
	// (an outdated client gets no verification sub-task: it would compare two programs)
	t, kind := s.pickTask(ss.id, idle, rec.Status == stQuarantined || ss.outdated != "", ss.offer.MemPerSlotMB, fit, freeMem)
	if t == nil {
		s.store.mu.Unlock()
		return ss.conn.Send(wire.TNoTask, struct{}{})
	}
	if t.Attempts >= cfg.MaxAttempts {
		id := t.ID
		s.store.mu.Unlock()
		s.failTask(id, fmt.Sprintf("gave up after %d attempts", cfg.MaxAttempts))
		return ss.conn.Send(wire.TNoTask, struct{}{})
	}
	a := &Attempt{ID: randomID(12), TaskID: t.ID, ClientID: ss.id, Kind: kind, Started: time.Now(), LastSeen: time.Now()}
	s.store.Attempts[a.ID] = a
	t.Attempts++
	s.store.touch()
	s.store.mu.Unlock()

	// nothing is sent yet: a task whose files are gone (a check concluded by another
	// client in the meantime: 120-client load test) is no task, not a broken connection
	unread := func(err error) error {
		s.store.mu.Lock()
		delete(s.store.Attempts, a.ID)
		s.store.touch()
		s.store.mu.Unlock()
		if t.Internal && errors.Is(err, fs.ErrNotExist) {
			s.log.Infof("task %s: its files are gone (its check has concluded); not sent", t.ID)
		} else {
			s.log.Warnf("cannot read task %s: %v", t.ID, err)
		}
		return ss.conn.Send(wire.TNoTask, struct{}{})
	}
	names, paths, err := s.bundle(t)
	if err != nil {
		return unread(err)
	}
	names[0] = anonStem(a.ID) + ".inp" // clients never see real names (privacy, canaries)
	for i := range names {
		if names[i] == checkpointName { // ORCA AutoStart reads <input name>.gbw
			names[i] = anonStem(a.ID) + ".gbw"
		}
	}
	// opened once, before anything is sent: a check concluding meanwhile removes its
	// directory, but files already open stay readable
	files := make([]*os.File, 0, len(paths))
	defer func() {
		for _, f := range files {
			f.Close()
		}
	}()
	for _, p := range paths {
		f, err := os.Open(p)
		if err != nil {
			return unread(err)
		}
		files = append(files, f)
	}
	metas, err := xfer.MetaOpen(names, files)
	if err != nil {
		return unread(err)
	}
	msg := wire.Task{TaskID: opaqueID(t.ID), AttemptID: a.ID, InputName: names[0], Files: metas, MemMB: t.MemMB,
		LeaseSec: int(cfg.TaskTimeout.Seconds()), Slots: procsOf(cfg, t), OrcaVersion: cfg.OrcaVersion, Kind: kind}
	if kind == "job" {
		msg.MaxRuntimeSec = int(cfg.MaxJobTime.Seconds())
	}
	if ss.outdated != "" { // orca.other_versions = separate | errors: its own version
		msg.OrcaVersion = ss.clientVersion()
	}
	err = ss.conn.Exclusive(func(tx wire.Tx) error {
		if err := tx.Send(wire.TTask, msg); err != nil {
			return err
		}
		for i := range names {
			if err := xfer.SendOpen(tx, names[i], files[i], cfg.CompressLevel); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	for i := range names {
		if names[i] == anonStem(a.ID)+".gbw" && paths[i] == s.checkpointPath(t.ID) {
			// kept orbitals are used ONCE: if this host fails too, the next one starts
			// from scratch (a poisoned .gbw cannot make every host fail)
			s.store.mu.Lock()
			a.FromCheckpoint, t.CheckpointFrom = t.CheckpointFrom, ""
			s.store.touch()
			s.store.mu.Unlock()
			files[i].Close() // (Windows removes no open file)
			os.Remove(paths[i])
		}
	}
	if t.Internal {
		kind = t.Purpose
	} else if kind == "job" {
		// canaries are a fraction of the real work, so an idle network does not burn
		// compute on them
		s.maybeCanary(ss.id)
	}
	s.log.Infof("assigned %s to %s (attempt %s, %s)", t.ID, ss.name, a.ID, kind)
	return nil
}

var (
	reQuotedName = regexp.MustCompile(`"([^"/\\\n]+)"`)
	reXYZFile    = regexp.MustCompile(`(?im)^\s*\*\s*(?:xyzfile|gzmtfile|pdbfile)\s+\S+\s+\S+\s+(\S+)`)
)

// bundle returns the input file plus companion files it references (same directory).
// Internal tasks keep all their files in state/internal/<check>/<purpose>/.
func (s *Server) bundle(t *TaskState) ([]string, []string, error) {
	taskID := t.ID
	if t.Internal {
		return s.internalBundle(t)
	}
	inPath := filepath.Join(s.conf().Root, dInput, filepath.FromSlash(taskID))
	b, err := os.ReadFile(inPath)
	if err != nil {
		return nil, nil, err
	}
	if len(b) > orca.MaxInputBytes {
		return nil, nil, fmt.Errorf("input larger than %d bytes", orca.MaxInputBytes)
	}
	base := path.Base(taskID)
	names, paths := []string{base}, []string{inPath}
	seen := map[string]bool{base: true}
	var refs []string
	for _, m := range reQuotedName.FindAllStringSubmatch(string(b), -1) {
		refs = append(refs, m[1])
	}
	for _, m := range reXYZFile.FindAllStringSubmatch(string(b), -1) {
		refs = append(refs, strings.Trim(m[1], `"`))
	}
	for _, r := range refs {
		if seen[r] || xfer.SafeName(r) != nil || strings.Contains(r, "/") {
			continue
		}
		p := filepath.Join(filepath.Dir(inPath), r)
		if st, err := os.Stat(p); err == nil && st.Mode().IsRegular() {
			names = append(names, r)
			paths = append(paths, p)
			seen[r] = true
		}
	}
	if cp := s.checkpointPath(taskID); fileExists(cp) {
		names, paths = append(names, checkpointName), append(paths, cp)
	}
	return names, paths, nil
}

// notePower keeps the client's power state and estimates its battery drain rate.
func (ss *Session) notePower(state string, pct float64, now time.Time) {
	ss.power, ss.battPct = state, pct
	if state != "battery" || pct < 0 {
		ss.battRefT, ss.drainPerH = time.Time{}, 0
		return
	}
	if ss.battRefT.IsZero() || pct > ss.battRefPct {
		ss.battRefT, ss.battRefPct = now, pct
		return
	}
	if dt := now.Sub(ss.battRefT); dt >= 10*time.Minute {
		ss.drainPerH = (ss.battRefPct - pct) / dt.Hours()
	}
}

// powerText is the POWER column of 'status': "ac", "-" (no battery), "bat 57% -18%/h".
func (ss *Session) powerText() string {
	switch ss.power {
	case "", "none":
		return "-"
	case "ac":
		if ss.battPct >= 0 {
			return fmt.Sprintf("ac %.0f%%", ss.battPct)
		}
		return "ac"
	}
	t := fmt.Sprintf("bat %.0f%%", ss.battPct)
	if ss.drainPerH > 0 {
		t += fmt.Sprintf(" -%.0f%%/h", ss.drainPerH)
	}
	return t
}

// noteHostname records the client's hostname and catches one key used on two machines (a
// copied client directory, a cloned VM): the server keeps only the newest connection, so
// such twins would keep kicking each other off. Two different clients with the same
// hostname are fine (the identity is the key; 'clients' shows both with their ids).
func (s *Server) noteHostname(ss *Session, h string) {
	var prev string
	var prevSeen time.Time
	_ = s.reg.update(ss.id, func(c *ClientRec) {
		prev, prevSeen = c.Hostname, c.LastSeen
		c.Hostname = h
		if prev != "" && prev != h {
			c.HostChanges = append(c.HostChanges, time.Now())
			if len(c.HostChanges) > 10 {
				c.HostChanges = c.HostChanges[len(c.HostChanges)-10:]
			}
		}
	})
	if prev == "" || prev == h {
		return
	}
	// a renamed computer changes once; twins alternate within minutes
	c := s.reg.get(ss.id)
	recent := 0
	for _, t := range c.HostChanges {
		if time.Since(t) < time.Hour {
			recent++
		}
	}
	if recent >= 2 && time.Since(prevSeen) < 10*time.Minute {
		s.alertf("quarantine", "client %s connects alternately from hosts %q and %q: one key on two machines (copied client directory or cloned VM?). Disable one ('clients disable') and give it its own credential", ss.name, prev, h)
	} else {
		s.log.Infof("client %s now reports hostname %q (was %q)", ss.name, h, prev)
	}
}

// noteDisplaced counts a live session pushed out by a new connection of the same key from
// another address. A client that roams does this once; two machines sharing a key (cloned
// VM, copied directory, possibly with the same hostname) do it again and again.
func (s *Server) noteDisplaced(id, name, oldAddr, newAddr string) {
	n := 0
	_ = s.reg.update(id, func(c *ClientRec) {
		var keep []time.Time
		for _, t := range c.Displaced {
			if time.Since(t) < time.Hour {
				keep = append(keep, t)
			}
		}
		c.Displaced = append(keep, time.Now())
		n = len(c.Displaced)
	})
	if n == 3 {
		s.alertf("quarantine", "client %s: a live connection was replaced by the same key from another address 3 times within an hour (%s, %s): one key on two machines? Disable one ('clients disable') and give it its own credential", name, oldAddr, newAddr)
	}
}
