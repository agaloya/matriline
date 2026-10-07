package main

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf16"

	"github.com/agaloya/matriline/common/ident"
	"github.com/agaloya/matriline/common/ledger"
	"github.com/agaloya/matriline/common/logx"
	"github.com/agaloya/matriline/common/orca"
	"github.com/agaloya/matriline/common/wire"
)

const agentVersion = "matriline-server/0.1.0"

// commit is set by tools/release.sh (-ldflags -X main.commit=<git revision>).
var commit = "dev"

// Server is the running daemon.
type Server struct {
	bans         banState
	recentMu     sync.Mutex
	recent       map[string]*doneTask // opaque task id -> recently accepted task (late duplicates)
	diskMu       sync.Mutex
	diskInflight int64 // bytes of uploads in progress (reserveDisk)
	cfgPath      string
	cfgMu        sync.RWMutex
	cfg          *Config

	key    *ident.Key
	reg    *Registry
	learn  learner // tasks.scheduler = learned
	store  *Store
	ledger *ledger.Ledger
	log    *logx.Logger

	sessMu   sync.Mutex
	sessions map[string]*Session // client id -> live session

	refMu       sync.RWMutex
	refTrees    map[string]bool // accepted ORCA tree hashes
	refFileSHAs map[string]bool // every file hash of the reference installations
	refGit      map[string]bool // accepted GIT hashes
	refNoGit    map[string]bool // accepted trees whose ORCA prints no GIT hash (e.g. the arm64 build)
	refListed   map[string]bool // trees accepted by hash only (orca.accepted_tree_hashes): no file list
	refVersion  string

	moveMu  sync.Mutex // serializes spool moves
	stop    chan struct{}
	stopReq chan struct{}
	// restartExe: an update replaced the program, or the admin asked for a restart; cmdRun
	// starts it after the stop (setRestart / restartTo, under updMu)
	restartExe string
	updMu      sync.Mutex   // state/update/state.json (update.go)
	hooks      chan hookJob // hooks.go
	busy       bool         // the queue had work at the last watchdog round (campaign_done)
	wg         sync.WaitGroup
}

func (s *Server) conf() *Config {
	s.cfgMu.RLock()
	defer s.cfgMu.RUnlock()
	return s.cfg
}

func openServer(cfgPath string) (*Server, error) {
	cfg, warns, err := loadConfig(cfgPath)
	if err != nil {
		for _, w := range warns {
			fmt.Fprintln(os.Stderr, w)
		}
		return nil, err
	}
	if !filepath.IsAbs(cfg.Root) {
		cfg.Root = filepath.Join(filepath.Dir(cfgPath), cfg.Root)
	}
	cfg.Root = filepath.Clean(cfg.Root)
	// Inputs and results are often unpublished research: no access for other local users.
	// Spool directories are 0750 (owner + group, e.g. a research team), state/ (keys,
	// ledger, staging) 0700. Chmod also hardens spools created by older versions, whose
	// 0755 directories let any local user read input/ when the spool was outside a
	// private home (seen in the lab with /srv).
	if err := os.Chmod(cfg.Root, 0o750); err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	for _, d := range append(spoolDirs, dState, filepath.Join(dState, "staging")) {
		p := filepath.Join(cfg.Root, d)
		mode := os.FileMode(0o750)
		if d == dState || strings.HasPrefix(d, dState+string(filepath.Separator)) {
			mode = 0o700
		}
		if err := os.MkdirAll(p, mode); err != nil {
			return nil, err
		}
		if err := os.Chmod(p, mode); err != nil {
			return nil, err
		}
	}
	lg, err := logx.New(filepath.Join(cfg.Root, dState, "server.log"), cfg.LogLevel)
	if err != nil {
		return nil, err
	}
	for _, w := range warns {
		lg.Warnf("config: %s", w)
	}
	key, created, err := ident.LoadOrCreate(filepath.Join(cfg.Root, dState, "server.key"))
	if err != nil {
		return nil, err
	}
	if created {
		lg.Infof("created server key %s", key.ID())
	}
	reg, err := openRegistry(filepath.Join(cfg.Root, dState, "clients.json"))
	if err != nil {
		return nil, err
	}
	st, err := openStore(cfg.Root)
	if err != nil {
		return nil, err
	}
	led, err := ledger.Open(filepath.Join(cfg.Root, dState, "ledger.log"), key)
	if err != nil {
		return nil, fmt.Errorf("ledger: %v (run 'matriline-server verify' to inspect)", err)
	}
	setOpaqueKey(key.Priv)
	s := &Server{cfgPath: cfgPath, cfg: cfg, key: key, reg: reg, store: st, ledger: led, log: lg,
		sessions: map[string]*Session{}, stop: make(chan struct{}), stopReq: make(chan struct{}, 1), hooks: make(chan hookJob, 1000)}
	return s, nil
}

// loadReference fingerprints the reference ORCA installations named in the config and runs
// a tiny job with each to learn its version and GIT hash.
func (s *Server) loadReference() {
	cfg := s.conf()
	trees, shas, gits := map[string]bool{}, map[string]bool{}, map[string]bool{}
	noGit, listed := map[string]bool{}, map[string]bool{}
	for _, h := range cfg.OrcaTreeHashes {
		// accepted by the admin by hash alone: its files are unknown here, and its output
		// may lack the GIT line (the official arm64 6.1.1 build prints none)
		trees[h], noGit[h], listed[h] = true, true, true
	}
	sources := map[string][]byte{} // name -> JSON
	var order []string
	for _, f := range cfg.OrcaFingerprints {
		if f == "builtin" {
			b := builtinFor(cfg.OrcaVersion)
			if len(b) == 0 {
				s.log.Warnf("no built-in fingerprints for ORCA %s: list fingerprint files in orca.accepted_fingerprints", cfg.OrcaVersion)
			}
			for n, raw := range b {
				sources[n] = raw
				order = append(order, n)
			}
			continue
		}
		raw, err := os.ReadFile(f)
		if err != nil {
			s.log.Warnf("accepted fingerprint %s: %v", f, err)
			continue
		}
		sources[f] = raw
		order = append(order, f)
	}
	sort.Strings(order)
	for _, f := range order {
		raw := sources[f]
		var err error
		var fp orca.Fingerprint
		if err == nil {
			err = json.Unmarshal(utf8Text(raw), &fp)
		}
		if err == nil && orca.TreeHashOf(fp.Files) != fp.TreeHash {
			err = errors.New("tree hash does not match the file list (edited file?)")
		}
		if err != nil {
			s.log.Warnf("accepted fingerprint %s: %v", f, err)
			continue
		}
		trees[fp.TreeHash], noGit[fp.TreeHash] = true, true
		for _, h := range fp.Files {
			shas[h] = true
		}
		s.log.Infof("accepted ORCA fingerprint %s: tree %s (%d files)", f, fp.TreeHash[:16], len(fp.Files))
	}
	for _, p := range cfg.OrcaPaths {
		t0 := time.Now()
		cache := filepath.Join(cfg.Root, dState, "fpcache-"+sanitize(p)+".json")
		if _, err := os.Stat(cache); err != nil {
			// the first start reads every file of ORCA: minutes on a slow disk (a Windows
			// VM took over 10), and the server answers nobody meanwhile; later starts reuse
			// the cache. Without a reference ORCA (built-in fingerprints) there is no wait.
			s.log.Infof("reference ORCA %s: fingerprinting its files for the first time; this can take several minutes", p)
		}
		fp, err := orca.FingerprintTree(p, cache, false)
		if err != nil {
			s.log.Warnf("reference ORCA %s: %v", p, err)
			continue
		}
		trees[fp.TreeHash] = true
		for _, h := range fp.Files {
			shas[h] = true
		}
		ver, git, err := probeOrca(p, filepath.Join(cfg.Root, dState))
		if err != nil || git == "" {
			// another architecture cannot run here (exec format error), and some builds
			// print no GIT hash: their outputs are accepted without one, for THIS tree only
			noGit[fp.TreeHash] = true
		}
		if err != nil {
			s.log.Warnf("reference ORCA %s: probe failed (another architecture?): %v", p, err)
		} else {
			if git != "" {
				gits[git] = true
			}
			if ver != cfg.OrcaVersion {
				s.log.Warnf("reference ORCA %s is version %s but orca.version = %s", p, ver, cfg.OrcaVersion)
			}
		}
		s.log.Infof("reference ORCA %s: version %s git %s tree %s (%d files, %.1fs)", p, ver, git, fp.TreeHash[:16], len(fp.Files), time.Since(t0).Seconds())
	}
	s.refMu.Lock()
	s.refTrees, s.refFileSHAs, s.refGit, s.refVersion = trees, shas, gits, cfg.OrcaVersion
	s.refNoGit, s.refListed = noGit, listed
	s.refMu.Unlock()
}

var reSan = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

func sanitize(s string) string { return strings.Trim(reSan.ReplaceAllString(s, "_"), "_") }

// probeOrca runs a 0.1 s HF/STO-3G water job to read version and GIT hash.
func probeOrca(dir, scratch string) (string, string, error) {
	tmp, err := os.MkdirTemp(scratch, "probe-")
	if err != nil {
		return "", "", err
	}
	defer os.RemoveAll(tmp)
	in := "! HF STO-3G\n* xyz 0 1\nO 0 0 0\nH 0 0.757 0.587\nH 0 -0.757 0.587\n*\n"
	if err := os.WriteFile(filepath.Join(tmp, "probe.inp"), []byte(in), 0o644); err != nil {
		return "", "", err
	}
	cmd := exec.Command(orcaExe(dir), "probe.inp")
	cmd.Dir = tmp
	cmd.Env = append([]string{"PATH=" + dir, "LD_LIBRARY_PATH=" + filepath.Join(dir, "lib"), "HOME=" + tmp}, platformEnv(tmp)...)
	out, err := cmd.Output()
	if err != nil {
		return "", "", err
	}
	info, err := orca.ParseOutput(strings.NewReader(string(out)))
	if err != nil {
		return "", "", err
	}
	if !info.Terminated {
		return "", "", fmt.Errorf("probe job did not terminate normally")
	}
	return info.Version, info.Git, nil
}

// run starts listeners and background loops and blocks until stop.
func (s *Server) run(ctx context.Context) error {
	cfg := s.conf()
	s.loadReference()
	s.noteOrcaVersion()
	s.recoverStaging()
	if _, _, err := s.store.scan(cfg.InputExt, 0); err != nil {
		s.log.Warnf("initial scan: %v", err)
	}
	adm, err := s.startAdmin()
	if err != nil {
		return err
	}
	defer adm.Close()
	s.loadBans()
	if cfg.SecondOpinion && cfg.NoSecondOpinion == "weird" {
		active := 0
		for _, c := range s.reg.list() {
			if c.Status == stActive {
				active++
			}
		}
		if active > 0 && active < 3 {
			s.log.Warnf("only %d active client(s): a single-host check never finds a third host for its second opinion, so with verify.second_opinion_unavailable = weird most checked results go to weird/ (consider accept for small setups)", active)
		}
	}
	if min, free := s.minFree(), diskFree(cfg.Root); min > 0 && free >= 0 && free < min {
		s.log.Warnf("only %d MB free on the project's disk, below storage.min_free (%d MB): every result will be refused until space is freed or min_free is lowered", free>>20, min>>20)
		s.alertf("storage", "only %d MB free, below storage.min_free (%d MB): results will be refused", free>>20, min>>20)
	}
	var lns []net.Listener
	if cfg.Connection == "direct" {
		for _, a := range cfg.Listen {
			ln, err := net.Listen("tcp", a)
			if err != nil {
				return fmt.Errorf("listen %s: %v", a, err)
			}
			lns = append(lns, ln)
			_, port, _ := net.SplitHostPort(ln.Addr().String())
			s.log.Infof("listening on %s (security=%s, enrollment=%s)%s", ln.Addr(), cfg.Mode, cfg.Enrollment, firewallHint(port))
			s.wg.Add(1)
			go s.acceptLoop(ln)
		}
	} else {
		s.wg.Add(1)
		go s.relayLoop()
	}
	s.expireChecks() // before any assignment: drop checks finished before an unclean stop
	s.wg.Add(7)
	go s.hookLoop()
	go s.summaryLoop()
	go s.updateLoop()
	go s.checkLoop()
	go s.scanLoop()
	go s.watchdog()
	go s.flushLoop()
	s.log.Infof("server %s ready, spool %s", s.key.ID(), cfg.Root)
	select {
	case <-ctx.Done():
	case <-s.stopReq:
	}
	close(s.stop)
	for _, ln := range lns {
		ln.Close()
	}
	s.sessMu.Lock()
	for _, ss := range s.sessions {
		ss.conn.Close()
	}
	s.sessMu.Unlock()
	s.wg.Wait()
	_ = s.store.flush()
	s.ledger.Close()
	s.log.Infof("server stopped")
	return nil
}

func (s *Server) acceptLoop(ln net.Listener) {
	defer s.wg.Done()
	for {
		raw, err := ln.Accept()
		if err != nil {
			select {
			case <-s.stop:
				return
			default:
			}
			s.log.Warnf("accept: %v", err)
			time.Sleep(100 * time.Millisecond)
			continue
		}
		wire.SetUserTimeout(raw, wire.DeadPeerTimeout)
		go s.handleConn(raw, true)
	}
}

func (s *Server) scanLoop() {
	defer s.wg.Done()
	t := time.NewTicker(s.conf().ScanInterval)
	defer t.Stop()
	for {
		select {
		case <-s.stop:
			return
		case <-t.C:
			s.rescan()
		}
	}
}

func (s *Server) rescan() {
	cfg := s.conf()
	added, removed, err := s.store.scan(cfg.InputExt, cfg.SettleTime)
	if err != nil {
		s.log.Warnf("scan: %v", err)
		return
	}
	if len(added) > 0 {
		s.log.Infof("queued %d new task(s)", len(added))
		s.event("system", "queued %d new task(s)", len(added))
		s.ledgerTasks("queued", added)
	}
	// Clients write "%pal nprocs N" into the copy they run (N = 1 unless tasks.parallel =
	// honor); the manifest keeps the hash of the input as sent, so signatures still hold.
	cfg = s.conf()
	for _, id := range added {
		if in, err := s.taskInput(id); err == nil {
			if n := orca.RequestedProcs(string(in)); n > 1 {
				s.store.mu.Lock()
				if t := s.store.Tasks[id]; t != nil {
					t.Procs = n
					s.store.touch()
				}
				s.store.mu.Unlock()
				if cfg.Parallel == "honor" {
					s.log.Infof("%s asks for %d processes; it runs with %d on a client that accepts them", id, n, min(n, cfg.MaxProcs))
				} else {
					s.log.Warnf("%s asks for %d processes; it will run on ONE core (tasks.parallel = downgrade; single-core inputs are recommended: several independent jobs use the cores better)", id, n)
				}
			}
		}
	}
	for _, id := range removed {
		s.event("system", "%s left input/ outside Matriline (deleted or moved by hand?)", id)
		s.cancelAttemptsOf(id, "input removed from input/")
	}
	s.checkStorage()
}

func (s *Server) ledgerTasks(kind string, ids []string) {
	for _, id := range ids {
		s.store.mu.Lock()
		t := s.store.Tasks[id]
		sha := ""
		if t != nil {
			sha = t.InputSHA
		}
		s.store.mu.Unlock()
		s.ledger.Append(ledger.Entry{Kind: kind, Task: id, Files: []ledger.FileHash{{Path: dInput + "/" + id, SHA256: sha}}})
	}
}

func (s *Server) flushLoop() {
	defer s.wg.Done()
	t := time.NewTicker(2 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-s.stop:
			return
		case <-t.C:
			if err := s.store.flush(); err != nil {
				s.log.Errorf("state flush: %v", err)
			}
		}
	}
}

// watchdog expires attempts whose client stopped reporting them (D30).
func (s *Server) watchdog() {
	defer s.wg.Done()
	t := time.NewTicker(15 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-s.stop:
			return
		case <-t.C:
		}
		s.expireChecks()
		if s.conf().Scheduler == "learned" {
			s.refreshModel(false)
		}
		timeout := s.conf().TaskTimeout
		now := time.Now()
		var lost []string
		s.store.mu.Lock()
		for id, a := range s.store.Attempts {
			if s.store.Tasks[a.TaskID] == nil {
				// its task is finished or gone (e.g. attempts left behind by d07d291)
				delete(s.store.Attempts, id)
				s.store.touch()
				continue
			}
			if !a.Lost && now.Sub(a.LastSeen) > timeout {
				a.Lost = true
				s.store.touch()
				s.log.Warnf("attempt %s of %s on %s lost (no report for %s); task requeued", id, a.TaskID, a.ClientID, timeout)
				if !strings.HasPrefix(a.TaskID, "~") {
					lost = append(lost, a.ClientID+": "+a.TaskID)
				}
			}
			// lost attempts are forgotten after 7 days; a late result is still accepted
			// as long as its task is still queued (it carries its own signed manifest)
			if a.Lost && now.Sub(a.LastSeen) > 7*24*time.Hour {
				delete(s.store.Attempts, id)
				s.store.touch()
			}
		}
		work := len(s.store.Tasks) + len(s.store.Attempts) + len(s.store.Checks)
		s.store.mu.Unlock()
		if len(lost) > 0 {
			s.alertf("client_lost", "no news for %s from the client running %s: the task(s) went back to the queue", timeout, strings.Join(lost, ", "))
		}
		s.noteDrained(work)
	}
}

// noteDrained raises campaign_done (alert and hook) when the queue becomes empty after
// having had work: every input done, nothing running, no check pending.
func (s *Server) noteDrained(work int) {
	if work > 0 {
		s.busy = true
		return
	}
	if !s.busy {
		return
	}
	s.busy = false
	out, _ := s.cmdStatus(nil)
	results := ""
	for _, l := range strings.Split(out, "\n") {
		if strings.HasPrefix(l, "results:") {
			results = strings.TrimSpace(strings.TrimPrefix(l, "results:"))
		}
	}
	summary := "all inputs are done" // the hook gets it in English, like the log
	if results == "" {
		s.alertf("campaign_done", "all inputs are done")
	} else {
		summary += ": " + results
		s.alertf("campaign_done", "all inputs are done: %s", results)
	}
	if h := s.conf().HookDone; h != "" {
		s.queueHook(h, []string{"done", summary}, []string{"MATRILINE_EVENT=done"})
	}
}

// runningCount returns live (non-lost) attempts of a task; caller holds store.mu.
func (s *Server) runningOf(taskID string) []*Attempt {
	var out []*Attempt
	for _, a := range s.store.Attempts {
		if a.TaskID == taskID && !a.Lost {
			out = append(out, a)
		}
	}
	return out
}

// pickTask chooses the next task for a client; caller holds store.mu.
// Returns nil if nothing is suitable. kind is "job" or "replica".
// A quarantined client computes ordinary tasks (all of them checked) but never verifies
// another client's result: its verdicts cannot be trusted. Canaries for itself still go.
// memPerSlot is the memory per core the client lends (0 = unknown): tasks known to need
// more (TaskState.MemMB) are not offered to it.
// utf8Text decodes a text file written as UTF-16 with a byte-order mark (what Windows
// PowerShell's ">" produces, e.g. "matriline-client fingerprint > fp.json") or UTF-8 with a
// BOM; other input is returned unchanged.
func utf8Text(b []byte) []byte {
	switch {
	case len(b) >= 3 && b[0] == 0xEF && b[1] == 0xBB && b[2] == 0xBF:
		return b[3:]
	case len(b) >= 2 && (b[0] == 0xFF && b[1] == 0xFE || b[0] == 0xFE && b[1] == 0xFF):
		u := make([]uint16, 0, len(b)/2)
		for i := 2; i+1 < len(b); i += 2 {
			if b[0] == 0xFF {
				u = append(u, uint16(b[i])|uint16(b[i+1])<<8)
			} else {
				u = append(u, uint16(b[i])<<8|uint16(b[i+1]))
			}
		}
		return []byte(string(utf16.Decode(u)))
	}
	return b
}

// procsOf is the number of cores (MPI processes) task t runs with.
func procsOf(cfg *Config, t *TaskState) int {
	if cfg.Parallel != "honor" || t.Procs <= 1 {
		return 1
	}
	return min(t.Procs, cfg.MaxProcs)
}

// pickTask chooses a task for a client; fit is the most cores the job may take (the
// client's free cores, capped by the largest job it accepts); freeMem > 0 is what the
// client's memory pool has left (resources.memory_total), else memory is per core.
func (s *Server) pickTask(clientID string, idle, quarantined bool, memPerSlot, fit, freeMem int) (*TaskState, string) {
	cfg := s.conf()
	memOK := func(t *TaskState) bool {
		if freeMem > 0 {
			return max(t.MemMB, memPerSlot)*procsOf(cfg, t) <= freeMem
		}
		return t.MemMB <= 0 || memPerSlot <= 0 || t.MemMB <= memPerSlot
	}
	running := map[string][]*Attempt{}
	for _, a := range s.store.Attempts {
		if !a.Lost {
			running[a.TaskID] = append(running[a.TaskID], a)
		}
	}
	order := s.store.queueOrder(cfg.Order)
	var first *TaskState
	var cands []*TaskState // same top priority, for the learned scheduler
	for _, id := range order {
		t := s.store.Tasks[id]
		if len(running[id]) > 0 || t.avoided(clientID) || (t.OnlyFor != "" && t.OnlyFor != clientID) {
			continue
		}
		if quarantined && t.Internal && t.OnlyFor != clientID {
			continue
		}
		if !memOK(t) || procsOf(cfg, t) > fit {
			continue
		}
		if first == nil {
			first = t
			if cfg.Scheduler != "learned" || t.Internal {
				break // verification sub-tasks and canaries always go first
			}
		}
		if t.Priority != first.Priority || len(cands) >= 2000 {
			break
		}
		if !t.Internal {
			cands = append(cands, t)
		}
	}
	if first != nil {
		if t := s.learnedPick(clientID, cands); t != nil && !first.Internal {
			return t, "job"
		}
		return first, "job"
	}
	if !cfg.DuplicateIdle || !idle {
		return nil, ""
	}
	// Nothing queued: duplicate the oldest running task not already on this client (D30).
	var best *TaskState
	var bestStart time.Time
	for _, id := range order {
		rs := running[id]
		if len(rs) == 0 || len(rs) >= cfg.MaxReplicas || s.store.Tasks[id].avoided(clientID) || s.store.Tasks[id].Internal ||
			!memOK(s.store.Tasks[id]) || procsOf(cfg, s.store.Tasks[id]) > fit {
			continue
		}
		mine := false
		start := rs[0].Started
		for _, a := range rs {
			if a.ClientID == clientID {
				mine = true
			}
			if a.Started.Before(start) {
				start = a.Started
			}
		}
		if mine {
			continue
		}
		if best == nil || start.Before(bestStart) {
			best, bestStart = s.store.Tasks[id], start
		}
	}
	if best != nil {
		return best, "replica"
	}
	return nil, ""
}

// cancelAttemptsOf cancels every live attempt of a task (except keep, if non-empty).
func (s *Server) cancelAttemptsOf(taskID, reason string, keep ...string) {
	s.store.mu.Lock()
	var victims []*Attempt
	for id, a := range s.store.Attempts {
		if a.TaskID != taskID {
			continue
		}
		delete(s.store.Attempts, id)
		s.store.touch()
		if len(keep) == 0 || id != keep[0] { // keep: forgotten too, but not told to stop
			victims = append(victims, a)
		}
	}
	s.store.mu.Unlock()
	for _, a := range victims {
		if ss := s.session(a.ClientID); ss != nil {
			_ = ss.conn.Send(wire.TCancel, wire.Cancel{AttemptID: a.ID, Reason: reason})
		}
	}
}

func (s *Server) session(clientID string) *Session {
	s.sessMu.Lock()
	defer s.sessMu.Unlock()
	return s.sessions[clientID]
}

// clientPub returns the registered key of a client.
func (s *Server) clientPub(id string) ed25519.PublicKey {
	if c := s.reg.get(id); c != nil {
		return c.PubKey
	}
	return nil
}

// orcaFailThreshold is the number of distinct hosts on which ORCA must fail before a task
// goes to errors/: the configured value, but never more than the active clients that exist
// (otherwise a small network would wait forever for hosts it does not have).
func (s *Server) orcaFailThreshold() int {
	n := 0
	for _, c := range s.reg.list() {
		if c.Status == stActive || c.Status == stDraining {
			n++
		}
	}
	t := s.conf().OrcaFailHosts
	if n < t {
		t = n
	}
	if t < 1 {
		t = 1
	}
	return t
}

// checkStorage warns (and alerts) when the spool approaches the configured limit.
func (s *Server) checkStorage() {
	cfg := s.conf()
	if cfg.StorageLimit <= 0 {
		return
	}
	used := dirSize(cfg.Root)
	pct := int(used * 100 / cfg.StorageLimit)
	if pct >= cfg.StorageWarnPct {
		s.alertf("storage", "spool uses %d%% of the storage limit (%d of %d bytes)", pct, used, cfg.StorageLimit)
	}
}

func (s *Server) storageFull(extra int64) bool {
	cfg := s.conf()
	return cfg.StorageLimit > 0 && dirSize(cfg.Root)+extra > cfg.StorageLimit
}

// reserveDisk books n bytes for an upload if the disk keeps storage.min_free afterwards,
// counting the uploads already in progress (their bytes are not on disk yet). The caller
// releases the booking with releaseDisk once the files are written.
func (s *Server) reserveDisk(n int64) bool {
	cfg := s.conf()
	s.diskMu.Lock()
	defer s.diskMu.Unlock()
	if min := s.minFree(); min > 0 {
		free := diskFree(cfg.Root)
		if free >= 0 && free-s.diskInflight-n < min {
			return false
		}
	}
	s.diskInflight += n
	return true
}

// minFreeAuto marks storage.min_free = auto: 5 % of the disk, at most 5 GB.
const minFreeAuto = -2

// minFree is the free space storage.min_free keeps on the project's disk (0 = off).
func (s *Server) minFree() int64 {
	cfg := s.conf()
	if cfg.StorageMinFree != minFreeAuto {
		return max(cfg.StorageMinFree, 0)
	}
	total := diskTotal(cfg.Root)
	if total <= 0 {
		return 1 << 30
	}
	return min(total/20, 5<<30)
}

func (s *Server) releaseDisk(n int64) {
	s.diskMu.Lock()
	s.diskInflight -= n
	s.diskMu.Unlock()
}

// clientFault records a client's failed verification (dishonest = true) or error (ORCA or
// machine) and applies the automatic rules: quarantine after verify.quarantine_after
// failures within verify.quarantine_window; an admin alert after tasks.error_alert_after
// errors in a row; a pause (no new tasks until "clients release") after
// tasks.error_pause_after errors within tasks.error_window. 0 disables a rule.
func (s *Server) clientFault(id string, dishonest bool, why string) {
	cfg := s.conf()
	now := time.Now()
	recent := func(ts []time.Time, w time.Duration) []time.Time {
		var out []time.Time
		for _, t := range ts {
			if now.Sub(t) < w {
				out = append(out, t)
			}
		}
		return append(out, now)
	}
	var fails, errs, streak int
	var status, name string
	_ = s.reg.update(id, func(c *ClientRec) {
		if dishonest {
			c.Failures++
			c.LastFailure = now
			c.FailTimes = recent(c.FailTimes, cfg.QuarantineWindow)
		} else {
			c.ErrTimes = recent(c.ErrTimes, cfg.ErrorWindow)
			c.ErrStreak++
		}
		fails, errs, streak, status, name = len(c.FailTimes), len(c.ErrTimes), c.ErrStreak, c.Status, c.Name
	})
	switch {
	case dishonest && cfg.QuarantineAfter > 0 && fails >= cfg.QuarantineAfter && status == stActive:
		s.quarantine(id, fmt.Sprintf("%d failed verifications within %s (last: %s)", fails, cfg.QuarantineWindow, why))
	case !dishonest && cfg.ErrorPauseAfter > 0 && errs >= cfg.ErrorPauseAfter && status == stActive:
		_, _ = s.setClientStatus(id, stDraining, fmt.Sprintf("paused: %d errors within %s", errs, cfg.ErrorWindow))
		s.alertf("errors", "client %s paused after %d errors within %s (last: %s); 'clients release %s' to resume", name, errs, cfg.ErrorWindow, why, name)
	case !dishonest && cfg.ErrorAlertAfter > 0 && streak == cfg.ErrorAlertAfter:
		s.alertf("errors", "client %s: %d errors in a row (last: %s)", name, streak, why)
	}
}

// nz returns a, or b when a is empty.
func nz(a, b string) string {
	if a == "" {
		return b
	}
	return a
}
