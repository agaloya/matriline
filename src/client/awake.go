package main

import (
	"context"
	"time"
)

// awakeLoop keeps the computer from going to sleep by itself while calculations run on
// mains power (schedule.keep_awake, user: a laptop slept when its screen went off and its
// jobs stopped). Like a video player: the screen may still turn off, closing a laptop's lid
// still suspends it, and on battery or with nothing to do the computer sleeps as usual. The
// computer's own power settings are not changed.
func (a *Agent) awakeLoop(ctx context.Context) {
	if !a.cfg.KeepAwake {
		return
	}
	var stop func()
	warned := false
	defer func() {
		if stop != nil {
			stop()
		}
	}()
	t := time.NewTicker(10 * time.Second) // a job shorter than this needs no lock
	defer t.Stop()
	for {
		want := a.runningCount() > 0 && readPower(powerSupplyDirVar).State != "battery"
		switch {
		case want && stop == nil:
			s, err := preventSleep()
			if err != nil {
				if !warned {
					a.log.Warnf("cannot keep this computer awake while it computes (%v): if it goes to sleep by itself, its calculations wait; see its power settings", err)
					warned = true
				}
			} else {
				stop = s
				a.log.Infof("keeping this computer awake while calculations run (schedule.keep_awake; the screen may still turn off)")
			}
		case !want && stop != nil:
			stop()
			stop = nil
			a.log.Infof("this computer may sleep again (no calculation running, or on battery)")
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
