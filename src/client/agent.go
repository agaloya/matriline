package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/agaloya/matriline/common/build"
	"io"
	mrand "math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/agaloya/matriline/common/ident"
	"github.com/agaloya/matriline/common/logx"
	"github.com/agaloya/matriline/common/orca"
	"github.com/agaloya/matriline/common/release"
	"github.com/agaloya/matriline/common/wire"
	"github.com/agaloya/matriline/common/xfer"
)

const agentVersion = "matriline-client/0.1.0"

// commit is set by tools/release.sh (-ldflags -X main.commit=<git revision>).
var commit = "dev"

// minJobMB is the least memory a single ORCA job is given on a very small machine.
const minJobMB = 512

type orcaInstall struct {
	dir     string
	version string
	git     string
	fp      *orca.Fingerprint
	benchMs float64
}

type evKind int

const (
	evJobDone evKind = iota
	evJobGone
	evTask
	evNoTask
	evCancel
	evAck
	evNotice
)

type event struct {
	kind   evKind
	job    *Job
	ack    wire.ResultAck
	cancel wire.Cancel
	notice wire.Notice
}

// Agent is the running client.
type Agent struct {
	pausedSeen  atomic.Bool // the owner's pause as last seen (for one log line per change)
	updateMu    sync.Mutex
	update      *pendingUpdate // a signed release to install when no job is left (update.go)
	refusedNote string         // a release that did not run here, for the server
	cfg         *Config
	log         *logx.Logger
	key         *ident.Key
	cred        *ident.Credential

	serverID     string
	self         string
	userns       bool
	sandboxDesc  string
	slots        int
	memPerSlotMB int         // default memory per core (the equal share when memory_total is set)
	memTotalMB   int         // memory pool for all jobs (0: fixed memPerSlotMB per core)
	mpi          *mpiInstall // nil: single-core jobs only
	installs     []*orcaInstall

	polMu sync.RWMutex
	pol   wire.Welcome

	jobsMu sync.Mutex
	jobs   map[string]*Job // attempt id -> job

	events   chan event
	execMu   sync.Mutex
	execSeen map[string]map[string]bool

	netlog        *netLog
	clockOffsetMs *float64
	limiter       *wire.RateLimiter
	draining      bool
	diskPeaks     map[string][]int64 // scratch use of recent jobs per diskClass, for disk-aware admission
	uploadAfter   time.Time
	batteryLow    bool // paused by min_battery_percent (hysteresis)
	// jobs found in the journal at start wait for the server's word before resuming (it
	// may have reassigned or completed them meanwhile); see resumePending
	pendingResume map[string]*Job
	resumeAt      time.Time // after a reconcile: resume what was not cancelled by then
}

func (a *Agent) policy() wire.Welcome {
	a.polMu.RLock()
	defer a.polMu.RUnlock()
	return a.pol
}

func (a *Agent) orcaFor(version string) *orcaInstall {
	for _, in := range a.installs {
		if version == "" || in.version == version {
			return in
		}
	}
	return nil
}

// jobMemMB is the memory per core a job reserves: the default share, or what the task is
// known to need when that is more and the pool allows it (acceptCheck).
func (a *Agent) jobMemMB(j *Job) int {
	if a.memTotalMB > 0 && j.Task.MemMB > a.memPerSlotMB {
		return j.Task.MemMB
	}
	return a.memPerSlotMB
}

// freeMemMB is what the memory pool has left, not counting job except.
func (a *Agent) freeMemMB(except *Job) int {
	a.jobsMu.Lock()
	defer a.jobsMu.Unlock()
	used := 0
	for _, j := range a.jobs {
		if j != except && j.active() {
			used += a.jobMemMB(j) * max(j.Task.Slots, 1)
		}
	}
	return a.memTotalMB - used
}

// runningCount is the number of cores in use: a job of N cores (MPI) counts N.
func (a *Agent) runningCount() int {
	a.jobsMu.Lock()
	defer a.jobsMu.Unlock()
	n := 0
	for _, j := range a.jobs {
		if j.active() {
			n += max(j.Task.Slots, 1)
		}
	}
	return n
}

// ---------------------------------------------------------------------------------------
// Startup

func newAgent(cfgPath string) (*Agent, error) { return openAgent(cfgPath, true) }

// openAgent: needCred = false lets 'doctor' check ORCA and the sandbox before this client
// has a credential (a new helper waiting for the admin's file; Nacomline before its first
// run); a.cred is then empty.
func openAgent(cfgPath string, needCred bool) (*Agent, error) {
	cfg, warns, err := loadConfig(cfgPath)
	for _, w := range warns {
		fmt.Fprintln(os.Stderr, w)
	}
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(cfg.StateDir, 0o700); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(cfg.ScratchDir, 0o700); err != nil {
		return nil, err
	}
	tempFile = cfg.TempFile
	// jobs hold other people's unpublished inputs: private even if the directories were
	// created beforehand with wider permissions
	for _, d := range []string{cfg.StateDir, cfg.ScratchDir} {
		if err := os.Chmod(d, 0o700); err != nil {
			return nil, err
		}
	}
	lg, err := logx.New(filepath.Join(cfg.StateDir, "client.log"), "debug")
	if err != nil {
		return nil, err
	}
	a := &Agent{cfg: cfg, log: lg, jobs: map[string]*Job{}, events: make(chan event, 4096),
		execSeen: map[string]map[string]bool{}, netlog: &netLog{}, diskPeaks: map[string][]int64{}}
	a.pol = wire.Welcome{ReturnFiles: []string{"*"}, SampleSec: 5, Percentiles: []int{50, 90, 99}, CompressLevel: 6, HeartbeatSec: 60}
	if a.self, err = os.Executable(); err != nil {
		return nil, err
	}
	cred, err := ident.ReadCredential(cfg.Credential)
	if err != nil && (needCred || !os.IsNotExist(err)) {
		return nil, fmt.Errorf("credential: %v", err)
	}
	if cred == nil {
		cred = &ident.Credential{}
	}
	a.cred = cred
	keyFile := filepath.Join(cfg.StateDir, "client.key")
	switch _, kerr := os.Stat(keyFile); {
	case cred.Key != nil:
		a.key = cred.Key
	case cred.ServerAddress == "" && kerr != nil:
		// 'doctor' without a credential creates no key: a missing key is how 'enable'
		// recognises a client whose key and credential the admin wiped (a macOS
		// checklist caught doctor creating one, after which enable undid a wipe)
	default:
		if a.key, _, err = ident.LoadOrCreate(keyFile); err != nil {
			return nil, err
		}
	}
	a.serverID = ident.IDOf(cred.ServerPub)
	return a, nil
}

// prepare fingerprints ORCA, probes the sandbox and sizes the resources.
func (a *Agent) prepare() error {
	cfg := a.cfg
	n, warn := cfg.slots()
	if warn != "" {
		a.log.Warnf("%s", warn)
	}
	a.memPerSlotMB = cfg.MemPerCoreMB
	// keep 15 % of the RAM (at least 512 MB) for the system and the user
	tot, _ := memInfo()
	reserve := max(tot*15/100, 512)
	if cfg.MemTotalMB > 0 {
		total := cfg.MemTotalMB
		if tot > 0 && int64(total) > tot-reserve {
			a.log.Warnf("ALERT: resources.memory_total = %d MB but this computer has %d MB (keeping %d MB for the system); lending %d MB", total, tot, reserve, tot-reserve)
			total = int(tot - reserve)
		}
		n = min(n, max(total/minJobMB, 1))
		a.memTotalMB, a.memPerSlotMB = total, total/n
	} else if tot > 0 {
		fit := int((tot - reserve) / int64(a.memPerSlotMB))
		if fit < 1 && tot-reserve >= minJobMB {
			// small machine (e.g. 1 core / 1.5 GB): still lend one core (D35), with the
			// memory that fits; ORCA's %maxcore follows memPerSlotMB
			a.log.Warnf("ALERT: %d MB of RAM cannot hold one job x %d MB; lending 1 core with %d MB", tot, a.memPerSlotMB, tot-reserve)
			a.memPerSlotMB = int(tot - reserve)
			fit = 1
		}
		if fit < n {
			a.log.Warnf("ALERT: %d MB of RAM cannot hold %d jobs x %d MB; lending %d core(s) instead (lower resources.memory_per_core or resources.cores)", tot, n, a.memPerSlotMB, fit)
			n = fit
		}
	}
	if n < 1 {
		return errors.New("not enough memory for a single job with the configured memory_per_core")
	}
	a.slots = n
	var quarantined []string // macOS: installations Gatekeeper would hold (gatekeeper_darwin.go)
	for _, p := range cfg.OrcaPaths {
		if _, err := os.Stat(orcaExe(p)); err != nil {
			continue
		}
		if q := quarantinedFiles(p); len(q) > 0 {
			a.log.Warnf("ORCA %s: %s", p, quarantineHint(p))
			quarantined = append(quarantined, p)
			continue
		}
		t0 := time.Now()
		cache := filepath.Join(cfg.StateDir, "fpcache-"+filepath.Base(p)+".json")
		if _, err := os.Stat(cache); err != nil {
			a.log.Infof("fingerprinting ORCA at %s (the first time takes a minute or two)", p)
		}
		fp, err := orca.FingerprintTree(p, cache, false)
		if err != nil {
			a.log.Warnf("ORCA %s: %v", p, err)
			continue
		}
		in := &orcaInstall{dir: p, fp: fp}
		a.installs = append(a.installs, in)
		a.log.Infof("ORCA %s fingerprint %s (%d files, %.1fs)", p, fp.TreeHash[:16], len(fp.Files), time.Since(t0).Seconds())
	}
	if len(a.installs) == 0 && len(quarantined) > 0 {
		return errors.New(quarantineHint(quarantined[0]))
	}
	if len(a.installs) == 0 {
		return errors.New("no ORCA installation found (check orca.paths)")
	}

	// probe each installation through the sandbox: learns version/GIT, verifies that the
	// sandbox works on this kernel and measures a small benchmark
	a.sandboxDesc = "disabled"
	if cfg.Sandbox {
		// a.userns: the strongest level (Linux: user+network namespaces; Windows:
		// AppContainer); without it the weaker one (Landlock only; low integrity)
		a.userns = true
		setSandboxStrong(true)
		if err := a.probe(a.installs[0], true); err != nil {
			a.log.Warnf("%s unavailable (%v); trying %s", sandboxDesc(true), err, sandboxDesc(false))
			if hint := sandboxHint(a.installs[0].dir); hint != "" {
				a.log.Warnf("%s", hint)
			}
			a.userns = false
			setSandboxStrong(false)
			if err := a.probe(a.installs[0], true); err != nil {
				return fmt.Errorf("the ORCA sandbox does not work on this system (%v). Fix it, or set security.sandbox = false to run ORCA unprotected", err)
			}
		}
		a.sandboxDesc = sandboxDesc(a.userns)
	}
	var ok []*orcaInstall // keep the working installations
	for _, in := range a.installs {
		if in.version == "" {
			if err := a.probe(in, cfg.Sandbox); err != nil {
				a.log.Warnf("ORCA %s does not work: %v", in.dir, err)
			}
		}
		a.log.Infof("ORCA %s: version %s build %s, benchmark %.0f ms", in.dir, in.version, in.git, in.benchMs)
		if in.version != "" {
			ok = append(ok, in)
		}
	}
	a.installs = ok
	if len(ok) == 0 {
		return errors.New("no working ORCA installation")
	}
	if cfg.MaxCoresJob > 1 {
		if cfg.Sandbox && !a.userns {
			a.log.Warnf("resources.max_cores_per_job = %d, but multi-core jobs need the sandbox's network namespace (not available here): single-core jobs only", cfg.MaxCoresJob)
		} else if m, err := findMPI(cfg); err != nil {
			a.log.Warnf("resources.max_cores_per_job = %d but %v: single-core jobs only", cfg.MaxCoresJob, err)
		} else {
			a.mpi = m
			a.log.Infof("%s %s at %s fingerprint %s: jobs of up to %d cores", m.kind, m.version, m.dir, m.fp.TreeHash[:16], min(cfg.MaxCoresJob, a.slots))
		}
	}
	if f := diskFree(cfg.ScratchDir); f >= 0 && cfg.DiskLimit > 0 && cfg.DiskLimit > f-cfg.DiskReserve {
		a.log.Warnf("resources.disk_limit (%d MB) is more than this disk can give while keeping resources.disk_reserve (%d MB) free: at most %d MB will be used", cfg.DiskLimit>>20, cfg.DiskReserve>>20, max(f-cfg.DiskReserve, 0)>>20)
	}
	a.log.Infof("lending %d slot(s) x %d MB; sandbox: %s", a.slots, a.memPerSlotMB, a.sandboxDesc)
	return nil
}

const probeInput = "! HF def2-SVP\n* xyz 0 1\nO 0 0 0\nH 0 0.757 0.587\nH 0 -0.757 0.587\n*\n"

func (a *Agent) probe(in *orcaInstall, sandboxed bool) error {
	top, err := os.MkdirTemp(a.cfg.ScratchDir, ".probe-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(top)
	dir := filepath.Join(top, "work")
	if err := makeWorkDir(dir); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "probe.inp"), []byte(probeInput), 0o644); err != nil {
		return err
	}
	prog := []string{orcaExe(in.dir), "probe.inp"}
	var cmd *exec.Cmd
	if sandboxed {
		cmd = sandboxCommand(a.self, dir, []string{in.dir}, 0, prog, a.userns, false)
		if cmd == nil {
			return errors.New("sandbox unavailable: " + sandboxErr)
		}
	} else {
		cmd = plainCommand(prog)
	}
	cmd.Dir = dir
	cmd.Env = orcaEnv(in.dir, dir)
	t0 := time.Now()
	out, err := cmd.CombinedOutput()
	el := time.Since(t0)
	if err != nil {
		tail := string(out)
		if len(tail) > 300 {
			tail = tail[len(tail)-300:]
		}
		return fmt.Errorf("%v: %s", err, strings.TrimSpace(tail))
	}
	info, err := orca.ParseOutput(strings.NewReader(string(out)))
	if err != nil || !info.Terminated {
		return errors.New("probe job did not terminate normally")
	}
	in.version, in.git, in.benchMs = info.Version, info.Git, float64(el.Milliseconds())
	return nil
}

// loadJournal restores jobs left by a previous run (power cut, crash, reboot).
func (a *Agent) loadJournal() {
	a.loadPeaks()
	entries, _ := os.ReadDir(a.cfg.ScratchDir)
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		dir := filepath.Join(a.cfg.ScratchDir, e.Name())
		j, err := loadJob(dir)
		if err != nil {
			a.log.Warnf("discarding unreadable job directory %s: %v", dir, err)
			os.RemoveAll(dir)
			continue
		}
		a.jobs[j.Task.AttemptID] = j
		switch j.State {
		case jsReceived, jsRunning:
			// not resumed at once: after a long absence the server may have given the task
			// to another host or completed it; ask first (resumePending)
			if a.pendingResume == nil {
				a.pendingResume = map[string]*Job{}
			}
			a.pendingResume[j.Task.AttemptID] = j
			a.log.Infof("job %s (%s) found after restart: resuming once the server confirms it", j.Task.TaskID, j.State)
		case jsFinished:
			a.log.Infof("result of %s waits for upload", j.Task.TaskID)
		}
	}
}

// accepting evaluates the local conditions for taking new tasks.
func (a *Agent) accepting() (bool, string) {
	cfg := a.cfg
	now := time.Now()
	p, paused := readPause(cfg)
	if a.pausedSeen.Swap(paused) != paused && a.log != nil { // once per change, by the running service
		if paused {
			a.log.Infof("%s: no new jobs", p.text())
		} else {
			a.log.Infof("pause over: taking jobs again")
		}
	}
	switch {
	case a.draining:
		return false, "draining (requested by the server)"
	case paused:
		return false, p.text()
	case a.pendingUpdate() != nil:
		return false, "installing Matriline " + a.pendingUpdate().m.Version + " when no job is left"
	case !cfg.EndDate.IsZero() && now.After(cfg.EndDate):
		return false, "end date reached"
	case !cfg.inSchedule(now):
		return false, "outside the configured schedule"
	case cfg.PauseBattery && onBattery():
		return false, "running on battery"
	}
	if cfg.MinBattery > 0 {
		pw := readPower(powerSupplyDirVar)
		switch {
		case pw.State != "battery" || pw.Percent < 0:
			a.batteryLow = false
		case pw.Percent < cfg.MinBattery:
			a.batteryLow = true
		case pw.Percent >= cfg.MinBattery+cfg.BatteryResume:
			a.batteryLow = false
		}
		if a.batteryLow {
			return false, fmt.Sprintf("battery at %.0f %% (below %.0f %%; resumes on mains or at %.0f %%)", pw.Percent, cfg.MinBattery, cfg.MinBattery+cfg.BatteryResume)
		}
	}
	if cfg.TempLimitC > 0 {
		if t, na := cpuTempC(); na == "" && t >= cfg.TempLimitC {
			return false, fmt.Sprintf("CPU temperature %.0f C above the limit", t)
		}
	}
	if cfg.BusyPercent > 0 {
		b1, t1 := cpuTimes()
		time.Sleep(200 * time.Millisecond)
		b2, t2 := cpuTimes()
		if t2 > t1 {
			others := float64(b2-b1)/float64(t2-t1)*100 - float64(a.runningCount())/float64(runtime.NumCPU())*100
			if others > float64(cfg.BusyPercent) {
				return false, "computer busy with other programs"
			}
		}
	}
	if cfg.DiskLimit > 0 && dirBytes(cfg.ScratchDir) >= cfg.DiskLimit {
		return false, "disk limit reached"
	}
	if f := diskFree(cfg.ScratchDir); f >= 0 && f < cfg.DiskReserve {
		a.log.Warnf("ALERT: less than resources.disk_reserve (%d MB) free on the scratch disk: free some space, or lower resources.disk_reserve in client.conf", cfg.DiskReserve>>20)
		return false, "scratch disk almost full (keeping resources.disk_reserve free for the system)"
	} else if need := a.diskNeed(classLight); f >= 0 && f-cfg.DiskReserve < need {
		return false, fmt.Sprintf("not enough scratch disk for another job (%d MB free, ~%d MB needed)", f>>20, need>>20)
	}
	return true, ""
}

// diskNeed estimates the scratch space one more job of the given class needs: the largest
// use among recent jobs of that class (at least 256 MB), plus what the running jobs may
// still grow to, each up to the peak of ITS class. Before, one peak served every job, so a
// single DLPNO job (8.8 GB) made a 5-slot host count every small DFT job as 8.8 GB and take
// almost no work (BDE campaign); two DLPNO jobs started together filled a 6.7 GB disk.
func (a *Agent) diskNeed(class string) int64 {
	a.jobsMu.Lock()
	defer a.jobsMu.Unlock()
	need := a.classPeak(class)
	for _, j := range a.jobs {
		if j.active() {
			j.mu.Lock()
			need += max(0, a.classPeak(j.DiskClass)-j.DiskNow)
			j.mu.Unlock()
		}
	}
	return need
}

// classPeak is the largest recent scratch use of a class, or disk_per_job when that is set
// (a job may use up to it) (caller holds jobsMu).
func (a *Agent) classPeak(class string) int64 {
	if a.cfg.DiskPerJob > 0 {
		return a.cfg.DiskPerJob
	}
	if class == "" {
		class = classLight
	}
	est := int64(256 << 20)
	if class == classHeavy {
		// no history yet: correlated jobs need GBs (a DLPNO peaked near 14 GB). A first job
		// that still does not fit dies of a full disk, which counts twice from then on.
		est = 2 << 30
	}
	for _, p := range a.diskPeaks[class] {
		est = max(est, p)
	}
	return est
}

// notePeak records a finished job's scratch use (last 20 per class), kept in
// state/diskpeaks.json: after a restart the client forgot that a DLPNO job used 7.6 GB and
// would have taken another one onto a disk with 7.8 GB free (lab). A job killed by a full
// disk only shows what it wrote until then, less than it needs: it counts twice that. Seen
// in the BDE campaign: a DLPNO replica recorded ~6.3 GB, really needed ~14 GB, and failed
// on six hosts in a row.
func (a *Agent) notePeak(class string, b int64, diskFull bool) {
	if class == "" {
		class = classLight
	}
	if diskFull {
		b *= 2
	}
	a.jobsMu.Lock()
	defer a.jobsMu.Unlock()
	ps := append(a.diskPeaks[class], b)
	if len(ps) > 20 {
		ps = ps[len(ps)-20:]
	}
	a.diskPeaks[class] = ps
	if buf, err := json.Marshal(a.diskPeaks); err == nil {
		os.WriteFile(filepath.Join(a.cfg.StateDir, "diskpeaks.json"), buf, 0o600)
	}
}

// loadPeaks restores the recent scratch uses saved by notePeak. The old format (one list
// for every job) is read as the heavy class.
func (a *Agent) loadPeaks() {
	a.diskPeaks = map[string][]int64{}
	buf, err := os.ReadFile(filepath.Join(a.cfg.StateDir, "diskpeaks.json"))
	if err != nil {
		return
	}
	if json.Unmarshal(buf, &a.diskPeaks) != nil || a.diskPeaks == nil {
		a.diskPeaks = map[string][]int64{}
		// old format, one list for every job: split it by size (the lab's lists were mostly
		// DFT jobs, and reading them all as heavy let a DLPNO through: a user's review)
		var old []int64
		if json.Unmarshal(buf, &old) == nil {
			for _, p := range old {
				c := classLight
				if p > 2<<30 {
					c = classHeavy
				}
				a.diskPeaks[c] = append(a.diskPeaks[c], p)
			}
		}
	}
}

const (
	classLight = "light" // SCF-level methods (HF, DFT, semiempirical): small scratch
	classHeavy = "heavy" // correlated methods: scratch of several GB
)

var reHeavyKw = regexp.MustCompile(`(?i)\b(dlpno|ccsd|qcisd|cisd|mp2|mp3|ri-mp2|scs-mp2|mdci|casscf|nevpt2|caspt2|mrci|adc2|eom|b2plyp|dsd-|pwpb95|revdsd|b2gp)`)

// diskClass sorts an input by the scratch disk it is likely to need (keyword lines only).
func diskClass(input string) string {
	for _, ln := range strings.Split(input, "\n") {
		if t := strings.TrimSpace(ln); (strings.HasPrefix(t, "!") || strings.HasPrefix(strings.ToLower(t), "%mdci")) && reHeavyKw.MatchString(t) {
			return classHeavy
		}
	}
	return classLight
}

// ---------------------------------------------------------------------------------------
// Connection management

func (a *Agent) run(ctx context.Context) error {
	// a fresh process: 'status' must not show the previous one's connection while this one
	// checks ORCA (minutes after a reboot or an update; code review)
	a.noteConnection(false, "starting", "")
	if err := a.prepare(); err != nil {
		return err
	}
	a.limiter = wire.NewRateLimiter(a.cfg.BandwidthBPS)
	a.loadJournal()
	go a.awakeLoop(ctx)
	startedAt := time.Now()
	backoff := time.Second
	unreachable, lastHint := 0, time.Time{}
	for {
		if time.Since(startedAt) > 10*time.Minute {
			// no server for 10 minutes: keep computing rather than idle
			a.resumePending("the server could not be reached for 10 minutes")
		}
		if !a.cfg.EndDate.IsZero() && time.Now().After(a.cfg.EndDate) && a.runningCount() == 0 {
			a.log.Warnf("end date %s reached: the client disables itself", a.cfg.EndDate.Format("2006-01-02"))
			os.WriteFile(filepath.Join(a.cfg.StateDir, "DISABLED"), []byte("end date reached\n"), 0o600)
			return nil
		}
		start := time.Now()
		err := a.session(ctx)
		if ctx.Err() != nil {
			return nil
		}
		var off *disabledErr
		if errors.As(err, &off) {
			a.disable(off.msg)
			return nil // exit status 0: a service manager with Restart=on-failure leaves it stopped
		}
		var fatal *fatalErr
		if errors.As(err, &fatal) {
			return fatal
		}
		var rs *restartErr
		if errors.As(err, &rs) {
			return rs
		}
		a.netlog.down(time.Now())
		a.noteConnection(false, "", err.Error())
		if time.Since(start) > time.Minute {
			backoff = time.Second
		}
		// the server never answered: after a few tries say what to check, in plain words
		// (a fresh-install test only saw "no route to host" repeated forever)
		if isDialError(err) {
			if unreachable++; unreachable >= 5 && time.Since(lastHint) > 30*time.Minute {
				a.log.Warnf("%s", unreachableHint(a.cred.ServerAddress, a.cred.RelayAddress, err))
				lastHint = time.Now()
			}
		} else {
			unreachable = 0
		}
		// +-20 % so that the clients of a restarted server do not all come back at once
		wait := time.Duration(float64(backoff) * (0.8 + 0.4*mrand.Float64()))
		a.log.Warnf("connection ended: %v (reconnecting in %s)", err, wait.Round(100*time.Millisecond))
		if !waitOrNetwork(ctx, wait) {
			return nil
		}
		// 1, 2, 4, 8, 16, 30 s: the cap was 64 s, so the client came back up to ~84 s
		// after a long outage ended (found in a real-network test)
		backoff = min(backoff*2, 30*time.Second)
	}
}

type fatalErr struct{ msg string }

func (f *fatalErr) Error() string { return f.msg }

func (a *Agent) session(ctx context.Context) error {
	d := &wire.Dialer{Key: a.key, ServerPub: a.cred.ServerPub, Relay: a.cred.RelayAddress, Limiter: a.limiter}
	conn, err := d.Dial(ctx, a.cred.ServerAddress)
	if err != nil {
		return err
	}
	defer conn.Close()
	began := time.Now()
	defer func() {
		// data used by this session (protocol frames; TLS adds a few %): what a volunteer on
		// a metered connection pays for
		dur := time.Since(began)
		perHour := ""
		if dur > time.Minute {
			perHour = ", " + humanBytes(uint64(float64(conn.BytesIn+conn.BytesOut)/dur.Hours())) + "/h"
		}
		a.log.Infof("session lasted %s: sent %s, received %s%s", dur.Round(time.Second),
			humanBytes(conn.BytesOut), humanBytes(conn.BytesIn), perHour)
	}()
	if err := conn.Send(wire.TAuth, wire.Auth{Agent: agentVersion, JoinToken: a.cred.JoinToken, Name: nz(a.cred.Name, hostname()),
		Platform: release.Platform(), CanUpdate: canUpdate(a.cfg)}); err != nil {
		return err
	}
	var w wire.Welcome
	conn.ReadIdle = 60 * time.Second
	if err := conn.Expect(wire.TWelcome, &w); err != nil {
		if strings.Contains(err.Error(), "revoked") {
			return &fatalErr{"access revoked by the server admin: " + err.Error()}
		}
		if i := strings.Index(err.Error(), "disabled by the server admin"); i >= 0 {
			return &disabledErr{err.Error()[i:]}
		}
		if strings.Contains(err.Error(), "already used or has expired") {
			return &fatalErr{err.Error() + " (if you did not use it yourself, someone else may have: tell the admin)"}
		}
		return err
	}
	if strings.HasPrefix(a.cred.JoinToken, "mle-") {
		// enrolled: the one-time token is spent; keep it out of the file
		a.cred.JoinToken = ""
		if err := ident.WriteCredential(a.cfg.Credential, a.cred); err != nil {
			a.log.Warnf("could not remove the used enrollment token from %s: %v", a.cfg.Credential, err)
		} else {
			a.log.Infof("enrolled with this computer's own key %s (state/client.key; keep it private, it never leaves this computer)", a.key.ID())
		}
	}
	// the server only talks when needed; dead peers are found by TCP keepalive and failing
	// heartbeats. ReadIdle = 0 alone keeps the deadline armed by Expect above, which cut
	// every session exactly 60 s after the welcome (seen in the lab), so clear it too.
	conn.ReadIdle = 0
	_ = conn.SetReadDeadline(time.Time{})
	if rtt, _, ok := tcpStats(conn.NetConn()); ok && w.ServerUnixMs > 0 {
		off := float64(w.ServerUnixMs) + rtt/2 - float64(time.Now().UnixMilli())
		a.clockOffsetMs = &off
	}
	a.polMu.Lock()
	a.pol = w
	a.polMu.Unlock()
	a.netlog.up(time.Now())
	a.log.Infof("connected to %s (%s mode); status %s", w.ServerID, conn.Mode, w.Status)
	a.noteConnection(true, w.Status, "")
	a.draining = w.Status == "draining" // the server's registry is the truth at every (re)connect
	if w.Message != "" {
		a.log.Warnf("server: %s", w.Message)
	}
	if w.Status == "pending" {
		return errors.New("waiting for the server admin to approve this client")
	}
	if !a.versionAccepted(w) {
		a.log.Errorf("none of the local ORCA installations matches the server's required build %v", w.OrcaVersions)
	}
	acc, why := a.accepting()
	offer := wire.OfferRes{Slots: a.slots, MemPerSlotMB: a.memPerSlotMB, MemTotalMB: a.memTotalMB, ScratchFree: diskFree(a.cfg.ScratchDir),
		OS: osName(), Arch: runtime.GOOS + "/" + runtime.GOARCH, CPUModel: cpuModel(), LogicalCPUs: runtime.NumCPU(), Hostname: hostname(),
		Accepting: acc && w.Status == "active", PauseReason: why, NoSecurity: build.NoSecurity}
	if a.mpi != nil {
		offer.MaxProcs = min(a.cfg.MaxCoresJob, a.slots)
		offer.MPI = a.mpi.kind + " " + a.mpi.version + " " + a.mpi.fp.TreeHash[:16]
	}
	for _, in := range a.installs {
		offer.Orca = append(offer.Orca, wire.OrcaInstall{Version: in.version, Git: in.git, TreeHash: in.fp.TreeHash, Path: in.dir})
		if offer.BenchScore == 0 {
			offer.BenchScore = in.benchMs
		}
	}
	if err := conn.Send(wire.TOfferRes, offer); err != nil {
		return err
	}
	if err := conn.Send(wire.TReconcile, wire.Reconcile{Attempts: a.attemptStates()}); err != nil {
		return err
	}
	if len(a.pendingResume) > 0 {
		a.resumeAt = time.Now().Add(5 * time.Second) // time for the server's cancels to arrive
	}
	// reader goroutine: turns frames into events (TASK files are received here)
	readErr := make(chan error, 1)
	go func() { readErr <- a.reader(conn) }()
	stopWatch := make(chan struct{})
	defer close(stopWatch)
	go a.watchLink(conn, stopWatch)
	return a.loop(ctx, conn, w, readErr)
}

func (a *Agent) versionAccepted(w wire.Welcome) bool {
	for _, v := range w.OrcaVersions {
		for _, in := range a.installs {
			if v == in.version || v == in.version+"@"+in.git {
				return true
			}
		}
	}
	return len(w.OrcaVersions) == 0
}

func (a *Agent) attemptStates() []wire.AttemptState {
	a.jobsMu.Lock()
	defer a.jobsMu.Unlock()
	var out []wire.AttemptState
	for _, j := range a.jobs {
		st := "running"
		if j.State == jsFinished {
			st = "finished"
		}
		as := wire.AttemptState{TaskID: j.Task.TaskID, AttemptID: j.Task.AttemptID, State: st, Progress: outSize(j)}
		if st == "running" {
			as.Tail = outTail(j)
		}
		out = append(out, as)
	}
	sort.Slice(out, func(i, k int) bool { return out[i].AttemptID < out[k].AttemptID })
	return out
}

// outTail is the end of a running job's ORCA output (at most 8 lines of 160 characters),
// for the server's live view.
func outTail(j *Job) string {
	stem := strings.TrimSuffix(j.Task.InputName, filepath.Ext(j.Task.InputName))
	f, err := os.Open(filepath.Join(j.workDir(), stem+".out"))
	if err != nil {
		return ""
	}
	defer f.Close()
	buf := make([]byte, 4096)
	if st, err := f.Stat(); err == nil && st.Size() > int64(len(buf)) {
		f.Seek(st.Size()-int64(len(buf)), io.SeekStart)
	}
	n, _ := io.ReadFull(f, buf)
	var lines []string
	for _, l := range strings.Split(string(buf[:n]), "\n") {
		if l = strings.TrimRight(l, " \r\t"); strings.TrimSpace(l) != "" {
			if len(l) > 160 {
				l = l[:160]
			}
			lines = append(lines, l)
		}
	}
	if len(lines) > 0 && n == len(buf) {
		lines = lines[1:] // probably cut
	}
	if len(lines) > 8 {
		lines = lines[len(lines)-8:]
	}
	return strings.Join(lines, "\n")
}

func outSize(j *Job) int64 {
	stem := strings.TrimSuffix(j.Task.InputName, filepath.Ext(j.Task.InputName))
	if st, err := os.Stat(filepath.Join(j.workDir(), stem+".out")); err == nil {
		return st.Size()
	}
	return 0
}

func (a *Agent) reader(conn *wire.Conn) error {
	for {
		t, p, err := conn.ReadFrame()
		if err != nil {
			return err
		}
		switch t {
		case wire.TTask:
			var task wire.Task
			if err := wire.Decode(p, &task); err != nil {
				return err
			}
			j, err := a.receiveTask(conn, task)
			if err != nil {
				return err
			}
			a.events <- event{kind: evTask, job: j}
		case wire.TNoTask:
			a.events <- event{kind: evNoTask}
		case wire.TCancel:
			var c wire.Cancel
			if err := wire.Decode(p, &c); err != nil {
				return err
			}
			a.events <- event{kind: evCancel, cancel: c}
		case wire.TResultAck:
			var ack wire.ResultAck
			if err := wire.Decode(p, &ack); err != nil {
				return err
			}
			a.events <- event{kind: evAck, ack: ack}
		case wire.TNotice:
			var n wire.Notice
			wire.Decode(p, &n)
			a.events <- event{kind: evNotice, notice: n}
		case wire.TError:
			var e wire.ErrorMsg
			wire.Decode(p, &e)
			return fmt.Errorf("server error: %s", e.Message)
		default:
			return fmt.Errorf("unexpected message %v from server", t)
		}
	}
}

// receiveTask stores the task files in a new journal entry.
func (a *Agent) receiveTask(conn *wire.Conn, t wire.Task) (*Job, error) {
	if xfer.SafeName(t.AttemptID) != nil || strings.Contains(t.AttemptID, "/") || len(t.Files) == 0 || len(t.Files) > 64 {
		return nil, errors.New("malformed task from server")
	}
	dir := filepath.Join(a.cfg.ScratchDir, t.AttemptID)
	work := filepath.Join(dir, "work")
	if err := makeJobDir(work, t.Slots); err != nil {
		return nil, err
	}
	for _, f := range t.Files {
		if strings.Contains(f.Name, "/") {
			return nil, errors.New("task file names must be plain names")
		}
		if err := xfer.ReceiveFile(conn, f, work); err != nil {
			os.RemoveAll(dir)
			return nil, err
		}
	}
	return &Job{Task: t, State: jsReceived, Received: time.Now(), dir: dir, cancel: make(chan string, 1), yield: make(chan string, 1)}, nil
}

// loop is the single owner of the session state.
func (a *Agent) loop(ctx context.Context, conn *wire.Conn, w wire.Welcome, readErr chan error) error {
	hb := time.NewTicker(time.Duration(max(w.HeartbeatSec, 5)) * time.Second)
	pauseTick := time.NewTicker(5 * time.Second) // 'pause --now' takes effect within seconds
	defer pauseTick.Stop()
	defer hb.Stop()
	tick := time.NewTicker(2 * time.Second)
	defer tick.Stop()
	outstanding := false
	nextAsk := time.Time{}
	uploading := false
	uploadDone := make(chan error, 1)
	var lastRetrans uint32
	var lastBytes uint64
	pendingUploads := func() []*Job {
		a.jobsMu.Lock()
		defer a.jobsMu.Unlock()
		var out []*Job
		for _, j := range a.jobs {
			if j.State == jsFinished && j.Result != nil {
				out = append(out, j)
			}
		}
		sort.Slice(out, func(i, k int) bool { return out[i].Ended.Before(out[k].Ended) })
		return out
	}
	acked := map[string]bool{}
	sent := map[string]bool{}
	startUpload := func() {
		if uploading || time.Now().Before(a.uploadAfter) {
			return
		}
		for _, j := range pendingUploads() {
			if sent[j.Task.AttemptID] || acked[j.Task.AttemptID] {
				continue
			}
			uploading = true
			sent[j.Task.AttemptID] = true
			go func(j *Job) { uploadDone <- a.upload(conn, j) }(j)
			return
		}
	}
	// no upload right at connect: the first tick (2 s) leaves the server time to answer
	// the RECONCILE with cancellations of results another host already delivered
	for {
		select {
		case <-ctx.Done():
			// an upload holds the connection exclusively, so BYE would wait for it to end
			// (a SIGTERM was ignored for minutes during a large upload in the lab); the
			// unacknowledged result is simply uploaded again after the restart
			if !uploading {
				conn.Send(wire.TBye, struct{}{})
			}
			conn.Close()
			return ctx.Err()
		case err := <-readErr:
			return err
		case err := <-uploadDone:
			uploading = false
			if err != nil {
				return fmt.Errorf("upload: %v", err)
			}
			startUpload()
		case <-pauseTick.C:
			if p, on := readPause(a.cfg); on && p.Now {
				a.yieldJobs(p.text())
			}
		case <-hb.C:
			acc, _ := a.accepting()
			h := wire.Heartbeat{Attempts: a.attemptStates(), Load1: loadAvg1(), FreeSlots: a.slots - a.runningCount(),
				Accepting: acc, SentUnix: time.Now().UnixMilli(), CPUTempC: -1}
			if t, na := cpuTempC(); na == "" {
				h.CPUTempC = t
			}
			pw := readPower(powerSupplyDirVar)
			h.Power, h.BatteryPct = pw.State, pw.Percent
			if err := conn.Send(wire.THeartbeat, h); err != nil {
				return err
			}
			if rtt, retr, ok := tcpStats(conn.NetConn()); ok {
				segs := (conn.BytesOut - lastBytes) / 1448
				a.netlog.sample(time.Now(), rtt, retr-lastRetrans, segs)
				lastRetrans, lastBytes = retr, conn.BytesOut
			}
		case <-tick.C:
			if err := a.tryUpdate(ctx); err != nil {
				return err
			}
			if n := a.refusal(); n != "" {
				if err := conn.Send(wire.TNotice, wire.Notice{Kind: "update_refused", Message: n}); err != nil {
					return err
				}
				a.refusalSent(n)
			}
			if !a.resumeAt.IsZero() && time.Now().After(a.resumeAt) {
				a.resumePending("confirmed by the server")
			}
			startUpload()
			if outstanding || time.Now().Before(nextAsk) || w.Status != "active" {
				continue
			}
			free := a.slots - a.runningCount()
			if free <= 0 {
				continue
			}
			if acc, why := a.accepting(); !acc {
				a.log.Debugf("not requesting tasks: %s", why)
				nextAsk = time.Now().Add(30 * time.Second)
				continue
			}
			g := wire.GetTask{Free: free}
			if a.memTotalMB > 0 {
				if g.FreeMemMB = a.freeMemMB(nil); g.FreeMemMB < a.memPerSlotMB {
					nextAsk = time.Now().Add(30 * time.Second) // the pool is taken by big jobs
					continue
				}
			}
			if err := conn.Send(wire.TGetTask, g); err != nil {
				return err
			}
			outstanding = true
		case ev := <-a.events:
			switch ev.kind {
			case evTask:
				outstanding = false
				j := ev.job
				if err := a.acceptCheck(j); err != nil {
					a.log.Warnf("rejecting %s: %v", j.Task.TaskID, err)
					os.RemoveAll(j.dir)
					if err := conn.Send(wire.TTaskReject, wire.TaskReply{AttemptID: j.Task.AttemptID, Reason: err.Error()}); err != nil {
						return err
					}
					continue
				}
				if err := j.save(); err != nil {
					return err
				}
				a.jobsMu.Lock()
				a.jobs[j.Task.AttemptID] = j
				a.jobsMu.Unlock()
				conn.Send(wire.TTaskAccept, wire.TaskReply{AttemptID: j.Task.AttemptID})
				go a.runJob(j)
			case evNoTask:
				outstanding = false
				nextAsk = time.Now().Add(15 * time.Second)
			case evJobDone:
				startUpload()
			case evJobGone:
				a.jobsMu.Lock()
				delete(a.jobs, ev.job.Task.AttemptID)
				a.jobsMu.Unlock()
			case evCancel:
				id := ev.cancel.AttemptID
				a.jobsMu.Lock()
				j := a.jobs[id]
				pending, drop := false, false
				if j != nil {
					_, pending = a.pendingResume[id]
					delete(a.pendingResume, id)
					if drop = pending || j.State == jsFinished; drop {
						delete(a.jobs, id)
					}
				}
				a.jobsMu.Unlock()
				switch {
				case j == nil:
				case pending:
					a.log.Infof("not resuming %s: %s", j.Task.TaskID, ev.cancel.Reason)
					os.RemoveAll(j.dir)
				case drop:
					a.log.Infof("dropping result of %s: %s", j.Task.TaskID, ev.cancel.Reason)
					os.RemoveAll(j.dir)
				default:
					select {
					case j.cancel <- ev.cancel.Reason:
					default:
					}
				}
			case evAck:
				a.jobsMu.Lock()
				j := a.jobs[ev.ack.AttemptID]
				delete(a.jobs, ev.ack.AttemptID)
				a.jobsMu.Unlock()
				acked[ev.ack.AttemptID] = true
				if j != nil {
					a.log.Infof("server acknowledged %s: %s", j.Task.TaskID, ev.ack.Verdict)
					os.RemoveAll(j.dir)
				}
			case evNotice:
				n := ev.notice
				if n.Kind != "update" { // its message is the signed release, logged by offerUpdate
					a.log.Warnf("server notice [%s]: %s", n.Kind, n.Message)
				}
				switch n.Kind {
				case "drain":
					a.draining = true
				case "active":
					a.draining = false
					w.Status = "active" // tasks are only requested while the session is active
				case "revoked":
					return &fatalErr{"access revoked by the server admin"}
				case "disabled":
					return &disabledErr{n.Message}
				case "update":
					a.offerUpdate(n.Message)
				case "warn":
					if strings.Contains(n.Message, "storage full") || strings.Contains(n.Message, "retry later") {
						a.uploadAfter = time.Now().Add(5 * time.Minute)
						sent = map[string]bool{} // re-send after the pause
					}
				}
			}
		}
	}
}

// upload sends one finished result (RESULT + files) atomically on the connection.
func (a *Agent) upload(conn *wire.Conn, j *Job) error {
	pol := a.policy()
	j.mu.Lock()
	res := *j.Result
	j.mu.Unlock()
	// The server's return policy may have changed since the result was built (e.g. *.tmp
	// excluded later): a DLPNO failure waited in the lab to upload 6.7 GB of scratch.
	// Re-sign with the current policy and delete what is no longer wanted.
	for _, f := range res.Files {
		if !returned(pol, f.Name) {
			a.log.Infof("result of %s: return policy changed; dropping excluded files before upload", j.Task.TaskID)
			a.buildResult(j, j.Failure, j.ExitCode)
			j.mu.Lock()
			res = *j.Result
			keep := j.keepSet()
			j.mu.Unlock()
			keepOnly(j.workDir(), keep)
			break
		}
	}
	for _, f := range res.Files {
		if st, err := os.Stat(filepath.Join(j.workDir(), f.Name)); err != nil || st.Size() != f.Size {
			a.log.Warnf("result of %s: %s changed or vanished since it was signed; re-signing the files present", j.Task.TaskID, f.Name)
			a.buildResult(j, j.Failure, j.ExitCode)
			j.mu.Lock()
			res = *j.Result
			j.mu.Unlock()
			break
		}
	}
	a.log.Infof("uploading result of %s (%d files)", j.Task.TaskID, len(res.Files))
	return conn.Exclusive(func(tx wire.Tx) error {
		if err := tx.Send(wire.TResult, res); err != nil {
			return err
		}
		for _, f := range res.Files {
			if err := xfer.SendFile(tx, f.Name, filepath.Join(j.workDir(), f.Name), pol.CompressLevel); err != nil {
				return err
			}
		}
		return nil
	})
}

// ---------------------------------------------------------------------------------------
// Connection quality log (feeds the net.* fields of the run report)

type netLog struct {
	mu      sync.Mutex
	outages [][2]time.Time
	downAt  time.Time
	samples []netSample
}

type netSample struct {
	t       time.Time
	rtt     float64
	retrans uint32
	segs    uint64
}

func (n *netLog) down(t time.Time) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.downAt.IsZero() {
		n.downAt = t
	}
}

func (n *netLog) up(t time.Time) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if !n.downAt.IsZero() {
		n.outages = append(n.outages, [2]time.Time{n.downAt, t})
		n.downAt = time.Time{}
	}
	if len(n.outages) > 10000 {
		n.outages = n.outages[len(n.outages)-10000:]
	}
}

func (n *netLog) sample(t time.Time, rtt float64, retrans uint32, segs uint64) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.samples = append(n.samples, netSample{t, rtt, retrans, segs})
	if len(n.samples) > 100000 {
		n.samples = n.samples[len(n.samples)-100000:]
	}
}

type netWindow struct {
	outages   []float64
	rtt       []float64
	retrans   uint64
	segments  uint64
	retransOK bool
}

// during summarizes connection events overlapping [a, b].
func (n *netLog) during(a, b time.Time) netWindow {
	n.mu.Lock()
	defer n.mu.Unlock()
	var w netWindow
	spans := append([][2]time.Time{}, n.outages...)
	if !n.downAt.IsZero() {
		spans = append(spans, [2]time.Time{n.downAt, b})
	}
	for _, o := range spans {
		s, e := o[0], o[1]
		if e.Before(a) || s.After(b) {
			continue
		}
		if s.Before(a) {
			s = a
		}
		if e.After(b) {
			e = b
		}
		w.outages = append(w.outages, e.Sub(s).Seconds())
	}
	for _, s := range n.samples {
		if s.t.Before(a) || s.t.After(b) {
			continue
		}
		w.rtt = append(w.rtt, s.rtt)
		w.retrans += uint64(s.retrans)
		w.segments += s.segs
		w.retransOK = true
	}
	return w
}

func hostname() string {
	h, _ := os.Hostname()
	return h
}

// disabledErr: the server admin switched this client off (matriline-server clients disable).
type disabledErr struct{ msg string }

func (d *disabledErr) Error() string { return d.msg }

// disable stops every job (their tasks go back to the server, which already requeued
// them), deletes all job data (and, when the admin withdrew it, the key and the
// credential), and leaves state/DISABLED so that a restart does nothing until
// 'matriline-client enable'. The message says who did it and how to come back.
func (a *Agent) disable(msg string) {
	a.log.Warnf("%s", msg)
	fmt.Fprintln(os.Stderr, "matriline-client: "+msg)
	a.jobsMu.Lock()
	waiting := map[*Job]bool{}
	for _, j := range a.jobs {
		if j.active() {
			select {
			case j.cancel <- "client disabled by the server admin":
				waiting[j] = true
			default:
			}
		}
	}
	a.jobsMu.Unlock()
	// a cancelled job still writes its report and result before it signals evJobDone:
	// delete only after that, or the files come back
	timeout := time.After(30 * time.Second)
	for len(waiting) > 0 {
		select {
		case ev := <-a.events:
			if ev.kind == evJobDone || ev.kind == evJobGone {
				delete(waiting, ev.job)
			}
		case <-timeout:
			a.log.Warnf("%d job(s) did not stop within 30 s; deleting anyway", len(waiting))
			waiting = nil
		}
	}
	var left []string
	rm := func(p string) {
		if err := os.RemoveAll(p); err != nil {
			left = append(left, p)
		}
	}
	if es, err := os.ReadDir(a.cfg.ScratchDir); err == nil {
		for _, e := range es {
			rm(filepath.Join(a.cfg.ScratchDir, e.Name()))
		}
	}
	wipe := strings.Contains(msg, "(credential withdrawn)") // clients disable --wipe
	for _, n := range []string{"jobs", "diskpeaks.json"} {
		rm(filepath.Join(a.cfg.StateDir, n))
	}
	if fs, err := filepath.Glob(filepath.Join(a.cfg.StateDir, "fpcache-*")); err == nil {
		for _, f := range fs {
			rm(f)
		}
	}
	if wipe {
		rm(filepath.Join(a.cfg.StateDir, "client.key"))
		rm(a.cfg.Credential)
	}
	os.WriteFile(filepath.Join(a.cfg.StateDir, "DISABLED"), []byte(msg+"\n"), 0o600)
	if len(left) > 0 {
		a.log.Errorf("could not delete: %s", strings.Join(left, ", "))
	} else if wipe {
		a.log.Warnf("job data, key and credential deleted; the client stays off (state/DISABLED)")
	} else {
		a.log.Warnf("job data deleted; the client stays off until 'matriline-client enable' (state/DISABLED)")
	}
}

// resumePending starts the journal jobs that were waiting for the server's confirmation.
func (a *Agent) resumePending(why string) {
	a.jobsMu.Lock()
	jobs := a.pendingResume
	a.pendingResume, a.resumeAt = nil, time.Time{}
	a.jobsMu.Unlock()
	for _, j := range jobs {
		a.log.Infof("resuming job %s (%s)", j.Task.TaskID, why)
		go a.runJob(j)
	}
}
