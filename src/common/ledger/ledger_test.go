package ledger

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agaloya/matriline/common/ident"
)

func TestChainAndTamper(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "ledger.log")
	k, _ := ident.Generate()
	l, err := Open(p, k)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if _, err := l.Append(Entry{Kind: "result", Task: "a.inp", Files: []FileHash{{"output/a/a.out", "00"}}}); err != nil {
			t.Fatal(err)
		}
	}
	l.Close()
	if es, err := Read(p, k.Pub); err != nil || len(es) != 3 {
		t.Fatalf("read: %v %d", err, len(es))
	}
	// torn tail is repaired on open
	f, _ := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString(`{"seq":4,"ki`)
	f.Close()
	l, err = Open(p, k)
	if err != nil {
		t.Fatal(err)
	}
	l.Append(Entry{Kind: "edit"})
	l.Close()
	if es, err := Read(p, k.Pub); err != nil || len(es) != 4 {
		t.Fatalf("after torn tail: %v %d", err, len(es))
	}
	// modifying an entry is detected
	b, _ := os.ReadFile(p)
	os.WriteFile(p, []byte(strings.Replace(string(b), "a.inp", "b.inp", 1)), 0o600)
	if _, err := Read(p, k.Pub); err == nil {
		t.Fatal("tampering not detected")
	}
	// removing an entry is detected
	lines := strings.SplitAfter(string(b), "\n")
	os.WriteFile(p, []byte(lines[0]+lines[2]+lines[3]), 0o600)
	if _, err := Read(p, k.Pub); err == nil {
		t.Fatal("removal not detected")
	}
}
