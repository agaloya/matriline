package logx

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRotation(t *testing.T) {
	p := filepath.Join(t.TempDir(), "x.log")
	r := &rotating{path: p}
	f, _ := os.Create(p)
	r.f = f
	line := []byte(strings.Repeat("x", 1<<20) + "\n")
	for i := 0; i < 25; i++ {
		if _, err := r.Write(line); err != nil {
			t.Fatal(err)
		}
	}
	a, _ := os.Stat(p)
	b, err := os.Stat(p + ".1")
	if err != nil || a.Size() > MaxSize || b.Size() > MaxSize {
		t.Fatalf("sizes: %v %v %v", a.Size(), b, err)
	}
}
