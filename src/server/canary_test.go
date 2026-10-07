package main

import (
	"math"
	"sort"
	"testing"

	"github.com/agaloya/matriline/common/orca"
)

// disguise moves a molecule by a cube symmetry and a translation: every vector between two
// atoms keeps its components up to order and sign (the integration grids do not change).
func TestDisguiseCubeSymmetry(t *testing.T) {
	in := []orca.Atom{{Sym: "C", X: 0.1, Y: 0.2, Z: 0.3}, {Sym: "O", X: 0.4, Y: -1.1, Z: 2.0}, {Sym: "H", X: -0.7, Y: 0.9, Z: 0.05}}
	key := func(a, b orca.Atom) []float64 {
		d := []float64{math.Abs(a.X - b.X), math.Abs(a.Y - b.Y), math.Abs(a.Z - b.Z)}
		sort.Float64s(d)
		return d
	}
	for n := 0; n < 50; n++ {
		out := disguise(in)
		bySym := map[string]orca.Atom{}
		for _, a := range out {
			bySym[a.Sym] = a
		}
		for i := range in {
			for j := i + 1; j < len(in); j++ {
				want, got := key(in[i], in[j]), key(bySym[in[i].Sym], bySym[in[j].Sym])
				for k := range want {
					if math.Abs(want[k]-got[k]) > 1e-12 {
						t.Fatalf("pair %s-%s: %v became %v", in[i].Sym, in[j].Sym, want, got)
					}
				}
			}
		}
	}
}
