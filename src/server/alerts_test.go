package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// The alert program gets the event and the message as plain arguments: shell syntax in a
// message (it may contain a client's name or a file name) is never run.
func TestAlertCommand(t *testing.T) {
	s := testServer(t)
	dir := t.TempDir()
	out := filepath.Join(dir, "got")
	prog := filepath.Join(dir, "notify.sh")
	os.WriteFile(prog, []byte("#!/bin/sh\nprintf '%s|%s' \"$1\" \"$2\" > "+out+"\n"), 0o755)
	msg := "client $(touch " + filepath.Join(dir, "pwned") + "); `id` quarantined"
	if err := s.runAlertCommand(prog, "quarantine", msg); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(out)
	if string(b) != "quarantine|"+msg {
		t.Fatalf("got %q", b)
	}
	if _, err := os.Stat(filepath.Join(dir, "pwned")); err == nil {
		t.Fatal("the message was run by a shell")
	}
	if err := s.runAlertCommand(filepath.Join(dir, "missing"), "x", "y"); err == nil {
		t.Fatal("missing program not reported")
	}
}

func testServer(t *testing.T) *Server {
	dir := t.TempDir()
	if err := cmdInit(dir, "en"); err != nil {
		t.Fatal(err)
	}
	s, err := openServer(filepath.Join(dir, "server.conf"))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// A hook that leaves a program running in the background still ends at its limit, and
// the SMTP password variable does not reach it (code review).
func TestRunChildLimit(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell")
	}
	s := testServer(t)
	t.Setenv(s.conf().MailPassEnv, "topsecret")
	start := time.Now()
	out, err := s.runChild("/bin/sh", []string{"-c", "echo pw=$" + s.conf().MailPassEnv + "; sleep 30 & sleep 30"}, nil, t.TempDir(), time.Second)
	if took := time.Since(start); took > 15*time.Second {
		t.Fatalf("the hook held the server %s (err %v)", took, err)
	}
	if strings.Contains(string(out), "topsecret") {
		t.Error("the SMTP password variable reached the hook")
	}
}
