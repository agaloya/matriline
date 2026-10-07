package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agaloya/matriline/common/orca"
)

func TestNewerVersion(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want bool
	}{{"6.1.1", "6.1.0", true}, {"6.0.10", "6.0.9", true}, {"6.0.9", "6.0.10", false}, {"6.1.1", "6.1.1", false}, {"6.1.1", "6.1", true}} {
		if got := newerVersion(c.a, c.b); got != c.want {
			t.Errorf("newerVersion(%q, %q) = %v", c.a, c.b, got)
		}
	}
}

// The configuration template written by init loads cleanly: no error, no warning.
func TestDefaultConfigLoads(t *testing.T) {
	p := filepath.Join(t.TempDir(), "server.conf")
	if err := os.WriteFile(p, []byte(defaultConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	_, warns, err := loadConfig(p)
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range warns {
		t.Errorf("warning: %s", w)
	}
}

// "builtin" loads the compiled-in fingerprints of the campaign's version.
func TestBuiltinFingerprints(t *testing.T) {
	b := builtinFor("6.1.1")
	// every official 6.1.1 build hashed from its downloaded archive (lab/tools/archprint.py)
	want := map[string]string{
		"linux-x86_64-avx2": "b3d0ee8aff8d9b55", "linux-x86_64": "087d462948fb8292",
		"linux-arm64": "82f8bc0ea2f2ef3d", "linux-riscv64": "556c7a3a5cb8547e",
		"macos-arm64": "aa2740d9a03b6e18", "macos-x86_64": "6b0472906ecfe002",
		"macos-arm64-openblas": "706b249e4f16bb5b", "windows-x86_64": "2b6c06f26361b3da",
		"windows-x86_64-autoci": "b7c671e918e7cec5", "windows-x86_64-autoci-msmpi": "292b81d369344a46",
		"windows-x86_64-autoci-both": "2999ba0b450a294d",
	}
	trees := map[string]bool{}
	for n, raw := range b {
		var fp orca.Fingerprint
		if err := json.Unmarshal(utf8Text(raw), &fp); err != nil || orca.TreeHashOf(fp.Files) != fp.TreeHash {
			t.Errorf("%s: unreadable or tree hash does not match its files (%v)", n, err)
		}
		if _, ok := fp.Files["setup"]; ok {
			t.Errorf("%s lists the installer's setup helper: no unpacked archive would match", n)
		}
		trees[fp.TreeHash[:16]] = true
	}
	for name, tree := range want {
		if !trees[tree] {
			t.Errorf("no built-in fingerprint for %s (tree %s)", name, tree)
		}
	}
	if len(builtinFor("9.9.9")) != 0 {
		t.Error("fingerprints for an unknown version")
	}
}

// setConfigValue on the real template (a section in two parts): the key is replaced where
// it is, never added twice, and the file still loads.
func TestSetConfigValueTwoParts(t *testing.T) {
	p := filepath.Join(t.TempDir(), "server.conf")
	os.WriteFile(p, []byte(defaultConfig), 0o600)
	if err := setConfigValue(p, "network", "blocked_ips", "192.0.2.1"); err != nil {
		t.Fatal(err)
	}
	cfg, warns, err := loadConfig(p)
	if err != nil {
		t.Fatalf("%v %v", err, warns)
	}
	if len(cfg.BlockedIPs) != 1 || cfg.BlockedIPs[0] != "192.0.2.1" {
		t.Errorf("blocked_ips = %v", cfg.BlockedIPs)
	}
	if err := setConfigValue(p, "network", "new_key_for_test", "x"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := loadConfig(p); err != nil {
		t.Errorf("after adding a key: %v", err)
	}
}

// TestVerifyMasterSwitch: verify.enabled = false turns every check off, the probation
// replicas of new clients included (the 120-client load test still replicated them).
func TestVerifyMasterSwitch(t *testing.T) {
	p := filepath.Join(t.TempDir(), "server.conf")
	os.WriteFile(p, []byte(setConfigText(defaultConfig, "verify", "enabled", "false")), 0o600)
	c, _, err := loadConfig(p)
	if err != nil {
		t.Fatal(err)
	}
	if c.ProbationReplication != 0 || c.ReplicationRate != 0 || c.CanaryRate != 0 || c.VerifySCF != 0 || c.SecondOpinion {
		t.Errorf("checks left on: probation %v replication %v canary %v scf %v second opinion %v",
			c.ProbationReplication, c.ReplicationRate, c.CanaryRate, c.VerifySCF, c.SecondOpinion)
	}
}

// network.listen takes a bare port (user: the ":" was puzzling): "44100" means every
// address, like ":44100"; host:port entries stay as they are.
func TestListenBarePort(t *testing.T) {
	p := filepath.Join(t.TempDir(), "server.conf")
	text := strings.Replace(defaultConfig, "\nlisten = 44100\n", "\nlisten = 44100, 127.0.0.1:443\n", 1)
	os.WriteFile(p, []byte(text), 0o600)
	cfg, _, err := loadConfig(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Listen) != 2 || cfg.Listen[0] != ":44100" || cfg.Listen[1] != "127.0.0.1:443" {
		t.Errorf("listen = %q", cfg.Listen)
	}
}
