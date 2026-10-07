package main

import (
	"crypto/ed25519"
	"github.com/agaloya/matriline/common/build"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/agaloya/matriline/common/ident"
	"github.com/agaloya/matriline/common/orcafind"
)

func TestSetConfValue(t *testing.T) {
	text := "[orca]\n# paths: where ORCA is\npaths = /opt/orca-6.1.1\n\n[resources]\ncores = 0\n"
	got, ok := setConfValue(text, "resources", "cores", "4")
	if !ok || !strings.Contains(got, "cores = 4\n") || strings.Contains(got, "cores = 0") {
		t.Fatalf("replace: %q", got)
	}
	if _, ok := setConfValue(text, "resources", "nosuch", "1"); ok {
		t.Fatal("unknown setting accepted")
	}
	if _, ok := setConfValue(text, "nosuch", "x", "1"); ok {
		t.Fatal("unknown section accepted")
	}
	got, _ = setConfValue(text, "resources", "cores", "4 # many")
	if !strings.Contains(got, `cores = '4 # many'`) {
		t.Fatalf("value with # not quoted: %q", got)
	}
}

func fakeOrca(t *testing.T, dir string) {
	t.Helper()
	os.MkdirAll(dir, 0o755)
	os.WriteFile(orcaExe(dir), []byte("x"), 0o755)
	os.WriteFile(filepath.Join(dir, "orca_scf"), []byte("x"), 0o755)
}

func TestFindOrcaDirs(t *testing.T) {
	home, _ := filepath.EvalSymlinks(t.TempDir()) // Windows: long form, not MATRIL~1
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home) // os.UserHomeDir on Windows
	fakeOrca(t, filepath.Join(home, "orca_6_1_1"))
	os.MkdirAll(filepath.Join(home, "orca-notes"), 0o755) // not an installation
	bin := t.TempDir()
	wrapped := filepath.Join(home, "elsewhere", "orca-x")
	fakeOrca(t, wrapped)
	os.WriteFile(filepath.Join(bin, "orca"), []byte("#!/bin/sh\nexec "+wrapped+"/orca \"$@\"\n"), 0o755)
	t.Setenv("PATH", bin)
	got := strings.Join(orcafind.Dirs(), ",")
	wants := []string{filepath.Join(home, "orca_6_1_1"), wrapped}
	if runtime.GOOS == "windows" {
		wants = wants[:1] // "#!" wrapper scripts are a Unix thing
	}
	for _, want := range wants {
		if !strings.Contains(got, want) {
			t.Errorf("%s not found in %s", want, got)
		}
	}
	if strings.Contains(got, "orca-notes") {
		t.Errorf("a directory without ORCA was listed: %s", got)
	}
}

// init --credential: the admin's preset is applied, settings the admin may not touch are
// ignored, ORCA is found, and the credential is installed.
func TestInitWithCredential(t *testing.T) {
	home, _ := filepath.EvalSymlinks(t.TempDir())
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home) // os.UserHomeDir on Windows
	t.Setenv("PATH", t.TempDir())
	fakeOrca(t, filepath.Join(home, "orca"))
	pub, _, _ := ed25519.GenerateKey(nil)
	cred := filepath.Join(t.TempDir(), "lab.cred")
	err := ident.WriteCredential(cred, &ident.Credential{Name: "lab", ServerPub: pub, ServerAddress: "example.org:44100",
		JoinToken: "mle-x", Preset: map[string]string{"resources.cores": "3", "schedule.windows": "mon-fri 20:00-07:00",
			"security.sandbox": "false", "resources.nosuch": "1"}})
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "cli")
	if err := cmdInitWith(dir, cred); err != nil {
		t.Fatal(err)
	}
	cfg, _, err := loadConfig(filepath.Join(dir, "client.conf"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Cores != 3 || len(cfg.Schedule) == 0 || cfg.Sandbox == build.NoSecurity {
		t.Errorf("cores %d, schedule %v, sandbox %v", cfg.Cores, cfg.Schedule, cfg.Sandbox)
	}
	if !strings.Contains(strings.Join(cfg.OrcaPaths, ","), filepath.Join(home, "orca")) {
		t.Errorf("orca paths %v", cfg.OrcaPaths)
	}
	if _, err := ident.ReadCredential(filepath.Join(dir, "credential.conf")); err != nil {
		t.Errorf("credential not installed: %v", err)
	}
	if err := cmdInitWith(dir, cred); err == nil {
		t.Error("an existing client.conf was overwritten")
	}
}

// A client without a credential: opening it for 'doctor' creates no key, so a client the
// admin wiped (key and credential deleted) still reads as wiped to 'enable' (a macOS
// checklist: doctor created a key and enable then undid the wipe).
func TestDoctorWithoutCredentialCreatesNoKey(t *testing.T) {
	dir := t.TempDir()
	if err := cmdInit(dir); err != nil {
		t.Fatal(err)
	}
	cfg := filepath.Join(dir, "client.conf")
	a, err := openAgent(cfg, false)
	if err != nil {
		t.Fatal(err)
	}
	if a.key != nil {
		t.Error("a key without a credential")
	}
	if _, err := os.Stat(filepath.Join(a.cfg.StateDir, "client.key")); err == nil {
		t.Error("state/client.key was created")
	}
	os.MkdirAll(a.cfg.StateDir, 0o700)
	os.WriteFile(filepath.Join(a.cfg.StateDir, "DISABLED"), []byte("wiped"), 0o600)
	if err := cmdEnable(cfg); err == nil || !strings.Contains(err.Error(), "deleted") {
		t.Errorf("enable on a wiped client: %v", err)
	}
}
