package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/agaloya/matriline/common/manifest"
	"github.com/agaloya/matriline/common/wire"
)

func TestOneResultPerTask(t *testing.T) {
	dir := t.TempDir()
	if err := cmdInit(dir, "en"); err != nil {
		t.Fatal(err)
	}
	s, err := openServer(filepath.Join(dir, "server.conf"))
	if err != nil {
		t.Fatal(err)
	}
	root := s.conf().Root
	put := func(rel, body string) {
		os.MkdirAll(filepath.Dir(filepath.Join(root, rel)), 0o755)
		os.WriteFile(filepath.Join(root, rel), []byte(body), 0o644)
	}
	exists := func(rel string) bool { return fileExists(filepath.Join(root, rel)) }
	// two weird results of x/a.inp, one of them still read by a check; another task a2
	for _, d := range []string{"weird/x/a", "weird/x/a.2", "weird/x/a.3"} {
		put(d+"/a.inp", "! HF\n")
		put(d+"/a.out", d)
	}
	put("weird/x/a2/a2.inp", "! HF\n") // another task whose name starts the same way
	if s.store.Checks == nil {
		s.store.Checks = map[string]*Check{}
	}
	s.store.Checks["busy"] = &Check{ID: "busy", TaskID: "x/a.inp", ResultRel: "weird/x/a.3"}
	s.retireWeird("x/a.inp")
	for rel, want := range map[string]bool{"weird/x/a": false, "weird/x/a.2": false, "outdated/x/a/a.out": true,
		"outdated/x/a.2/a.out": true, "weird/x/a.3": true, "weird/x/a2/a2.inp": true} {
		if exists(rel) != want {
			t.Errorf("after retireWeird %s exists: %v, want %v", rel, !want, want)
		}
	}
	// accepting the weird one by hand: the result in output/ makes room
	delete(s.store.Checks, "busy")
	put("output/x/a/a.inp", "! HF\n")
	put("output/x/a/a.out", "accepted before")
	if _, err := s.cmdAccept("weird/x/a.3"); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(root, "output/x/a/a.out")); string(b) != "weird/x/a.3" {
		t.Errorf("output/x/a holds %q, not the accepted weird result", b)
	}
	if !exists("outdated/x/a.3/a.out") && !exists("outdated/x/a.4/a.out") {
		entries, _ := os.ReadDir(filepath.Join(root, "outdated/x"))
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("the replaced output is not in outdated/: %s", strings.Join(names, " "))
	}
	if !exists("completed/x/a.inp") {
		t.Error("input not in completed/")
	}
	out, _ := verifySpool(root, s.key)
	if !strings.Contains(out, "RESULT: spool consistent") {
		t.Errorf("verify:\n%s", out)
	}
}

// Filing an accepted result must not wait for a lock it already holds (8e36e1f locked
// moveMu twice and the server stopped at its first accepted result).
func TestFileAcceptedResultNoDeadlock(t *testing.T) {
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
	os.WriteFile(filepath.Join(root, "input", "x", "a.inp"), []byte("! HF\n"), 0o644)
	os.MkdirAll(filepath.Join(root, "weird", "x", "a"), 0o755)
	os.WriteFile(filepath.Join(root, "weird", "x", "a", "a.inp"), []byte("! HF\n"), 0o644)
	s.store.Tasks["x/a.inp"] = &TaskState{ID: "x/a.inp", Attempts: 1}
	s.store.Attempts["aaaaaaaaaaaaaaaaaaaaaaaa"] = &Attempt{ID: "aaaaaaaaaaaaaaaaaaaaaaaa", TaskID: "x/a.inp", ClientID: "c1"}
	stage := t.TempDir()
	os.WriteFile(filepath.Join(stage, "a.out"), []byte("x"), 0o644)
	ss := &Session{s: s, id: "c1", name: "one"}
	done := make(chan error, 1)
	go func() {
		_, err := s.fileResult(ss, wire.Result{TaskID: "x/a.inp", AttemptID: "aaaaaaaaaaaaaaaaaaaaaaaa"}, &Verdict{OK: true}, stage)
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("fileResult hung (lock taken twice?)")
	}
	if !fileExists(filepath.Join(root, "output", "x", "a")) || !fileExists(filepath.Join(root, "outdated", "x", "a")) {
		t.Error("accepted result not in output/, or the old weird one not in outdated/")
	}
}

// Code review of D64: an accept never leaves two results of a task in output/, it
// drops the queued retry, and earlier weird results wait until the new result is final.
func TestAcceptRules(t *testing.T) {
	dir := t.TempDir()
	if err := cmdInit(dir, "en"); err != nil {
		t.Fatal(err)
	}
	s, err := openServer(filepath.Join(dir, "server.conf"))
	if err != nil {
		t.Fatal(err)
	}
	root := s.conf().Root
	put := func(rel, body string) {
		os.MkdirAll(filepath.Dir(filepath.Join(root, rel)), 0o755)
		os.WriteFile(filepath.Join(root, rel), []byte(body), 0o644)
	}
	exists := func(rel string) bool { return fileExists(filepath.Join(root, rel)) }
	if s.store.Checks == nil {
		s.store.Checks = map[string]*Check{}
	}
	// 1. the result in output/ is being checked: accept refuses and moves nothing
	put("output/x/a/a.inp", "! HF\n")
	put("output/x/a/a.out", "current")
	put("weird/x/a/a.inp", "! HF\n")
	put("weird/x/a/a.out", "old")
	s.store.Checks["c1"] = &Check{ID: "c1", TaskID: "x/a.inp", ResultRel: "output/x/a"}
	if _, err := s.cmdAccept("weird/x/a"); err == nil {
		t.Error("accept went ahead while a check reads output/x/a")
	}
	if !exists("weird/x/a/a.out") || exists("output/x/a.2") {
		t.Error("a refused accept moved something")
	}
	// 2. a new result with a check planned on it is not final: fileResult asks checked()
	delete(s.store.Checks, "c1")
	s.store.Checks["c2"] = &Check{ID: "c2", TaskID: "x/b.inp", ResultRel: "output/x/b"}
	if !s.checked("output/x/b") {
		t.Fatal("checked() misses a running check")
	}
	// 3. accept drops a queued retry and cancels its attempt
	put("weird/x/c/c.inp", "! HF\n")
	put("weird/x/c/c.out", "weird")
	put("input/x/c.inp", "! HF\n")
	s.store.Tasks["x/c.inp"] = &TaskState{ID: "x/c.inp", Attempts: 1}
	s.store.Attempts["cccccccccccccccccccccccc"] = &Attempt{ID: "cccccccccccccccccccccccc", TaskID: "x/c.inp", ClientID: "c1"}
	if _, err := s.cmdAccept("weird/x/c"); err != nil {
		t.Fatal(err)
	}
	if exists("input/x/c.inp") || s.store.Tasks["x/c.inp"] != nil || s.store.Attempts["cccccccccccccccccccccccc"] != nil {
		t.Error("the queued retry survived the accept")
	}
	if !exists("output/x/c/c.out") || exists("output/x/c.2") || !exists("completed/x/c.inp") {
		t.Error("accepted result not in output/x/c, a duplicate, or no input in completed/")
	}
	// 4. a weird task with a result in output/ already is not recomputed
	put("completed/x/d.inp", "! HF\n")
	put("output/x/d/d.inp", "! HF\n")
	s.retryWeird("x/d.inp", "c1", "weird/x/d")
	if exists("input/x/d.inp") {
		t.Error("retryWeird queued a task that has an accepted result")
	}
}

// Results are matched to their task by the signed manifest, also without the input copy.
func TestTaskResultDirsByManifest(t *testing.T) {
	dir := t.TempDir()
	if err := cmdInit(dir, "en"); err != nil {
		t.Fatal(err)
	}
	s, err := openServer(filepath.Join(dir, "server.conf"))
	if err != nil {
		t.Fatal(err)
	}
	root := s.conf().Root
	mk := func(rel, task string) {
		os.MkdirAll(filepath.Join(root, rel), 0o755)
		body, _ := json.Marshal(manifest.Manifest{TaskID: opaqueID(task)})
		raw, _ := json.Marshal(manifest.Signed{Body: body})
		os.WriteFile(filepath.Join(root, rel, manifestFile), raw, 0o644)
	}
	mk("weird/x/a", "x/a.inp")
	mk("weird/x/a.2", "x/other.inp") // a manifest of another task: not this one's
	got := s.taskResultDirs("weird", "x/a.inp")
	if len(got) != 1 || got[0] != "weird/x/a" {
		t.Errorf("taskResultDirs = %v, want [weird/x/a]", got)
	}
}

// A condemned result keeps the input copy the client ran (signed in its manifest); the
// original was written over it and verify reported the file modified (lab campaign).
func TestCondemnKeepsClientInput(t *testing.T) {
	dir := t.TempDir()
	if err := cmdInit(dir, "en"); err != nil {
		t.Fatal(err)
	}
	s, err := openServer(filepath.Join(dir, "server.conf"))
	if err != nil {
		t.Fatal(err)
	}
	root := s.conf().Root
	put := func(rel, body string) {
		os.MkdirAll(filepath.Dir(filepath.Join(root, rel)), 0o755)
		os.WriteFile(filepath.Join(root, rel), []byte(body), 0o644)
	}
	put("completed/x/a.inp", "! HF\n")
	put("output/x/a/a.inp", "%pal nprocs 1 end\n! HF\n")
	put("output/x/a/a.out", "out")
	s.ledgerDir("accept", "output/x/a", "test")
	s.condemn(&Check{ID: "c1", TaskID: "x/a.inp", ResultRel: "output/x/a"}, []string{"test"}, dWeird, false)
	if b, _ := os.ReadFile(filepath.Join(root, "weird/x/a/a.inp")); !strings.HasPrefix(string(b), "%pal") {
		t.Errorf("weird/x/a/a.inp = %q, not the client's copy", b)
	}
	if out, _ := verifySpool(root, s.key); !strings.Contains(out, "RESULT: spool consistent") {
		t.Errorf("verify:\n%s", out)
	}
	// accepting it again leaves the copy with the result and the original in completed/
	if _, err := s.cmdAccept("weird/x/a"); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(root, "completed/x/a.inp")); string(b) != "! HF\n" {
		t.Errorf("completed/ input replaced: %q", b)
	}
	if !fileExists(filepath.Join(root, "output/x/a/a.inp")) {
		t.Error("the result lost its input copy")
	}
}
