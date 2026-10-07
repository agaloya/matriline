package release

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
)

// TestKey makes a signing key trusted for the test and returns it.
func testKey(t *testing.T) ed25519.PrivateKey {
	pub, priv, _ := ed25519.GenerateKey(nil)
	old := TrustedKeys
	TrustedKeys = []string{ident.EncodeKey(pub)}
	t.Cleanup(func() { TrustedKeys = old })
	return priv
}

func TestNewer(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want bool
	}{{"0.1.1", "0.1.0", true}, {"0.1.10", "0.1.9", true}, {"1.0.0", "0.9.9", true}, {"0.1.0", "0.1.0", false},
		{"0.1.0", "0.2.0", false}, {"x", "0.1.0", false}, {"0.1", "0.0.1", false}} {
		if got := Newer(c.a, c.b); got != c.want {
			t.Errorf("Newer(%s, %s) = %v", c.a, c.b, got)
		}
	}
	if v := VersionOf("matriline-client/0.1.0"); v != "0.1.0" {
		t.Errorf("VersionOf: %q", v)
	}
}

func TestSignVerifyDownload(t *testing.T) {
	if _, err := Verify([]byte("{}"), nil); err == nil {
		t.Fatal("verified with no trusted key")
	}
	priv := testKey(t)
	bin := []byte("new program")
	sum := sha256.Sum256(bin)
	name := AssetName("client", "linux", "amd64")
	body, _ := json.Marshal(Manifest{Version: "0.2.0", Commit: "abc", Expires: time.Now().Add(Validity).Format("2006-01-02"), Files: map[string]string{name: hex.EncodeToString(sum[:]), "bad": "00"}})
	sig := Sign(body, priv)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/latest/download/" + ManifestName:
			w.Write(body)
		case "/latest/download/" + SigName:
			w.Write(sig)
		case "/download/v0.2.0/" + name, "/download/v0.2.0/bad":
			w.Write(bin)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	_, _, m, err := Fetch(context.Background(), srv.URL+"/")
	if err != nil || m.Version != "0.2.0" {
		t.Fatalf("Fetch: %v %+v", err, m)
	}
	// tampered manifest, or another key
	bad := append([]byte(nil), body...)
	bad[len(bad)-2] ^= 1
	if _, err := Verify(bad, sig); err == nil {
		t.Error("tampered release.json verified")
	}
	_, other, _ := ed25519.GenerateKey(nil)
	if _, err := Verify(body, Sign(body, other)); err == nil {
		t.Error("release signed by an unknown key verified")
	}
	dir := t.TempDir()
	if _, err := Download(context.Background(), srv.URL, m, "bad", dir); err == nil {
		t.Error("a binary whose hash differs was accepted")
	}
	p, err := Download(context.Background(), srv.URL, m, name, dir)
	if err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(dir, "matriline-client")
	os.WriteFile(exe, []byte("old program"), 0o755)
	if err := Install(p, exe); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(exe); string(b) != "new program" {
		t.Errorf("installed: %q", b)
	}
	if b, _ := os.ReadFile(exe + ".previous"); string(b) != "old program" {
		t.Errorf("previous: %q", b)
	}
	if es, _ := os.ReadDir(dir); len(es) != 2 { // program, .previous
		t.Errorf("left files behind: %d entries", len(es))
	}
	// expired release
	old, _ := json.Marshal(Manifest{Version: "0.2.0", Expires: "2020-01-01", Files: map[string]string{"a": "b"}})
	if _, err := Verify(old, Sign(old, priv)); err == nil || !strings.Contains(err.Error(), "expired") {
		t.Errorf("expired release: %v", err)
	}
	if URLOK("http://192.168.1.1/x") == nil || URLOK("https://github.com/x") != nil {
		t.Error("URLOK")
	}
}

// The new program must run and report the release's version before it is installed.
func TestPreflight(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell scripts")
	}
	dir := t.TempDir()
	pf := func(name, script, version string) error {
		p := filepath.Join(dir, name)
		os.WriteFile(p, []byte(script), 0o755)
		h := sha256.Sum256([]byte(script))
		return Preflight(context.Background(), p, &Manifest{Version: version, Files: map[string]string{"x": hex.EncodeToString(h[:])}}, "x")
	}
	if err := pf("good", "#!/bin/sh\necho 'matriline-client/9.9.9 (commit x)'\n", "9.9.9"); err != nil {
		t.Errorf("good program: %v", err)
	}
	if err := pf("good2", "#!/bin/sh\necho 'matriline-client/9.9.9 (commit x)'\n", "9.9.8"); !errors.Is(err, ErrDoesNotRun) {
		t.Errorf("wrong version: %v", err)
	}
	if err := pf("bad", "#!/bin/sh\necho boom >&2\nexit 3\n", "9.9.9"); !errors.Is(err, ErrDoesNotRun) || !strings.Contains(err.Error(), "boom") {
		t.Errorf("crashing program: %v", err)
	}
	if err := pf("junk", "not a program", "9.9.9"); !errors.Is(err, ErrDoesNotRun) {
		t.Errorf("not a program for this computer: %v", err)
	}
	preflightTimeout = time.Second
	if err := pf("slow", "#!/bin/sh\nsleep 120\n", "9.9.9"); err == nil || errors.Is(err, ErrDoesNotRun) {
		t.Errorf("a hung program counted as 'does not run': %v", err)
	}
	preflightTimeout = 60 * time.Second
	// a file that changed after the download, or vanished: not a verdict on the release
	h := sha256.Sum256([]byte("x"))
	if err := Preflight(context.Background(), filepath.Join(dir, "gone"), &Manifest{Version: "9.9.9", Files: map[string]string{"x": hex.EncodeToString(h[:])}}, "x"); err == nil || errors.Is(err, ErrDoesNotRun) {
		t.Errorf("missing file: %v", err)
	}
	if _, ok := parseVersion("v0.2.0"); ok {
		t.Error("v0.2.0 accepted")
	}
	if _, ok := parseVersion("0.02.0"); ok {
		t.Error("leading zero accepted")
	}
	// install twice: the program before is always .previous, never a moment without one
	exe := filepath.Join(dir, "prog")
	os.WriteFile(exe, []byte("v1"), 0o755)
	for _, v := range []string{"v2", "v3"} {
		n := filepath.Join(dir, "n"+v)
		os.WriteFile(n, []byte(v), 0o755)
		if err := Install(n, exe); err != nil {
			t.Fatal(err)
		}
	}
	if b, _ := os.ReadFile(exe); string(b) != "v3" {
		t.Errorf("installed %q", b)
	}
	if b, _ := os.ReadFile(exe + ".previous"); string(b) != "v2" {
		t.Errorf("previous %q", b)
	}
	os.WriteFile(filepath.Join(dir, ".update-123"), nil, 0o600)
	os.WriteFile(filepath.Join(dir, ".update-new"), nil, 0o600) // another client's download in progress
	old := time.Now().Add(-25 * time.Hour)
	os.Chtimes(filepath.Join(dir, ".update-123"), old, old)
	CleanDownloads(exe)
	if !fileExists(filepath.Join(dir, ".update-new")) {
		t.Error("a recent download was removed")
	}
	if fileExists(filepath.Join(dir, ".update-123")) {
		t.Error("stale download left")
	}
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}
