package main

import (
	"context"
	"path/filepath"
	"sync/atomic"
	"time"

	"github.com/agaloya/matriline/common/orca"
)

// Deep check (security.orca_deep_check_interval): every interval, each ORCA installation
// is hashed again from scratch, ignoring the fingerprint cache (which trusts files whose
// size and time did not change). If a tree no longer matches the one checked at start,
// someone or something changed ORCA's files while the client ran: it stops taking jobs
// until it is restarted (and checks ORCA again then). 0 turns it off.

var orcaChanged atomic.Value // string: why the client stopped taking jobs ("" = fine)

func orcaChangedReason() string {
	s, _ := orcaChanged.Load().(string)
	return s
}

func (a *Agent) deepCheckLoop(ctx context.Context) {
	if a.cfg.DeepCheck <= 0 {
		return
	}
	t := time.NewTicker(a.cfg.DeepCheck)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		for _, in := range a.installs {
			cache := filepath.Join(a.cfg.StateDir, "fpcache-"+filepath.Base(in.dir)+".json")
			fp, err := orca.FingerprintTree(in.dir, cache, true)
			switch {
			case err != nil:
				orcaChanged.Store("ORCA at " + in.dir + " cannot be read any more (" + err.Error() + "): restart the client to check it again")
			case fp.TreeHash != in.fp.TreeHash:
				orcaChanged.Store("ORCA at " + in.dir + " changed while the client ran (tree " + fp.TreeHash[:16] + ", was " + in.fp.TreeHash[:16] + "): restart the client to check it again")
			default:
				continue
			}
			a.log.Errorf("deep check: %s", orcaChangedReason())
		}
	}
}
