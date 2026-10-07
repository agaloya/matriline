// Package orcafind finds the ORCA installations on a computer, for the client's and the
// server's 'init' (zero-configuration setup).
package orcafind

import (
	"bufio"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
)

// Dirs lists the ORCA installations on this computer: the directory of an "orca"
// found in PATH (a wrapper script that execs one is followed) and the usual install places.
func Dirs() []string {
	exe := "orca"
	if runtime.GOOS == "windows" {
		exe = "orca.exe"
	}
	var cands []string
	// every "orca" in the PATH, not only the first: on Linux the first is often GNOME's
	// screen reader (/usr/bin/orca), which hasOrcaModules then leaves out
	for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
		p, err := exec.LookPath(filepath.Join(dir, exe))
		if dir == "" || err != nil {
			continue
		}
		if r, err := filepath.EvalSymlinks(p); err == nil {
			cands = append(cands, filepath.Dir(r))
			cands = append(cands, wrapperTargets(r)...)
		}
	}
	home, _ := os.UserHomeDir()
	var globs []string
	switch runtime.GOOS {
	case "windows":
		for _, v := range []string{"ProgramFiles", "ProgramFiles(x86)", "LOCALAPPDATA"} {
			if d := os.Getenv(v); d != "" {
				globs = append(globs, filepath.Join(d, "ORCA*"), filepath.Join(d, "orca*"))
			}
		}
		globs = append(globs, `C:\ORCA*`, `C:\orca*`)
	case "darwin":
		globs = append(globs, "/Applications/orca*", "/Applications/ORCA*", "/opt/orca*", "/usr/local/orca*")
	default:
		globs = append(globs, "/opt/orca*", "/usr/local/orca*")
	}
	if home != "" {
		globs = append(globs, filepath.Join(home, "orca*"), filepath.Join(home, "ORCA*"),
			filepath.Join(home, ".local", "orca*"), filepath.Join(home, "Applications", "orca*"))
	}
	for _, g := range globs {
		m, _ := filepath.Glob(g)
		cands = append(cands, m...)
		// one level down too: a folder that collects ORCA versions, e.g.
		// ~/orca/orca_6_1_1_macosx_arm64_openmpi411 (found on a Mac)
		for _, d := range m {
			sub, _ := filepath.Glob(filepath.Join(d, "orca*"))
			sub2, _ := filepath.Glob(filepath.Join(d, "ORCA*"))
			cands = append(append(cands, sub...), sub2...)
		}
	}
	seen := map[string]bool{}
	var out []string
	for _, d := range cands {
		if r, err := filepath.EvalSymlinks(d); err == nil {
			d = r
		}
		if seen[d] {
			continue
		}
		seen[d] = true
		if st, err := os.Stat(filepath.Join(d, exe)); err == nil && !st.IsDir() && hasOrcaModules(d) {
			out = append(out, d)
		}
	}
	sort.Strings(out)
	return out
}

// hasOrcaModules: a real ORCA directory holds its orca_* programs next to "orca".
func hasOrcaModules(d string) bool {
	m, _ := filepath.Glob(filepath.Join(d, "orca_scf*"))
	return len(m) > 0
}

var reExecTarget = regexp.MustCompile(`(/[^\s"']+)/orca(\s|"|'|$)`)

// wrapperTargets returns the directories a small shell wrapper named orca runs ORCA from
// (e.g. "exec /opt/orca-6.1.1/orca "$@"").
func wrapperTargets(p string) []string {
	f, err := os.Open(p)
	if err != nil {
		return nil
	}
	defer f.Close()
	head := make([]byte, 2)
	if n, _ := f.Read(head); n < 2 || string(head) != "#!" {
		return nil
	}
	f.Seek(0, 0)
	var out []string
	sc := bufio.NewScanner(f)
	for n := 0; sc.Scan() && n < 50; n++ {
		if m := reExecTarget.FindStringSubmatch(sc.Text()); m != nil {
			out = append(out, m[1])
		}
	}
	return out
}
