package ident

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// Processes that create the key at the same moment all end with the one in the file.
func TestLoadOrCreateRace(t *testing.T) {
	for round := 0; round < 20; round++ {
		path := filepath.Join(t.TempDir(), "state", "client.key")
		const n = 20
		ids := make([]string, n)
		var wg sync.WaitGroup
		start := make(chan struct{})
		for i := range n {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				k, _, err := LoadOrCreate(path)
				if err != nil {
					t.Error(err)
					return
				}
				ids[i] = k.ID()
			}()
		}
		close(start)
		wg.Wait()
		k, created, err := LoadOrCreate(path)
		if err != nil || created {
			t.Fatalf("reload: created=%v err=%v", created, err)
		}
		for i, id := range ids {
			if id != k.ID() {
				t.Fatalf("round %d: caller %d got %s, the file holds %s", round, i, id, k.ID())
			}
		}
		if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
			t.Errorf("mode %v", fi.Mode().Perm())
		}
		if es, _ := os.ReadDir(filepath.Dir(path)); len(es) != 1 {
			t.Errorf("leftover files: %d entries", len(es))
		}
	}
}
