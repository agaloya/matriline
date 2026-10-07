package orca

import (
	"math"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const nh3 = "! HF def2-SVP Opt Freq\n%scf convergence tight end\n* xyz 0 1\nN 0 0 0.1\nH 0 0.94 -0.27\nH 0.81 -0.47 -0.27\nH -0.81 -0.47 -0.27\n*\n"

func TestBuildCheckInput(t *testing.T) {
	in, err := BuildCheckInput(nh3, "g.xyz", "o.gbw", []string{"EnGrad"}, []string{"%scf MaxIter 3 end"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(in, "Opt") || strings.Contains(in, "Freq") || !strings.Contains(in, "* xyzfile 0 1 g.xyz") || !strings.Contains(in, "MORead") {
		t.Fatalf("bad check input:\n%s", in)
	}
	if _, err := BuildCheckInput("$new_job\n"+nh3, "g", "", nil, nil); err != ErrUnsupported {
		t.Fatal("multi-job input must be unsupported")
	}
}

func runOrca(t *testing.T, dir, name, input string) {
	t.Helper()
	os.WriteFile(filepath.Join(dir, name+".inp"), []byte(input), 0o644)
	cmd := exec.Command("/opt/orca-6.1.1/orca", name+".inp")
	cmd.Dir = dir
	cmd.Env = []string{"PATH=/opt/orca-6.1.1", "LD_LIBRARY_PATH=/opt/orca-6.1.1/lib", "HOME=" + dir}
	out, err := cmd.Output()
	os.WriteFile(filepath.Join(dir, name+".out"), out, 0o644)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
}

// Real-ORCA test of the random-direction Hessian probe (slow: ~10 s).
func TestHessianProbe(t *testing.T) {
	if _, err := os.Stat("/opt/orca-6.1.1/orca"); err != nil || testing.Short() {
		t.Skip("needs ORCA")
	}
	dir := t.TempDir()
	runOrca(t, dir, "ref", nh3)
	atoms, H, err := ParseHess(filepath.Join(dir, "ref.hess"))
	if err != nil {
		t.Fatal(err)
	}
	rng := rand.New(rand.NewSource(7))
	v := make([]float64, len(H))
	norm := 0.0
	for i := range v {
		v[i] = rng.NormFloat64()
		norm += v[i] * v[i]
	}
	for i := range v {
		v[i] /= math.Sqrt(norm)
	}
	h := 0.005
	grad := func(name string, sign float64) []float64 {
		WriteXYZ(filepath.Join(dir, name+".xyz"), Displace(atoms, v, sign*h), "probe")
		in, _ := BuildCheckInput(nh3, name+".xyz", "", []string{"EnGrad"}, nil)
		runOrca(t, dir, name, in)
		_, g, err := ParseEngrad(filepath.Join(dir, name+".engrad"))
		if err != nil {
			t.Fatal(err)
		}
		return g
	}
	gp, gm := grad("plus", 1), grad("minus", -1)
	e := HessianProbeError(H, v, gp, gm, h)
	t.Logf("genuine Hessian: relative probe error %.4f", e)
	if e > 0.05 {
		t.Fatalf("genuine Hessian rejected (%.3f)", e)
	}
	// a Hessian scaled by 3 % (e.g. fabricated frequencies) must be caught
	for r := range H {
		for c := range H[r] {
			H[r][c] *= 1.03
		}
	}
	e2 := HessianProbeError(H, v, gp, gm, h)
	t.Logf("tampered Hessian (x1.03): relative probe error %.4f", e2)
	if e2 < 0.02 {
		t.Fatalf("tampered Hessian not detected")
	}
	scf, err := ParseSCF(filepath.Join(dir, "ref.out"))
	if err != nil || !scf.HasTotal || len(scf.Final) != 4 {
		t.Fatalf("ParseSCF: %+v %v", scf, err)
	}
}

// The COSX final-grid correction must be removed from the compared energy (values from a
// real B3LYP/def2-SVP check run with ORCA 6.1.1).
func TestParseSCFWithoutCOSXCorrection(t *testing.T) {
	p := filepath.Join(t.TempDir(), "x.out")
	out := `Exchange energy change after final integration :      0.000054654 Eh
Total energy after final integration           :    -76.321082431 Eh

Total Energy       :        -76.32108243149382 Eh           -2076.80224 eV
`
	if err := os.WriteFile(p, []byte(out), 0o644); err != nil {
		t.Fatal(err)
	}
	scf, err := ParseSCF(p)
	if err != nil || !scf.HasTotal {
		t.Fatal(err)
	}
	if d := math.Abs(scf.SCF - -76.321137085); d > 1e-8 {
		t.Fatalf("SCF = %.9f", scf.SCF)
	}
}
