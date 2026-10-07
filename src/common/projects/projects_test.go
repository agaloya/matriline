package projects

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolve(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("APPDATA", filepath.Join(home, "AppData"))
	t.Setenv("MATRILINE_NO_REGISTRY", "")
	texts := Texts{Which: "which?", Several: "several"}
	mk := func(name string) string {
		p := filepath.Join(home, name, "server.conf")
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte("[general]\n"), 0o600)
		if err := Add("server", p); err != nil {
			t.Fatal(err)
		}
		return p
	}
	if got, _ := Resolve("server", "server.conf", nil, nil, texts); got != "server.conf" {
		t.Fatalf("nothing recorded: %q", got)
	}
	a := mk("projA")
	mk("projA") // twice: listed once
	if got, _ := Resolve("server", "server.conf", nil, nil, texts); got != a {
		t.Fatalf("one project: %q", got)
	}
	b := mk("projB")
	if _, err := Resolve("server", "server.conf", nil, nil, texts); err == nil || !strings.Contains(err.Error(), b) {
		t.Fatalf("two projects, no terminal: %v", err)
	}
	var out bytes.Buffer
	if got, err := Resolve("server", "server.conf", strings.NewReader("2\n"), &out, texts); err != nil || got != b || !strings.Contains(out.String(), "which?") {
		t.Fatalf("chosen: %q %v %s", got, err, out.String())
	}
	os.RemoveAll(filepath.Dir(a)) // a deleted project drops off the list
	if got, _ := Resolve("server", "server.conf", nil, nil, texts); got != b || len(List("server")) != 1 {
		t.Fatalf("after deleting one: %q %v", got, List("server"))
	}
	// a project made before the list existed joins it when a command runs inside it
	old := filepath.Join(home, "old", "server.conf")
	os.MkdirAll(filepath.Dir(old), 0o755)
	os.WriteFile(old, []byte("[general]\n"), 0o600)
	if got, _ := Resolve("server", old, nil, nil, texts); got != old || len(List("server")) != 2 {
		t.Fatalf("used inside an old project: %q %v", got, List("server"))
	}
	t.Setenv("MATRILINE_NO_REGISTRY", "1")
	mk("projC") // not recorded (Nacomline's own projects)
	if len(List("server")) != 2 {
		t.Fatalf("MATRILINE_NO_REGISTRY ignored: %v", List("server"))
	}
}

// A project whose folder cannot be read for a while stays on the list (code review).
func TestUnreadableProjectStays(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root reads everything")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("MATRILINE_NO_REGISTRY", "")
	parent := filepath.Join(home, "disk")
	p := filepath.Join(parent, "proj", "server.conf")
	os.MkdirAll(filepath.Dir(p), 0o755)
	os.WriteFile(p, []byte("[general]\n"), 0o600)
	Add("server", p)
	os.Chmod(parent, 0)
	during := List("server")
	os.Chmod(parent, 0o755)
	if len(during) != 1 || len(List("server")) != 1 {
		t.Fatalf("while unreadable %v, after %v", during, List("server"))
	}
}
