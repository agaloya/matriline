//go:build darwin

package main

import (
	"os"
	"os/exec"
	"path/filepath"
)

// Gatekeeper: files extracted from a downloaded archive carry the com.apple.quarantine
// extended attribute. ORCA's programs are not notarized, so a quarantined ORCA does not fail:
// macOS holds it before main() while it waits for the user to answer a dialog, and the job
// hangs (seen with ORCA 6.1.1 from the .tar.bz2; in a VM or a service nobody sees the dialog).
// The client refuses such an installation and gives the command that clears the attribute.

// quarantineExes are the programs every job runs: checking them keeps the test fast (ORCA has
// about 3000 files).
var quarantineExes = []string{"orca", "orca_startup", "orca_leanscf", "orca_scfgrad", "orca_util", "orca_guess"}

// quarantinedFiles returns the main ORCA programs in dir that carry com.apple.quarantine.
// The attribute is read with /usr/bin/xattr: package syscall has no getxattr on darwin and
// the client uses no cgo.
func quarantinedFiles(dir string) []string {
	var out []string
	for _, n := range quarantineExes {
		p := filepath.Join(dir, n)
		if _, err := os.Stat(p); err != nil {
			continue
		}
		// exit status 0: the attribute exists; 1: it does not (or xattr itself failed)
		if exec.Command("/usr/bin/xattr", "-p", "com.apple.quarantine", p).Run() == nil {
			out = append(out, p)
		}
	}
	return out
}

// quarantineHint is the command that lets ORCA run.
func quarantineHint(dir string) string {
	return "ORCA's files are quarantined by macOS (downloaded from the internet), so ORCA would hang waiting for a Gatekeeper dialog. Run once: xattr -dr com.apple.quarantine '" + dir + "'"
}
