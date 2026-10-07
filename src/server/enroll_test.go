package main

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/agaloya/matriline/common/ident"
	"github.com/agaloya/matriline/common/wire"
)

// A credential from "keys issue" works exactly once: the first device enrolls its own key
// under the issued name; a copy of the file used afterwards is refused; the enrolled device
// reconnects with its key alone.
func TestOneTimeCredential(t *testing.T) {
	dir := t.TempDir()
	if err := cmdInit(dir, "en"); err != nil {
		t.Fatal(err)
	}
	s, err := openServer(filepath.Join(dir, "server.conf"))
	if err != nil {
		t.Fatal(err)
	}
	s.loadBans()
	ln, err := net.Listen("tcp", "127.0.0.1:0") // loopback only
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go s.handleConn(c, true)
		}
	}()
	credPath := filepath.Join(t.TempDir(), "dev.cred")
	if _, err := s.cmdKeys([]string{"issue", "dev", credPath, ln.Addr().String()}); err != nil {
		t.Fatal(err)
	}
	cred, err := ident.ReadCredential(credPath)
	if err != nil {
		t.Fatal(err)
	}
	if cred.Key != nil || !strings.HasPrefix(cred.JoinToken, "mle-") {
		t.Fatalf("issued credential must hold a one-time token and no private key: %+v", cred)
	}
	connect := func(k *ident.Key, token string) (wire.Welcome, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		c, err := (&wire.Dialer{Key: k, ServerPub: cred.ServerPub}).Dial(ctx, cred.ServerAddress)
		if err != nil {
			return wire.Welcome{}, err
		}
		defer c.Close()
		if err := c.Send(wire.TAuth, wire.Auth{Agent: "test", JoinToken: token, Name: "whatever"}); err != nil {
			return wire.Welcome{}, err
		}
		var w wire.Welcome
		return w, c.Expect(wire.TWelcome, &w)
	}
	device, _ := ident.Generate()
	if w, err := connect(device, cred.JoinToken); err != nil || w.Status != "active" {
		t.Fatalf("first enrollment: %v (status %q)", err, w.Status)
	}
	if rec := s.reg.get(device.ID()); rec == nil || rec.Name != "dev" {
		t.Fatalf("device key not registered under the issued name: %+v", rec)
	}
	thief, _ := ident.Generate()
	if _, err := connect(thief, cred.JoinToken); err == nil || !strings.Contains(err.Error(), "already used") {
		t.Fatalf("a copy of the used credential was admitted (err %v)", err)
	}
	if s.reg.get(thief.ID()) != nil {
		t.Fatal("thief registered")
	}
	if w, err := connect(device, ""); err != nil || w.Status != "active" {
		t.Fatalf("enrolled device cannot reconnect with its own key: %v", err)
	}
	if out, err := s.cmdKeys([]string{"revoke", "dev"}); err != nil {
		t.Fatalf("revoke: %v %s", err, out)
	}
	if _, err := connect(device, ""); err == nil || !strings.Contains(err.Error(), "revoked") {
		t.Fatalf("revoked device admitted (err %v)", err)
	}

	// one ticket for two computers: each gets its own key and name, a third is refused
	multi := filepath.Join(t.TempDir(), "lab.cred")
	if _, err := s.cmdKeys([]string{"issue", "lab", multi, ln.Addr().String(), "--uses", "2"}); err != nil {
		t.Fatal(err)
	}
	mc, _ := ident.ReadCredential(multi)
	for i, want := range []string{"lab-1", "lab-2"} {
		k, _ := ident.Generate()
		if _, err := connect(k, mc.JoinToken); err != nil {
			t.Fatalf("computer %d: %v", i+1, err)
		}
		if rec := s.reg.get(k.ID()); rec == nil || rec.Name != want {
			t.Fatalf("computer %d registered as %+v, want %s", i+1, rec, want)
		}
	}
	extra, _ := ident.Generate()
	if _, err := connect(extra, mc.JoinToken); err == nil {
		t.Fatal("a third computer enrolled with a two-use ticket")
	}
}

// keys issue --preset: client settings travel in the credential; the client's own files
// and sandbox cannot be preset.
func TestPresetInCredential(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "good.conf")
	os.WriteFile(good, []byte("[resources]\ncores = 4\nmax_cores_per_job = 2\n"), 0o644)
	p, err := readPreset(good)
	if err != nil || p["resources.cores"] != "4" || p["resources.max_cores_per_job"] != "2" {
		t.Fatalf("%v %v", p, err)
	}
	bad := filepath.Join(dir, "bad.conf")
	os.WriteFile(bad, []byte("[security]\nsandbox = false\n"), 0o644)
	if _, err := readPreset(bad); err == nil {
		t.Fatal("security.sandbox accepted in a preset")
	}
}

// Changing orca.version in the middle of a campaign is warned about, once.
func TestCampaignVersionChange(t *testing.T) {
	dir := t.TempDir()
	if err := cmdInit(dir, "en"); err != nil {
		t.Fatal(err)
	}
	s, err := openServer(filepath.Join(dir, "server.conf"))
	if err != nil {
		t.Fatal(err)
	}
	if w := s.noteOrcaVersion(); w != "" {
		t.Fatalf("first version warned: %s", w)
	}
	cfg := *s.conf()
	cfg.OrcaVersion = "6.2.0"
	s.cfg = &cfg
	if w := s.noteOrcaVersion(); !strings.Contains(w, "changed from 6.1.1") {
		t.Fatalf("change not warned: %q", w)
	}
	if w := s.noteOrcaVersion(); w != "" {
		t.Fatalf("warned twice: %s", w)
	}
}

// orca.other_versions: a client without the campaign's version gets no task (refuse); with
// separate its result completes the input but lives in other-versions/<version>/output;
// with errors it goes to errors/other-versions/<version> and the input stays queued.
func TestOtherOrcaVersions(t *testing.T) {
	for _, mode := range []string{"refuse", "separate", "errors"} {
		dir := t.TempDir()
		if err := cmdInit(dir, "en"); err != nil {
			t.Fatal(err)
		}
		s, err := openServer(filepath.Join(dir, "server.conf"))
		if err != nil {
			t.Fatal(err)
		}
		cfg := *s.conf()
		cfg.OtherVersions = mode
		s.cfg = &cfg
		ss := &Session{s: s, id: "c1", name: "old", offer: wire.OfferRes{Orca: []wire.OrcaInstall{{Version: "6.1.0"}}}}
		ss.conn = nil
		if ss.hasCampaignOrca() {
			t.Fatal("6.1.0 counted as the campaign's 6.1.1")
		}
		if mode == "refuse" {
			continue
		}
		ss.outdated = "6.1.0"
		os.MkdirAll(filepath.Join(cfg.Root, dInput, "x"), 0o755)
		os.WriteFile(filepath.Join(cfg.Root, dInput, "x", "a.inp"), []byte("! HF\n"), 0o644)
		task := &TaskState{ID: "x/a.inp", Attempts: 1}
		s.store.Tasks[task.ID] = task
		s.store.Attempts["at1234567890abcd"] = &Attempt{ID: "at1234567890abcd", TaskID: task.ID, ClientID: "c1"}
		stage := t.TempDir()
		os.WriteFile(filepath.Join(stage, "a.out"), []byte("x"), 0o644)
		f, err := s.fileOtherVersion(ss, wire.Result{TaskID: task.ID, AttemptID: "at1234567890abcd"}, &Verdict{OK: true, OtherVersion: "6.1.0"}, stage, task)
		if err != nil {
			t.Fatal(err)
		}
		_, queued := s.store.Tasks[task.ID]
		switch mode {
		case "separate":
			if _, err := os.Stat(filepath.Join(cfg.Root, "other-versions", "6.1.0", "output", "x", "a")); err != nil || queued {
				t.Errorf("separate: %v queued=%v (%s)", err, queued, f.verdict)
			}
		case "errors":
			if _, err := os.Stat(filepath.Join(cfg.Root, dErrors, "other-versions", "6.1.0", "x", "a")); err != nil || !queued {
				t.Errorf("errors: %v queued=%v (%s)", err, queued, f.verdict)
			}
		}
	}
}

func TestUTF8Text(t *testing.T) {
	want := `{"tree_hash":"x"}`
	le := []byte{0xFF, 0xFE}
	for _, r := range want {
		le = append(le, byte(r), 0)
	}
	if got := string(utf8Text(le)); got != want {
		t.Fatalf("utf-16le: %q", got)
	}
	if got := string(utf8Text(append([]byte{0xEF, 0xBB, 0xBF}, want...))); got != want {
		t.Fatalf("bom: %q", got)
	}
}
