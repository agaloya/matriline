package main

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/agaloya/matriline/common/ident"
	"github.com/agaloya/matriline/common/release"
)

// a signed release 9.9.9 served over HTTP, with a client for linux-amd64 and this server
func fakeRelease(t *testing.T) *httptest.Server {
	pub, priv, _ := ed25519.GenerateKey(nil)
	old := release.TrustedKeys
	release.TrustedKeys = []string{ident.EncodeKey(pub)}
	t.Cleanup(func() { release.TrustedKeys = old })
	bins := map[string][]byte{
		release.AssetName("client", "linux", "amd64"):             []byte(newServer("client")),
		release.AssetName("server", runtime.GOOS, runtime.GOARCH): []byte(newServer("server")),
	}
	m := release.Manifest{Version: "9.9.9", Commit: "abc", Date: "2026-10-06", Expires: time.Now().Add(release.Validity).Format("2006-01-02"), Files: map[string]string{}}
	for n, b := range bins {
		h := sha256.Sum256(b)
		m.Files[n] = hex.EncodeToString(h[:])
	}
	body, _ := json.Marshal(m)
	sig := release.Sign(body, priv)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		n := strings.TrimPrefix(p, "/download/v9.9.9/")
		switch {
		case p == "/latest/download/"+release.ManifestName:
			w.Write(body)
		case p == "/latest/download/"+release.SigName:
			w.Write(sig)
		case n != p && bins[n] != nil:
			w.Write(bins[n])
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// newServer is the "new program" of the fake release: a script that passes the preflight.
func newServer(prog string) string {
	return "#!/bin/sh\necho 'matriline-" + prog + "/9.9.9 (commit test)'\n"
}

func TestUpdateRollout(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell scripts")
	}
	srv := fakeRelease(t)
	dir := t.TempDir()
	if err := cmdInit(dir, "en"); err != nil {
		t.Fatal(err)
	}
	s, err := openServer(filepath.Join(dir, "server.conf"))
	if err != nil {
		t.Fatal(err)
	}
	s.cfg.UpdateURL = srv.URL
	exe := filepath.Join(t.TempDir(), "matriline-server")
	os.WriteFile(exe, []byte("server 0.1.0"), 0o755)
	selfExe = func() (string, error) { return exe, nil }
	t.Cleanup(func() { selfExe = release.Executable })
	now := time.Now()
	s.reg.add(&ClientRec{ID: "k1", Name: "ok", Status: stActive, LastSeen: now, Platform: "linux-amd64", CanUpdate: true})
	s.reg.add(&ClientRec{ID: "k2", Name: "old", Status: stActive, LastSeen: now})                                            // an old client
	s.reg.add(&ClientRec{ID: "k3", Name: "mac", Status: stActive, LastSeen: now, Platform: "darwin-arm64", CanUpdate: true}) // no binary
	s.reg.add(&ClientRec{ID: "k4", Name: "gone", Status: stDisabled, LastSeen: now})                                         // does not count

	// alert mode: told once, nothing installed
	out, err := s.checkUpdate(context.Background(), false)
	if err != nil || !strings.Contains(out, "9.9.9 is available") {
		t.Fatalf("check: %q %v", out, err)
	}
	if st := s.loadUpdateState(); st.Alerted != "9.9.9" || st.Rollout != "" {
		t.Fatalf("state after the alert: %+v", st)
	}
	// apply refuses while some clients cannot update, and says which
	_, err = s.cmdUpdate([]string{"apply"})
	if err == nil || !strings.Contains(err.Error(), "old (its version cannot update itself)") || !strings.Contains(err.Error(), "mac (no client for darwin-arm64") || strings.Contains(err.Error(), "gone") || strings.Contains(err.Error(), "newbie") || strings.Contains(err.Error(), "suspect") {
		t.Fatalf("apply with blockers: %v", err)
	}
	s.reg.update("k2", func(c *ClientRec) { c.Status = stRevoked })
	s.reg.update("k3", func(c *ClientRec) { c.LastSeen = now.Add(-60 * 24 * time.Hour) }) // gone for 2 months
	if out, err = s.cmdUpdate([]string{"apply"}); err != nil {
		t.Fatal(err)
	}
	st := s.loadUpdateState()
	if st.Rollout != "9.9.9" {
		t.Fatalf("no rollout: %q %+v", out, st)
	}
	// no client connected: after the grace time the server installs its own program
	st.Since = time.Now().Add(-5 * time.Minute)
	s.saveUpdateState(st)
	s.advanceRollout()
	if b, _ := os.ReadFile(exe); string(b) != newServer("server") || s.restartTo() != exe {
		t.Fatalf("server program %q, restart %q", b, s.restartTo())
	}
	if b, _ := os.ReadFile(exe + ".previous"); string(b) != "server 0.1.0" {
		t.Errorf("previous program %q", b)
	}
	if st := s.loadUpdateState(); st.Rollout != "" {
		t.Errorf("rollout not finished: %+v", st)
	}
}

// A release whose signature does not verify is never stored or offered.
func TestUpdateBadSignature(t *testing.T) {
	srv := fakeRelease(t)
	pub, _, _ := ed25519.GenerateKey(nil)
	release.TrustedKeys = []string{ident.EncodeKey(pub)} // another key: the release is not ours
	dir := t.TempDir()
	if err := cmdInit(dir, "en"); err != nil {
		t.Fatal(err)
	}
	s, err := openServer(filepath.Join(dir, "server.conf"))
	if err != nil {
		t.Fatal(err)
	}
	s.cfg.UpdateURL = srv.URL
	if _, err := s.checkUpdate(context.Background(), true); err == nil || !strings.Contains(err.Error(), "not signed by a trusted") {
		t.Fatalf("check with a foreign signature: %v", err)
	}
	if _, _, m := s.knownRelease(); m != nil {
		t.Error("an unverified release was stored")
	}
}

// update apply --force does not wait for clients; a server never installs an older or
// equal release (code review).
func TestUpdateForceAndNoDowngrade(t *testing.T) {
	srv := fakeRelease(t)
	dir := t.TempDir()
	if err := cmdInit(dir, "en"); err != nil {
		t.Fatal(err)
	}
	s, err := openServer(filepath.Join(dir, "server.conf"))
	if err != nil {
		t.Fatal(err)
	}
	s.cfg.UpdateURL = srv.URL
	exe := filepath.Join(t.TempDir(), "matriline-server")
	os.WriteFile(exe, []byte("server 0.1.0"), 0o755)
	selfExe = func() (string, error) { return exe, nil }
	t.Cleanup(func() { selfExe = release.Executable })
	s.reg.add(&ClientRec{ID: "k2", Name: "old", Status: stActive, LastSeen: time.Now()})
	if _, err := s.cmdUpdate([]string{"apply"}); err == nil {
		t.Fatal("apply went ahead with a client that cannot update")
	}
	if _, err := s.cmdUpdate([]string{"apply", "--force"}); err != nil {
		t.Fatal(err)
	}
	st := s.loadUpdateState()
	if !st.Force || st.Rollout != "9.9.9" {
		t.Fatalf("forced rollout: %+v", st)
	}
	// a release that is not newer than this server: the rollout ends, nothing installed
	st.Rollout = "0.0.1"
	s.saveUpdateState(st)
	s.advanceRollout()
	if b, _ := os.ReadFile(exe); string(b) != "server 0.1.0" || s.loadUpdateState().Rollout != "" {
		t.Errorf("installed %q, rollout %q", b, s.loadUpdateState().Rollout)
	}
	if err := release.URLOK("http://192.168.1.1/x"); err == nil {
		t.Error("plain http accepted")
	}
}

func TestCleanReported(t *testing.T) {
	tail := cleanTail("a\x1b]52;c;ZXZpbA==\x07b\n" + strings.Repeat("x", 500) + "\n1\n2\n3\n4\n5\n6\n7\n8")
	if strings.ContainsAny(tail, "\x1b\x07") || strings.Count(tail, "\n") != 7 {
		t.Errorf("tail not cleaned: %q", tail)
	}
	if got := cleanTail("\u202eNormal\u200b termination"); got != "?Normal? termination" {
		t.Errorf("bidi/zero-width not neutralized: %q", got)
	}
	if w := cleanWord("linux-amd64\x1b[2J"); w != "linux-amd642J" && strings.ContainsRune(w, 0x1b) {
		t.Errorf("cleanWord: %q", w)
	}
}

// A client on which the release does not run refuses it for itself only: the rollout
// waits, nobody else is affected, a refusal from a client never offered it counts for
// nothing, and --force goes on (third code review).
func TestUpdateRefused(t *testing.T) {
	srv := fakeRelease(t)
	dir := t.TempDir()
	if err := cmdInit(dir, "en"); err != nil {
		t.Fatal(err)
	}
	s, err := openServer(filepath.Join(dir, "server.conf"))
	if err != nil {
		t.Fatal(err)
	}
	s.cfg.UpdateURL = srv.URL
	if _, err := s.cmdUpdate([]string{"apply"}); err != nil {
		t.Fatal(err)
	}
	liar := &Session{s: s, id: "k9", name: "liar"} // never offered the release
	s.updateRefused(liar, "9.9.9: does not run")
	honest := &Session{s: s, id: "k1", name: "lab1"}
	honest.offered.Store("9.9.9")
	s.updateRefused(honest, "9.9.9: the new program does not run here: exec format error")
	st := s.loadUpdateState()
	if st.Rollout != "9.9.9" || st.Failed != "" {
		t.Fatalf("a client's refusal changed the release for everyone: %+v", st)
	}
	if _, ok := st.Refused["9.9.9"]["k9"]; ok {
		t.Error("a refusal from a client that was not offered the release was recorded")
	}
	if _, ok := st.Refused["9.9.9"]["k1"]; !ok {
		t.Error("refusal not recorded")
	}
	if out, _ := s.cmdUpdate([]string{"status"}); !strings.Contains(out, "does not run on client") {
		t.Errorf("status does not show the refusal:\n%s", out)
	}
	if _, err := s.cmdUpdate([]string{"apply", "--force"}); err != nil {
		t.Fatal(err)
	}
	if st := s.loadUpdateState(); len(st.Refused["9.9.9"]) != 0 || !st.Force {
		t.Errorf("--force did not clear the refusals: %+v", st)
	}
}
