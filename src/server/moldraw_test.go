package main

import (
	"math/rand"
	"strings"
	"testing"
)

const (
	naphthaleneXYZ = `C 2.4608 0.5933 0.0418
C 2.4033 -0.7947 -0.0447
C 1.1665 -1.4405 -0.0873
C -0.0292 -0.7051 -0.0439
C -1.2817 -1.3391 -0.0859
C -2.4608 -0.5933 -0.0418
C -2.4033 0.7947 0.0447
C -1.1665 1.4405 0.0874
C 0.0292 0.7051 0.0439
C 1.2817 1.3391 0.0859
H 3.4232 1.0972 0.0751
H 3.3206 -1.3764 -0.0791
H 1.1418 -2.5256 -0.1550
H -1.3468 -2.4226 -0.1535
H -3.4232 -1.0972 -0.0751
H -3.3206 1.3764 0.0791
H -1.1418 2.5256 0.1549
H 1.3468 2.4226 0.1535`
	pyrroleXYZ = `C -0.8872 0.7884 0.1396
C -1.0268 -0.6110 -0.0209
C 0.2391 -1.1378 -0.1484
N 1.1372 -0.1054 -0.0705
C 0.4597 1.0730 0.1051
H -1.6834 1.5104 0.2666
H -1.9512 -1.1735 -0.0412
H 0.5770 -2.1559 -0.2875
H 2.1428 -0.1986 -0.1329
H 0.9928 2.0104 0.1902`
	diacetyleneXYZ = `C -1.9625 -0.0989 -0.5142
C -0.7671 -0.0387 -0.4284
C 0.7671 0.0387 -0.3183
C 1.9625 0.0990 -0.2325
H -3.0233 -0.1524 -0.5903
H 3.0233 0.1524 -0.1564`
	formateXYZ = `O 1.2510 -0.2159 0.0363
C 0.0066 -0.0236 0.0002
O -0.9551 -0.8367 -0.0288
H -0.3026 1.0762 -0.0077`
)

func orcaIn(xyz string) string { return "! HF\n* xyz 0 1\n" + xyz + "\n*\n" }

// shuffled: the same molecule with its atoms in another order
func shuffled(xyz string, r *rand.Rand) string {
	l := strings.Split(xyz, "\n")
	r.Shuffle(len(l), func(a, b int) { l[a], l[b] = l[b], l[a] })
	return strings.Join(l, "\n")
}

func orders(m *molecule) map[int]int {
	c := map[int]int{}
	for _, o := range m.order {
		c[o]++
	}
	return c
}

// Bond orders from geometry, with the cases found in review: a Kekulé structure
// whatever the atom order, no atom beyond its valence, triple bonds, metals.
func TestBondOrders(t *testing.T) {
	r := rand.New(rand.NewSource(5))
	for i := 0; i < 200; i++ {
		if n := orders(newMolecule(orcaIn(shuffled(naphthaleneXYZ, r))))[2]; n != 5 {
			t.Fatalf("naphthalene, atom order %d: %d double bonds, want 5", i, n)
		}
	}
	for i := 0; i < 200; i++ {
		m := newMolecule(orcaIn(shuffled(pyrroleXYZ, r)))
		for n, b := range m.bonds {
			if m.order[n] > 1 && (m.atoms[b[0]].el == "N" || m.atoms[b[1]].el == "N") {
				t.Fatalf("pyrrole: the N-H nitrogen took a double bond")
			}
		}
		if c := orders(m)[2]; c != 2 {
			t.Fatalf("pyrrole: %d double bonds, want 2", c)
		}
	}
	if c := orders(newMolecule(orcaIn(diacetyleneXYZ))); c[3] != 2 || c[2] != 0 {
		t.Errorf("diacetylene: %v, want two triple bonds and no double (C≡C-C≡C)", c)
	}
	if c := orders(newMolecule(orcaIn(formateXYZ))); c[2] != 1 {
		t.Errorf("formate: %v, want one C=O", c)
	}
	if c := orders(newMolecule(orcaIn("C 0 0 0\nO 0 0 1.128"))); c[3] != 1 {
		t.Errorf("carbon monoxide: %v, want a triple bond", c)
	}
	m := newMolecule(orcaIn("Fe 0 0 0\nC 0 0 1.80\nO 0 0 2.95\nC 0 1.80 0\nO 0 2.95 0"))
	for n, b := range m.bonds {
		if (m.atoms[b[0]].el == "Fe" || m.atoms[b[1]].el == "Fe") && m.order[n] != 1 {
			t.Errorf("iron carbonyl: a multiple Fe-C bond")
		}
	}
	if len(newMolecule(orcaIn("Pd 0 0 0\nCl 0 0 2.30\nP 0 2.30 0")).bonds) != 2 {
		t.Error("palladium: its bonds are lost")
	}
}

// TestDrawingIsText: atom symbols are letters only (one reached the terminal raw), in any
// case; ORCA's point charges are no atoms.
func TestDrawingIsText(t *testing.T) {
	in := "! HF\n* xyz 0 1\nCL 0 0 0\nc 0 0 1.75\n\x1b]0;pwned\x07 0 0 3\nQ 0.5 0 0 0\nH 0 0.9 2.1\n*\n"
	at := parseXYZ(in)
	if len(at) != 3 || at[0].el != "Cl" || at[1].el != "C" {
		t.Fatalf("atoms %v", at)
	}
	d := brailleMolecule(in, 40, 10)
	if strings.ContainsAny(d, "\x1b\x07") || !strings.Contains(d, "Cl") {
		t.Errorf("drawing:\n%q", d)
	}
}
