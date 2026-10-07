package main

import (
	"math"
	"os"
	"strings"
	"testing"
	"time"
)

func TestJacobi3(t *testing.T) {
	a := [3][3]float64{{4, 1, 2}, {1, 3, 0.5}, {2, 0.5, 5}}
	vals, vecs := jacobi3(a)
	for i := 0; i < 3; i++ {
		for r := 0; r < 3; r++ {
			av := a[r][0]*vecs[i][0] + a[r][1]*vecs[i][1] + a[r][2]*vecs[i][2]
			if math.Abs(av-vals[i]*vecs[i][r]) > 1e-9 {
				t.Fatalf("eigenpair %d wrong: %v %v", i, vals[i], vecs[i])
			}
		}
	}
}

func TestDrawMolecule(t *testing.T) {
	in := `! B3LYP def2-SVP
* xyz 0 1
C        0.000000     1.396792     0.000000
C        1.209657     0.698396     0.000000
C        1.209657    -0.698396     0.000000
C        0.000000    -1.396792     0.000000
C       -1.209657    -0.698396     0.000000
C       -1.209657     0.698396     0.000000
H        0.000000     2.484212     0.000000
H        2.151390     1.242106     0.000000
H        2.151390    -1.242106     0.000000
H        0.000000    -2.484212     0.000000
H       -2.151390    -1.242106     0.000000
H       -2.151390     1.242106     0.000000
*
`
	// skeletal formula: no letters for C and H, braille dots for the bonds
	d := brailleMolecule(in, 60, 16)
	t.Logf("\n%s", d)
	if strings.ContainsAny(d, "CH") || strings.Count(d, "\n") < 4 || !strings.ContainsAny(d, "\u28c0\u2840\u2880\u2809\u2812\u2824") && strings.IndexFunc(d, func(r rune) bool { return r >= 0x2801 && r <= 0x28ff }) < 0 {
		t.Errorf("benzene drawing:\n%s", d)
	}
	m := newMolecule(in)
	double := 0
	for _, o := range m.order {
		if o == 2 {
			double++
		}
	}
	if double != 3 { // a Kekulé structure: three of the ring's six bonds double
		t.Errorf("benzene: %d double bonds, want 3", double)
	}
	if img := skeletalImage(in, 420, 300); img == nil || img.Bounds().Dx() != 420 {
		t.Error("no picture")
	}
	if brailleMolecule("! HF\n*xyzfile 0 1 a.xyz\n", 56, 14) != "" || skeletalImage("! HF\n", 10, 10) != nil {
		t.Error("drew a molecule from an external file")
	}
}

func TestLiveCommand(t *testing.T) {
	dir := t.TempDir()
	if err := cmdInit(dir, "en"); err != nil {
		t.Fatal(err)
	}
	s, err := openServer(dir + "/server.conf")
	if err != nil {
		t.Fatal(err)
	}
	if out, _ := s.cmdLive(nil); !strings.Contains(out, "nothing runs") {
		t.Errorf("idle: %q", out)
	}
	root := s.conf().Root
	os.MkdirAll(root+"/input/m", 0o755)
	os.WriteFile(root+"/input/m/w.inp", []byte("! HF def2-SVP\n* xyz 0 1\nO 0 0 0\nH 0 0.757 0.587\nH 0 -0.757 0.587\n*\n"), 0o644)
	s.reg.add(&ClientRec{ID: "k1", Name: "lab1", Status: stActive})
	s.store.Tasks["m/w.inp"] = &TaskState{ID: "m/w.inp"}
	s.store.Attempts["a1"] = &Attempt{ID: "a1", TaskID: "m/w.inp", ClientID: "k1", Kind: "job", Started: time.Now(), Tail: "SCF ITERATIONS\n  1  -75.9"}
	out, err := s.cmdLive([]string{"1"})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("\n%s", out)
	for _, want := range []string{"1 job(s) running", "lab1", "m/w.inp", "! HF def2-SVP", "-- molecule --", "SCF ITERATIONS"} {
		if !strings.Contains(out, want) {
			t.Errorf("live view lacks %q", want)
		}
	}
}
