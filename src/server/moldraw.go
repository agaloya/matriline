package main

import (
	"bytes"
	"crypto/sha256"
	"image"
	"image/color"
	"image/png"
	"math"
	"math/rand"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// Drawings of a job's molecule for the terminal (user: the letters-and-dots drawing was
// unreadable for large molecules): a skeletal formula, as chemists draw on paper (carbons
// as corners, hydrogens hidden except on N and O, double bonds from the bond lengths), in
// Unicode braille dots for every terminal, or as a picture in the style of RDKit's 2D
// depictions for terminals that show images (termimage.go). Standard library only.

type molAtom struct {
	el            string
	x, y, z       float64
	px, py, depth float64 // after the view is chosen
}

type molecule struct {
	atoms  []molAtom
	bonds  [][2]int
	order  []int    // bond order: 1, 2, 3 (aromatic rings get alternating 2s)
	labels []string // what is written at each atom ("" for C and H)
}

// newMolecule reads the input's xyz block; nil without one (or with more than 300 atoms).
func newMolecule(in string) *molecule {
	at := parseXYZ(in)
	if len(at) < 2 || len(at) > 300 {
		return nil
	}
	m := &molecule{}
	for _, a := range at {
		m.atoms = append(m.atoms, molAtom{el: a.el, x: a.x, y: a.y, z: a.z})
	}
	for i := range m.atoms {
		for k := i + 1; k < len(m.atoms); k++ {
			if m.dist(i, k) < 1.2*(radius(m.atoms[i].el)+radius(m.atoms[k].el)) {
				m.bonds = append(m.bonds, [2]int{i, k})
			}
		}
	}
	if len(m.bonds) > 4*len(m.atoms) { // not a molecule (atoms piled up): no drawing
		return nil
	}
	m.bondOrders()
	m.labels = make([]string, len(m.atoms))
	for i := range m.atoms {
		m.labels[i] = m.label(i)
	}
	m.bestView()
	return m
}

func (m *molecule) dist(i, k int) float64 {
	a, b := m.atoms[i], m.atoms[k]
	return math.Sqrt((a.x-b.x)*(a.x-b.x) + (a.y-b.y)*(a.y-b.y) + (a.z-b.z)*(a.z-b.z))
}

func (m *molecule) heavy(i int) bool { return m.atoms[i].el != "H" }

// multiple bonds are drawn only between these elements (a metal-carbon bond is single:
// metal carbonyls drew M=C=O); valence caps the bonds an atom can take (S, P, Se: none)
var (
	multiOK = map[string]bool{"B": true, "C": true, "N": true, "O": true, "P": true, "S": true, "Se": true, "Si": true}
	valence = map[string]int{"B": 3, "C": 4, "N": 3, "O": 2, "Si": 4}
)

// bondOrders from the lengths (how much shorter than the sum of covalent radii): > 0.27 Å
// triple (C≡C, C≡N, C≡O, N≡N), > 0.15 Å double, > 0.07 Å aromatic, else single; no atom
// beyond its valence (hydrogens counted: pyrrole's N-H takes no double bond). The aromatic
// bonds get a Kekulé structure: the most double bonds an atom can take one of, from 64
// shuffled greedy passes (fixed seeds: the same picture every time; one pass alone left
// benzene short in a third of the atom orders: code review).
func (m *molecule) bondOrders() {
	m.order = make([]int, len(m.bonds))
	used := make([]int, len(m.atoms)) // bond valence used so far
	for n, b := range m.bonds {
		m.order[n] = 1
		used[b[0]]++
		used[b[1]]++
	}
	free := func(i int) int {
		v, ok := valence[m.atoms[i].el]
		if !ok {
			return 6
		}
		return v - used[i]
	}
	type cand struct {
		n     int
		short float64
	}
	var multi, arom []cand
	for n, b := range m.bonds {
		i, k := b[0], b[1]
		if !m.heavy(i) || !m.heavy(k) || !multiOK[m.atoms[i].el] || !multiOK[m.atoms[k].el] {
			continue
		}
		short := radius(m.atoms[i].el) + radius(m.atoms[k].el) - m.dist(i, k)
		switch {
		case short > 0.15:
			multi = append(multi, cand{n, short})
		case short > 0.07:
			arom = append(arom, cand{n, short})
		}
	}
	// the clearest first: on one atom, of two equal bonds (a carboxylate) only one is double
	sort.SliceStable(multi, func(a, b int) bool { return multi[a].short > multi[b].short })
	for _, c := range multi {
		i, k := m.bonds[c.n][0], m.bonds[c.n][1]
		extra := min(1, free(i), free(k))
		if c.short > 0.27 { // a triple bond: one beyond the valence allowed (C≡O, C≡N-R carry formal charges)
			extra = min(2, free(i)+1, free(k)+1)
		}
		if extra > 0 {
			m.order[c.n] += extra
			used[i] += extra
			used[k] += extra
		}
	}
	if len(arom) == 0 {
		return
	}
	best := []int(nil)
	rng := rand.New(rand.NewSource(1))
	idx := make([]int, len(arom))
	for i := range idx {
		idx[i] = i
	}
	for pass := 0; pass < 64; pass++ {
		if pass > 0 {
			rng.Shuffle(len(idx), func(a, b int) { idx[a], idx[b] = idx[b], idx[a] })
		}
		took := map[int]bool{}
		var pick []int
		for _, a := range idx {
			i, k := m.bonds[arom[a].n][0], m.bonds[arom[a].n][1]
			if !took[i] && !took[k] && free(i) > 0 && free(k) > 0 {
				took[i], took[k] = true, true
				pick = append(pick, arom[a].n)
			}
		}
		if len(pick) > len(best) {
			best = pick
		}
	}
	for _, n := range best {
		m.order[n] = 2
	}
}

// bestView: among the principal-axes view and rotations of it, the one where the heavy
// atoms' bonds look longest (not seen end-on) and non-bonded atoms overlap least.
func (m *molecule) bestView() {
	n := len(m.atoms)
	var c [3]float64
	for _, a := range m.atoms {
		c[0], c[1], c[2] = c[0]+a.x, c[1]+a.y, c[2]+a.z
	}
	var cov [3][3]float64
	p := make([][3]float64, n)
	for i, a := range m.atoms {
		p[i] = [3]float64{a.x - c[0]/float64(n), a.y - c[1]/float64(n), a.z - c[2]/float64(n)}
		for r := 0; r < 3; r++ {
			for k := 0; k < 3; k++ {
				cov[r][k] += p[i][r] * p[i][k]
			}
		}
	}
	vals, vecs := jacobi3(cov)
	idx := []int{0, 1, 2}
	sort.Slice(idx, func(i, k int) bool { return vals[idx[i]] > vals[idx[k]] })
	base := make([][3]float64, n)
	for i := range p {
		for d := 0; d < 3; d++ {
			v := vecs[idx[d]]
			base[i][d] = p[i][0]*v[0] + p[i][1]*v[1] + p[i][2]*v[2]
		}
	}
	bonded := map[[2]int]bool{}
	for _, b := range m.bonds {
		bonded[b] = true
	}
	var heavy []int
	for i := range m.atoms {
		if m.heavy(i) {
			heavy = append(heavy, i)
		}
	}
	score := func(q [][3]float64) float64 {
		sc := 0.0
		for _, b := range m.bonds {
			if m.heavy(b[0]) && m.heavy(b[1]) {
				d2 := math.Hypot(q[b[0]][0]-q[b[1]][0], q[b[0]][1]-q[b[1]][1])
				sc += math.Min(d2/m.dist(b[0], b[1]), 1) // foreshortened bonds lose
			}
		}
		for a := 0; a < len(heavy); a++ {
			for k := a + 1; k < len(heavy); k++ {
				i, j := heavy[a], heavy[k]
				if !bonded[[2]int{i, j}] {
					if d2 := math.Hypot(q[i][0]-q[j][0], q[i][1]-q[j][1]); d2 < 1 {
						sc -= 2 * (1 - d2) // atoms drawn closer than 1 Å: confusing
					}
				}
			}
		}
		return sc
	}
	best, bestScore := base, score(base)
	q := make([][3]float64, n)
	for ia := 0; ia < 12; ia++ {
		for ib := 0; ib < 12; ib++ {
			ca, sa := math.Cos(float64(ia)*math.Pi/12), math.Sin(float64(ia)*math.Pi/12)
			cb, sb := math.Cos(float64(ib)*math.Pi/12), math.Sin(float64(ib)*math.Pi/12)
			for i, v := range base {
				x, y, z := v[0], v[1]*ca-v[2]*sa, v[1]*sa+v[2]*ca
				q[i] = [3]float64{x*cb + z*sb, y, -x*sb + z*cb}
			}
			if sc := score(q); sc > bestScore+1e-9 {
				bestScore = sc
				best = append([][3]float64(nil), q...)
			}
		}
	}
	for i := range m.atoms {
		m.atoms[i].px, m.atoms[i].py, m.atoms[i].depth = best[i][0], best[i][1], best[i][2]
	}
}

// label is what a skeletal formula writes for atom i ("" for a carbon): O, NH, NH2 ...
func (m *molecule) label(i int) string {
	a := m.atoms[i]
	if a.el == "C" || a.el == "H" {
		return ""
	}
	h := 0
	for _, b := range m.bonds {
		if b[0] == i && !m.heavy(b[1]) || b[1] == i && !m.heavy(b[0]) {
			h++
		}
	}
	switch {
	case h == 1:
		return a.el + "H"
	case h > 1:
		return a.el + "H" + strconv.Itoa(h)
	}
	return a.el
}

func (m *molecule) heavyBox() (minX, maxX, minY, maxY float64) {
	minX, maxX, minY, maxY = math.Inf(1), math.Inf(-1), math.Inf(1), math.Inf(-1)
	for i, a := range m.atoms {
		if m.heavy(i) {
			minX, maxX = math.Min(minX, a.px), math.Max(maxX, a.px)
			minY, maxY = math.Min(minY, a.py), math.Max(maxY, a.py)
		}
	}
	return
}

// brailleMolecule draws the skeletal formula in cols x rows characters of Unicode braille
// (2 x 4 dots each); "" without a geometry.
func brailleMolecule(in string, cols, rows int) string {
	m := newMolecule(in)
	if m == nil {
		return ""
	}
	minX, maxX, minY, maxY := m.heavyBox()
	W, H := cols*2, rows*4
	s := math.Min(float64(W-6)/math.Max(maxX-minX, 1e-6), float64(H-6)/math.Max(maxY-minY, 1e-6))
	s = math.Min(s, 9) // small molecules: about 13 dots per bond at most
	ox, oy := (float64(W)-(maxX-minX)*s)/2, (float64(H)-(maxY-minY)*s)/2
	pos := func(i int) (float64, float64) {
		return ox + (m.atoms[i].px-minX)*s, oy + (m.atoms[i].py-minY)*s
	}
	grid := make([][]bool, H)
	for i := range grid {
		grid[i] = make([]bool, W)
	}
	dot := func(x, y float64) {
		if xi, yi := int(x+0.5), int(y+0.5); xi >= 0 && xi < W && yi >= 0 && yi < H {
			grid[yi][xi] = true
		}
	}
	for n, b := range m.bonds {
		i, k := b[0], b[1]
		if !m.heavy(i) || !m.heavy(k) {
			continue
		}
		x0, y0 := pos(i)
		x1, y1 := pos(k)
		l := math.Hypot(x1-x0, y1-y0)
		if l < 1e-6 {
			continue
		}
		t0, t1 := 0.0, 1.0 // stop short of a written atom, so the letter stays readable
		if m.labels[i] != "" {
			t0 = 2.5 / l
		}
		if m.labels[k] != "" {
			t1 = 1 - 2.5/l
		}
		for j := 0; j <= int(l*2); j++ {
			if t := float64(j) / (l * 2); t >= t0 && t <= t1 {
				dot(x0+(x1-x0)*t, y0+(y1-y0)*t)
				if m.order[n] >= 2 { // a double bond: a second line beside it
					dot(x0+(x1-x0)*t-(y1-y0)/l*2, y0+(y1-y0)*t+(x1-x0)/l*2)
				}
				if m.order[n] == 3 { // a triple bond: one on each side
					dot(x0+(x1-x0)*t+(y1-y0)/l*2, y0+(y1-y0)*t-(x1-x0)/l*2)
				}
			}
		}
	}
	labels := map[[2]int]string{}
	for i, t := range m.labels {
		if t == "" {
			continue
		}
		x, y := pos(i)
		c, r := min(int(x)/2, cols-len(t)), min(int(y)/4, rows-1) // the whole label inside
		for labels[[2]int{c, r}] != "" && c+1 < cols {            // two atoms in one cell: both written
			c++
		}
		labels[[2]int{max(c, 0), r}] = t
	}
	bits := [4][2]rune{{0x01, 0x08}, {0x02, 0x10}, {0x04, 0x20}, {0x40, 0x80}}
	var out []string
	for r := 0; r < rows; r++ {
		line := make([]rune, cols)
		for c := 0; c < cols; c++ {
			var v rune
			for dy := 0; dy < 4; dy++ {
				for dx := 0; dx < 2; dx++ {
					if grid[r*4+dy][c*2+dx] {
						v |= bits[dy][dx]
					}
				}
			}
			line[c] = ' '
			if v != 0 {
				line[c] = 0x2800 + v
			}
		}
		for c := 0; c < cols; c++ {
			if t, ok := labels[[2]int{c, r}]; ok {
				for j, ch := range t {
					if c+j < cols {
						line[c+j] = ch
					}
				}
			}
		}
		out = append(out, strings.TrimRight(string(line), " "))
	}
	for len(out) > 0 && out[len(out)-1] == "" { // no empty lines below the drawing
		out = out[:len(out)-1]
	}
	for len(out) > 0 && out[0] == "" {
		out = out[1:]
	}
	return strings.Join(out, "\n") + "\n"
}

// glyphs: 5 x 7 pixel letters for the atom labels (the standard library draws no text)
var glyphs = map[rune][7]string{
	'A': {".###.", "#...#", "#...#", "#####", "#...#", "#...#", "#...#"},
	'B': {"####.", "#...#", "#...#", "####.", "#...#", "#...#", "####."},
	'C': {".###.", "#...#", "#....", "#....", "#....", "#...#", ".###."},
	'D': {"####.", "#...#", "#...#", "#...#", "#...#", "#...#", "####."},
	'E': {"#####", "#....", "#....", "####.", "#....", "#....", "#####"},
	'F': {"#####", "#....", "#....", "####.", "#....", "#....", "#...."},
	'G': {".###.", "#...#", "#....", "#.###", "#...#", "#...#", ".###."},
	'H': {"#...#", "#...#", "#...#", "#####", "#...#", "#...#", "#...#"},
	'I': {".###.", "..#..", "..#..", "..#..", "..#..", "..#..", ".###."},
	'J': {"..###", "...#.", "...#.", "...#.", "#..#.", "#..#.", ".##.."},
	'K': {"#...#", "#..#.", "#.#..", "##...", "#.#..", "#..#.", "#...#"},
	'L': {"#....", "#....", "#....", "#....", "#....", "#....", "#####"},
	'M': {"#...#", "##.##", "#.#.#", "#.#.#", "#...#", "#...#", "#...#"},
	'N': {"#...#", "##..#", "#.#.#", "#.#.#", "#..##", "#...#", "#...#"},
	'O': {".###.", "#...#", "#...#", "#...#", "#...#", "#...#", ".###."},
	'P': {"####.", "#...#", "#...#", "####.", "#....", "#....", "#...."},
	'Q': {".###.", "#...#", "#...#", "#...#", "#.#.#", "#..#.", ".##.#"},
	'R': {"####.", "#...#", "#...#", "####.", "#.#..", "#..#.", "#...#"},
	'S': {".####", "#....", "#....", ".###.", "....#", "....#", "####."},
	'T': {"#####", "..#..", "..#..", "..#..", "..#..", "..#..", "..#.."},
	'U': {"#...#", "#...#", "#...#", "#...#", "#...#", "#...#", ".###."},
	'V': {"#...#", "#...#", "#...#", "#...#", "#...#", ".#.#.", "..#.."},
	'W': {"#...#", "#...#", "#...#", "#.#.#", "#.#.#", "#.#.#", ".#.#."},
	'X': {"#...#", "#...#", ".#.#.", "..#..", ".#.#.", "#...#", "#...#"},
	'Y': {"#...#", "#...#", ".#.#.", "..#..", "..#..", "..#..", "..#.."},
	'Z': {"#####", "....#", "...#.", "..#..", ".#...", "#....", "#####"},
	'a': {".....", ".....", ".###.", "....#", ".####", "#...#", ".####"},
	'b': {"#....", "#....", "####.", "#...#", "#...#", "#...#", "####."},
	'c': {".....", ".....", ".###.", "#....", "#....", "#...#", ".###."},
	'd': {"....#", "....#", ".####", "#...#", "#...#", "#...#", ".####"},
	'e': {".....", ".....", ".###.", "#...#", "#####", "#....", ".###."},
	'f': {"..##.", ".#...", ".#...", "###..", ".#...", ".#...", ".#..."},
	'g': {".....", ".####", "#...#", "#...#", ".####", "....#", ".###."},
	'h': {"#....", "#....", "#.##.", "##..#", "#...#", "#...#", "#...#"},
	'i': {"..#..", ".....", ".##..", "..#..", "..#..", "..#..", ".###."},
	'k': {"#....", "#....", "#..#.", "#.#..", "##...", "#.#..", "#..#."},
	'l': {".##..", "..#..", "..#..", "..#..", "..#..", "..#..", ".###."},
	'm': {".....", ".....", "##.#.", "#.#.#", "#.#.#", "#.#.#", "#.#.#"},
	'n': {".....", ".....", "#.##.", "##..#", "#...#", "#...#", "#...#"},
	'o': {".....", ".....", ".###.", "#...#", "#...#", "#...#", ".###."},
	'p': {".....", ".....", "####.", "#...#", "####.", "#....", "#...."},
	'r': {".....", ".....", "#.##.", "##..#", "#....", "#....", "#...."},
	's': {".....", ".....", ".####", "#....", ".###.", "....#", "####."},
	't': {".#...", ".#...", "###..", ".#...", ".#...", ".#..#", "..##."},
	'u': {".....", ".....", "#...#", "#...#", "#...#", "#..##", ".##.#"},
	'y': {".....", ".....", "#...#", "#...#", ".####", "....#", ".###."},
	'0': {".###.", "#...#", "#..##", "#.#.#", "##..#", "#...#", ".###."},
	'1': {"..#..", ".##..", "..#..", "..#..", "..#..", "..#..", ".###."},
	'2': {".###.", "#...#", "....#", "...#.", "..#..", ".#...", "#####"},
	'3': {"####.", "....#", "....#", ".###.", "....#", "....#", "####."},
	'4': {"...#.", "..##.", ".#.#.", "#..#.", "#####", "...#.", "...#."},
	'5': {"#####", "#....", "####.", "....#", "....#", "#...#", ".###."},
	'6': {".###.", "#....", "#....", "####.", "#...#", "#...#", ".###."},
	'7': {"#####", "....#", "...#.", "..#..", ".#...", ".#...", ".#..."},
	'8': {".###.", "#...#", "#...#", ".###.", "#...#", "#...#", ".###."},
	'9': {".###.", "#...#", "#...#", ".####", "....#", "....#", ".###."},
	'?': {".###.", "#...#", "....#", "...#.", "..#..", ".....", "..#.."},
}

var elemColor = map[string]color.RGBA{"O": {220, 30, 30, 255}, "N": {40, 60, 230, 255}, "S": {190, 150, 0, 255},
	"P": {230, 120, 0, 255}, "F": {40, 160, 40, 255}, "Cl": {40, 160, 40, 255}, "Br": {150, 40, 40, 255}, "I": {120, 0, 150, 255}}

// skeletalImage draws the skeletal formula as a w x h picture in the style of RDKit's 2D
// depictions; nil without a geometry.
func skeletalImage(in string, w, h int) *image.RGBA {
	m := newMolecule(in)
	if m == nil {
		return nil
	}
	const k = 3 // drawn at 3x and averaged down: smooth lines
	W, H := w*k, h*k
	img := image.NewRGBA(image.Rect(0, 0, W, H))
	for i := range img.Pix {
		img.Pix[i] = 255
	}
	minX, maxX, minY, maxY := m.heavyBox()
	pad := 28.0 * k
	s := math.Min((float64(W)-2*pad)/math.Max(maxX-minX, 1e-6), (float64(H)-2*pad)/math.Max(maxY-minY, 1e-6))
	s = math.Min(s, 55*k) // small molecules: a bond about 80 px long at most
	ox, oy := (float64(W)-(maxX-minX)*s)/2, (float64(H)-(maxY-minY)*s)/2
	pos := func(i int) (float64, float64) {
		return ox + (m.atoms[i].px-minX)*s, oy + (m.atoms[i].py-minY)*s
	}
	disc := func(x, y, r float64, c color.RGBA) {
		for yy := int(y - r); yy <= int(y+r); yy++ {
			for xx := int(x - r); xx <= int(x+r); xx++ {
				if xx >= 0 && yy >= 0 && xx < W && yy < H && (float64(xx)-x)*(float64(xx)-x)+(float64(yy)-y)*(float64(yy)-y) <= r*r {
					img.SetRGBA(xx, yy, c)
				}
			}
		}
	}
	line := func(x0, y0, x1, y1 float64, c0, c1 color.RGBA) {
		l := math.Hypot(x1-x0, y1-y0)
		for t := 0.0; t <= l; t += 0.5 {
			c := c0
			if t > l/2 {
				c = c1
			}
			disc(x0+(x1-x0)*t/l, y0+(y1-y0)*t/l, 1.6*k, c)
		}
	}
	col := func(i int) color.RGBA {
		if c, ok := elemColor[m.atoms[i].el]; ok {
			return c
		}
		return color.RGBA{30, 30, 30, 255}
	}
	gap := func(i int, ux, uy float64) float64 { // lines stop short of a written atom
		t := m.labels[i]
		if t == "" {
			return 0
		}
		hw, hh := float64(len(t)*6*2*k)/2, float64(7*2*k)/2 // half the label's width and height
		return math.Min(hw/math.Max(math.Abs(ux), 1e-6), hh/math.Max(math.Abs(uy), 1e-6)) + 3*k
	}
	cx, cy := float64(W)/2, float64(H)/2
	for n, b := range m.bonds {
		i, j := b[0], b[1]
		if !m.heavy(i) || !m.heavy(j) {
			continue
		}
		x0, y0 := pos(i)
		x1, y1 := pos(j)
		l := math.Hypot(x1-x0, y1-y0)
		if l < 1e-6 {
			continue
		}
		ux, uy := (x1-x0)/l, (y1-y0)/l
		g0, g1 := gap(i, ux, uy), gap(j, ux, uy)
		if g0+g1 >= l { // labels touching: no line left to draw
			continue
		}
		x0, y0 = x0+ux*g0, y0+uy*g0
		x1, y1 = x1-ux*g1, y1-uy*g1
		line(x0, y0, x1, y1, col(i), col(j))
		if m.order[n] >= 2 {
			// the second line on the side of the molecule's centre, shortened as RDKit draws it
			nx, ny := -uy*6*k, ux*6*k
			mx, my := (x0+x1)/2, (y0+y1)/2
			if math.Hypot(mx+nx-cx, my+ny-cy) > math.Hypot(mx-nx-cx, my-ny-cy) {
				nx, ny = -nx, -ny
			}
			sh := 0.15
			line(x0+nx+(x1-x0)*sh, y0+ny+(y1-y0)*sh, x1+nx-(x1-x0)*sh, y1+ny-(y1-y0)*sh, col(i), col(j))
			if m.order[n] == 3 {
				line(x0-nx+(x1-x0)*sh, y0-ny+(y1-y0)*sh, x1-nx-(x1-x0)*sh, y1-ny-(y1-y0)*sh, col(i), col(j))
			}
		}
	}
	for i := range m.atoms {
		t := m.labels[i]
		if t == "" {
			continue
		}
		x, y := pos(i)
		px := 2 * k // glyph pixel size
		x -= float64(len(t)*6*px) / 2
		y -= float64(7*px) / 2
		for n, ch := range t {
			g, ok := glyphs[ch]
			if !ok {
				g = glyphs['?']
			}
			for r, row := range g {
				for c, v := range row {
					if v != '#' {
						continue
					}
					for yy := 0; yy < px; yy++ {
						for xx := 0; xx < px; xx++ {
							X, Y := int(x)+(n*6+c)*px+xx, int(y)+r*px+yy
							if X >= 0 && Y >= 0 && X < W && Y < H {
								img.SetRGBA(X, Y, col(i))
							}
						}
					}
				}
			}
		}
	}
	out := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			var R, G, B int
			for dy := 0; dy < k; dy++ {
				for dx := 0; dx < k; dx++ {
					p := img.RGBAAt(x*k+dx, y*k+dy)
					R, G, B = R+int(p.R), G+int(p.G), B+int(p.B)
				}
			}
			out.SetRGBA(x, y, color.RGBA{uint8(R / (k * k)), uint8(G / (k * k)), uint8(B / (k * k)), 255})
		}
	}
	return out
}

func pngBytes(img image.Image) []byte {
	var buf bytes.Buffer
	if png.Encode(&buf, img) != nil {
		return nil
	}
	return buf.Bytes()
}

// cachedBraille: the braille drawing of an input, kept for the last 64 inputs ('watch' asks
// for the same job every 2 s, and a drawing of 300 atoms takes a fraction of a second)
func cachedBraille(in string) string {
	sum := sha256.Sum256([]byte(in))
	k := string(sum[:])
	brailleMu.Lock()
	d, ok := brailleCache[k]
	brailleMu.Unlock()
	if ok {
		return d
	}
	d = brailleMolecule(in, 60, 16)
	brailleMu.Lock()
	if len(brailleCache) >= 64 {
		clear(brailleCache)
	}
	brailleCache[k] = d
	brailleMu.Unlock()
	return d
}

var (
	brailleMu    sync.Mutex
	brailleCache = map[string]string{}
)
