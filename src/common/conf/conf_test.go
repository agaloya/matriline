package conf

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestParse(t *testing.T) {
	p := filepath.Join(t.TempDir(), "c.conf")
	os.WriteFile(p, []byte("top = 1\n[a]\nx = hello # c\ny = \"q # not comment\"\nempty =      # only comment\nd = 90s\nz = 1.5GiB\nl = a, b ,c\nb = yes\n"), 0o644)
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	var errs []error
	if c.String("a.x", "") != "hello" || c.String("a.y", "") != "q # not comment" || c.String("a.empty", "z") != "" {
		t.Fatal("strings")
	}
	if c.Duration("a.d", 0, &errs) != 90*time.Second || c.Size("a.z", 0, &errs) != 1610612736 || !c.Bool("a.b", false, &errs) {
		t.Fatal("typed")
	}
	if l := c.List("a.l", nil); len(l) != 3 || l[1] != "b" {
		t.Fatal("list")
	}
	if u := c.Unused(); len(u) != 1 {
		t.Fatalf("unused %v", u)
	}
}

// Values with or without quotes mean the same (user), and quoted text is literal.
func TestQuotes(t *testing.T) {
	p := filepath.Join(t.TempDir(), "c.conf")
	os.WriteFile(p, []byte("[o]\na = fingerprint\nb = \"fingerprint\"\nc = 'fingerprint'   # comment\nwin = \"C:\\new\\ORCA_6.1.1\"\nl1 = \"x\", \"y\"\nl2 = 'x', 'y'\nl3 = x, y\n"), 0o644)
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"o.a", "o.b", "o.c"} {
		if v := c.String(k, ""); v != "fingerprint" {
			t.Errorf("%s = %q", k, v)
		}
	}
	if v := c.String("o.win", ""); v != `C:\new\ORCA_6.1.1` {
		t.Errorf("windows path %q", v)
	}
	for _, k := range []string{"o.l1", "o.l2", "o.l3"} {
		if l := c.List(k, nil); len(l) != 2 || l[0] != "x" || l[1] != "y" {
			t.Errorf("%s = %q", k, l)
		}
	}
}

// Quote writes values that Load reads back unchanged (code review: %q escapes
// were read back literally).
func TestQuoteRoundTrip(t *testing.T) {
	for _, v := range []string{`C:\ORCA 6.1.1 #2`, `a"b`, `it's #1`, " padded ", "plain", `/opt/orca-6.1.1`, `'x'`} {
		q, err := Quote(v)
		if err != nil {
			t.Fatalf("%q: %v", v, err)
		}
		p := filepath.Join(t.TempDir(), "c.conf")
		os.WriteFile(p, []byte("[s]\nk = "+q+"\n"), 0o644)
		c, err := Load(p)
		if err != nil {
			t.Fatalf("%q written as %s: %v", v, q, err)
		}
		if got := c.String("s.k", ""); got != v {
			t.Errorf("%q written as %s, read back %q", v, q, got)
		}
	}
	if _, err := Quote(`a'b"c #`); err == nil {
		t.Error("a value that cannot be written was accepted")
	}
}
