package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// clients disable keeps the client's key unless --wipe; enable undoes only the first.
func TestDisableEnable(t *testing.T) {
	dir := t.TempDir()
	if err := cmdInit(dir, "en"); err != nil {
		t.Fatal(err)
	}
	s, err := openServer(filepath.Join(dir, "server.conf"))
	if err != nil {
		t.Fatal(err)
	}
	s.reg.add(&ClientRec{ID: "k1", Name: "lab1", Status: stActive})
	s.reg.add(&ClientRec{ID: "k2", Name: "lab2", Status: stActive})
	out, err := s.cmdClients([]string{"disable", "lab1", "project", "ended"})
	if err != nil || !strings.Contains(out, "next connection") {
		t.Fatalf("disable: %q %v", out, err)
	}
	if txt := disabledText(s.reg.get("k1").Note); !strings.Contains(txt, "(project ended)") || strings.Contains(txt, "credential withdrawn") || !strings.Contains(txt, "key and the credential stay") {
		t.Errorf("disable without --wipe: %q", txt)
	}
	if _, err := s.cmdClients([]string{"enable", "lab1"}); err != nil || s.reg.get("k1").Status != stActive {
		t.Fatalf("enable: %v, status %s", err, s.reg.get("k1").Status)
	}
	if _, err := s.cmdClients([]string{"enable", "lab1"}); err == nil {
		t.Error("enable of an active client did not fail")
	}
	if _, err := s.cmdClients([]string{"disable", "lab2", "--wipe"}); err != nil {
		t.Fatal(err)
	}
	if txt := disabledText(s.reg.get("k2").Note); !strings.Contains(txt, "(credential withdrawn)") || !strings.HasPrefix(txt, "disabled by the server admin") {
		t.Errorf("disable --wipe: %q", txt) // the client looks for both
	}
	if _, err := s.cmdClients([]string{"enable", "lab2"}); err == nil || !strings.Contains(err.Error(), "new credential") {
		t.Errorf("enable of a wiped client: %v", err)
	}
}

// pause and cancel move inputs out of input/ themselves: the scan must not report them
// as "left input/ outside Matriline" (found by the command matrix).
func TestPauseNoFalseRemovalEvent(t *testing.T) {
	dir := t.TempDir()
	if err := cmdInit(dir, "en"); err != nil {
		t.Fatal(err)
	}
	s, err := openServer(filepath.Join(dir, "server.conf"))
	if err != nil {
		t.Fatal(err)
	}
	s.cfg.SettleTime = 0
	root := s.conf().Root
	os.MkdirAll(filepath.Join(root, "input", "m"), 0o755)
	os.WriteFile(filepath.Join(root, "input", "m", "a.inp"), []byte("! HF\n"), 0o644)
	os.WriteFile(filepath.Join(root, "input", "m", "b.inp"), []byte("! HF\n"), 0o644)
	s.rescan()
	if _, err := s.cmdMove("input/m/a.inp", dInput, dPaused, "pause"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.cmdMove("input/m/b.inp", dInput, dCancelled, "cancel"); err != nil {
		t.Fatal(err)
	}
	s.rescan()
	b, _ := os.ReadFile(filepath.Join(root, "state", "events.log"))
	if strings.Contains(string(b), "outside Matriline") {
		t.Errorf("false removal event:\n%s", b)
	}
}

func TestClientRename(t *testing.T) {
	dir := t.TempDir()
	if err := cmdInit(dir, "en"); err != nil {
		t.Fatal(err)
	}
	s, err := openServer(filepath.Join(dir, "server.conf"))
	if err != nil {
		t.Fatal(err)
	}
	s.reg.add(&ClientRec{ID: "k1", Name: "lab1", Status: stActive})
	s.reg.add(&ClientRec{ID: "k2", Name: "lab2", Status: stActive})
	if _, err := s.cmdClients([]string{"rename", "lab1", "office", "left"}); err != nil {
		t.Fatal(err)
	}
	if n := s.reg.get("k1").Name; n != "office-left" {
		t.Errorf("name %q", n)
	}
	if _, err := s.cmdClients([]string{"rename", "lab2", "office-left"}); err == nil {
		t.Error("two clients with one name")
	}
	for _, bad := range []string{"k1", "ml1-x", "..", "."} {
		if _, err := s.cmdClients([]string{"rename", "lab2", bad}); err == nil {
			t.Errorf("rename to %q accepted", bad)
		}
	}
}
