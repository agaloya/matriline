package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPause(t *testing.T) {
	dir := t.TempDir()
	if err := cmdInit(dir); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(dir, "client.conf")
	cfg, _, err := loadConfig(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, on := readPause(cfg); on {
		t.Fatal("paused before any pause")
	}
	if err := cmdPause(cfgPath, []string{"90m", "--now"}); err != nil {
		t.Fatal(err)
	}
	p, on := readPause(cfg)
	if !on || !p.Now || p.Until.Sub(time.Now()) < 89*time.Minute {
		t.Fatalf("pause 90m --now: %+v %v", p, on)
	}
	a := &Agent{cfg: cfg}
	if acc, why := a.accepting(); acc || why != p.text() {
		t.Fatalf("accepting while paused: %v %q", acc, why)
	}
	if err := cmdPause(cfgPath, []string{"soon"}); err == nil {
		t.Fatal("bad duration accepted")
	}
	if err := cmdResume(cfgPath); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(pausePath(cfg)); !os.IsNotExist(err) {
		t.Fatal("resume left the pause file")
	}
	// an expired pause is no pause
	os.WriteFile(pausePath(cfg), []byte(`{"until":"2020-01-01T00:00:00Z"}`), 0o600)
	if _, on := readPause(cfg); on {
		t.Fatal("expired pause in force")
	}
}

// Memory pool: a job known to need more than the default share reserves it; the rest of
// the pool is what new tasks may use.
func TestMemoryPoolAccounting(t *testing.T) {
	a := &Agent{memTotalMB: 8000, memPerSlotMB: 2000, jobs: map[string]*Job{}}
	small := &Job{State: jsRunning}
	big := &Job{State: jsRunning}
	big.Task.MemMB, big.Task.Slots = 3000, 2
	a.jobs["s"], a.jobs["b"] = small, big
	if a.jobMemMB(small) != 2000 || a.jobMemMB(big) != 3000 {
		t.Fatalf("shares %d %d", a.jobMemMB(small), a.jobMemMB(big))
	}
	if f := a.freeMemMB(nil); f != 8000-2000-6000 {
		t.Fatalf("free %d", f)
	}
	if f := a.freeMemMB(big); f != 6000 {
		t.Fatalf("free without big %d", f)
	}
}
