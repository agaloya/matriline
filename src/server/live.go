package main

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"
)

// The live view (matriline-server watch, and the web page): what every client computes,
// and for one of them the input, a drawing of the molecule and the latest output lines.

type liveRow struct {
	n                  int
	client, task, kind string
	label              string // what the admin sees: the task, or the task a check verifies
	started            time.Time
	progress           int64
	tail               string
	lost               bool
	phase              string // starting, computing, uploading (phaseOf)
}

// phaseOf names what an attempt is doing, for the live views.
func phaseOf(a *Attempt) string {
	switch {
	case a.Lost:
		return "lost"
	case a.Phase == "uploading" || a.Phase == "finished":
		return "uploading"
	case a.Phase == "running" || a.Progress > 0:
		return "computing"
	}
	return "starting"
}

func (s *Server) liveRows() []liveRow {
	s.store.mu.Lock()
	var rows []liveRow
	for _, a := range s.store.Attempts {
		name := a.ClientID
		if c := s.reg.get(a.ClientID); c != nil {
			name = c.Name
		}
		r := liveRow{client: name, task: a.TaskID, label: a.TaskID, kind: a.Kind, started: a.Started, progress: a.Progress, tail: a.Tail, lost: a.Lost, phase: phaseOf(a)}
		// a verification sub-task: show which result it checks and how (scf, grad, replica...)
		if t := s.store.Tasks[a.TaskID]; t != nil && t.Internal {
			if ck := s.store.Checks[t.CheckID]; ck != nil {
				r.label, r.kind = ck.TaskID, "check:"+nz(ck.Subtasks[a.TaskID], "?")
			}
		}
		rows = append(rows, r)
	}
	s.store.mu.Unlock()
	sort.Slice(rows, func(i, k int) bool {
		if rows[i].client != rows[k].client {
			return rows[i].client < rows[k].client
		}
		return rows[i].label < rows[k].label
	})
	for i := range rows {
		rows[i].n = i + 1
	}
	return rows
}

// cmdLive: live [n] [full] - the running jobs; job n in detail (default the first); full
// shows the whole input instead of its first lines.
func (s *Server) cmdLive(a []string) (string, error) {
	sel, full := 1, false
	for _, w := range a {
		if w == "full" {
			full = true
		} else if v, err := strconv.Atoi(w); err == nil && v > 0 {
			sel = v
		} else {
			return "", fmt.Errorf("usage: live [job number] [full]")
		}
	}
	rows := s.liveRows()
	var b strings.Builder
	fmt.Fprintf(&b, "%s   %d job(s) running\n\n", time.Now().Format("15:04:05"), len(rows))
	if len(rows) == 0 {
		b.WriteString("nothing runs right now\n")
		return b.String(), nil
	}
	fmt.Fprintf(&b, "  #  %-14s %-40s %-13s %-9s %9s %10s\n", "CLIENT", "TASK", "KIND", "PHASE", "TIME", "OUTPUT")
	for _, r := range rows {
		mark := " "
		if r.n == sel {
			mark = ">"
		}
		kind := r.kind
		if r.lost {
			kind = "lost"
		}
		fmt.Fprintf(&b, "%s%2d  %-14s %-40s %-13s %-9s %9s %10s\n", mark, r.n, clip(r.client, 14), clip(r.label, 40), clip(kind, 13),
			r.phase, time.Since(r.started).Round(time.Second), humanBytes(r.progress))
	}
	if sel > len(rows) {
		sel = len(rows)
	}
	r := rows[sel-1]
	what := r.label
	if r.label != r.task {
		what = r.kind + " of " + r.label
	}
	fmt.Fprintf(&b, "\n== %d: %s on %s ==\n", r.n, what, r.client)
	in, err := s.taskInput(r.task)
	if err == nil {
		lines := strings.Split(strings.TrimRight(string(in), "\n"), "\n")
		for i, l := range lines { // no control characters to the admin's terminal (code review)
			lines[i] = strings.Map(func(r rune) rune {
				if r < 32 && r != '\t' || r == 127 || r >= 0x80 && r < 0xa0 {
					return -1
				}
				return r
			}, l)
		}
		if !full && len(lines) > 12 {
			lines = append(lines[:12], fmt.Sprintf("... (%d more lines; 'full' shows them)", len(lines)-12))
		}
		b.WriteString("-- input --\n" + strings.Join(lines, "\n") + "\n")
		if d := cachedBraille(string(in)); d != "" {
			b.WriteString("-- molecule --\n" + d)
		}
	}
	b.WriteString("-- output (latest lines) --\n")
	if r.tail == "" {
		b.WriteString("(nothing reported yet)\n")
	} else {
		b.WriteString(r.tail + "\n")
	}
	return b.String(), nil
}

// cleanTail keeps what a client reports of its output printable: no control characters
// (escape sequences would reach the admin's terminal in 'watch'), at most 8 lines of 160
// characters (code review).
func cleanTail(s string) string {
	lines := strings.Split(s, "\n")
	if len(lines) > 8 {
		lines = lines[len(lines)-8:]
	}
	for i, l := range lines {
		l = strings.Map(func(r rune) rune {
			switch {
			case r == '\t':
				return ' '
			case r >= 0x20 && r < 0x7f:
				return r
			case r < 0x20 || r == 0x7f:
				return -1
			}
			return '?' // ORCA writes ASCII: anything else (bidi, zero-width, combining) is shown as ?
		}, l)
		if len(l) > 160 {
			l = l[:160]
		}
		lines[i] = l
	}
	return strings.Join(lines, "\n")
}

// cleanWord keeps a short self-reported label (program version, platform) to safe
// characters.
func cleanWord(s string) string {
	s = strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("._-/()+ ", r) {
			return r
		}
		return -1
	}, s)
	if len(s) > 64 {
		s = s[:64]
	}
	return s
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return "…" + s[len(s)-n+1:]
}

func humanBytes(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.0f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}

type atom struct {
	el      string
	x, y, z float64
}

// parseXYZ reads the "* xyz charge mult ... *" block of an ORCA input.
func parseXYZ(in string) []atom {
	var out []atom
	inBlock := false
	for _, l := range strings.Split(in, "\n") {
		f := strings.Fields(l)
		if len(f) == 0 {
			continue
		}
		if !inBlock {
			if f[0] == "*" && len(f) >= 2 && strings.EqualFold(f[1], "xyz") || strings.EqualFold(f[0], "*xyz") {
				inBlock = true
			}
			continue
		}
		if f[0] == "*" {
			break
		}
		if len(f) < 4 {
			continue
		}
		var c [3]float64
		ok := true
		for i := 0; i < 3; i++ {
			v, err := strconv.ParseFloat(f[i+1], 64)
			ok = ok && err == nil
			c[i] = v
		}
		// element symbols only: letters, normalised ("CL" -> "Cl"); "Q" (point charges) and
		// anything else is skipped (a symbol reaches the admin's terminal: code review)
		if el := strings.TrimRight(f[0], "0123456789:"); ok && validElement(el) {
			out = append(out, atom{strings.ToUpper(el[:1]) + strings.ToLower(el[1:]), c[0], c[1], c[2]})
		}
	}
	return out
}

// covalent radii (Å), Cordero et al., Dalton Trans. 2008, 2832 (low-spin values for Mn, Fe,
// Co; sp3 carbon); others count as 1.5
var covRadius = map[string]float64{
	"H": 0.31, "He": 0.28, "Li": 1.28, "Be": 0.96, "B": 0.84, "C": 0.76, "N": 0.71, "O": 0.66, "F": 0.57, "Ne": 0.58,
	"Na": 1.66, "Mg": 1.41, "Al": 1.21, "Si": 1.11, "P": 1.07, "S": 1.05, "Cl": 1.02, "Ar": 1.06,
	"K": 2.03, "Ca": 1.76, "Sc": 1.70, "Ti": 1.60, "V": 1.53, "Cr": 1.39, "Mn": 1.39, "Fe": 1.32, "Co": 1.26,
	"Ni": 1.24, "Cu": 1.32, "Zn": 1.22, "Ga": 1.22, "Ge": 1.20, "As": 1.19, "Se": 1.20, "Br": 1.20, "Kr": 1.16,
	"Rb": 2.20, "Sr": 1.95, "Y": 1.90, "Zr": 1.75, "Nb": 1.64, "Mo": 1.54, "Tc": 1.47, "Ru": 1.46, "Rh": 1.42,
	"Pd": 1.39, "Ag": 1.45, "Cd": 1.44, "In": 1.42, "Sn": 1.39, "Sb": 1.39, "Te": 1.38, "I": 1.39, "Xe": 1.40,
	"Cs": 2.44, "Ba": 2.15, "La": 2.07, "Ce": 2.04, "Pr": 2.03, "Nd": 2.01, "Pm": 1.99, "Sm": 1.98, "Eu": 1.98,
	"Gd": 1.96, "Tb": 1.94, "Dy": 1.92, "Ho": 1.92, "Er": 1.89, "Tm": 1.90, "Yb": 1.87, "Lu": 1.87,
	"Hf": 1.75, "Ta": 1.70, "W": 1.62, "Re": 1.51, "Os": 1.44, "Ir": 1.41, "Pt": 1.36, "Au": 1.36, "Hg": 1.32,
	"Tl": 1.45, "Pb": 1.46, "Bi": 1.48, "Po": 1.40, "At": 1.50, "Rn": 1.50, "Fr": 2.60, "Ra": 2.21,
	"Ac": 2.15, "Th": 2.06, "Pa": 2.00, "U": 1.96, "Np": 1.90, "Pu": 1.87, "Am": 1.80, "Cm": 1.69,
}

func radius(el string) float64 {
	if r, ok := covRadius[el]; ok {
		return r
	}
	return 1.5
}

// validElement: one to three letters, not ORCA's point charge "Q" or dummy "DA"
func validElement(s string) bool {
	if len(s) == 0 || len(s) > 3 || strings.EqualFold(s, "Q") || strings.EqualFold(s, "DA") {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z') {
			return false
		}
	}
	return true
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

// jacobi3 diagonalizes a symmetric 3x3 matrix: eigenvalues and eigenvectors (rows).
func jacobi3(a [3][3]float64) ([3]float64, [3][3]float64) {
	v := [3][3]float64{{1, 0, 0}, {0, 1, 0}, {0, 0, 1}}
	for sweep := 0; sweep < 50; sweep++ {
		off := a[0][1]*a[0][1] + a[0][2]*a[0][2] + a[1][2]*a[1][2]
		if off < 1e-18 {
			break
		}
		for p := 0; p < 2; p++ {
			for q := p + 1; q < 3; q++ {
				if math.Abs(a[p][q]) < 1e-15 {
					continue
				}
				th := 0.5 * math.Atan2(2*a[p][q], a[q][q]-a[p][p])
				cs, sn := math.Cos(th), math.Sin(th)
				for k := 0; k < 3; k++ { // A = J^T A J
					akp, akq := a[k][p], a[k][q]
					a[k][p], a[k][q] = cs*akp-sn*akq, sn*akp+cs*akq
				}
				for k := 0; k < 3; k++ {
					apk, aqk := a[p][k], a[q][k]
					a[p][k], a[q][k] = cs*apk-sn*aqk, sn*apk+cs*aqk
				}
				for k := 0; k < 3; k++ {
					vkp, vkq := v[k][p], v[k][q]
					v[k][p], v[k][q] = cs*vkp-sn*vkq, sn*vkp+cs*vkq
				}
			}
		}
	}
	var vecs [3][3]float64
	for i := 0; i < 3; i++ {
		for k := 0; k < 3; k++ {
			vecs[i][k] = v[k][i] // column i of V = eigenvector i
		}
	}
	return [3]float64{a[0][0], a[1][1], a[2][2]}, vecs
}
