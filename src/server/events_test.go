package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agaloya/matriline/common/ledger"
)

func TestTaskCheck(t *testing.T) {
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
	for _, id := range []string{"x/dup.inp", "x/done.inp", "x/gone.inp", "x/redo.inp", "x/weird.inp"} {
		s.ledger.Append(ledger.Entry{Kind: "queued", Task: id})
	}
	put("input/x/dup.inp")
	put("paused/x/dup.inp")
	put("completed/x/done.inp")
	put("completed/x/done.inp.2")
	put("output/x/done/done.inp")
	put("completed/x/redo.inp") // recompute pending: allowed
	put("input/x/redo.inp")
	s.ledger.Append(ledger.Entry{Kind: "requeue", Task: "x/redo.inp"})
	put("weird/x/weird/weird.inp") // weird on arrival: only the copy in weird/
	report, err := s.checkTasks(true)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(report, "\n")
	if len(report) != 2 || !strings.Contains(got, "duplicate: x/dup.inp is in input/, paused/") || !strings.Contains(got, "lost: x/gone.inp") {
		t.Fatalf("report:\n%s", got)
	}
	report, _ = s.checkTasks(true)
	if len(report) != 1 {
		t.Errorf("the lost task was reported again: %v", report)
	}
	ev, _ := os.ReadFile(filepath.Join(root, dState, "events.log"))
	if !strings.Contains(string(ev), "system task check: lost: x/gone.inp") {
		t.Errorf("events.log:\n%s", ev)
	}
	s.adminEvent("pause", []string{"input/x"}, "moved 1 file", nil)
	s.adminEvent("status", nil, "...", nil)
	ev, _ = os.ReadFile(filepath.Join(root, dState, "events.log"))
	if !strings.Contains(string(ev), "admin pause input/x: moved 1 file") || strings.Contains(string(ev), "admin status") {
		t.Errorf("admin events:\n%s", ev)
	}
}

func TestEventsCommand(t *testing.T) {
	dir := t.TempDir()
	if err := cmdInit(dir, "en"); err != nil {
		t.Fatal(err)
	}
	s, err := openServer(filepath.Join(dir, "server.conf"))
	if err != nil {
		t.Fatal(err)
	}
	if out, _ := s.admin("events", nil); out != "no events yet" {
		t.Errorf("empty log: %q", out)
	}
	for i := 0; i < 5; i++ {
		s.event("system", "line %d", i)
	}
	out, err := s.admin("events", []string{"2"})
	if err != nil || strings.Count(out, "\n") != 1 || !strings.HasSuffix(out, "line 4") {
		t.Errorf("events 2: %q %v", out, err)
	}
	if _, err := s.admin("events", []string{"x"}); err == nil {
		t.Error("a bad count was accepted")
	}
}
