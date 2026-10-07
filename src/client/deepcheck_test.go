package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/agaloya/matriline/common/logx"
	"github.com/agaloya/matriline/common/orca"
)

// A file of ORCA changed while the client runs: the deep check notices it even though the
// cache would say "unchanged", and the client stops taking jobs.
func TestDeepCheckNoticesChange(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "orca")
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "orca"), []byte("binary v1"), 0o755)
	state := t.TempDir()
	fp, err := orca.FingerprintTree(dir, filepath.Join(state, "fpcache-orca.json"), false)
	if err != nil {
		t.Fatal(err)
	}
	lg, _ := logx.New("", "error")
	a := &Agent{cfg: &Config{DeepCheck: 50 * time.Millisecond, StateDir: state}, log: lg,
		installs: []*orcaInstall{{dir: dir, fp: fp}}}
	orcaChanged.Store("")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	defer func() { cancel(); <-done }() // it may still write its cache into state
	go func() { a.deepCheckLoop(ctx); close(done) }()
	time.Sleep(200 * time.Millisecond)
	if r := orcaChangedReason(); r != "" {
		t.Fatalf("unchanged ORCA reported: %s", r)
	}
	// same size and modification time: the cache alone would not notice
	st, _ := os.Stat(filepath.Join(dir, "orca"))
	os.WriteFile(filepath.Join(dir, "orca"), []byte("binary v2"), 0o755)
	os.Chtimes(filepath.Join(dir, "orca"), st.ModTime(), st.ModTime())
	time.Sleep(300 * time.Millisecond)
	if orcaChangedReason() == "" {
		t.Fatal("changed ORCA not noticed")
	}
	orcaChanged.Store("")
}
