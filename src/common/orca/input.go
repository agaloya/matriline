package orca

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Input safety and normalization.
//
// Safety policy (D43): no program outside the selected ORCA installation may run. The input
// check below is the first of three layers; it rejects every known ORCA hook that starts an
// external program (found by inspecting the ORCA 6.1.x binaries: ProgExt/EXTOPT and the
// EXTOPTEXE variable for otool_external, NBO -> gennbo, MRCC -> dmrcc, SYSCMD) and any file
// reference that is not a bare file name inside the job directory. Layers 2 and 3 (clean
// environment + sandbox with an execute allow-list, and process auditing) make the policy
// hold even for hooks this list does not know.

var (
	reComment  = regexp.MustCompile(`#[^#\n]*(#|$)`)
	reDenied   = regexp.MustCompile(`(?i)\b(progext|extopt|extoptexe|syscmd|nboexe|xtbexe|progopencosmors|mrcc|dmrcc|gennbo|otool_external)\b`)
	reNBO      = regexp.MustCompile(`(?im)(^\s*!.*\bnbo\b|^\s*%nbo\b)`)
	reQuoted   = regexp.MustCompile(`"([^"\n]*)"`)
	reFileArg  = regexp.MustCompile(`(?im)^\s*\*\s*(xyzfile|gzmtfile|pdbfile)\s+\S+\s+\S+\s+(\S+)`)
	reMaxcore  = regexp.MustCompile(`(?i)^\s*%maxcore\b`)
	rePalStart = regexp.MustCompile(`(?i)^\s*%pal\b`)
	rePalWord  = regexp.MustCompile(`(?i)^pal\d+$`)
	reEnd      = regexp.MustCompile(`(?i)\bend\b`)
	reNewJob   = regexp.MustCompile(`(?i)^\s*\$new_job\b`)
)

// MaxInputBytes bounds input size.
const MaxInputBytes = 4 << 20

// CheckInput returns the list of safety violations (empty = acceptable).
// bundle is the set of file names shipped with the task.
func CheckInput(input string, bundle map[string]bool) []string {
	var v []string
	if len(input) > MaxInputBytes {
		v = append(v, "input larger than 4 MiB")
	}
	clean := reComment.ReplaceAllString(input, " ")
	for _, m := range reDenied.FindAllString(clean, -1) {
		v = append(v, fmt.Sprintf("keyword %q starts an external program (not allowed)", m))
	}
	if reNBO.MatchString(clean) {
		v = append(v, "NBO analysis requires the external gennbo program (not allowed)")
	}
	for _, m := range reQuoted.FindAllStringSubmatch(clean, -1) {
		if !bareName(m[1]) {
			v = append(v, fmt.Sprintf("file reference %q is not a bare file name in the job directory", m[1]))
		}
	}
	for _, m := range reFileArg.FindAllStringSubmatch(clean, -1) {
		name := strings.Trim(m[2], `"`)
		if !bareName(name) {
			v = append(v, fmt.Sprintf("coordinate file %q is not a bare file name", name))
		} else if bundle != nil && !bundle[name] {
			v = append(v, fmt.Sprintf("coordinate file %q is not part of the task", name))
		}
	}
	return v
}

// reSafeName is the only accepted shape of file names. ORCA passes file names to
// system(3) shell commands, so shell metacharacters (; | & $ ` ' " spaces ...) must never
// reach it: they would allow command injection.
var reSafeName = regexp.MustCompile(`^[A-Za-z0-9_+][A-Za-z0-9._+-]{0,127}$`)

// SafeFileName reports whether a name is a plain file name without shell metacharacters.
func SafeFileName(s string) bool { return reSafeName.MatchString(s) && !strings.Contains(s, "..") }

func bareName(s string) bool {
	if s == "" {
		return true // empty strings are harmless
	}
	return SafeFileName(s)
}

// Resources applied to an input by the client (D29). Recorded in the manifest so that the
// server can reproduce the exact normalized input when verifying the echoed input.
type Resources struct {
	MaxcoreMB int `json:"maxcore_mb"`
	Nprocs    int `json:"nprocs"`
}

// Normalize removes every %maxcore line, %pal block and PALn simple keyword, then inserts
// the client's own limits at the top and after each $new_job line. It is deterministic so
// that client and server obtain byte-identical results.
func Normalize(input string, r Resources) string {
	lines := strings.Split(strings.ReplaceAll(input, "\r\n", "\n"), "\n")
	header := []string{fmt.Sprintf("%%maxcore %d", r.MaxcoreMB), fmt.Sprintf("%%pal nprocs %d end", r.Nprocs)}
	out := append([]string{}, header...)
	inPal := false
	for _, ln := range lines {
		if inPal {
			if loc := reEnd.FindStringIndex(stripComment(ln)); loc != nil {
				inPal = false
			}
			continue
		}
		if reMaxcore.MatchString(ln) {
			continue
		}
		if rePalStart.MatchString(ln) {
			if reEnd.FindStringIndex(stripComment(ln)) == nil {
				inPal = true
			}
			continue
		}
		if t := strings.TrimSpace(ln); strings.HasPrefix(t, "!") {
			ln = dropPalKeywords(ln)
		}
		out = append(out, ln)
		if reNewJob.MatchString(ln) {
			out = append(out, header...)
		}
	}
	return strings.Join(out, "\n")
}

func stripComment(s string) string { return reComment.ReplaceAllString(s, " ") }

func dropPalKeywords(ln string) string {
	f := strings.Fields(ln)
	keep := f[:0]
	for _, w := range f {
		if rePalWord.MatchString(w) {
			continue
		}
		keep = append(keep, w)
	}
	if len(keep) == 1 && keep[0] == "!" {
		return "!"
	}
	return strings.Join(keep, " ")
}

// EchoMatches compares the input echoed in an ORCA output with the normalized input that
// should have been run. Trailing whitespace and trailing blank lines are ignored.
func EchoMatches(normalized string, echoed []string) (bool, string) {
	want := trimLines(strings.Split(normalized, "\n"))
	got := trimLines(echoed)
	if len(want) != len(got) {
		return false, fmt.Sprintf("echoed input has %d lines, expected %d", len(got), len(want))
	}
	for i := range want {
		if want[i] != got[i] {
			return false, fmt.Sprintf("echoed input line %d differs: %q vs %q", i+1, got[i], want[i])
		}
	}
	return true, ""
}

func trimLines(in []string) []string {
	out := make([]string, len(in))
	for i, s := range in {
		out[i] = strings.TrimRight(s, " \t\r")
	}
	for len(out) > 0 && out[len(out)-1] == "" {
		out = out[:len(out)-1]
	}
	return out
}

// RequestedProcs returns the number of MPI processes an input asks for ("%pal nprocs N"
// or "! PALn"); 1 when it asks for none.
func RequestedProcs(input string) int {
	n := 1
	inPal := false
	for _, ln := range strings.Split(input, "\n") {
		t := strings.ToLower(strings.TrimSpace(ln))
		if strings.HasPrefix(t, "!") {
			for _, f := range strings.Fields(t[1:]) {
				if strings.HasPrefix(f, "pal") {
					if v, err := strconv.Atoi(f[3:]); err == nil && v > n {
						n = v
					}
				}
			}
		}
		if strings.HasPrefix(t, "%pal") {
			inPal = true
		}
		if inPal {
			f := strings.Fields(t)
			for i := 0; i+1 < len(f); i++ {
				if f[i] == "nprocs" {
					if v, err := strconv.Atoi(f[i+1]); err == nil && v > n {
						n = v
					}
				}
			}
			if strings.Contains(t, "end") {
				inPal = false
			}
		}
	}
	return n
}
