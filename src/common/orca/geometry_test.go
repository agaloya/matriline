package orca

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConnectivityDetectsIsomer(t *testing.T) {
	ethanol := []Atom{{"C", 0, 0, 0}, {"C", 1.52, 0, 0}, {"O", 2.0, 1.35, 0}}
	dimethylEther := []Atom{{"C", 0, 0, 0}, {"O", 1.42, 0, 0}, {"C", 2.0, 1.33, 0}} // same formula
	if ok, _ := SameConnectivity(ethanol, ethanol); !ok {
		t.Fatal("identical geometry rejected")
	}
	moved := []Atom{{"C", 0.05, 0, 0}, {"C", 1.55, 0.02, 0}, {"O", 2.03, 1.30, 0.04}}
	if ok, why := SameConnectivity(ethanol, moved); !ok {
		t.Fatalf("an optimization step was rejected: %s", why)
	}
	if ok, _ := SameConnectivity(ethanol, dimethylEther); ok {
		t.Fatal("isomer not detected")
	}
}

func TestStereo(t *testing.T) {
	// CHFClBr-like centre: mirror image (z -> -z) is the enantiomer
	c := []Atom{{"C", 0, 0, 0}, {"H", 0, 0, 1.09}, {"F", 1.33, 0, -0.4}, {"Cl", -0.85, 1.4, -0.5}, {"Br", -0.9, -1.55, -0.55}}
	mirror := make([]Atom, len(c))
	for i, a := range c {
		mirror[i] = Atom{a.Sym, a.X, a.Y, -a.Z}
	}
	if ok, _ := SameStereo(c, c); !ok {
		t.Fatal("identical centre rejected")
	}
	if ok, _ := SameStereo(c, mirror); ok {
		t.Fatal("enantiomer not detected")
	}
	// 1,2-difluoroethene: cis vs trans (same bonds)
	cis := []Atom{{"C", 0, 0, 0}, {"C", 1.33, 0, 0}, {"F", -0.7, 1.15, 0}, {"F", 2.03, 1.15, 0}, {"H", -0.55, -0.95, 0}, {"H", 1.88, -0.95, 0}}
	trans := []Atom{{"C", 0, 0, 0}, {"C", 1.33, 0, 0}, {"F", -0.7, 1.15, 0}, {"F", 2.03, -1.15, 0}, {"H", -0.55, -0.95, 0}, {"H", 1.88, 0.95, 0}}
	if ok, _ := SameConnectivity(cis, trans); !ok {
		t.Fatal("cis/trans should have the same bonds")
	}
	if ok, _ := SameStereo(cis, trans); ok {
		t.Fatal("cis -> trans not detected")
	}
}

// MATRILINE_GEOMETRY_CHECK=<dir with output/ and completed/> replays the consistency
// check on real results (no false positive allowed on honest ORCA runs).
func TestGeometryOnRealResults(t *testing.T) {
	root := os.Getenv("MATRILINE_GEOMETRY_CHECK")
	if root == "" {
		t.Skip("set MATRILINE_GEOMETRY_CHECK")
	}
	outs, _ := filepath.Glob(filepath.Join(root, "output", "*", "*", "*.out"))
	checked := 0
	for _, out := range outs {
		rel, _ := filepath.Rel(filepath.Join(root, "output"), filepath.Dir(out))
		inp, err := os.ReadFile(filepath.Join(root, "completed", rel+".inp"))
		if err != nil {
			continue
		}
		want, ok := InputAtoms(string(inp))
		if !ok {
			continue
		}
		g, err := ParseSCF(out)
		if err != nil || g.First == nil {
			continue
		}
		checked++
		if same, why := SameGeometry(want, g.First, 1e-3); !same {
			t.Errorf("%s: start geometry: %s", rel, why)
			continue
		}
		info, _ := ParseOutputFile(out)
		if info != nil && info.EchoGap {
			t.Errorf("%s: echoed input numbering has a gap in an honest output", rel)
		}
		if info != nil && info.OptSteps == 0 && !strings.Contains(strings.ToLower(string(inp)), "opt") {
			if same, why := SameGeometry(want, g.Final, 1e-3); !same {
				t.Errorf("%s: SP geometry: %s", rel, why)
			}
		} else if same, why := SameConnectivity(want, g.Final); !same {
			t.Errorf("%s: connectivity: %s", rel, why)
		} else if same, why := SameStereo(want, g.Final); !same {
			t.Errorf("%s: stereo: %s", rel, why)
		} else if mirrored, why := MirrorMatch(want, g.Final); mirrored {
			t.Errorf("%s: mirror: %s", rel, why)
		}
	}
	t.Logf("checked %d real results", checked)
}

// MATRILINE_STEREO_DIR=<dir with <mol>_P/_M .inp and .out> (real ORCA xTB optimizations):
// an honest optimization keeps its stereochemistry; the enantiomer's output passed off as
// the input's must be rejected.
func TestStereoOnRealOptimizations(t *testing.T) {
	dir := os.Getenv("MATRILINE_STEREO_DIR")
	if dir == "" {
		t.Skip("set MATRILINE_STEREO_DIR")
	}
	for _, m := range []string{"chiral", "allene", "biaryl", "sulfoxide", "phosphine", "ferrocene"} {
		in, _ := os.ReadFile(filepath.Join(dir, m+"_P.inp"))
		want, ok := InputAtoms(string(in))
		if !ok {
			t.Fatalf("%s: input", m)
		}
		own, err1 := ParseSCF(filepath.Join(dir, m+"_P.out"))
		mirror, err2 := ParseSCF(filepath.Join(dir, m+"_M.out"))
		if err1 != nil || err2 != nil {
			t.Fatalf("%s: outputs", m)
		}
		okOwn, why1 := SameStereo(want, own.Final)
		okMirror, why2 := SameStereo(want, mirror.Final)
		t.Logf("%-7s honest: %v %s | enantiomer passed off: %v %s", m, okOwn, why1, okMirror, why2)
		if !okOwn {
			t.Errorf("%s: honest optimization rejected", m)
		}
		if okMirror {
			t.Errorf("%s: enantiomer not detected", m)
		}
		if m1, why := MirrorMatch(want, own.Final); m1 {
			t.Errorf("%s: honest optimization flagged as mirror image: %s", m, why)
		}
		m2, why := MirrorMatch(want, mirror.Final)
		t.Logf("%-7s mirror superposition on the enantiomer: %v %s", m, m2, why)
		if !m2 {
			t.Errorf("%s: enantiomer not detected by superposition", m)
		}
	}
}

func TestRequestedProcs(t *testing.T) {
	for in, want := range map[string]int{
		"! B3LYP def2-SVP\n* xyz 0 1\nH 0 0 0\n*\n": 1,
		"! B3LYP def2-SVP PAL8\n":                   8,
		"%pal nprocs 4 end\n! HF\n":                 4,
		"%pal\n  nprocs 6\nend\n":                   6,
	} {
		if got := RequestedProcs(in); got != want {
			t.Errorf("%q: %d, want %d", in, got, want)
		}
	}
}

func TestSuperposedRMSD(t *testing.T) {
	a := []Atom{{"C", 0, 0, 0}, {"O", 1.2, 0, 0}, {"H", -0.5, 0.9, 0}, {"H", -0.5, -0.9, 0.3}, {"F", 0.3, 0.2, 1.4}}
	// rotate by 90 degrees about z and translate: RMSD must be ~0
	b := make([]Atom, len(a))
	for i, x := range a {
		b[i] = Atom{x.Sym, -x.Y + 3, x.X - 1, x.Z + 2}
	}
	if r := superposedRMSD(a, b); r > 1e-6 {
		t.Fatalf("rigid motion gave RMSD %g", r)
	}
	if mirrored, _ := MirrorMatch(a, b); mirrored {
		t.Fatal("rotated copy flagged as mirror image")
	}
	m := make([]Atom, len(a))
	for i, x := range a {
		m[i] = Atom{x.Sym, -x.X, x.Y, x.Z}
	}
	if mirrored, why := MirrorMatch(a, m); !mirrored {
		t.Fatalf("mirror image not detected: %s (direct %.3f)", why, superposedRMSD(a, m))
	}
}

func TestMimicCoords(t *testing.T) {
	atoms := []Atom{{"O", 0, 0, 0.1}, {"H", 0, 0.76, -0.5}, {"H", 0, -0.76, -0.5}}
	inline := "! B3LYP def2-SVP Opt\n* xyz 0 1\nO 0 0 0\nH 0 0.7 0.5\nH 0 -0.7 0.5\n*\n"
	chk, err := BuildCheckInput(inline, "geom.xyz", "", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	in, name := MimicCoords(inline, chk, atoms)
	if name != "" || strings.Contains(in, "xyzfile") || !strings.Contains(in, "* xyz 0 1\n") {
		t.Fatalf("inline original gave %q (file %q)", in, name)
	}
	if got, ok := InputAtoms(in); !ok || len(got) != 3 || got[1].Y != 0.76 {
		t.Fatalf("inline coordinates not readable: %v", got)
	}
	file := "! B3LYP def2-SVP\n* xyzfile 0 1 water.xyz\n"
	chk, _ = BuildCheckInput(file, "geom.xyz", "", nil, nil)
	if in, name = MimicCoords(file, chk, atoms); name != "water.xyz" || !strings.Contains(in, "* xyzfile 0 1 water.xyz") {
		t.Fatalf("xyzfile original gave %q (file %q)", in, name)
	}
}
