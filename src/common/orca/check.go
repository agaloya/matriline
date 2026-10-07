package orca

import (
	"bufio"
	"errors"
	"fmt"
	"math"
	"os"
	"regexp"
	"strconv"
	"strings"
)

// Helpers for the active verification layers (D44). The tricks were validated against real
// ORCA 6.1.1 runs in this project:
//   - "! MORead" with the returned .gbw and a capped "%scf MaxIter": genuine converged orbitals
//     usually converge in <= 2 cycles (4 seen for a phenol at its final Opt geometry) and reproduce the energy (to ~1e-7 Eh); orbitals from another
//     geometry or method stop with "SCF NOT CONVERGED" (and ORCA aborts).
//   - "! NoIter" is NOT usable: ORCA 6.1.1 prints FINAL SINGLE POINT ENERGY 0.0 with it.
//   - With RIJCOSX (the default for hybrid functionals) ORCA recomputes the exchange on a
//     larger grid after the SCF ("Exchange energy change after final integration"). That
//     non-variational correction depends on how the SCF ended: the same converged orbitals
//     gave 9.7e-6 Eh in one run and 5.5e-5 Eh in the check (B3LYP/def2-SVP water), while the
//     energy before the correction agreed to 2e-8 Eh. Checks compare that energy (SCFInfo.SCF).
//   - "! EnGrad" at the final geometry prints "Norm of the Cartesian gradient"; ~1e-5 for a
//     converged optimization.

// BohrPerAngstrom converts Å to bohr (CODATA 2018: a0 = 0.529177210903 Å).
const BohrPerAngstrom = 1 / 0.529177210903

// Atom is one atom with Cartesian coordinates.
type Atom struct {
	Sym     string
	X, Y, Z float64
}

var (
	reCartHeader = regexp.MustCompile(`^CARTESIAN COORDINATES \(ANGSTROEM\)`)
	reSCFTotal   = regexp.MustCompile(`^Total Energy\s+:\s+(-?\d+\.\d+)\s+Eh`)
	reSCFCycles  = regexp.MustCompile(`SCF CONVERGED AFTER\s+(\d+)\s+CYCLES`)
	reXChange    = regexp.MustCompile(`^Exchange energy change after final integration\s+:\s+(-?[0-9.Ee+-]+)\s+Eh`)
	reGradNorm   = regexp.MustCompile(`Norm of the Cartesian gradient\s+\.\.\.\s+([0-9.Ee+-]+)`)
	reGradMax    = regexp.MustCompile(`^MAX gradient\s+\.\.\.\s+([0-9.Ee+-]+)`)
	reCoordStart = regexp.MustCompile(`(?i)^\s*\*\s*(xyz|int|internal|gzmt)\s+(-?\d+)\s+(\d+)\s*$`)
	reCoordFile  = regexp.MustCompile(`(?i)^\s*\*\s*(xyzfile|gzmtfile|pdbfile)\s+(-?\d+)\s+(\d+)\s+\S+`)
	reJobKw      = regexp.MustCompile(`(?i)^(opt|copt|zopt|gdiis-opt|looseopt|normalopt|tightopt|verytightopt|optts|scants|freq|numfreq|anfreq|numgrad|engrad|sp|neb.*|irc|md|goat.*|mdci-opt|moread|autostart|noautostart|keepdens|largeprint|printbasis)$`)
	reBaseLine   = regexp.MustCompile(`(?i)^\s*%(moinp|base)\b`)
)

// SCFInfo summarizes the SCF parts of an output.
type SCFInfo struct {
	LastTotal float64 // last "Total Energy :" of an SCF summary
	HasTotal  bool
	SCF       float64 // LastTotal without the COSX final-grid correction (variational energy)
	Cycles    []int
	GradNorm  float64
	GradMax   float64 // largest Cartesian gradient component ("MAX gradient")
	HasGrad   bool
	Final     []Atom // last CARTESIAN COORDINATES (ANGSTROEM) block
	First     []Atom // first such block (the geometry ORCA started from)
}

// ParseSCF scans an output for SCF energies, cycle counts, gradient norm and geometry.
func ParseSCF(path string) (*SCFInfo, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	in := &SCFInfo{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 256<<10), 16<<20)
	var lines []string
	for sc.Scan() {
		lines = append(lines, sc.Text())
	}
	xchange := 0.0 // final-grid correction of the SCF being read
	for i := 0; i < len(lines); i++ {
		t := strings.TrimSpace(lines[i])
		if m := reXChange.FindStringSubmatch(t); m != nil {
			xchange, _ = strconv.ParseFloat(m[1], 64)
		}
		if m := reSCFTotal.FindStringSubmatch(t); m != nil {
			in.LastTotal, _ = strconv.ParseFloat(m[1], 64)
			in.SCF = in.LastTotal - xchange
			in.HasTotal = true
			xchange = 0
		}
		if m := reSCFCycles.FindStringSubmatch(t); m != nil {
			n, _ := strconv.Atoi(m[1])
			in.Cycles = append(in.Cycles, n)
		}
		if m := reGradMax.FindStringSubmatch(t); m != nil {
			in.GradMax, _ = strconv.ParseFloat(m[1], 64)
		}
		if m := reGradNorm.FindStringSubmatch(t); m != nil {
			in.GradNorm, _ = strconv.ParseFloat(m[1], 64)
			in.HasGrad = true
		}
		if reCartHeader.MatchString(t) {
			var atoms []Atom
			for k := i + 2; k < len(lines); k++ { // skip the dashes line
				f := strings.Fields(lines[k])
				if len(f) != 4 {
					break
				}
				x, e1 := strconv.ParseFloat(f[1], 64)
				y, e2 := strconv.ParseFloat(f[2], 64)
				z, e3 := strconv.ParseFloat(f[3], 64)
				if e1 != nil || e2 != nil || e3 != nil {
					break
				}
				atoms = append(atoms, Atom{f[0], x, y, z})
			}
			if len(atoms) > 0 {
				in.Final = atoms
				if in.First == nil {
					in.First = atoms
				}
			}
		}
	}
	return in, sc.Err()
}

// WriteXYZ writes atoms (Å) in XYZ format.
func WriteXYZ(path string, atoms []Atom, comment string) error {
	var b strings.Builder
	fmt.Fprintf(&b, "%d\n%s\n", len(atoms), comment)
	for _, a := range atoms {
		fmt.Fprintf(&b, "%-3s %18.10f %18.10f %18.10f\n", a.Sym, a.X, a.Y, a.Z)
	}
	return os.WriteFile(path, []byte(b.String()), 0o644)
}

// ChargeMult returns charge and multiplicity from the coordinate line of an input.
func ChargeMult(input string) (int, int, error) {
	for _, ln := range strings.Split(input, "\n") {
		if m := reCoordStart.FindStringSubmatch(ln); m != nil {
			c, _ := strconv.Atoi(m[2])
			mu, _ := strconv.Atoi(m[3])
			return c, mu, nil
		}
		if m := reCoordFile.FindStringSubmatch(ln); m != nil {
			c, _ := strconv.Atoi(m[2])
			mu, _ := strconv.Atoi(m[3])
			return c, mu, nil
		}
	}
	return 0, 0, errors.New("no coordinate line with charge and multiplicity")
}

// ErrUnsupported marks inputs the active checks cannot handle (they are skipped, not failed).
var ErrUnsupported = errors.New("input structure not supported by active checks")

// CheckInput builds a verification input from an original input: same method, basis and
// blocks; job-type keywords removed; geometry replaced by an xyz file; orbitals read from
// gbwName (if not empty). extra keywords (e.g. "EnGrad") and extra block lines are added.
func BuildCheckInput(original, xyzName, gbwName string, extraKw []string, extraBlocks []string) (string, error) {
	low := strings.ToLower(original)
	if strings.Contains(low, "$new_job") || strings.Contains(low, "%compound") || strings.Contains(low, "%coords") {
		return "", ErrUnsupported
	}
	charge, mult, err := ChargeMult(original)
	if err != nil {
		return "", ErrUnsupported
	}
	lines := strings.Split(strings.ReplaceAll(original, "\r\n", "\n"), "\n")
	var out []string
	inCoords := false
	for _, ln := range lines {
		t := strings.TrimSpace(ln)
		if inCoords {
			if t == "*" {
				inCoords = false
			} else if f := strings.Fields(t); len(f) > 4 {
				return "", ErrUnsupported // per-atom basis sets, fragments, ... in the coordinates
			}
			continue
		}
		switch {
		case reCoordStart.MatchString(ln):
			inCoords = true
			continue
		case reCoordFile.MatchString(ln), reBaseLine.MatchString(ln):
			continue
		case strings.HasPrefix(t, "!"):
			var keep []string
			for _, w := range strings.Fields(strings.TrimPrefix(t, "!")) {
				if !reJobKw.MatchString(w) {
					keep = append(keep, w)
				}
			}
			if len(keep) > 0 {
				out = append(out, "! "+strings.Join(keep, " "))
			}
			continue
		}
		out = append(out, ln)
	}
	kw := append([]string{}, extraKw...)
	if gbwName != "" {
		kw = append(kw, "MORead")
	}
	if len(kw) > 0 {
		out = append(out, "! "+strings.Join(kw, " "))
	}
	if gbwName != "" {
		out = append(out, fmt.Sprintf("%%moinp \"%s\"", gbwName))
	}
	out = append(out, extraBlocks...)
	out = append(out, fmt.Sprintf("* xyzfile %d %d %s", charge, mult, xyzName))
	return strings.Join(out, "\n") + "\n", nil
}

// ---------------------------------------------------------------------------------------
// .hess and .engrad files

// ParseHess reads the $hessian (Eh/bohr^2) and $atoms (bohr) blocks of an ORCA .hess file.
func ParseHess(path string) ([]Atom, [][]float64, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	lines := strings.Split(string(b), "\n")
	var H [][]float64
	var atoms []Atom
	for i := 0; i < len(lines); i++ {
		switch strings.TrimSpace(lines[i]) {
		case "$hessian":
			n, err := strconv.Atoi(strings.TrimSpace(lines[i+1]))
			if err != nil || n <= 0 || n > 3000 {
				return nil, nil, errors.New("bad $hessian dimension")
			}
			H = make([][]float64, n)
			for r := range H {
				H[r] = make([]float64, n)
			}
			k := i + 2
			for col0 := 0; col0 < n; {
				hdr := strings.Fields(lines[k])
				ncol := len(hdr)
				if ncol == 0 {
					return nil, nil, errors.New("bad $hessian block")
				}
				for r := 0; r < n; r++ {
					f := strings.Fields(lines[k+1+r])
					if len(f) != ncol+1 {
						return nil, nil, errors.New("bad $hessian row")
					}
					for c := 0; c < ncol; c++ {
						v, err := strconv.ParseFloat(f[c+1], 64)
						if err != nil {
							return nil, nil, err
						}
						H[r][col0+c] = v
					}
				}
				col0 += ncol
				k += n + 1
			}
		case "$atoms":
			n, _ := strconv.Atoi(strings.TrimSpace(lines[i+1]))
			for k := 0; k < n && i+2+k < len(lines); k++ {
				f := strings.Fields(lines[i+2+k])
				if len(f) < 5 {
					return nil, nil, errors.New("bad $atoms line")
				}
				x, _ := strconv.ParseFloat(f[2], 64)
				y, _ := strconv.ParseFloat(f[3], 64)
				z, _ := strconv.ParseFloat(f[4], 64)
				atoms = append(atoms, Atom{f[0], x, y, z})
			}
		}
	}
	if H == nil || len(atoms)*3 != len(H) {
		return nil, nil, errors.New("hessian and atoms missing or inconsistent")
	}
	return atoms, H, nil
}

// ParseEngrad reads energy (Eh) and gradient (Eh/bohr) from an ORCA .engrad file.
func ParseEngrad(path string) (float64, []float64, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, nil, err
	}
	var nums []string
	for _, ln := range strings.Split(string(b), "\n") {
		t := strings.TrimSpace(ln)
		if t == "" || strings.HasPrefix(t, "#") {
			continue
		}
		nums = append(nums, strings.Fields(t)...)
	}
	if len(nums) < 2 {
		return 0, nil, errors.New("short engrad file")
	}
	n, err := strconv.Atoi(nums[0])
	if err != nil || len(nums) < 2+3*n {
		return 0, nil, errors.New("bad engrad file")
	}
	e, err := strconv.ParseFloat(nums[1], 64)
	if err != nil {
		return 0, nil, err
	}
	g := make([]float64, 3*n)
	for i := range g {
		if g[i], err = strconv.ParseFloat(nums[2+i], 64); err != nil {
			return 0, nil, err
		}
	}
	return e, g, nil
}

// Displace returns atoms (bohr in, Å out) moved by h*v (v in bohr units, len 3N).
func Displace(atomsBohr []Atom, v []float64, h float64) []Atom {
	out := make([]Atom, len(atomsBohr))
	for i, a := range atomsBohr {
		out[i] = Atom{a.Sym,
			(a.X + h*v[3*i]) / BohrPerAngstrom,
			(a.Y + h*v[3*i+1]) / BohrPerAngstrom,
			(a.Z + h*v[3*i+2]) / BohrPerAngstrom}
	}
	return out
}

// HessianProbeError compares H·v with the central finite difference (g+ - g-)/(2h).
// Returns ||Hv - d|| / max(||Hv||, 1e-6).
func HessianProbeError(H [][]float64, v, gPlus, gMinus []float64, h float64) float64 {
	n := len(v)
	var num, den float64
	for r := 0; r < n; r++ {
		hv := 0.0
		for c := 0; c < n; c++ {
			hv += H[r][c] * v[c]
		}
		d := (gPlus[r] - gMinus[r]) / (2 * h)
		num += (hv - d) * (hv - d)
		den += hv * hv
	}
	return math.Sqrt(num) / math.Max(math.Sqrt(den), 1e-6)
}

// MimicCoords rewrites the "* xyzfile" line of a check input (BuildCheckInput) so that
// its geometry is given the way the original input gives it: inline coordinates, or an
// xyz file with the original's file name. A canary must look like an ordinary task, or a
// cheater could compute canaries honestly and forge everything else. xyzName is the file
// to write the geometry to ("" = inline, nothing to write).
func MimicCoords(original, check string, atoms []Atom) (input, xyzName string) {
	lines := strings.Split(strings.TrimRight(check, "\n"), "\n")
	last := lines[len(lines)-1]
	m := reCoordFile.FindStringSubmatch(last)
	if m == nil {
		return check, "geom.xyz"
	}
	for _, ln := range strings.Split(strings.ReplaceAll(original, "\r\n", "\n"), "\n") {
		if c := reCoordStart.FindStringSubmatch(ln); c != nil && strings.EqualFold(c[1], "xyz") {
			var b strings.Builder
			fmt.Fprintf(&b, "* xyz %s %s\n", m[2], m[3])
			for _, a := range atoms {
				fmt.Fprintf(&b, "  %-2s %14.8f %14.8f %14.8f\n", a.Sym, a.X, a.Y, a.Z)
			}
			b.WriteString("*")
			lines[len(lines)-1] = b.String()
			return strings.Join(lines, "\n") + "\n", ""
		}
		if f := reCoordFile.FindStringSubmatch(ln); f != nil && strings.EqualFold(f[1], "xyzfile") {
			name := strings.Fields(ln)[4]
			if strings.ContainsAny(name, "/\\") || name == "" {
				break
			}
			lines[len(lines)-1] = fmt.Sprintf("* xyzfile %s %s %s", m[2], m[3], name)
			return strings.Join(lines, "\n") + "\n", name
		}
	}
	return check, "geom.xyz"
}
