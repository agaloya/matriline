package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/agaloya/matriline/common/manifest"
	"github.com/agaloya/matriline/common/xfer"
)

// redo: an accepted task goes back to input/; its result and completed/ input move to
// outdated/.
func TestRedo(t *testing.T) {
	dir := t.TempDir()
	if err := cmdInit(dir, "en"); err != nil {
		t.Fatal(err)
	}
	s, err := openServer(filepath.Join(dir, "server.conf"))
	if err != nil {
		t.Fatal(err)
	}
	root := s.conf().Root
	put := func(p string) {
		os.MkdirAll(filepath.Dir(filepath.Join(root, p)), 0o755)
		os.WriteFile(filepath.Join(root, p), []byte("! HF\n"), 0o644)
	}
	put("output/x/a/a.inp")
	put("output/x/a/a.out")
	put("completed/x/a.inp")
	if _, err := s.cmdRedo("output/x"); err == nil {
		t.Error("a directory without an input was accepted")
	}
	if _, err := s.cmdRedo("completed/x/a.inp"); err != nil {
		t.Fatal(err)
	}
	for p, want := range map[string]bool{"input/x/a.inp": true, "output/x/a": false, "completed/x/a.inp": false,
		"outdated/x/a/a.out": true, "outdated/x/a/completed-a.inp": true} {
		if fileExists(filepath.Join(root, p)) != want {
			t.Errorf("%s exists: %v, want %v", p, !want, want)
		}
	}
	if _, err := s.cmdRedo("completed/x/a.inp"); err == nil {
		t.Error("redo twice")
	}
}

// A client that reconnects without an attempt the server believes it runs (the server
// lost the result it had already acknowledged): the attempt is lost and its task requeued
// at once, instead of after tasks.task_timeout.
func TestReconcileForgottenAttempt(t *testing.T) {
	dir := t.TempDir()
	if err := cmdInit(dir, "en"); err != nil {
		t.Fatal(err)
	}
	s, err := openServer(filepath.Join(dir, "server.conf"))
	if err != nil {
		t.Fatal(err)
	}
	s.store.Tasks["x/a.inp"] = &TaskState{ID: "x/a.inp", Attempts: 1}
	s.store.Attempts["aaaaaaaaaaaaaaaaaaaaaaaa"] = &Attempt{ID: "aaaaaaaaaaaaaaaaaaaaaaaa", TaskID: "x/a.inp", ClientID: "c1", Started: time.Now().Add(-time.Minute)}
	s.store.Attempts["bbbbbbbbbbbbbbbbbbbbbbbb"] = &Attempt{ID: "bbbbbbbbbbbbbbbbbbbbbbbb", TaskID: "x/a.inp", ClientID: "c2", Started: time.Now().Add(-time.Minute)}
	ss := &Session{s: s, id: "c1", name: "one"}
	ss.reconcile(nil)
	if !s.store.Attempts["aaaaaaaaaaaaaaaaaaaaaaaa"].Lost {
		t.Error("the forgotten attempt is still running")
	}
	if s.store.Attempts["bbbbbbbbbbbbbbbbbbbbbbbb"].Lost {
		t.Error("another client's attempt was touched")
	}
}

// A check whose files are gone was finished before an unclean stop: it is dropped, with
// its sub-task, and its result is left alone.
func TestFinishedCheckDropped(t *testing.T) {
	dir := t.TempDir()
	if err := cmdInit(dir, "en"); err != nil {
		t.Fatal(err)
	}
	s, err := openServer(filepath.Join(dir, "server.conf"))
	if err != nil {
		t.Fatal(err)
	}
	root := s.conf().Root
	os.MkdirAll(filepath.Join(root, "output", "x", "a"), 0o755)
	if s.store.Checks == nil {
		s.store.Checks = map[string]*Check{}
	}
	sub := "~check/cccccccc/grad.inp"
	s.store.Checks["cccccccc"] = &Check{ID: "cccccccc", TaskID: "x/a.inp", ResultRel: "output/x/a", Client: "c1",
		Subtasks: map[string]string{sub: "grad"}, Created: time.Now()}
	s.store.Tasks[sub] = &TaskState{ID: sub, Internal: true, CheckID: "cccccccc", Purpose: "grad"}
	s.expireChecks()
	if s.store.Checks["cccccccc"] != nil || s.store.Tasks[sub] != nil {
		t.Error("the finished check or its sub-task is still there")
	}
	if !fileExists(filepath.Join(root, "output", "x", "a")) {
		t.Error("the result was moved")
	}
}

// A canary source whose output changed (its result was replaced at the same path) gives
// no canary and leaves the pool: the stored energy no longer matches the geometry.
func TestCanarySourceChanged(t *testing.T) {
	dir := t.TempDir()
	if err := cmdInit(dir, "en"); err != nil {
		t.Fatal(err)
	}
	s, err := openServer(filepath.Join(dir, "server.conf"))
	if err != nil {
		t.Fatal(err)
	}
	root := s.conf().Root
	os.MkdirAll(filepath.Join(root, "input", "x"), 0o755)
	os.WriteFile(filepath.Join(root, "input", "x", "a.inp"), []byte("! HF def2-SVP\n* xyz 0 1\nH 0 0 0\nH 0 0 0.74\n*\n"), 0o644)
	os.MkdirAll(filepath.Join(root, "output", "x", "a"), 0o755)
	out := filepath.Join(root, "output", "x", "a", "a.out")
	os.WriteFile(out, []byte("first result\n"), 0o644)
	_, sha, _ := xfer.HashFile(out)
	src := CanarySource{TaskID: "x/a.inp", ResultRel: "output/x/a", SCFTotal: -1.1, OutSHA: sha}
	s.store.Canaries = []CanarySource{src}
	os.WriteFile(out, []byte("a later result at the same path\n"), 0o644)
	if err := s.createCanary("c1", src); err == nil || !strings.Contains(err.Error(), "changed") {
		t.Fatalf("canary from a changed source: %v", err)
	}
	if len(s.store.Canaries) != 0 {
		t.Error("the changed source is still in the pool")
	}
}

// A client quarantined again soon after an automatic release is not released by itself.
func TestNoSecondAutoRelease(t *testing.T) {
	dir := t.TempDir()
	if err := cmdInit(dir, "en"); err != nil {
		t.Fatal(err)
	}
	s, err := openServer(filepath.Join(dir, "server.conf"))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.reg.add(&ClientRec{ID: "c1", Name: "cheat", Status: stActive}); err != nil {
		t.Fatal(err)
	}
	// quarantine schedules its re-checks in a goroutine that sets RecheckPending and
	// RecheckRound = 1: wait for it, or it may overwrite the pending count set below (it
	// did on a slow machine: macOS amd64 under Rosetta)
	quarantine := func(why string) {
		s.quarantine("c1", why)
		for i := 0; s.reg.get("c1").RecheckRound != 1; i++ {
			if i > 500 {
				t.Fatal("the re-check round did not start")
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	release := func() {
		_ = s.reg.update("c1", func(c *ClientRec) { c.RecheckPending = 1 })
		s.recheckDone("c1", false)
	}
	quarantine("test")
	release()
	if c := s.reg.get("c1"); c.Status != stActive {
		t.Fatalf("first quarantine not released: %s", c.Status)
	}
	quarantine("test again")
	release()
	if c := s.reg.get("c1"); c.Status != stQuarantined {
		t.Errorf("released a second time: %s", c.Status)
	}
}

// Empty sub-directories go everywhere but in spool.keep_empty_dirs (input/ by default).
func TestPruneSpool(t *testing.T) {
	dir := t.TempDir()
	if err := cmdInit(dir, "en"); err != nil {
		t.Fatal(err)
	}
	s, err := openServer(filepath.Join(dir, "server.conf"))
	if err != nil {
		t.Fatal(err)
	}
	root := s.conf().Root
	for _, d := range []string{"input/batch/sub", "output/x/y", "weird/w", "outdated/o", "other-versions/6.1.0/output/z"} {
		os.MkdirAll(filepath.Join(root, d), 0o755)
	}
	os.MkdirAll(filepath.Join(root, "output", "full"), 0o755)
	os.WriteFile(filepath.Join(root, "output", "full", "a.out"), []byte("x"), 0o644)
	s.pruneSpool()
	for d, want := range map[string]bool{"input/batch/sub": true, "output/x": false, "weird/w": false,
		"outdated/o": false, "other-versions/6.1.0": false, "output/full/a.out": true, "output": true} {
		if fileExists(filepath.Join(root, d)) != want {
			t.Errorf("%s exists: %v, want %v", d, !want, want)
		}
	}
}

// A name taken by something Stat cannot follow (a file where a directory belongs) does
// not hang: freeName returns and the move reports the error.
func TestFreeNameDoesNotHang(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "output"), 0o755)
	os.WriteFile(filepath.Join(root, "output", "a"), []byte("x"), 0o644) // a file, not a directory
	done := make(chan string, 1)
	go func() { done <- freeName(root, "output/a/mol") }()
	select {
	case got := <-done:
		if got != "output/a/mol" {
			t.Errorf("freeName = %q", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("freeName did not return")
	}
	os.MkdirAll(filepath.Join(root, "output", "b"), 0o755)
	if got := freeName(root, "output/b"); got != "output/b.2" {
		t.Errorf("taken name: %q", got)
	}
}

// A re-adopted attempt (the server lost track of it) is not timed against its
// re-adoption: an honest 3-hour job must not look impossibly fast.
func TestTimingSkipsAdopted(t *testing.T) {
	dir := t.TempDir()
	if err := cmdInit(dir, "en"); err != nil {
		t.Fatal(err)
	}
	s, err := openServer(filepath.Join(dir, "server.conf"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	m := &manifest.Manifest{InputName: "a.inp", StartedUnix: now.Add(-3 * time.Hour).Unix(), EndedUnix: now.Unix()}
	for _, adopted := range []bool{false, true} {
		s.store.Attempts["aaaaaaaaaaaaaaaaaaaaaaaa"] = &Attempt{ID: "aaaaaaaaaaaaaaaaaaaaaaaa", Started: now.Add(-time.Minute), Adopted: adopted}
		v := &Verdict{OK: true}
		s.checkTiming(v, m, t.TempDir(), "aaaaaaaaaaaaaaaaaaaaaaaa")
		if v.OK != adopted {
			t.Errorf("adopted=%v: timing verdict OK=%v (%v)", adopted, v.OK, v.Reasons)
		}
	}
}

// Revoking one client drops only its attempts: another client's run of the same task
// goes on. A quarantined client keeps its running attempts.
func TestRevokeKeepsOthersAttempts(t *testing.T) {
	dir := t.TempDir()
	if err := cmdInit(dir, "en"); err != nil {
		t.Fatal(err)
	}
	s, err := openServer(filepath.Join(dir, "server.conf"))
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"c1", "c2"} {
		if err := s.reg.add(&ClientRec{ID: id, Name: "n" + id, Status: stActive}); err != nil {
			t.Fatal(err)
		}
	}
	s.store.Tasks["x/a.inp"] = &TaskState{ID: "x/a.inp"}
	s.store.Attempts["a1aaaaaaaaaaaaaaaaaaaaaa"] = &Attempt{ID: "a1aaaaaaaaaaaaaaaaaaaaaa", TaskID: "x/a.inp", ClientID: "c1"}
	s.store.Attempts["a2aaaaaaaaaaaaaaaaaaaaaa"] = &Attempt{ID: "a2aaaaaaaaaaaaaaaaaaaaaa", TaskID: "x/a.inp", ClientID: "c2"}
	if _, err := s.setClientStatus("c2", stQuarantined, "test"); err != nil {
		t.Fatal(err)
	}
	if s.store.Attempts["a2aaaaaaaaaaaaaaaaaaaaaa"] == nil {
		t.Error("quarantine dropped the client's running attempt")
	}
	if _, err := s.setClientStatus("c1", stRevoked, "test"); err != nil {
		t.Fatal(err)
	}
	if s.store.Attempts["a1aaaaaaaaaaaaaaaaaaaaaa"] != nil {
		t.Error("the revoked client's attempt is still there")
	}
	if s.store.Attempts["a2aaaaaaaaaaaaaaaaaaaaaa"] == nil {
		t.Error("revoking c1 dropped c2's attempt of the same task")
	}
}

// A registered independent client counts for planning a second opinion even when it is
// offline (second_opinion_unavailable = weird): the check waits instead of condemning.
func TestIndependentKnown(t *testing.T) {
	dir := t.TempDir()
	if err := cmdInit(dir, "en"); err != nil {
		t.Fatal(err)
	}
	s, err := openServer(filepath.Join(dir, "server.conf"))
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []*ClientRec{{ID: "p", Name: "producer", Status: stActive}, {ID: "v", Name: "verifier", Status: stActive}} {
		s.reg.add(c)
	}
	if s.independentKnown([]string{"p", "v"}) {
		t.Error("no third client, yet one was found")
	}
	s.reg.add(&ClientRec{ID: "h", Name: "honest", Status: stActive})
	if !s.independentKnown([]string{"p", "v"}) {
		t.Error("a registered third client was not counted")
	}
	if s.independentHost([]string{"p", "v"}) {
		t.Error("an offline client counted as connected")
	}
}

// A machine failure keeps the task away from that client for 6 h only when another client
// could take it; with a single client, for the 5-minute cool-down.
func TestMachineFailureAvoidWithOneClient(t *testing.T) {
	dir := t.TempDir()
	if err := cmdInit(dir, "en"); err != nil {
		t.Fatal(err)
	}
	s, err := openServer(filepath.Join(dir, "server.conf"))
	if err != nil {
		t.Fatal(err)
	}
	s.reg.add(&ClientRec{ID: "c1", Name: "only", Status: stActive})
	s.store.Tasks["x/a.inp"] = &TaskState{ID: "x/a.inp"}
	s.dropAttempt("a1", "x/a.inp", "c1", "machine failure: test", true)
	if until := s.store.Tasks["x/a.inp"].AvoidOn["c1"]; time.Until(until) > 10*time.Minute {
		t.Fatalf("single client avoided until %s", until)
	}
	s.reg.add(&ClientRec{ID: "c2", Name: "other", Status: stActive})
	s.dropAttempt("a2", "x/a.inp", "c1", "machine failure: test", true)
	if until := s.store.Tasks["x/a.inp"].AvoidOn["c1"]; time.Until(until) < 5*time.Hour {
		t.Fatalf("with another client, avoided only until %s", until)
	}
}
