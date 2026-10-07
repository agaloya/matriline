package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// A result hook gets the task, the verdict and the folder; a drained queue runs the done
// hook (and raises campaign_done).
func TestHooks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script")
	}
	dir := t.TempDir()
	if err := cmdInit(dir, "en"); err != nil {
		t.Fatal(err)
	}
	s, err := openServer(filepath.Join(dir, "server.conf"))
	if err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(t.TempDir(), "hook.log")
	script := filepath.Join(t.TempDir(), "hook.sh")
	os.WriteFile(script, []byte("#!/bin/sh\necho \"$* | $MATRILINE_EVENT $MATRILINE_VERDICT\" >> "+log+"\n"), 0o755)
	s.cfg.HookResult, s.cfg.HookDone = script, script
	s.wg.Add(1)
	go s.hookLoop()
	defer close(s.stop)
	s.hookResult("m/a.inp", "output/m/a")
	s.hookResult("~check/x/scf.inp", "output/m/a") // verification sub-tasks: no hook
	s.noteDrained(3)
	s.noteDrained(0)
	s.noteDrained(0) // only once per drain
	var b []byte
	for i := 0; i < 100; i++ {
		b, _ = os.ReadFile(log)
		if strings.Count(string(b), "\n") >= 2 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	time.Sleep(200 * time.Millisecond)
	b, _ = os.ReadFile(log)
	got := string(b)
	want := "result m/a.inp output " + filepath.Join(s.conf().Root, "output", "m", "a") + " | result output"
	if !strings.Contains(got, want) {
		t.Errorf("result hook:\n%s\nwant %q", got, want)
	}
	if strings.Contains(got, "~check") || strings.Count(got, "done all inputs are done") != 1 {
		t.Errorf("hooks run:\n%s", got)
	}
}

// --json gives parseable output for the commands scripts read.
func TestJSONOutput(t *testing.T) {
	dir := t.TempDir()
	if err := cmdInit(dir, "en"); err != nil {
		t.Fatal(err)
	}
	s, err := openServer(filepath.Join(dir, "server.conf"))
	if err != nil {
		t.Fatal(err)
	}
	s.reg.add(&ClientRec{ID: "k1", Name: "lab1", Status: stActive, Platform: "linux-amd64"})
	for _, cmd := range []string{"status", "live", "clients", "review", "events"} {
		out, err := s.admin(cmd, []string{"--json"})
		if err != nil {
			t.Fatalf("%s --json: %v", cmd, err)
		}
		var v map[string]any
		if err := json.Unmarshal([]byte(out), &v); err != nil {
			t.Fatalf("%s --json is not JSON: %v\n%s", cmd, err, out)
		}
	}
	out, _ := s.admin("clients", []string{"--json"})
	if !strings.Contains(out, `"platform": "linux-amd64"`) {
		t.Errorf("clients --json:\n%s", out)
	}
	if _, err := s.admin("verify", []string{"--json"}); err == nil {
		t.Error("--json accepted for a command without it")
	}
}
