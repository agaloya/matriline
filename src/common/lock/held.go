package lock

import (
	"os"
	"time"
)

// Held reports whether a running process holds dir/name (e.g. "is the client running?").
func Held(dir, name string) bool {
	if _, err := os.Stat(dir); err != nil {
		return false
	}
	f, err := Acquire(dir, name)
	if err != nil {
		return true
	}
	f.Close()
	return false
}

// AcquireWait is Acquire, waiting up to wait for a holder that is ending: a service
// stopped and started again at once (or restarted after an update) finds the old process
// still exiting (Windows scheduled tasks, seen in lab/tests/services-win.sh). A second
// instance proper is still refused, after the wait.
func AcquireWait(dir, name string, wait time.Duration) (*os.File, error) {
	end := time.Now().Add(wait)
	for {
		f, err := Acquire(dir, name)
		if err == nil || time.Now().After(end) {
			return f, err
		}
		time.Sleep(500 * time.Millisecond)
	}
}
