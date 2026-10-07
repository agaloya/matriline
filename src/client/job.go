package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"github.com/agaloya/matriline/common/build"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/agaloya/matriline/common/ident"
	"github.com/agaloya/matriline/common/manifest"
	"github.com/agaloya/matriline/common/orca"
	"github.com/agaloya/matriline/common/report"
	"github.com/agaloya/matriline/common/wire"
	"github.com/agaloya/matriline/common/xfer"
)

// Job states stored in job.json (the journal entry of an attempt).
const (
	jsReceived = "received"
	jsRunning  = "running"
	jsFinished = "finished"
)

// Job is one attempt executed by this client. Its directory (<scratch>/<attempt>) holds
// job.json and work/ (where ORCA runs) and survives power cuts.
type Job struct {
	mu        sync.Mutex
	Task      wire.Task      `json:"task"`
	State     string         `json:"state"`
	Failure   string         `json:"failure,omitempty"`
	FailText  string         `json:"fail_text,omitempty"`
	ExitCode  int            `json:"exit_code"`
	Received  time.Time      `json:"received"`
	Started   time.Time      `json:"started"`
	Ended     time.Time      `json:"ended"`
	Resumes   int            `json:"resumes"`
	Clean     bool           `json:"clean_restart,omitempty"` // resumed run failed; rerun from scratch
	DiskNow   int64          `json:"disk_now,omitempty"`      // bytes in the work dir (last sample)
	DiskPeak  int64          `json:"disk_peak,omitempty"`
	DiskClass string         `json:"disk_class,omitempty"` // diskClass of the input
	MinFree   int64          `json:"min_free,omitempty"`   // lowest free scratch seen while running (bytes, 0 = not sampled)
	Res       orca.Resources `json:"resources"`
	OrcaDir   string         `json:"orca_dir"`
	Result    *wire.Result   `json:"result,omitempty"`
	Execs     map[string]int `json:"execs,omitempty"`
	dir       string
	cancel    chan string
	yield     chan string // pause --now: stop and give the task back
	// rusage of the finished run (runJob), read once by writeReport
	cpuSec   float64
	maxrssKB int64
	rusageOK bool
}

// active: received or running (holding a core).
func (j *Job) active() bool { return j.State == jsRunning || j.State == jsReceived }

func (j *Job) workDir() string { return filepath.Join(j.dir, "work") }

func (j *Job) save() error {
	j.mu.Lock()
	b, err := json.MarshalIndent(j, "", " ")
	j.mu.Unlock()
	if err != nil {
		return err
	}
	return ident.WriteFileAtomic(filepath.Join(j.dir, "job.json"), b, 0o600)
}

func loadJob(dir string) (*Job, error) {
	b, err := os.ReadFile(filepath.Join(dir, "job.json"))
	if err != nil {
		return nil, err
	}
	j := &Job{dir: dir, cancel: make(chan string, 1), yield: make(chan string, 1)}
	if err := json.Unmarshal(b, j); err != nil {
		return nil, err
	}
	return j, nil
}

// ---------------------------------------------------------------------------------------

// sampler collects telemetry series while a job runs.
type sampler struct {
	temp, freq, rss, avail, load, busy, scratch []float64
	tempNA, freqNA                              string
	rssPeakKB                                   int64
}

// orcaEnv is the clean environment ORCA runs in, for jobs and for the probe alike: no
// inherited variables (e.g. EXTOPTEXE) can redirect ORCA to external programs; PATH holds
// only the ORCA installation (D43); plus what the OS needs (extraEnv).
func orcaEnv(orcaDir, work string) []string {
	env := []string{"PATH=" + orcaDir, "LD_LIBRARY_PATH=" + filepath.Join(orcaDir, "lib"),
		"HOME=" + work, "TMPDIR=" + work, "OMP_NUM_THREADS=1", "LANG=C"}
	return append(env, extraEnv(work)...)
}

// runJob executes ORCA for a job and fills in the result. It blocks until ORCA exits.
func (a *Agent) runJob(j *Job) {
	pol := a.policy()
	inst := a.orcaFor(j.Task.OrcaVersion)
	if inst == nil {
		a.finishJob(j, "machine", "required ORCA version not installed", -1, nil)
		return
	}
	j.mu.Lock()
	if j.State == jsRunning {
		j.Resumes++ // interrupted by a crash or power cut: ORCA reuses existing .gbw (AutoStart)
	}
	j.State, j.Started, j.OrcaDir = jsRunning, time.Now(), inst.dir
	j.Execs = map[string]int{}
	j.mu.Unlock()
	j.save()

	work := j.workDir()
	stem := strings.TrimSuffix(j.Task.InputName, path.Ext(j.Task.InputName))
	inPath := filepath.Join(work, j.Task.InputName)
	raw, err := os.ReadFile(inPath)
	if err != nil {
		a.finishJob(j, "machine", "input missing: "+err.Error(), -1, nil)
		return
	}
	// D29: the client writes its own limits into the input (75 % of the memory per core)
	// (per MPI process: a job of N cores gets N times the memory per core)
	j.Res = orca.Resources{MaxcoreMB: a.jobMemMB(j) * 3 / 4, Nprocs: max(j.Task.Slots, 1)}
	mpi := a.mpi
	if j.Res.Nprocs == 1 {
		mpi = nil
	}
	if err := os.WriteFile(inPath, []byte(orca.Normalize(string(raw), j.Res)), 0o644); err != nil {
		a.finishJob(j, "machine", err.Error(), -1, nil)
		return
	}
	if cheatBefore(j, inPath) { // lab adversarial builds only (-tags cheat); no-op otherwise
		a.finishJob(j, "", "", 0, nil)
		return
	}
	out, err := os.Create(filepath.Join(work, stem+".out"))
	if err != nil {
		a.finishJob(j, "machine", err.Error(), -1, nil)
		return
	}
	defer out.Close()
	errf, _ := os.Create(filepath.Join(work, "matriline.stderr"))
	defer errf.Close()

	prog := []string{orcaExe(inst.dir), j.Task.InputName}
	var cmd *exec.Cmd
	sandbox := "disabled"
	// Per-file size limit inside the sandbox: the client's own setting. It used to be the
	// server's results.max_bytes, but that limits what is RETURNED; ORCA scratch files are
	// never returned and DLPNO writes single temporary files of several GB, which a 2 GB
	// limit would kill. The disk guard protects the disk.
	if a.cfg.Sandbox {
		execDirs := []string{inst.dir}
		if mpi != nil {
			execDirs = append(execDirs, mpi.dir)
		}
		cmd = sandboxCommand(a.self, work, execDirs, a.cfg.MaxFileMB, prog, a.userns, mpi != nil)
		sandbox = a.sandboxDesc
		if d := mpiSandboxDesc(); mpi != nil && d != "" {
			sandbox = d
		}
		if cmd == nil {
			a.finishJob(j, "machine", "sandbox not available on this OS (set security.sandbox = false to run unprotected)", -1, nil)
			return
		}
	} else {
		cmd = plainCommand(prog)
	}
	cmd.Dir = work
	cmd.Env = orcaEnv(inst.dir, work)
	if mpi != nil {
		cmd.Env = mpiEnv(cmd.Env, mpi, work)
	}
	cmd.Stdout, cmd.Stderr = out, errf
	if err := cmd.Start(); err != nil {
		a.finishJob(j, "machine", "cannot start ORCA: "+err.Error(), -1, nil)
		return
	}
	trackGroup(cmd) // Windows: job object, so killGroup ends ORCA's helpers too
	defer releaseGroup(cmd)
	a.log.Infof("job %s (%s) started, pid %d, sandbox %s", j.Task.TaskID, j.Task.AttemptID, cmd.Process.Pid, sandbox)

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	smp := &sampler{}
	audit := time.NewTicker(50 * time.Millisecond)
	defer audit.Stop()
	sampleEvery := time.Duration(pol.SampleSec) * time.Second
	if sampleEvery < time.Second {
		sampleEvery = 5 * time.Second
	}
	sample := time.NewTicker(sampleEvery)
	defer sample.Stop()
	// the computer's own limit (limits.max_task_time: the task goes to another host) and the
	// project's (tasks.max_job_time, sent by the server: the task stops for good and goes to
	// errors/ with its orbitals), whichever comes first
	var deadline, projectDeadline <-chan time.Time
	own, project := a.cfg.MaxTaskTime, time.Duration(j.Task.MaxRuntimeSec)*time.Second
	if own > 0 && (project <= 0 || own < project) {
		deadline = time.After(own)
	} else if project > 0 {
		projectDeadline = time.After(project)
	}
	var killReason, killText string
	prevBusy, prevTotal := cpuTimes()
	memLimitKB := int64(a.jobMemMB(j)) * int64(j.Res.Nprocs) * 1024 * 2 // hard stop at 2x the lent memory
	a.takeSample(j, smp, cmd.Process.Pid, &prevBusy, &prevTotal)
loop:
	for {
		select {
		case err = <-done:
			// close ORCA's output files now: Windows cannot delete an open file, and the
			// work directory is cleaned before this function returns ("being used by
			// another process" for matriline.stderr, Windows 11)
			out.Close()
			errf.Close()
			break loop
		case <-audit.C:
			if v := a.audit(j, inst, cmd.Process.Pid, a.self); v != "" && killReason == "" {
				killReason, killText = "safety", v
				killGroup(cmd)
			}
		case <-sample.C:
			used := a.takeSample(j, smp, cmd.Process.Pid, &prevBusy, &prevTotal)
			if smp.rssPeakKB > memLimitKB && killReason == "" {
				killReason, killText = "machine", fmt.Sprintf("memory use %d MB exceeds twice the lent memory", smp.rssPeakKB/1024)
				killGroup(cmd)
			}
			free := diskFree(work)
			j.mu.Lock()
			if free >= 0 && (j.MinFree == 0 || free < j.MinFree) {
				j.MinFree = free
			}
			j.DiskNow = used
			if used > j.DiskPeak {
				j.DiskPeak = used
			}
			j.mu.Unlock()
			switch {
			case killReason != "":
			case a.cfg.DiskPerJob > 0 && used > a.cfg.DiskPerJob:
				killReason, killText = "machine", "resources.disk_per_job reached"
				killGroup(cmd)
			case a.cfg.DiskLimit > 0 && used > a.cfg.DiskLimit:
				// (the total of all jobs is checked before taking new ones; one job alone
				// beyond the whole limit is stopped)
				killReason, killText = "machine", "resources.disk_limit reached"
				killGroup(cmd)
			case free >= 0 && free < a.cfg.DiskReserve/2:
				killReason, killText = "machine", fmt.Sprintf("scratch disk almost full (%d MB free; half of resources.disk_reserve)", free>>20)
				killGroup(cmd)
			}
		case <-deadline:
			if killReason == "" {
				killReason, killText = "timeout", "max_task_time exceeded"
				killGroup(cmd)
			}
		case <-projectDeadline:
			if killReason == "" {
				killReason, killText = "time_limit", fmt.Sprintf("the project's tasks.max_job_time (%s) was reached", time.Duration(j.Task.MaxRuntimeSec)*time.Second)
				killGroup(cmd)
			}
		case why := <-j.yield:
			if killReason == "" {
				killReason, killText = "returned", why
				killGroup(cmd)
			}
		case why := <-j.cancel:
			if killReason == "" {
				killReason, killText = "cancelled", why
				killGroup(cmd)
			}
		}
	}
	// nothing of this run may keep writing once the result is hashed (stragglers of the
	// process group; the PID namespace already handles this when the sandbox uses one)
	killGroup(cmd)
	a.takeSample(j, smp, -1, &prevBusy, &prevTotal)
	exit := -1
	if cmd.ProcessState != nil {
		exit = cmd.ProcessState.ExitCode()
		if cpu, maxrss, ok := rusageOf(cmd.ProcessState); ok {
			j.mu.Lock()
			j.cpuSec, j.maxrssKB, j.rusageOK = cpu, maxrss, true
			j.mu.Unlock()
		}
	}
	if killReason == "cancelled" {
		a.discardJob(j, killText)
		return
	}
	if killReason != "" {
		a.log.Warnf("job %s stopped: %s", j.Task.TaskID, killText)
		a.finishJob(j, killReason, killText, exit, smp)
		return
	}
	info, perr := orca.ParseOutputFile(filepath.Join(work, stem+".out"))
	switch {
	case perr == nil && info.Terminated && !info.ErrorTerm && exit == 0:
		cheatAfter(j, stem) // lab adversarial builds only (-tags cheat); no-op otherwise
		a.finishJob(j, "", "", exit, smp)
	case cmd.ProcessState != nil && signaled(cmd.ProcessState):
		a.finishJob(j, "machine", "ORCA killed by a signal (out of memory?)", exit, smp)
	case machineStatus(exit) != "":
		a.finishJob(j, "machine", machineStatus(exit), exit, smp)
	case exit >= 125 && exit <= 127 && (info == nil || !info.Terminated) && helperFailed(work, stem):
		// the sandbox helper could not start ORCA (125-127, Linux and Windows helpers) or
		// "cannot execute"/"not found": this computer's fault, not the input's. After a
		// power cut a Windows client failed so twice and the task was blamed. But ORCA itself
		// also exits with 126 on an input it cannot read ("expect a '$', '!', ..."): that is
		// the input's error (a test input whose comment held a second '#' was retried as a
		// machine failure forever, user's real test).
		msg := fmt.Sprintf("the sandbox could not start ORCA (exit %d)", exit)
		if t, _ := stderrTail(work); t != "" {
			msg += ": " + t
		}
		a.finishJob(j, "machine", msg, exit, smp)
	default:
		msg := "ORCA error termination"
		if info != nil && info.ErrorText != "" {
			msg = info.ErrorText
		} else if perr != nil || info == nil || !info.Terminated {
			// no ORCA error line: say what is known (exit code, the end of stderr), e.g. a
			// sandbox helper that could not start ORCA
			msg = fmt.Sprintf("ORCA ended without an error message (exit %d)", exit)
			if t, ok := stderrTail(work); ok {
				msg = fmt.Sprintf("ORCA error (exit %d): %s", exit, t) // ORCA writes input errors to stderr
			}
		}
		// a full scratch disk makes ORCA die in whatever module was writing: that is the
		// machine's fault, not the input's (ubuntu's 8 GB disk filled in the lab). Free
		// space is also judged by its lowest sample during the run: ORCA deletes its
		// temporary files when it dies, so the disk looks fine again by now (a DLPNO
		// failure in the BDE campaign was blamed on the task this way).
		j.mu.Lock()
		minFree := j.MinFree
		j.mu.Unlock()
		if f := diskFree(work); (f >= 0 && f < 256<<20) || (minFree > 0 && minFree < 512<<20) || outMentions(filepath.Join(work, stem+".out"), "No space left on device") {
			a.finishJob(j, "machine", "scratch disk full ("+msg+")", exit, smp)
			return
		}
		if j.Resumes > 0 && !j.Clean {
			// After a power cut the files ORCA restarts from (AutoStart .gbw, .tmp) can be
			// half written; ORCA then dies in GUESS/LEANSCF (seen in the lab). Blaming the
			// task would push a healthy input towards errors/, so rerun once from scratch.
			a.log.Warnf("job %s failed after resuming (%s); restarting it from scratch", j.Task.TaskID, msg)
			if err := keepOnly(work, j.taskFiles()); err != nil {
				a.finishJob(j, "machine", "cleaning the work directory: "+err.Error(), exit, smp)
				return
			}
			j.mu.Lock()
			j.Clean = true
			j.mu.Unlock()
			a.runJob(j)
			return
		}
		a.finishJob(j, "orca", msg, exit, smp)
	}
}

// buildResult hashes the files to return and signs the manifest (j.Result). It is also
// called again by upload when a listed file vanished or changed after signing (e.g. a
// .tmp removed while the disk was full), instead of retrying a broken upload forever.
func (a *Agent) buildResult(j *Job, failure string, exit int) {
	pol := a.policy()
	work := j.workDir()
	// select files according to the server policy (default: everything)
	var names, paths []string
	entries, _ := os.ReadDir(work)
	for _, e := range entries {
		if !e.Type().IsRegular() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		n := e.Name()
		if !returned(pol, n) {
			continue
		}
		if n == "matriline.stderr" {
			if info, err := e.Info(); err == nil && info.Size() == 0 {
				continue
			}
		}
		names = append(names, n)
		paths = append(paths, filepath.Join(work, n))
	}
	metas, err := xfer.Meta(names, paths)
	if err != nil {
		a.log.Errorf("hashing results of %s: %v", j.Task.TaskID, err)
	}
	inst := a.orcaFor(j.Task.OrcaVersion)
	m := &manifest.Manifest{ServerID: a.serverID, TaskID: j.Task.TaskID, AttemptID: j.Task.AttemptID,
		InputName: j.Task.InputName, Files: metas, ExitCode: exit, Kind: j.Task.Kind,
		MaxcoreMB: j.Res.MaxcoreMB, Nprocs: j.Res.Nprocs,
		StartedUnix: j.Started.Unix(), EndedUnix: j.Ended.Unix()}
	for _, f := range j.Task.Files {
		if f.Name == j.Task.InputName {
			m.InputSHA256 = f.SHA256 // hash of the input as received (before normalization)
		}
	}
	if inst != nil {
		m.OrcaVersion, m.OrcaGit, m.OrcaTree = inst.version, inst.git, inst.fp.TreeHash
		if j.Res.Nprocs > 1 && a.mpi != nil {
			m.MPIVersion, m.MPITree = a.mpi.version, a.mpi.fp.TreeHash
		}
		j.mu.Lock()
		for exe, n := range j.Execs {
			if n == 0 {
				n = 1
			}
			sha, inTree := inst.fp.Lookup(exe)
			e := manifest.Exec{Path: exe, SHA256: sha, Count: n}
			if !inTree && m.MPITree != "" {
				if sha, ok := a.mpi.fp.Lookup(exe); ok {
					e.SHA256, e.MPI, inTree = sha, true, true
				}
			}
			if !inTree {
				e.System = true // the system shell ORCA uses for system(3)
				_, e.SHA256, _ = xfer.HashFile(exe)
			}
			m.Execs = append(m.Execs, e)
		}
		j.mu.Unlock()
	}
	signed, err := manifest.Sign(m, a.key)
	if err != nil {
		a.log.Errorf("signing manifest: %v", err)
	}
	j.mu.Lock()
	j.Result = &wire.Result{AttemptID: j.Task.AttemptID, TaskID: j.Task.TaskID, ExitCode: exit,
		Failure: failure, Manifest: signed, Files: metas}
	j.mu.Unlock()
	j.save()
}

// returned: whether the return policy sends file name back (the report always).
func returned(pol wire.Welcome, name string) bool {
	return name == "matriline.report" || (matchAny(pol.ReturnFiles, name) && !matchAny(pol.ReturnExclude, name))
}

// keepSet is what waits for the upload: the task's files and the result's (j.mu held).
func (j *Job) keepSet() map[string]bool {
	keep := j.taskFiles()
	if j.Result != nil {
		for _, f := range j.Result.Files {
			keep[f.Name] = true
		}
	}
	return keep
}

// taskFiles are the names of the files the server sent (input first).
func (j *Job) taskFiles() map[string]bool {
	m := map[string]bool{j.Task.InputName: true}
	for _, f := range j.Task.Files {
		m[f.Name] = true
	}
	return m
}

// keepOnly deletes everything in dir except the named files.
func keepOnly(dir string, keep map[string]bool) error {
	es, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, e := range es {
		if !keep[e.Name()] {
			if err := os.RemoveAll(filepath.Join(dir, e.Name())); err != nil {
				return err
			}
		}
	}
	return nil
}

// audit records every executable in the job's process tree and reports a violation when
// one is not part of the ORCA installation (D43 layer 3), or of the fingerprinted OpenMPI
// for a multi-core job.
func (a *Agent) audit(j *Job, inst *orcaInstall, pid int, self string) string {
	if build.NoSecurity {
		return ""
	}
	for _, p := range processTree(pid) {
		if p.exe == "" || p.exe == self {
			continue
		}
		exe := strings.TrimSuffix(p.exe, " (deleted)")
		_, ok := inst.fp.Lookup(exe)
		if !ok && a.mpi != nil && j.Task.Slots > 1 {
			_, ok = a.mpi.fp.Lookup(exe)
		}
		if !ok && isCrashReporter(exe) {
			// Windows Error Reporting starts WerFault.exe (a system program) when a process
			// crashes or misbehaves: not a foreign program run by the input; ORCA's own
			// result says whether the job failed
			if a.noteOnce(j, exe) {
				a.log.Warnf("job %s: Windows Error Reporting ran (%s): a program of the job may have crashed", j.Task.TaskID, exe)
			}
			continue
		}
		if !ok && isConsoleHost(exe) {
			// Windows starts its console host for a console program that has no console
			// (every job of a client started at boot, with nobody logged in; MS-MPI's
			// launcher for its process manager): part of Windows, not run by the input.
			// The programs it starts are still checked like any other.
			continue
		}
		if !ok && !a.isShell(exe) {
			return "program outside the ORCA installation was executed: " + exe
		}
		a.noteExec(j, p.pid, exe)
	}
	return ""
}

// machineStatus names Windows exit codes (NTSTATUS) that mean this computer could not run
// ORCA at all, so the input is not to blame: 0xC0000142 when a program cannot initialize
// (seen when the client had lost its console), the out-of-memory ones. Unix exit codes
// never reach these values.
func machineStatus(exit int) string {
	switch uint32(exit) {
	case 0xC0000142:
		return "ORCA could not start (Windows STATUS_DLL_INIT_FAILED; no console?)"
	case 0xC0000017, 0xC000012D, 0xC000009A:
		return fmt.Sprintf("out of memory (Windows status 0x%X)", uint32(exit))
	}
	return ""
}

// tail is the end of s, at most n bytes.
func tail(s string, n int) string {
	if len(s) > n {
		return "..." + s[len(s)-n:]
	}
	return s
}

// stderrTail is the end (300 bytes) of the job's trimmed stderr; ok = the file was read
// and is not empty.
// helperFailed tells a sandbox helper that could not start ORCA from ORCA itself ending
// with the same exit code: the helper's messages start a line with "sandbox-exec:" on every
// system, and a program that could not run at all gets the shell's own line ("sh: 1: orca:
// not found", "/bin/sh: orca: cannot execute binary file"), while ORCA writes its own error
// ("ERROR: expect a '$', '!', ...") into its output (with the client's environment; to
// stderr otherwise). Nothing written anywhere: ORCA never ran. The whole stderr is read,
// line by line: MPI's noise after the helper's line, or an ORCA message that merely says
// "not found", must not decide it (code review).
func helperFailed(work, stem string) bool {
	b, err := os.ReadFile(filepath.Join(work, "matriline.stderr"))
	if err == nil && len(strings.TrimSpace(string(b))) > 0 {
		for _, ln := range strings.Split(string(b), "\n") {
			ln = strings.TrimSpace(ln)
			if strings.HasPrefix(ln, "sandbox-exec:") || shellCannotRun.MatchString(ln) {
				return true
			}
		}
		return false
	}
	st, err := os.Stat(filepath.Join(work, stem+".out"))
	return err != nil || st.Size() == 0
}

// shellCannotRun: a shell's own message for a program it could not run.
var shellCannotRun = regexp.MustCompile(`^[^\s:]+: (?:(?:line )?\d+: )?[^\s:]+: (?:not found|command not found|cannot execute.*|Permission denied|No such file or directory)$`)

func stderrTail(work string) (t string, ok bool) {
	b, err := os.ReadFile(filepath.Join(work, "matriline.stderr"))
	if err != nil || len(b) == 0 {
		return "", false
	}
	return tail(strings.TrimSpace(string(b)), 300), true
}

// isConsoleHost: Windows' console host, System32\conhost.exe.
func isConsoleHost(exe string) bool {
	return runtime.GOOS == "windows" && strings.EqualFold(filepath.Base(exe), "conhost.exe") &&
		strings.HasSuffix(strings.ToLower(filepath.Dir(exe)), `\system32`)
}

// isCrashReporter: Windows Error Reporting's programs in System32.
func isCrashReporter(exe string) bool {
	if runtime.GOOS != "windows" {
		return false
	}
	b := strings.ToLower(filepath.Base(exe))
	dir := strings.ToLower(filepath.Dir(exe))
	return (b == "werfault.exe" || b == "werfaultsecure.exe") && strings.HasSuffix(dir, `\system32`)
}

// seen reports whether key is new for job j, and marks it seen.
func (a *Agent) seen(j *Job, key string) bool {
	a.execMu.Lock()
	defer a.execMu.Unlock()
	if a.execSeen[j.Task.AttemptID] == nil {
		a.execSeen[j.Task.AttemptID] = map[string]bool{}
	}
	if a.execSeen[j.Task.AttemptID][key] {
		return false
	}
	a.execSeen[j.Task.AttemptID][key] = true
	return true
}

// noteOnce reports whether exe is new for job j (for one-time log lines).
func (a *Agent) noteOnce(j *Job, exe string) bool { return a.seen(j, "note:"+exe) }

func (a *Agent) isShell(exe string) bool {
	for _, s := range systemShells() {
		// Windows paths are case-insensitive (SystemRoot is often C:\WINDOWS while the
		// process image is reported as C:\Windows\System32\cmd.exe)
		if s == exe || (runtime.GOOS == "windows" && strings.EqualFold(s, exe)) {
			return true
		}
	}
	return false
}

// noteExec counts distinct (pid, exe) pairs.
func (a *Agent) noteExec(j *Job, pid int, exe string) {
	if a.seen(j, strconv.Itoa(pid)+exe) {
		j.mu.Lock()
		j.Execs[exe]++
		j.mu.Unlock()
	}
}

// takeSample records one sample of the machine and the job; it returns the bytes in the
// job's work directory.
func (a *Agent) takeSample(j *Job, s *sampler, pid int, prevBusy, prevTotal *uint64) int64 {
	if t, na := cpuTempC(); na == "" {
		s.temp = append(s.temp, t)
	} else {
		s.tempNA = na
	}
	if f, na := cpuFreqMHz(); na == "" {
		s.freq = append(s.freq, f)
	} else {
		s.freqNA = na
	}
	_, avail := memInfo()
	if avail > 0 {
		s.avail = append(s.avail, float64(avail))
	}
	if l := loadAvg1(); l >= 0 {
		s.load = append(s.load, l)
	}
	busy, total := cpuTimes()
	if total > *prevTotal {
		// share of the whole machine used by everything, minus Matriline's own jobs
		all := float64(busy-*prevBusy) / float64(total-*prevTotal) * 100
		own := float64(a.runningCount()) / float64(runtime.NumCPU()) * 100
		s.busy = append(s.busy, max(all-own, 0))
	}
	*prevBusy, *prevTotal = busy, total
	if pid > 0 {
		var rss int64
		for _, p := range processTree(pid) {
			rss += p.rssKB
		}
		if rss > s.rssPeakKB {
			s.rssPeakKB = rss
		}
		s.rss = append(s.rss, float64(rss)/1024)
	}
	used := dirBytes(j.workDir())
	s.scratch = append(s.scratch, float64(used)/(1<<20))
	return used
}

func dirBytes(dir string) int64 {
	var n int64
	filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err == nil && d.Type().IsRegular() {
			if i, err := d.Info(); err == nil {
				n += i.Size()
			}
		}
		return nil
	})
	return n
}

// finishJob writes the report and the signed manifest and queues the result for upload.
func (a *Agent) finishJob(j *Job, failure, text string, exit int, s *sampler) {
	pol := a.policy()
	j.mu.Lock()
	j.State, j.Failure, j.FailText, j.ExitCode, j.Ended = jsFinished, failure, text, exit, time.Now()
	j.mu.Unlock()
	work := j.workDir()
	os.MkdirAll(work, 0o700)
	if s == nil {
		s = &sampler{tempNA: "not_sampled", freqNA: "not_sampled"}
	}
	a.writeReport(j, s, pol)
	a.buildResult(j, failure, exit)
	j.mu.Lock()
	peak, class := j.DiskPeak, j.DiskClass
	keep := j.keepSet()
	j.mu.Unlock()
	a.notePeak(class, peak, failure == "machine" && strings.HasPrefix(text, "scratch disk full"))
	// free the scratch disk now: only the result (and the input) wait for the upload
	if err := keepOnly(work, keep); err != nil {
		a.log.Warnf("cleaning %s: %v", work, err)
	}
	a.execMu.Lock()
	delete(a.execSeen, j.Task.AttemptID)
	a.execMu.Unlock()
	if failure == "" {
		a.log.Infof("job %s finished OK in %s", j.Task.TaskID, j.Ended.Sub(j.Started).Round(time.Second))
	} else {
		a.log.Warnf("job %s finished with failure %s: %s", j.Task.TaskID, failure, text)
	}
	a.events <- event{kind: evJobDone, job: j}
}

func matchAny(patterns []string, name string) bool {
	for _, p := range patterns {
		if p == "*" {
			return true
		}
		if ok, _ := path.Match(p, name); ok {
			return true
		}
	}
	return false
}

// writeReport produces matriline.report (D17): only data not already in the ORCA output.
func (a *Agent) writeReport(j *Job, s *sampler, pol wire.Welcome) {
	r := report.New(pol.MetadataFields)
	pc := pol.Percentiles
	r.Set("job.task", j.Task.TaskID)
	r.Set("job.attempt", j.Task.AttemptID)
	r.Set("job.kind", j.Task.Kind)
	if j.Failure != "" {
		r.Set("job.failure", j.Failure)
		r.Set("job.failure_text", j.FailText)
	}
	r.SetInt("job.exit_code", int64(j.ExitCode))
	r.SetInt("job.power_loss_resumes", int64(j.Resumes))
	r.Set("time.received", j.Received.UTC().Format(time.RFC3339))
	r.Set("time.start", j.Started.UTC().Format(time.RFC3339Nano))
	r.Set("time.end", j.Ended.UTC().Format(time.RFC3339Nano))
	wall := j.Ended.Sub(j.Started).Seconds()
	r.SetFloat("time.wall_s", wall, 3)
	r.SetFloat("time.queue_wait_s", j.Started.Sub(j.Received).Seconds(), 3)
	if a.clockOffsetMs != nil {
		r.SetFloat("time.clock_offset_ms", *a.clockOffsetMs, 1)
	} else {
		r.NA("time.clock_offset_ms", "unknown")
	}
	if cpu, maxrss, ok := lastRusage(j); ok {
		r.SetFloat("time.cpu_s", cpu, 3)
		if wall > 0 {
			r.SetFloat("time.cpu_efficiency", cpu/wall, 3)
		}
		r.SetFloat("mem.maxrss_largest_process_mb", float64(maxrss)/1024, 1)
	} else {
		r.NA("time.cpu_s", "unavailable")
	}
	if h, err := os.Hostname(); err == nil {
		r.Set("host.name", h)
	}
	if pw := readPower(powerSupplyDirVar); pw.State != "" {
		r.Set("host.power", pw.State)
		if pw.Percent >= 0 {
			r.SetFloat("host.battery_percent", pw.Percent, 0)
		} else {
			r.NA("host.battery_percent", "no_battery")
		}
	}
	r.Set("host.os", osName())
	r.Set("host.kernel", kernelVersion())
	r.Set("host.arch", runtime.GOOS+"/"+runtime.GOARCH)
	r.Set("host.cpu_model", cpuModel())
	r.SetInt("host.logical_cpus", int64(runtime.NumCPU()))
	tot, _ := memInfo()
	if tot > 0 {
		r.SetInt("host.ram_total_mb", tot)
	} else {
		r.NA("host.ram_total_mb", "unsupported_os")
	}
	r.SetInt("host.slots", int64(a.slots))
	r.SetInt("host.mem_per_slot_mb", int64(a.memPerSlotMB))
	r.Set("host.agent", agentVersion)
	r.Set("host.sandbox", a.sandboxDesc)
	r.SetInt("host.physical_cores", int64(physicalCores()))
	// the effective client configuration (defaults included): with the host data it
	// explains the job's performance, e.g. for the learned scheduler
	cv := reflect.ValueOf(*a.cfg)
	for i := 0; i < cv.NumField(); i++ {
		if f := cv.Type().Field(i); f.IsExported() {
			r.Set("config."+strings.ToLower(f.Name), fmt.Sprint(cv.Field(i).Interface()))
		}
	}
	if j.OrcaDir != "" {
		r.Set("orca.dir", j.OrcaDir)
	}
	r.SetInt("orca.maxcore_mb", int64(j.Res.MaxcoreMB))
	r.SetInt("orca.nprocs", int64(j.Res.Nprocs))
	j.mu.Lock()
	r.SetInt("orca.programs_distinct", int64(len(j.Execs)))
	j.mu.Unlock()
	r.SetFloat("mem.rss_tree_peak_mb", float64(s.rssPeakKB)/1024, 1)
	r.Stats("mem.rss_tree_mb", s.rss, pc, 1, "not_sampled")
	r.Stats("mem.available_mb", s.avail, pc, 0, "unsupported_os")
	r.Stats("cpu.temp_c", s.temp, pc, 1, nz(s.tempNA, "not_sampled"))
	r.Stats("cpu.freq_mhz", s.freq, pc, 0, nz(s.freqNA, "not_sampled"))
	r.Stats("cpu.load1", s.load, pc, 2, "unsupported_os")
	r.Stats("cpu.others_busy_pct", s.busy, pc, 1, "unsupported_os")
	r.Stats("disk.scratch_mb", s.scratch, pc, 1, "not_sampled")
	if f := diskFree(j.workDir()); f >= 0 {
		r.SetInt("disk.free_mb_end", f>>20)
	}
	// connection quality during the job
	ev := a.netlog.during(j.Started, j.Ended)
	r.SetInt("net.disconnects", int64(len(ev.outages)))
	var totalOut, maxOut float64
	for _, d := range ev.outages {
		totalOut += d
		if d > maxOut {
			maxOut = d
		}
	}
	r.SetFloat("net.outage_total_s", totalOut, 1)
	r.SetFloat("net.outage_max_s", maxOut, 1)
	r.Stats("net.rtt_ms", ev.rtt, pc, 2, "no_samples")
	if ev.retransOK {
		r.SetInt("net.tcp_retransmits", int64(ev.retrans))
		if ev.segments > 0 {
			r.SetFloat("net.loss_estimate_pct", float64(ev.retrans)/float64(ev.segments)*100, 3)
		}
	} else {
		r.NA("net.tcp_retransmits", "no_samples")
	}
	if err := r.Write(filepath.Join(j.workDir(), "matriline.report")); err != nil {
		a.log.Errorf("writing report: %v", err)
	}
}

func nz(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// lastRusage returns, once, the rusage the runner captured from the process state.
func lastRusage(j *Job) (cpuSec float64, maxrssKB int64, ok bool) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if !j.rusageOK {
		return 0, 0, false
	}
	j.rusageOK = false
	return j.cpuSec, j.maxrssKB, true
}

// discardJob removes a cancelled job.
func (a *Agent) discardJob(j *Job, why string) {
	a.log.Infof("job %s cancelled: %s", j.Task.TaskID, why)
	os.RemoveAll(j.dir)
	a.events <- event{kind: evJobGone, job: j}
}

// acceptCheck decides whether a received task can run here (local policy, D27/D43).
func (a *Agent) acceptCheck(j *Job) error {
	t, dir := j.Task, j.workDir()
	if t.Slots > 1 {
		switch {
		case a.mpi == nil:
			return errors.New("multi-core task, but this client takes single-core tasks only (resources.max_cores_per_job, orca.mpi_path)")
		case t.Slots > a.cfg.MaxCoresJob || t.Slots > a.slots:
			return fmt.Errorf("task needs %d cores; this client lends at most %d per job", t.Slots, min(a.cfg.MaxCoresJob, a.slots))
		}
	}
	for _, f := range t.Files {
		if !orca.SafeFileName(f.Name) {
			return fmt.Errorf("file name %q contains characters that are unsafe for ORCA's shell calls", f.Name)
		}
	}
	if a.orcaFor(t.OrcaVersion) == nil {
		return fmt.Errorf("ORCA %s is not installed here", t.OrcaVersion)
	}
	if a.memTotalMB > 0 {
		if need, free := a.jobMemMB(j)*max(t.Slots, 1), a.freeMemMB(j); need > free {
			return fmt.Errorf("task needs %d MB and only %d MB of the memory pool are free now", need, free)
		}
	} else if t.MemMB > 0 && t.MemMB > a.memPerSlotMB {
		return fmt.Errorf("task needs %d MB but this client lends %d MB per core", t.MemMB, a.memPerSlotMB)
	}
	bundle := map[string]bool{}
	for _, f := range t.Files {
		bundle[f.Name] = true
	}
	in, err := os.ReadFile(filepath.Join(dir, t.InputName))
	if err != nil {
		return err
	}
	if v := orca.CheckInput(string(in), bundle); len(v) > 0 {
		sort.Strings(v)
		return errors.New("unsafe input: " + strings.Join(v, "; "))
	}
	// the server cannot know this host's scratch disk: refuse a job that would not fit
	// (it then goes to another host, and is not offered here again for 6 h)
	j.DiskClass = diskClass(string(in))
	if f := diskFree(a.cfg.ScratchDir); f >= 0 {
		if need := a.diskNeed(j.DiskClass); f-a.cfg.DiskReserve < need {
			return fmt.Errorf("not enough scratch disk for this %s job here (%d MB free, ~%d MB needed)", j.DiskClass, f>>20, need>>20)
		}
	}
	return nil
}

// outMentions reports whether the (possibly large) ORCA output contains s.
func outMentions(path, s string) bool {
	b, err := os.ReadFile(path)
	return err == nil && strings.Contains(string(b), s)
}
