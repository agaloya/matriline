package orca

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const water = `! HF STO-3G PAL4
%maxcore 9000
%pal
  nprocs 8
end
* xyz 0 1
O 0.000 0.000 0.000
H 0.000 0.757 0.587
H 0.000 -0.757 0.587
*
`

func TestNormalize(t *testing.T) {
	n := Normalize(water, Resources{MaxcoreMB: 800, Nprocs: 1})
	if strings.Contains(n, "9000") || strings.Contains(n, "nprocs 8") || strings.Contains(n, "PAL4") {
		t.Fatalf("not normalized:\n%s", n)
	}
	if !strings.HasPrefix(n, "%maxcore 800\n%pal nprocs 1 end\n! HF STO-3G\n") {
		t.Fatalf("bad header:\n%s", n)
	}
	if Normalize(water, Resources{800, 1}) != n {
		t.Fatal("not deterministic")
	}
}

func TestCheckInput(t *testing.T) {
	bad := map[string]string{
		"extopt": "! ExtOpt Opt\n%method ProgExt \"/tmp/evil.sh\" end\n",
		"nbo":    "! B3LYP def2-SVP NBO\n",
		"path":   "%moinp \"/etc/passwd\"\n",
		"dotdot": "* xyzfile 0 1 ../x.xyz\n",
		"syscmd": "%compound\n SysCmd(\"rm -rf ~\")\nend\n",
		"mrcc":   "! MRCC\n",
	}
	for name, in := range bad {
		if v := CheckInput(in, map[string]bool{}); len(v) == 0 {
			t.Errorf("%s: not rejected", name)
		}
	}
	good := "! B3LYP def2-SVP Opt Freq # NBO in a comment is fine #\n* xyzfile 0 1 mol.xyz\n%base \"job\"\n"
	if v := CheckInput(good, map[string]bool{"mol.xyz": true}); len(v) != 0 {
		t.Errorf("good input rejected: %v", v)
	}
}

// Runs the real ORCA when available and checks that the echoed input equals the
// normalized input byte for byte.
func TestRealOrcaEcho(t *testing.T) {
	orca := "/opt/orca-6.1.1/orca"
	if _, err := os.Stat(orca); err != nil {
		t.Skip("ORCA not installed")
	}
	dir := t.TempDir()
	n := Normalize(water, Resources{MaxcoreMB: 500, Nprocs: 1})
	os.WriteFile(filepath.Join(dir, "w.inp"), []byte(n), 0o644)
	cmd := exec.Command(orca, "w.inp")
	cmd.Dir = dir
	cmd.Env = []string{"PATH=/opt/orca-6.1.1", "LD_LIBRARY_PATH=/opt/orca-6.1.1/lib", "HOME=" + dir}
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("orca failed: %v", err)
	}
	os.WriteFile(filepath.Join(dir, "w.out"), out, 0o644)
	info, err := ParseOutputFile(filepath.Join(dir, "w.out"))
	if err != nil {
		t.Fatal(err)
	}
	if !info.Terminated || info.Version != "6.1.1" || info.Git != "487d211c" || len(info.FinalEnergies) != 1 {
		t.Fatalf("parse: %+v", info)
	}
	if ok, why := EchoMatches(n, info.EchoedInput); !ok {
		t.Fatalf("echo mismatch: %s", why)
	}
	if len(info.Modules) < 3 {
		t.Fatalf("modules: %v", info.Modules)
	}
	t.Logf("modules=%v energy=%v run=%.3fs", info.Modules, info.FinalEnergies, info.TotalRunSec)
}
