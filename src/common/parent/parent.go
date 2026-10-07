// Package parent stops a program whose starter is gone. A program that embeds Matriline
// (Nacomline) sets MATRILINE_PARENT_PID to its own pid for the Matriline programs it
// starts; when it ends without stopping them (killed with -9, or a terminal 'web' ended),
// they would keep running, holding ports and locks. Linux ends them by itself
// (Pdeathsig) and Windows by the embedder's job object; macOS and the BSDs need this
// (seen on a Mac).
package parent

import (
	"os"
	"runtime"
	"strconv"
	"syscall"
	"time"
)

// Watch checks every 2 s whether the process named by MATRILINE_PARENT_PID is still this
// one's parent; when it is not, it sends SIGTERM to this process, which then stops as it
// does for 'stop' or Ctrl+C. Without the variable (or on Windows) it does nothing.
func Watch() {
	pid, err := strconv.Atoi(os.Getenv("MATRILINE_PARENT_PID"))
	if err != nil || pid <= 1 || runtime.GOOS == "windows" {
		return
	}
	go func() {
		for os.Getppid() == pid {
			time.Sleep(2 * time.Second)
		}
		if p, err := os.FindProcess(os.Getpid()); err == nil {
			p.Signal(syscall.SIGTERM)
		}
	}()
}
