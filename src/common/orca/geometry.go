package orca

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// Geometry consistency: the output must belong to THIS input. A cheater returning a real
// output computed for another input (e.g. an isomer with the same formula, method and
// basis) passes the SCF and gradient checks, which only test the returned files against
// themselves; tying the geometry to the input closes that gap at no cost.

// InputAtoms returns the Cartesian atoms (Å) of an "* xyz c m" block; ok=false for other
// geometry forms (xyzfile, internal coordinates) or special atoms, which are not checked.
func InputAtoms(input string) ([]Atom, bool) {
	var atoms []Atom
	in := false
	for _, ln := range strings.Split(input, "\n") {
		t := strings.TrimSpace(ln)
		switch {
		case !in && reCoordStart.MatchString(ln):
			if f := strings.Fields(strings.TrimPrefix(t, "*")); len(f) == 0 || strings.ToLower(f[0]) != "xyz" {
				return nil, false
			}
			in = true
		case in && strings.HasPrefix(t, "*"):
			return atoms, len(atoms) > 0
		case in && t != "" && !strings.HasPrefix(t, "#"):
			f := strings.Fields(t)
			if len(f) < 4 || strings.ContainsAny(f[0], ":>") {
				return nil, false // point charges, ghost atoms, per-atom options
			}
			x, e1 := strconv.ParseFloat(f[1], 64)
			y, e2 := strconv.ParseFloat(f[2], 64)
			z, e3 := strconv.ParseFloat(f[3], 64)
			if e1 != nil || e2 != nil || e3 != nil {
				return nil, false
			}
			atoms = append(atoms, Atom{element(f[0]), x, y, z})
		}
	}
	return nil, false
}

func element(s string) string {
	s = strings.TrimRight(s, "0123456789")
	if len(s) > 1 {
		return strings.ToUpper(s[:1]) + strings.ToLower(s[1:])
	}
	return strings.ToUpper(s)
}

// SameGeometry: same elements in the same order and coordinates within tol (Å).
func SameGeometry(a, b []Atom, tol float64) (bool, string) {
	if len(a) != len(b) {
		return false, fmt.Sprintf("%d atoms in the input, %d in the output", len(a), len(b))
	}
	for i := range a {
		if element(a[i].Sym) != element(b[i].Sym) {
			return false, fmt.Sprintf("atom %d is %s in the input but %s in the output", i+1, element(a[i].Sym), element(b[i].Sym))
		}
		if d := math.Max(math.Abs(a[i].X-b[i].X), math.Max(math.Abs(a[i].Y-b[i].Y), math.Abs(a[i].Z-b[i].Z))); d > tol {
			return false, fmt.Sprintf("atom %d moved by %.4f Å", i+1, d)
		}
	}
	return true, ""
}

// covalent radii (Å), Cordero et al., Dalton Trans. 2008, 2832, doi:10.1039/b801115j
var covRadius = map[string]float64{"H": 0.31, "He": 0.28, "Li": 1.28, "Be": 0.96, "B": 0.84,
	"C": 0.76, "N": 0.71, "O": 0.66, "F": 0.57, "Ne": 0.58, "Na": 1.66, "Mg": 1.41, "Al": 1.21,
	"Si": 1.11, "P": 1.07, "S": 1.05, "Cl": 1.02, "Ar": 1.06, "K": 2.03, "Ca": 1.76, "Br": 1.20,
	"I": 1.39, "Se": 1.20, "As": 1.19, "Ge": 1.20}

func bonded(a, b Atom) bool {
	ra, ok := covRadius[element(a.Sym)]
	if !ok {
		ra = 1.5
	}
	rb, ok := covRadius[element(b.Sym)]
	if !ok {
		rb = 1.5
	}
	d := math.Sqrt((a.X-b.X)*(a.X-b.X) + (a.Y-b.Y)*(a.Y-b.Y) + (a.Z-b.Z)*(a.Z-b.Z))
	return d < 1.25*(ra+rb)
}

// SameConnectivity: same elements in order and the same bonded pairs (distance below 1.25x
// the sum of covalent radii). An optimization moves atoms but keeps the bonds; another
// isomer does not.
func SameConnectivity(a, b []Atom) (bool, string) {
	if len(a) != len(b) {
		return false, fmt.Sprintf("%d atoms in the input, %d in the output", len(a), len(b))
	}
	for i := range a {
		if element(a[i].Sym) != element(b[i].Sym) {
			return false, fmt.Sprintf("atom %d is %s in the input but %s in the output", i+1, element(a[i].Sym), element(b[i].Sym))
		}
	}
	for i := range a {
		for k := i + 1; k < len(a); k++ {
			if bi, bo := bonded(a[i], a[k]), bonded(b[i], b[k]); bi != bo {
				state := map[bool]string{true: "bonded", false: "not bonded"}
				return false, fmt.Sprintf("atoms %d-%d are %s in the input but %s in the final geometry", i+1, k+1, state[bi], state[bo])
			}
		}
	}
	return true, ""
}

// SameStereo compares the stereochemistry of two geometries with the same connectivity:
// the handedness of every atom with four bonded neighbours (sign of the signed volume of
// its first three neighbours; R/S centres) and the cis/trans arrangement around every
// bond between two three-coordinate atoms (sign of the dihedral; E/Z double bonds).
// An optimization keeps both; an enantiomer or diastereomer passed off as the input's
// molecule does not.
func SameStereo(a, b []Atom) (bool, string) {
	nb := neighbours(a)
	for i, ns := range nb {
		// pyramidal centres with a lone pair that do not invert at room temperature
		// (sulfoxides, sulfonium, phosphines, arsines; NOT amines, which invert easily)
		if len(ns) == 3 && stablePyramid[element(a[i].Sym)] && pyramidal(a, i, ns) && pyramidal(b, i, ns) {
			if va, vb := signedVolume(a, i, ns), signedVolume(b, i, ns); va*vb < 0 {
				return false, fmt.Sprintf("atom %d (%s) has the opposite handedness (pyramidal stereocentre)", i+1, element(a[i].Sym))
			}
		}
		if len(ns) == 4 {
			va, vb := signedVolume(a, i, ns), signedVolume(b, i, ns)
			if va*vb < 0 && math.Abs(va) > 0.1 && math.Abs(vb) > 0.1 {
				return false, fmt.Sprintf("atom %d (%s) has the opposite handedness (enantiomer or diastereomer)", i+1, element(a[i].Sym))
			}
		}
	}
	for i, ns := range nb {
		for _, k := range ns {
			if k <= i || len(ns) != 3 || len(nb[k]) != 3 {
				continue
			}
			p, q := other(ns, k), other(nb[k], i)
			da, db := dihedral(a, p, i, k, q), dihedral(b, p, i, k, q)
			// trans ~180, cis ~0: compare which side, ignoring nearly perpendicular cases
			if math.Abs(math.Abs(da)-90) > 30 && math.Abs(math.Abs(db)-90) > 30 && (math.Abs(da) < 90) != (math.Abs(db) < 90) {
				return false, fmt.Sprintf("bond %d-%d changed between cis and trans", i+1, k+1)
			}
			// clearly twisted (biaryls, helicenes): the sign of the twist is the P/M
			// (axial or helical) chirality
			if twisted(da) && twisted(db) && da*db < 0 {
				return false, fmt.Sprintf("bond %d-%d changed its twist sign (P/M axial or helical chirality)", i+1, k+1)
			}
		}
	}
	// allenes: a nearly linear 2-coordinate centre between two 3-coordinate atoms; the
	// dihedral between substituents on the two ends gives the P/M axial chirality
	for c, ns := range nb {
		if len(ns) != 2 || len(nb[ns[0]]) != 3 || len(nb[ns[1]]) != 3 || angle(a, ns[0], c, ns[1]) < 170 {
			continue
		}
		e1, e2 := ns[0], ns[1]
		p, q := other(nb[e1], c), other(nb[e2], c)
		da, db := dihedral(a, p, e1, e2, q), dihedral(b, p, e1, e2, q)
		if twisted(da) && twisted(db) && da*db < 0 {
			return false, fmt.Sprintf("allene axis %d-%d-%d changed its P/M chirality", e1+1, c+1, e2+1)
		}
	}
	return true, ""
}

var stablePyramid = map[string]bool{"P": true, "S": true, "As": true, "Se": true}

// pyramidal: the three bond angles add up to clearly less than 360 degrees.
func pyramidal(at []Atom, c int, ns []int) bool {
	return angle(at, ns[0], c, ns[1])+angle(at, ns[1], c, ns[2])+angle(at, ns[0], c, ns[2]) < 345
}

// twisted: clearly away from planar (0 or 180 degrees), where the sign is meaningful.
func twisted(d float64) bool { return math.Abs(d) > 20 && math.Abs(d) < 160 }

// angle p-c-q in degrees.
func angle(at []Atom, p, c, q int) float64 {
	u, v := vec(at, c, p), vec(at, c, q)
	return math.Acos(math.Max(-1, math.Min(1, dot(u, v)/math.Sqrt(dot(u, u)*dot(v, v))))) * 180 / math.Pi
}

func neighbours(at []Atom) [][]int {
	nb := make([][]int, len(at))
	for i := range at {
		for k := range at {
			if i != k && bonded(at[i], at[k]) {
				nb[i] = append(nb[i], k)
			}
		}
	}
	return nb
}

func other(ns []int, not int) int {
	for _, n := range ns {
		if n != not {
			return n
		}
	}
	return not
}

func vec(at []Atom, from, to int) [3]float64 {
	return [3]float64{at[to].X - at[from].X, at[to].Y - at[from].Y, at[to].Z - at[from].Z}
}

func cross(u, v [3]float64) [3]float64 {
	return [3]float64{u[1]*v[2] - u[2]*v[1], u[2]*v[0] - u[0]*v[2], u[0]*v[1] - u[1]*v[0]}
}

func dot(u, v [3]float64) float64 { return u[0]*v[0] + u[1]*v[1] + u[2]*v[2] }

func signedVolume(at []Atom, c int, ns []int) float64 {
	return dot(vec(at, c, ns[0]), cross(vec(at, c, ns[1]), vec(at, c, ns[2])))
}

// dihedral p-i-k-q in degrees.
func dihedral(at []Atom, p, i, k, q int) float64 {
	b0, b1, b2 := vec(at, i, p), vec(at, i, k), vec(at, k, q)
	n1, n2 := cross(b0, b1), cross(b1, b2)
	m := cross(n1, b1)
	l := math.Sqrt(dot(b1, b1))
	x, y := dot(n1, n2), dot(m, n2)/l
	return math.Atan2(y, x) * 180 / math.Pi
}

// MirrorMatch reports whether b superposes clearly better on the MIRROR IMAGE of a than on
// a itself: then b is a's enantiomer (or a mirror-image conformer), whatever kind of
// chirality the molecule has (central, axial, helical or planar, e.g. a 1,2-disubstituted
// ferrocene, which the local checks above cannot see). Superposition: optimal rotation
// after centring, by Horn's quaternion method (J. Opt. Soc. Am. A 4 (1987) 629,
// doi:10.1364/JOSAA.4.000629). An achiral molecule superposes on its mirror image just as
// well, so it is never flagged.
func MirrorMatch(a, b []Atom) (bool, string) {
	if len(a) != len(b) || len(a) < 4 {
		return false, ""
	}
	mirror := make([]Atom, len(a))
	for i, x := range a {
		mirror[i] = Atom{x.Sym, -x.X, x.Y, x.Z}
	}
	d, m := superposedRMSD(a, b), superposedRMSD(mirror, b)
	if d > 0.3 && m < 0.6*d {
		return true, fmt.Sprintf("the final geometry matches the mirror image of the input (RMSD %.2f Å) much better than the input (%.2f Å)", m, d)
	}
	return false, ""
}

// superposedRMSD is the RMSD (Å) of b on a after centring both and rotating b optimally.
func superposedRMSD(a, b []Atom) float64 {
	n := float64(len(a))
	ca, cb := centroid(a), centroid(b)
	var S [3][3]float64 // correlation matrix
	var ga, gb float64
	for i := range a {
		p := [3]float64{a[i].X - ca[0], a[i].Y - ca[1], a[i].Z - ca[2]}
		q := [3]float64{b[i].X - cb[0], b[i].Y - cb[1], b[i].Z - cb[2]}
		for r := 0; r < 3; r++ {
			for c := 0; c < 3; c++ {
				S[r][c] += q[r] * p[c]
			}
		}
		ga += dot(p, p)
		gb += dot(q, q)
	}
	// Horn's symmetric 4x4 matrix; its largest eigenvalue is the best inner product
	N := [4][4]float64{
		{S[0][0] + S[1][1] + S[2][2], S[1][2] - S[2][1], S[2][0] - S[0][2], S[0][1] - S[1][0]},
		{S[1][2] - S[2][1], S[0][0] - S[1][1] - S[2][2], S[0][1] + S[1][0], S[2][0] + S[0][2]},
		{S[2][0] - S[0][2], S[0][1] + S[1][0], -S[0][0] + S[1][1] - S[2][2], S[1][2] + S[2][1]},
		{S[0][1] - S[1][0], S[2][0] + S[0][2], S[1][2] + S[2][1], -S[0][0] - S[1][1] + S[2][2]},
	}
	lmax := maxEigen4(N)
	return math.Sqrt(math.Max(0, (ga+gb-2*lmax)/n))
}

func centroid(at []Atom) [3]float64 {
	var c [3]float64
	for _, x := range at {
		c[0], c[1], c[2] = c[0]+x.X, c[1]+x.Y, c[2]+x.Z
	}
	n := float64(len(at))
	return [3]float64{c[0] / n, c[1] / n, c[2] / n}
}

// maxEigen4 returns the largest eigenvalue of a symmetric 4x4 matrix (cyclic Jacobi).
func maxEigen4(A [4][4]float64) float64 {
	for sweep := 0; sweep < 50; sweep++ {
		off := 0.0
		for p := 0; p < 4; p++ {
			for q := p + 1; q < 4; q++ {
				off += A[p][q] * A[p][q]
			}
		}
		if off < 1e-22 {
			break
		}
		for p := 0; p < 4; p++ {
			for q := p + 1; q < 4; q++ {
				if math.Abs(A[p][q]) < 1e-300 {
					continue
				}
				theta := (A[q][q] - A[p][p]) / (2 * A[p][q])
				t := 1 / (math.Abs(theta) + math.Sqrt(theta*theta+1))
				if theta < 0 {
					t = -t
				}
				c := 1 / math.Sqrt(t*t+1)
				sn := t * c
				for k := 0; k < 4; k++ {
					akp, akq := A[k][p], A[k][q]
					A[k][p], A[k][q] = c*akp-sn*akq, sn*akp+c*akq
				}
				for k := 0; k < 4; k++ {
					apk, aqk := A[p][k], A[q][k]
					A[p][k], A[q][k] = c*apk-sn*aqk, sn*apk+c*aqk
				}
			}
		}
	}
	m := A[0][0]
	for i := 1; i < 4; i++ {
		m = math.Max(m, A[i][i])
	}
	return m
}
