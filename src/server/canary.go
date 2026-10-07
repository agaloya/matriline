package main

import (
	"fmt"
	"math"
	mrand "math/rand/v2"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/agaloya/matriline/common/ledger"
	"github.com/agaloya/matriline/common/orca"
	"github.com/agaloya/matriline/common/wire"
	"github.com/agaloya/matriline/common/xfer"
)

// Canaries (D44): tasks whose answer the server already knows, disguised as ordinary tasks.
// Sources are only results that PASSED an active check (otherwise a fake result could become
// the reference and condemn honest clients). Each canary is a single point at the source's
// final geometry moved by one of the cube's 48 symmetries, a random translation and an atom
// permutation, so its coordinates differ from the source's; the SCF energy, integration
// grids included, is invariant under exactly these moves (see disguise).

// CanarySource is a trusted reference.
type CanarySource struct {
	TaskID    string  `json:"task_id"`
	ResultRel string  `json:"result_rel"`
	SCFTotal  float64 `json:"scf_total"` // variational SCF energy (orca.SCFInfo.SCF)
	// OutSHA: the output the energy and the geometry come from. A canary is made only while
	// that file is unchanged: the result at ResultRel can be replaced (condemned and
	// recomputed, or redo), and a canary built from the new geometry but checked against
	// the old energy accused an honest client (Windows adversarial round, copy attack).
	OutSHA string `json:"out_sha,omitempty"`
}

const (
	canaryTolerance = 1e-5 // Eh; cube symmetries keep the grids (disguise), so this is generous
	maxCanaryPool   = 500
)

// addCanarySource registers a verified result as a possible canary.
func (s *Server) addCanarySource(ck *Check) {
	stem := strings.TrimSuffix(path.Base(ck.TaskID), path.Ext(ck.TaskID))
	dir := filepath.Join(s.conf().Root, filepath.FromSlash(ck.ResultRel))
	scf, err := orca.ParseSCF(filepath.Join(dir, stem+".out"))
	if err != nil || !scf.HasTotal || len(scf.Final) == 0 {
		return
	}
	info, _ := orca.ParseOutputFile(filepath.Join(dir, stem+".out"))
	if info == nil || info.Jobs > 1 {
		return
	}
	for _, m := range info.Modules {
		for _, c := range correlatedModules {
			if strings.Contains(m, c) {
				return // keep canaries cheap
			}
		}
	}
	s.store.mu.Lock()
	defer s.store.mu.Unlock()
	if len(s.store.Canaries) >= maxCanaryPool {
		s.store.Canaries = s.store.Canaries[1:]
	}
	_, sha, err := xfer.HashFile(filepath.Join(dir, stem+".out"))
	if err != nil {
		return
	}
	s.store.Canaries = append(s.store.Canaries, CanarySource{TaskID: ck.TaskID, ResultRel: ck.ResultRel, SCFTotal: scf.SCF, OutSHA: sha})
	s.store.touch()
}

// maybeCanary creates a canary for this client with probability canary_rate (x probation).
// Called once per real job assigned; the canary is queued for the same client.
func (s *Server) maybeCanary(clientID string) {
	cfg := s.conf()
	if cfg.CanaryRate <= 0 {
		return
	}
	rate := cfg.CanaryRate
	if c := s.reg.get(clientID); c != nil && c.Results <= cfg.ProbationResults {
		rate *= cfg.ProbationBoost
	}
	if secureFloat() >= rate {
		return
	}
	s.store.mu.Lock()
	n := len(s.store.Canaries)
	var src CanarySource
	if n > 0 {
		src = s.store.Canaries[int(secureFloat()*float64(n))%n]
	}
	s.store.mu.Unlock()
	if n == 0 {
		return
	}
	if err := s.createCanary(clientID, src); err != nil {
		s.log.Debugf("canary not created: %v", err)
	}
}

// dropCanarySource removes a source from the pool.
func (s *Server) dropCanarySource(src CanarySource) {
	s.store.mu.Lock()
	defer s.store.mu.Unlock()
	keep := s.store.Canaries[:0]
	for _, c := range s.store.Canaries {
		if c != src {
			keep = append(keep, c)
		}
	}
	s.store.Canaries = keep
	s.store.touch()
}

func (s *Server) createCanary(clientID string, src CanarySource) error {
	cfg := s.conf()
	orig, err := s.taskInput(src.TaskID)
	if err != nil {
		return err
	}
	stem := strings.TrimSuffix(path.Base(src.TaskID), path.Ext(src.TaskID))
	out := filepath.Join(cfg.Root, filepath.FromSlash(src.ResultRel), stem+".out")
	if _, sha, err := xfer.HashFile(out); err != nil || sha != src.OutSHA {
		s.dropCanarySource(src) // replaced, moved or gone: its energy no longer matches
		return fmt.Errorf("source %s changed since it was verified", src.ResultRel)
	}
	scf, err := orca.ParseSCF(out)
	if err != nil || len(scf.Final) == 0 {
		return fmt.Errorf("source geometry unavailable")
	}
	in, err := orca.BuildCheckInput(string(orig), "geom.xyz", "", nil, nil)
	if err != nil {
		return err
	}
	atoms := disguise(scf.Final)
	in, xyzName := orca.MimicCoords(string(orig), in, atoms)
	ck := &Check{ID: randomID(8), TaskID: src.TaskID, ResultRel: src.ResultRel, Client: clientID,
		Subtasks: map[string]string{}, Done: map[string]string{}, Verifiers: map[string]string{}, Created: time.Now()}
	ck.Expect.Canary, ck.Expect.SCFTotal = true, src.SCFTotal
	d := filepath.Join(cfg.Root, dState, "internal", ck.ID, "canary")
	if err := os.MkdirAll(d, 0o750); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(d, "canary.inp"), []byte(in), 0o644); err != nil {
		return err
	}
	if xyzName != "" {
		if err := orca.WriteXYZ(filepath.Join(d, xyzName), atoms, ""); err != nil {
			return err
		}
	}
	id := "~check/" + ck.ID + "/canary.inp"
	ck.Subtasks[id] = "canary"
	s.store.mu.Lock()
	if s.store.Checks == nil {
		s.store.Checks = map[string]*Check{}
	}
	s.store.Checks[ck.ID] = ck
	s.store.Tasks[id] = &TaskState{ID: id, Priority: internalPrio + 1, Seq: randomSeq(), Added: time.Now(),
		Internal: true, CheckID: ck.ID, Purpose: "canary", OnlyFor: clientID, InputSHA: sha256hex([]byte(in))}
	s.store.touch()
	s.store.mu.Unlock()
	s.log.Debugf("canary %s created for %s from %s", ck.ID, clientID, src.TaskID)
	return nil
}

// disguise applies one of the 48 symmetries of the cube (a signed permutation of the
// axes: the 24 rotations and their mirror images), a random translation of up to 5 Å and a
// random atom permutation. ORCA's integration grids (Lebedev, atom-centred) are invariant
// under exactly these operations, so the canary's energy equals the source's (2e-8 Eh
// measured). A uniformly random rotation, used before, moved the DFT energy of LiF by up
// to 1.5e-4 Eh, three times the tolerance: an honest client failed a canary (Windows
// adversarial round, dodge). A mirror image has the same energy (no parity-violating terms).
// Recognising a molecule is possible under any rotation (its distances do not change),
// so the random rotation added no real disguise; the translation and order still differ.
func disguise(in []orca.Atom) []orca.Atom {
	rng := mrand.New(mrand.NewPCG(uint64(secureFloat()*(1<<53)), uint64(time.Now().UnixNano())))
	perm := rng.Perm(3)
	var sign [3]float64
	for k := range sign {
		sign[k] = float64(1 - 2*rng.IntN(2))
	}
	t := [3]float64{(rng.Float64()*2 - 1) * 5, (rng.Float64()*2 - 1) * 5, (rng.Float64()*2 - 1) * 5}
	out := make([]orca.Atom, len(in))
	for i, a := range in {
		p := [3]float64{a.X, a.Y, a.Z}
		var r [3]float64
		for k := 0; k < 3; k++ {
			r[k] = sign[k]*p[perm[k]] + t[k]
		}
		out[i] = orca.Atom{Sym: a.Sym, X: r[0], Y: r[1], Z: r[2]}
	}
	rng.Shuffle(len(out), func(i, j int) { out[i], out[j] = out[j], out[i] })
	return out
}

func (s *Server) evaluateCanary(ck *Check, r wire.Result, outPath string) string {
	if r.Failure != "" {
		return "fail: canary failed (" + r.Failure + ")"
	}
	scf, err := orca.ParseSCF(outPath)
	if err != nil || !scf.HasTotal {
		return "fail: no SCF energy in the canary output"
	}
	if d := math.Abs(scf.SCF - ck.Expect.SCFTotal); d > canaryTolerance {
		return fmt.Sprintf("fail: canary energy %.9f, known answer %.9f (diff %.2e Eh)", scf.SCF, ck.Expect.SCFTotal, d)
	}
	return "ok"
}

func (s *Server) finishCanary(ck *Check, fails []string) {
	if len(fails) == 0 {
		s.ledger.Append(ledger.Entry{Kind: "canary", Client: ck.Client, Verdict: "passed", Note: "check " + ck.ID})
		_ = s.reg.update(ck.Client, func(c *ClientRec) { c.Verified++ })
		return
	}
	s.ledger.Append(ledger.Entry{Kind: "canary", Client: ck.Client, Verdict: "failed", Note: "check " + ck.ID + ": " + strings.Join(fails, "; ")})
	s.clientFault(ck.Client, true, "failed a canary")
	s.log.Warnf("client %s FAILED a canary: %s", ck.Client, strings.Join(fails, "; "))
	s.alertf("verify_failed", "client %s failed a canary task: %s", ck.Client, strings.Join(fails, "; "))
	if s.conf().FailurePolicy == "quarantine" {
		s.quarantine(ck.Client, "failed a canary task")
	}
}
