package main

import (
	"encoding/json"
	"fmt"
	"github.com/agaloya/matriline/common/wire"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The attempt whose result was accepted is not told to stop, but it must leave the store:
// before, it stayed as a running attempt forever (status showed a 1-slot client running 9).
func TestAcceptedAttemptLeavesTheStore(t *testing.T) {
	dir := t.TempDir()
	if err := cmdInit(dir, "en"); err != nil {
		t.Fatal(err)
	}
	s, err := openServer(filepath.Join(dir, "server.conf"))
	if err != nil {
		t.Fatal(err)
	}
	s.store.mu.Lock()
	s.store.Attempts["won"] = &Attempt{ID: "won", TaskID: "a/x.inp", ClientID: "c1", Started: time.Now()}
	s.store.Attempts["dup"] = &Attempt{ID: "dup", TaskID: "a/x.inp", ClientID: "c2", Started: time.Now()}
	s.store.Attempts["other"] = &Attempt{ID: "other", TaskID: "a/y.inp", ClientID: "c1", Started: time.Now()}
	s.store.mu.Unlock()
	s.cancelAttemptsOf("a/x.inp", "completed by another host", "won")
	s.store.mu.Lock()
	defer s.store.mu.Unlock()
	if len(s.store.Attempts) != 1 || s.store.Attempts["other"] == nil {
		t.Fatalf("attempts left: %v", s.store.Attempts)
	}
}

func TestDiskRefusalAlertsWhenNoClientFits(t *testing.T) {
	dir := t.TempDir()
	if err := cmdInit(dir, "en"); err != nil {
		t.Fatal(err)
	}
	s, err := openServer(filepath.Join(dir, "server.conf"))
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"c1", "c2"} {
		if err := s.reg.add(&ClientRec{ID: id, Name: id, Status: stActive}); err != nil {
			t.Fatal(err)
		}
	}
	s.store.mu.Lock()
	s.store.Tasks["big/dlpno.inp"] = &TaskState{ID: "big/dlpno.inp"}
	s.store.mu.Unlock()
	s.noteDiskRefusal("big/dlpno.inp", "c1", "not enough scratch disk")
	if s.store.Tasks["big/dlpno.inp"].DiskAlerted {
		t.Fatal("alerted while another client may still fit it")
	}
	s.noteDiskRefusal("big/dlpno.inp", "c2", "not enough scratch disk")
	if !s.store.Tasks["big/dlpno.inp"].DiskAlerted {
		t.Fatal("no alert although every active client refused the task")
	}
}

func TestCheckStalled(t *testing.T) {
	dir := t.TempDir()
	if err := cmdInit(dir, "en"); err != nil {
		t.Fatal(err)
	}
	s, err := openServer(filepath.Join(dir, "server.conf"))
	if err != nil {
		t.Fatal(err)
	}
	far := time.Now().Add(24 * time.Hour)
	st := &TaskState{ID: "~check/c1/scf.inp", Internal: true, CheckID: "c1",
		AvoidOn: map[string]time.Time{"producer": far, "accuser": far}}
	ck := &Check{ID: "c1", Subtasks: map[string]string{st.ID: "scf"}}
	s.store.mu.Lock()
	defer s.store.mu.Unlock()
	s.store.Tasks[st.ID] = st
	if !s.checkStalled(ck, map[string]string{"producer": stActive, "accuser": stActive}) {
		t.Fatal("only excluded hosts online: the check should be stalled")
	}
	if s.checkStalled(ck, map[string]string{"producer": stActive, "other": stActive}) {
		t.Fatal("an eligible host is online: not stalled")
	}
	if !s.checkStalled(ck, map[string]string{"other": stQuarantined}) {
		t.Fatal("a quarantined host does not verify others: stalled")
	}
	s.store.Attempts["a1"] = &Attempt{ID: "a1", TaskID: st.ID, ClientID: "x"}
	if s.checkStalled(ck, nil) {
		t.Fatal("a running sub-task is not stalled")
	}
}

func TestSharedKeyDetection(t *testing.T) {
	dir := t.TempDir()
	if err := cmdInit(dir, "en"); err != nil {
		t.Fatal(err)
	}
	s, err := openServer(filepath.Join(dir, "server.conf"))
	if err != nil {
		t.Fatal(err)
	}
	s.reg.add(&ClientRec{ID: "k1", Name: "lab1", Status: stActive})
	for i := 0; i < 3; i++ {
		s.noteDisplaced("k1", "lab1", "198.51.100.1", "198.51.100.2")
	}
	if c := s.reg.get("k1"); len(c.Displaced) != 3 {
		t.Fatalf("displacements counted: %d", len(c.Displaced))
	}
	// alternating hostnames within the hour
	ss := &Session{s: s, id: "k1", name: "lab1"}
	for _, h := range []string{"alpha", "beta", "alpha"} {
		s.noteHostname(ss, h)
	}
	if c := s.reg.get("k1"); len(c.HostChanges) != 2 || c.Hostname != "alpha" {
		t.Fatalf("host changes %d, hostname %q", len(c.HostChanges), c.Hostname)
	}
}

func TestReputationMultiplier(t *testing.T) {
	cfg := &Config{Reputation: true, TrustedAfter: 50, TrustedMultiplier: 0.5, RecentFailureWindow: 720 * time.Hour, RecentFailureMultiplier: 2}
	now := time.Now()
	if m := reputationMultiplier(cfg, &ClientRec{Verified: 10}, now); m != 1 {
		t.Fatalf("new client: %v", m)
	}
	if m := reputationMultiplier(cfg, &ClientRec{Verified: 80}, now); m != 0.5 {
		t.Fatalf("clean veteran: %v", m)
	}
	if m := reputationMultiplier(cfg, &ClientRec{Verified: 80, LastFailure: now.Add(-24 * time.Hour)}, now); m != 2 {
		t.Fatalf("recent failure: %v", m)
	}
	if m := reputationMultiplier(cfg, &ClientRec{Verified: 80, LastFailure: now.Add(-40 * 24 * time.Hour)}, now); m != 0.5 {
		t.Fatalf("old failure forgiven: %v", m)
	}
	cfg.Reputation = false
	if m := reputationMultiplier(cfg, &ClientRec{Verified: 80}, now); m != 1 {
		t.Fatalf("disabled: %v", m)
	}
}

// Real ORCA 6.1.1 DLPNO-CCSD(T) failures are a memory requirement, not an input error: the
// task learns it needs more memory per core. "not enough memory to treat a single 4ext-batch;
// increase MaxCore by at least 100.5 MB" at %maxcore 1024, and "not enough memory for Triples
// evaluation: MemNeeded 2974.2 MB > MemAvailable 1096.9 MB" at %maxcore 1536.
func TestMemoryShortage(t *testing.T) {
	for _, tc := range []struct {
		file    string
		wantMB  int
		lacking int // a lending this low is not offered the task
	}{
		{"dlpno_maxcore_short.out", 1875, 1024}, // ceil((1024 + 100.5) * 1.25 / 0.75)
		{"dlpno_triples_short.out", 6942, 6144}, // ceil(1536 * 2974.2/1096.9 * 1.25 / 0.75)
	} {
		dir := t.TempDir()
		if err := cmdInit(dir, "en"); err != nil {
			t.Fatal(err)
		}
		s, err := openServer(filepath.Join(dir, "server.conf"))
		if err != nil {
			t.Fatal(err)
		}
		stage := t.TempDir()
		raw, err := os.ReadFile("testdata/" + tc.file)
		if err != nil {
			t.Fatal(err)
		}
		os.WriteFile(filepath.Join(stage, "ph.out"), raw, 0o644)
		task := &TaskState{ID: "bde/ph.inp", Attempts: 1}
		s.store.Tasks[task.ID] = task
		ss := &Session{s: s, id: "c1", name: "small", offer: wire.OfferRes{MemPerSlotMB: 1024}}
		if !s.memoryShortage(ss, wire.Result{TaskID: task.ID, AttemptID: "a1", Failure: "orca"}, stage, task) {
			t.Fatalf("%s: not recognized as a memory shortage", tc.file)
		}
		if task.MemMB != tc.wantMB || task.Attempts != 0 {
			t.Fatalf("%s: MemMB %d, attempts %d", tc.file, task.MemMB, task.Attempts)
		}
		s.store.mu.Lock()
		if got, _ := s.pickTask("c2", false, false, tc.lacking, 1, 0); got != nil {
			t.Fatalf("%s: offered to a client lending %d MB per core", tc.file, tc.lacking)
		}
		if got, _ := s.pickTask("c2", false, false, 8192, 1, 0); got == nil || got.ID != task.ID {
			t.Fatalf("%s: not offered to a client lending 8192 MB per core", tc.file)
		}
		s.store.mu.Unlock()
	}
}

// A client with a memory pool (resources.memory_total) gets a task that needs more memory
// per core than its default share when the pool has that much free, and not otherwise.
func TestMemoryPool(t *testing.T) {
	dir := t.TempDir()
	if err := cmdInit(dir, "en"); err != nil {
		t.Fatal(err)
	}
	s, err := openServer(filepath.Join(dir, "server.conf"))
	if err != nil {
		t.Fatal(err)
	}
	big := &TaskState{ID: "x/big.inp", MemMB: 3000}
	s.store.Tasks[big.ID] = big
	s.store.mu.Lock()
	defer s.store.mu.Unlock()
	if got, _ := s.pickTask("c1", false, false, 1024, 1, 0); got != nil {
		t.Fatal("3000 MB task given to a fixed 1024 MB/core client")
	}
	if got, _ := s.pickTask("c1", false, false, 1024, 1, 2000); got != nil {
		t.Fatal("3000 MB task given with 2000 MB free in the pool")
	}
	if got, _ := s.pickTask("c1", false, false, 1024, 1, 4000); got == nil {
		t.Fatal("3000 MB task not given with 4000 MB free in the pool")
	}
}

// An ORCA failure of an attempt that started from orbitals kept from another host's failed
// attempt does not count against the input (the orbitals may be poisoned), and the kept
// orbitals are not offered again.
func TestCheckpointFailureNotCounted(t *testing.T) {
	dir := t.TempDir()
	if err := cmdInit(dir, "en"); err != nil {
		t.Fatal(err)
	}
	s, err := openServer(filepath.Join(dir, "server.conf"))
	if err != nil {
		t.Fatal(err)
	}
	task := &TaskState{ID: "x/a.inp", Attempts: 2}
	s.store.Tasks[task.ID] = task
	s.store.Attempts["at1234567890abcd"] = &Attempt{ID: "at1234567890abcd", TaskID: task.ID, ClientID: "c2", FromCheckpoint: "c1"}
	stage := t.TempDir()
	os.WriteFile(filepath.Join(stage, anonStem("at1234567890abcd")+".out"), []byte("ORCA finished by error termination in SCF\n"), 0o644)
	ss := &Session{s: s, id: "c2", name: "honest"}
	f, err := s.fileResult(ss, wire.Result{TaskID: task.ID, AttemptID: "at1234567890abcd", Failure: "orca"}, &Verdict{OK: true}, stage)
	if err != nil {
		t.Fatal(err)
	}
	if len(task.OrcaFailOn) != 0 || task.Attempts != 1 || !strings.Contains(f.verdict, "kept orbitals") {
		t.Fatalf("counted: fails %v attempts %d verdict %q", task.OrcaFailOn, task.Attempts, f.verdict)
	}
	if _, still := s.store.Attempts["at1234567890abcd"]; still {
		t.Fatal("attempt not dropped")
	}
}

// tasks.max_job_time (user): a job stopped by the project's limit goes to errors/ with its
// orbitals and a note saying how to continue, and 'retry' continues from those orbitals.
func TestMaxJobTimeToErrorsAndRetry(t *testing.T) {
	dir := t.TempDir()
	if err := cmdInit(dir, "en"); err != nil {
		t.Fatal(err)
	}
	s, err := openServer(filepath.Join(dir, "server.conf"))
	if err != nil {
		t.Fatal(err)
	}
	if s.conf().MaxJobTime != 24*time.Hour {
		t.Errorf("default max_job_time %v, want 24h", s.conf().MaxJobTime)
	}
	in := filepath.Join(dir, "input", "x", "a.inp")
	os.MkdirAll(filepath.Dir(in), 0o750)
	os.WriteFile(in, []byte("! HF def2-SVP\n* xyz 0 1\nH 0 0 0\nH 0 0 0.74\n*\n"), 0o644)
	task := &TaskState{ID: "x/a.inp", Attempts: 1}
	s.store.Tasks[task.ID] = task
	// a trusted host (past probation, no failures): its orbitals may seed the retry
	s.reg.add(&ClientRec{ID: "c1", Name: "slowpc", Status: stActive, Results: s.conf().ProbationResults + 5})
	ss := &Session{s: s, id: "c1", name: "slowpc"}
	body, _ := json.Marshal(map[string]string{"client_id": "c1"})
	signed, _ := json.Marshal(map[string][]byte{"body": body})
	stageWith := func(at string) string {
		stage := t.TempDir()
		os.WriteFile(filepath.Join(stage, anonStem(at)+".out"), []byte("SCF ITERATIONS\n"), 0o644)
		os.WriteFile(filepath.Join(stage, anonStem(at)+".gbw"), []byte("orbitals so far"), 0o644)
		return stage
	}
	// a "time_limit" a minute after the start is not believed: a machine failure, requeued
	const early = "at0000000000early"
	s.store.Attempts[early] = &Attempt{ID: early, TaskID: task.ID, ClientID: "c1", Started: time.Now().Add(-time.Minute)}
	if f, err := s.fileResult(ss, wire.Result{TaskID: task.ID, AttemptID: early, Failure: "time_limit", Manifest: signed}, &Verdict{OK: true}, stageWith(early)); err != nil || f.verdict == "error" {
		t.Fatalf("an early time_limit was believed: %q %v", f.verdict, err)
	}
	const at = "at1234567890abcd"
	s.store.Attempts[at] = &Attempt{ID: at, TaskID: task.ID, ClientID: "c1", Started: time.Now().Add(-25 * time.Hour)}
	f, err := s.fileResult(ss, wire.Result{TaskID: task.ID, AttemptID: at, Failure: "time_limit", Manifest: signed}, &Verdict{OK: true}, stageWith(at))
	if err != nil {
		t.Fatal(err)
	}
	if f.verdict != "error" {
		t.Fatalf("verdict %q, want error", f.verdict)
	}
	errDir := filepath.Join(dir, "errors", "x", "a")
	if b, err := os.ReadFile(filepath.Join(errDir, "a.gbw")); err != nil || string(b) != "orbitals so far" {
		t.Fatalf("orbitals not kept in errors/: %v", err)
	}
	note, _ := os.ReadFile(filepath.Join(errDir, verdictFile))
	if !strings.Contains(string(note), "max_job_time") || !strings.Contains(string(note), "retry errors/x/a") {
		t.Errorf("the note does not say how to continue:\n%s", note)
	}
	os.Remove(in) // as after the task left input/
	msg, err := s.cmdRequeue("errors/x/a", dErrors)
	if err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(s.checkpointPath("x/a.inp")); err != nil || string(b) != "orbitals so far" {
		t.Errorf("retry did not keep the orbitals for the next host: %v (%s)", err, msg)
	}
	if tk := s.store.Tasks["x/a.inp"]; tk == nil || tk.CheckpointFrom != "c1" {
		t.Errorf("the orbitals' producer is not recorded: %+v", tk)
	}
	if !strings.Contains(msg, "orbitals") {
		t.Errorf("retry message: %s", msg)
	}
}

// An input that fails within minutes on two different computers goes to errors/, whatever
// they call the failure (clients older than the exit-126 fix said "machine").
func TestQuickFailOnTwoComputers(t *testing.T) {
	dir := t.TempDir()
	if err := cmdInit(dir, "en"); err != nil {
		t.Fatal(err)
	}
	s, err := openServer(filepath.Join(dir, "server.conf"))
	if err != nil {
		t.Fatal(err)
	}
	in := filepath.Join(dir, "input", "x", "bad.inp")
	os.MkdirAll(filepath.Dir(in), 0o750)
	os.WriteFile(in, []byte("# bad: C#C\n! HF\n* xyz 0 1\nH 0 0 0\nH 0 0 0.74\n*\n"), 0o644)
	task := &TaskState{ID: "x/bad.inp", Attempts: 1}
	s.store.Tasks[task.ID] = task
	for _, id := range []string{"c1", "c2"} {
		s.reg.add(&ClientRec{ID: id, Name: id, Status: stActive})
	}
	fail := func(client, at string, healthy bool) filed {
		s.store.Attempts[at] = &Attempt{ID: at, TaskID: task.ID, ClientID: client, Started: time.Now().Add(-5 * time.Second)}
		stage := t.TempDir()
		os.WriteFile(filepath.Join(stage, anonStem(at)+".out"), []byte("ERROR: expect a '$', '!'\n"), 0o644)
		f, err := s.fileResult(&Session{s: s, id: client, name: client, healthy: healthy}, wire.Result{TaskID: task.ID, AttemptID: at, Failure: "machine"}, &Verdict{OK: true}, stage)
		if err != nil {
			t.Fatal(err)
		}
		return f
	}
	// computers that have produced nothing since their last machine failure (a sandbox
	// that never starts) do not count, however many there are (code review)
	for i, c := range []string{"b1", "b2", "b3"} {
		s.reg.add(&ClientRec{ID: c, Name: c, Status: stActive})
		if f := fail(c, fmt.Sprintf("at0000000000010%d", i), false); f.verdict == "error" {
			t.Fatal("broken computers sent it to errors/")
		}
	}
	if f := fail("c1", "at00000000000001", true); f.verdict == "error" {
		t.Fatal("one computer was enough to send it to errors/")
	}
	if f := fail("c2", "at00000000000002", true); f.verdict != "error" {
		t.Fatalf("failed at once on two computers but not in errors/: %q", f.verdict)
	}
	if _, err := os.Stat(filepath.Join(dir, "errors", "x", "bad")); err != nil {
		t.Fatal("not in errors/x/bad")
	}
}
