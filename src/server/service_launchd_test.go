package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// The LaunchAgent's label is per configuration, and paths with XML characters stay valid.
func TestLaunchdPlist(t *testing.T) {
	a, b := launchdLabel("/Users/u/ml/A/server.conf"), launchdLabel("/Users/u/ml/B/server.conf")
	if a == b || !strings.HasPrefix(a, "org.matriline.matriline-server-") {
		t.Fatalf("labels %q %q", a, b)
	}
	cfg := "/Users/u/R&D <lab>/server.conf"
	p := launchdPlist(a, "/Users/u/bin/matriline-server", cfg, "/Users/u/R&D <lab>/state/service.log")
	if strings.Contains(p, "R&D <lab>") || !strings.Contains(p, "R&amp;D &lt;lab&gt;/server.conf") {
		t.Errorf("not escaped:\n%s", p)
	}
	if runtime.GOOS != "darwin" {
		return
	}
	f := filepath.Join(t.TempDir(), a+".plist")
	os.WriteFile(f, []byte(p), 0o644)
	if out, err := exec.Command("plutil", "-lint", f).CombinedOutput(); err != nil {
		t.Fatalf("plutil: %v %s", err, out)
	}
	out, err := exec.Command("plutil", "-extract", "ProgramArguments.2", "raw", f).Output()
	if err != nil || strings.TrimSpace(string(out)) != cfg {
		t.Errorf("ProgramArguments[2] = %q (%v), want %q", out, err, cfg)
	}
}
