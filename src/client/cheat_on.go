//go:build cheat

package main

// Adversarial client for the integrity tests (D37, D44). NEVER shipped: compiled only with
// "go build -tags cheat" by lab/tests. MATRILINE_CHEAT selects the attack:
//
//	energy  run ORCA honestly, then add +2e-4 Eh to every reported energy (plausible lie)
//	loose   compute cheaper (LooseSCF LooseOpt) and erase the keywords from the echoed input
//	copy    compute nothing: return the output of the previous job, renamed
//	forge   like copy, and also rewrite the echoed input to match this task (a real output
//	        of another input, e.g. an isomer, passed off as this one)
//	hess    run honestly, then scale the returned Hessian by 1.02
//	slander compute its own jobs honestly, but report shifted energies (as "energy") for
//	        the verification sub-tasks it recognizes (inputs reading guess.gbw/geom.xyz),
//	        so honest results look forged: tests disputes and their rescue
//	dodge   like "energy", except on tasks that look like the server's own (an xyz file
//	        or MORead): tests that canaries cannot be told apart from ordinary tasks
//
// A real cheater controls the client completely (it holds its own signing key), so these
// attacks act before the result manifest is signed, exactly where a modified client would.

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
)

var (
	cheatMode = os.Getenv("MATRILINE_CHEAT")
	lastMu    sync.Mutex
	lastWork  string // work dir and stem of the previous finished job (for "copy")
	lastStem  string
	reEcho    = regexp.MustCompile(`^\|\s*\d+> `)
	reEnergy  = regexp.MustCompile(`^(\s*(?:FINAL SINGLE POINT ENERGY|Total Energy\s*:|Total Enthalpy\s*\.\.\.|Final Gibbs free energy\s*\.\.\.|Total energy after final integration\s*:)\s+)(-?\d+\.\d+)`)
)

// cheatBefore may alter the input before ORCA runs; true = ORCA must not run at all.
func cheatBefore(j *Job, inPath string) bool {
	stem := strings.TrimSuffix(filepath.Base(inPath), filepath.Ext(inPath))
	switch cheatMode {
	case "loose":
		b, err := os.ReadFile(inPath)
		if err == nil {
			os.WriteFile(inPath, []byte(strings.Replace(string(b), "\n!", "\n! LooseSCF LooseOpt\n!", 1)), 0o644)
		}
	case "copy", "forge":
		lastMu.Lock()
		w, st := lastWork, lastStem
		lastMu.Unlock()
		if w == "" {
			return false // nothing to copy yet: compute honestly once
		}
		es, _ := os.ReadDir(w)
		for _, e := range es {
			n := e.Name()
			if !strings.HasPrefix(n, st+".") || strings.HasSuffix(n, ".inp") {
				continue // keep this task's own input
			}
			b, err := os.ReadFile(filepath.Join(w, n))
			if err == nil {
				os.WriteFile(filepath.Join(j.workDir(), stem+strings.TrimPrefix(n, st)), []byte(strings.ReplaceAll(string(b), st, stem)), 0o644)
			}
		}
		if cheatMode == "forge" {
			in, _ := os.ReadFile(inPath)
			var echo []string
			for i, ln := range strings.Split(strings.TrimRight(string(in), "\n"), "\n") {
				echo = append(echo, fmt.Sprintf("|%3d> %s", i+1, ln))
			}
			done := false
			rewrite(filepath.Join(j.workDir(), stem+".out"), func(ln string) string {
				if reEcho.MatchString(ln) {
					if done {
						return ""
					}
					done = true
					return strings.Join(echo, "\n")
				}
				return ln
			})
		}
		return true
	}
	return false
}

// cheatAfter may alter ORCA's files after an honest run, before they are hashed and signed.
func cheatAfter(j *Job, stem string) {
	w := j.workDir()
	mode := cheatMode
	in, _ := os.ReadFile(filepath.Join(w, stem+".inp"))
	looksInternal := strings.Contains(string(in), "guess.gbw") || strings.Contains(string(in), "geom.xyz") ||
		strings.Contains(strings.ToLower(string(in)), "xyzfile") || strings.Contains(strings.ToLower(string(in)), "moread")
	switch {
	case mode == "slander" && looksInternal:
		mode = "energy"
	case mode == "dodge" && !looksInternal:
		mode = "energy"
	}
	switch mode {
	case "energy":
		rewrite(filepath.Join(w, stem+".out"), func(ln string) string {
			if m := reEnergy.FindStringSubmatch(ln); m != nil {
				v, _ := strconv.ParseFloat(m[2], 64)
				return m[1] + strconv.FormatFloat(v+2e-4, 'f', len(m[2])-strings.Index(m[2], ".")-1, 64) + ln[len(m[0]):]
			}
			return ln
		})
	case "loose":
		rewrite(filepath.Join(w, stem+".out"), func(ln string) string {
			if strings.Contains(ln, "> ! LooseSCF LooseOpt") {
				return "" // the echoed extra line disappears (line numbers would still shift)
			}
			return ln
		})
	case "hess":
		inBlock := false
		rewrite(filepath.Join(w, stem+".hess"), func(ln string) string {
			t := strings.TrimSpace(ln)
			if strings.HasPrefix(t, "$") {
				inBlock = t == "$hessian"
				return ln
			}
			f := strings.Fields(ln)
			if !inBlock || len(f) < 2 || strings.Contains(f[0], ".") {
				return ln
			}
			out := []string{f[0]}
			for _, x := range f[1:] {
				v, err := strconv.ParseFloat(x, 64)
				if err != nil {
					return ln
				}
				out = append(out, fmt.Sprintf("%.10E", v*1.02))
			}
			return "  " + strings.Join(out, "  ")
		})
	}
	// keep a private copy: the client deletes the job directory once the server
	// acknowledges, and a real cheater would keep outputs around to recycle them
	keep := filepath.Join(os.TempDir(), "matriline-cheat-cache")
	os.RemoveAll(keep)
	os.MkdirAll(keep, 0o700)
	es, _ := os.ReadDir(w)
	for _, e := range es {
		if b, err := os.ReadFile(filepath.Join(w, e.Name())); err == nil {
			os.WriteFile(filepath.Join(keep, e.Name()), b, 0o600)
		}
	}
	lastMu.Lock()
	lastWork, lastStem = keep, stem
	lastMu.Unlock()
}

func rewrite(path string, f func(string) string) {
	b, err := os.ReadFile(path)
	if err != nil {
		return
	}
	lines := strings.Split(string(b), "\n")
	var out []string
	for _, ln := range lines {
		if n := f(ln); n != "" || ln == "" {
			out = append(out, n)
		}
	}
	os.WriteFile(path, []byte(strings.Join(out, "\n")), 0o644)
}
