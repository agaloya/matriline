package main

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/agaloya/matriline/common/ident"
	"github.com/agaloya/matriline/common/logx"
	"github.com/agaloya/matriline/common/release"
	"github.com/agaloya/matriline/common/wire"
)

func TestSelfUpdate(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	old := release.TrustedKeys
	release.TrustedKeys = []string{ident.EncodeKey(pub)}
	t.Cleanup(func() { release.TrustedKeys = old })
	name := release.AssetName("client", runtime.GOOS, runtime.GOARCH)
	if runtime.GOOS == "windows" {
		t.Skip("shell scripts")
	}
	bin := []byte("#!/bin/sh\necho 'matriline-client/9.9.9 (commit test)'\n")
	h := sha256.Sum256(bin)
	body, _ := json.Marshal(release.Manifest{Version: "9.9.9", Commit: "abc", Expires: time.Now().Add(release.Validity).Format("2006-01-02"), Files: map[string]string{name: hex.EncodeToString(h[:])}})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/download/v9.9.9/"+name {
			w.Write(bin)
		} else {
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	exe := filepath.Join(t.TempDir(), "matriline-client")
	os.WriteFile(exe, []byte("client 0.1.0"), 0o755)
	selfExe = func() (string, error) { return exe, nil }
	t.Cleanup(func() { selfExe = release.Executable })
	lg, _ := logx.New("", "error")
	a := &Agent{cfg: &Config{UpdateAllow: true, UpdateURL: srv.URL}, log: lg, jobs: map[string]*Job{}}
	offer := func(sig []byte) string {
		b, _ := json.Marshal(wire.UpdateOffer{URL: "http://192.0.2.1/not-used", Manifest: body, Sig: sig})
		return string(b)
	}
	// a release signed by another key is ignored
	_, other, _ := ed25519.GenerateKey(nil)
	a.offerUpdate(offer(release.Sign(body, other)))
	if a.pendingUpdate() != nil {
		t.Fatal("accepted a release with a foreign signature")
	}
	a.offerUpdate(offer(release.Sign(body, priv)))
	if a.pendingUpdate() == nil {
		t.Fatal("signed release not taken")
	}
	if ok, why := a.accepting(); ok || !strings.Contains(why, "9.9.9") {
		t.Errorf("takes jobs while an update waits: %v %q", ok, why)
	}
	// downloaded in the background (from the client's own update_url), installed only
	// when no job is left
	a.jobs["x"] = &Job{}
	for i := 0; i < 500; i++ {
		if err := a.tryUpdate(context.Background()); err != nil {
			t.Fatalf("updated with a job left: %v", err)
		}
		if file, fails := st(a); file != "" || fails > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if file, fails := st(a); file == "" {
		t.Fatalf("not downloaded (%d failures)", fails)
	}
	// the same offer again keeps the download
	a.offerUpdate(offer(release.Sign(body, priv)))
	if file, _ := st(a); file == "" {
		t.Fatal("a repeated offer dropped the download")
	}
	delete(a.jobs, "x")
	err := a.tryUpdate(context.Background())
	var rs *restartErr
	if !errors.As(err, &rs) || rs.exe != exe {
		t.Fatalf("tryUpdate: %v", err)
	}
	if b, _ := os.ReadFile(exe); string(b) != string(bin) {
		t.Errorf("installed %q", b)
	}
}

// st reads the download state under its lock.
func st(a *Agent) (string, int) {
	a.updateMu.Lock()
	defer a.updateMu.Unlock()
	return a.update.file, a.update.fails
}

// A release that does not run here is never installed, never retried, and reported.
func TestSelfUpdateRefused(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell scripts")
	}
	pub, priv, _ := ed25519.GenerateKey(nil)
	old := release.TrustedKeys
	release.TrustedKeys = []string{ident.EncodeKey(pub)}
	t.Cleanup(func() { release.TrustedKeys = old })
	name := release.AssetName("client", runtime.GOOS, runtime.GOARCH)
	bin := []byte("#!/bin/sh\nexit 3\n") // crashes at once
	h := sha256.Sum256(bin)
	body, _ := json.Marshal(release.Manifest{Version: "9.9.9", Expires: time.Now().Add(release.Validity).Format("2006-01-02"), Files: map[string]string{name: hex.EncodeToString(h[:])}})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(bin) }))
	defer srv.Close()
	exe := filepath.Join(t.TempDir(), "matriline-client")
	os.WriteFile(exe, []byte("old"), 0o755)
	selfExe = func() (string, error) { return exe, nil }
	t.Cleanup(func() { selfExe = release.Executable })
	lg, _ := logx.New("", "error")
	a := &Agent{cfg: &Config{UpdateAllow: true, UpdateURL: srv.URL, StateDir: t.TempDir()}, log: lg, jobs: map[string]*Job{}}
	msg, _ := json.Marshal(wire.UpdateOffer{Manifest: body, Sig: release.Sign(body, priv)})
	a.offerUpdate(string(msg))
	for i := 0; i < 500 && a.pendingUpdate() != nil; i++ {
		if err := a.tryUpdate(context.Background()); err != nil {
			t.Fatalf("installed a program that does not run: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if b, _ := os.ReadFile(exe); string(b) != "old" {
		t.Fatalf("program replaced by %q", b)
	}
	n := a.refusal()
	if !strings.HasPrefix(n, "9.9.9: ") {
		t.Errorf("refusal not reported: %q", n)
	}
	a.refusalSent(n)
	a.offerUpdate(string(msg))
	if a.pendingUpdate() != nil {
		t.Error("a refused version was taken again")
	}
	if a.refusal() != n {
		t.Error("a re-offer of the refused version did not report the refusal again")
	}
}
