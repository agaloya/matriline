package main

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math"
	mrand "math/rand/v2"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/agaloya/matriline/common/ledger"
	"github.com/agaloya/matriline/common/orca"
	"github.com/agaloya/matriline/common/wire"
)

// Active verification (D44). After a result has been accepted into output/, the server may
// schedule independent checks that run as INTERNAL tasks on other clients:
//
//	scf      re-read the returned orbitals (.gbw) at the final geometry with MORead and at most
//	         3 SCF iterations: genuine orbitals converge at once and reproduce the energy.
//	grad     same plus EnGrad: the gradient of an optimized structure must vanish.
//	hess     two gradients at x +/- h*v for a secret random unit vector v: their finite
//	         difference must equal H*v computed from the returned Hessian.
//	replica  the whole input runs again on another host; final energies must agree.
//	canary   (see canary.go) a task with a known answer, disguised as a normal task.
//
// Internal tasks are indistinguishable from normal ones for clients (opaque ids, anonymous
// names). A failed check is confirmed once on a third host before the result is moved to
// weird/, so a dishonest verifier cannot condemn an honest result on its own.

// Check is one verification of one accepted result.
type Check struct {
	ID        string            `json:"id"`
	TaskID    string            `json:"task_id"`
	ResultRel string            `json:"result_rel"` // output/... directory
	Client    string            `json:"client"`     // producer of the result
	Subtasks  map[string]string `json:"subtasks"`   // internal task id -> purpose
	Done      map[string]string `json:"done"`       // purpose -> "ok" or "fail: ..."
	Verifiers map[string]string `json:"verifiers"`  // purpose -> client id
	Expect    Expectation       `json:"expect"`
	Created   time.Time         `json:"created"`
	ConfirmOf string            `json:"confirm_of,omitempty"` // first check id when confirming
	Suspects  []string          `json:"suspects,omitempty"`   // verifiers of the first round
	FirstFail []string          `json:"first_fail,omitempty"` // failures of the first round (confirmations)
	Recheck   bool              `json:"recheck,omitempty"`    // re-verification of a quarantined client
	Rescue    bool              `json:"rescue,omitempty"`     // tie-break of a disputed/unconfirmed result in weird/
	Accusers  []string          `json:"accusers,omitempty"`   // rescue: the verifiers whose failure it re-examines
	Stalled   time.Time         `json:"stalled,omitempty"`    // since when no connected host can run what is left
}

// stallLimit: a check whose pending sub-tasks no connected host may run is given up after
// this long instead of checkLifetime (72 h). Seen in the lab: a verifier left right after
// the server planned checks that only it could run, and 27 sub-tasks waited for days.
const stallLimit = 30 * time.Minute

// Expectation holds the reference values a check is compared with.
type Expectation struct {
	SCFTotal float64   `json:"scf_total"` // variational SCF energy (orca.SCFInfo.SCF)
	Final    float64   `json:"final"`
	HasFinal bool      `json:"has_final"`
	Hv       []float64 `json:"hv,omitempty"`
	V        []float64 `json:"v,omitempty"`
	Step     float64   `json:"step,omitempty"`
	Canary   bool      `json:"canary,omitempty"`
	GradMax  float64   `json:"grad_max,omitempty"` // largest acceptable gradient component
}

// optGradTol is the MAX gradient (Eh/bohr) accepted at the final geometry: 4x ORCA's
// TolMaxG for the requested level (ORCA 6 manual, geometry optimization convergence
// criteria: Loose 3e-3, Normal 3e-4, Tight 1e-4, VeryTight 3e-5). Honest ORCA runs end with
// up to 2.9x TolMaxG (115 lab optimizations), because ORCA also accepts convergence on
// small energy and displacement changes; 4x keeps them clear of false positives.
func optGradTol(echoed []string) float64 {
	kw := strings.ToLower(strings.Join(echoed, " "))
	tol := 3e-4
	switch {
	case strings.Contains(kw, "verytightopt"):
		tol = 3e-5
	case strings.Contains(kw, "tightopt"):
		tol = 1e-4
	case strings.Contains(kw, "looseopt"):
		tol = 3e-3
	}
	return 4 * tol
}

const (
	gradTolerance = 2e-3 // gradient norm (Eh/bohr) fallback when no MAX gradient is printed
	checkLifetime = 72 * time.Hour
	internalPrio  = 1000
)

var correlatedModules = []string{"MDCI", "MP2", "CIS", "MRCI", "CASSCF", "ROCIS", "RASCI", "CIPSI", "AUTOCI"}

func secureFloat() float64 {
	var b [8]byte
	rand.Read(b[:])
	return float64(binary.BigEndian.Uint64(b[:])>>11) / (1 << 53)
}

// planChecks decides (randomly, according to the policy) which checks to run on a result
// just accepted into dest (spool-relative).
func (s *Server) planChecks(ss *Session, r wire.Result, v *Verdict, dest string) {
	cfg := s.conf()
	boost, replication := 1.0, cfg.ReplicationRate
	c := s.reg.get(ss.id)
	if c != nil && c.Results <= cfg.ProbationResults {
		boost, replication = cfg.ProbationBoost, math.Max(replication, cfg.ProbationReplication)
	} else if c != nil {
		boost = reputationMultiplier(cfg, c, time.Now())
	}
	// a quarantined client keeps computing, but every result it returns is checked
	quarantined := c != nil && c.Status == stQuarantined
	if quarantined {
		replication = 1
	}
	want := map[string]bool{}
	roll := func(frac float64) bool { return quarantined || frac > 0 && secureFloat() < frac*boost }
	dir := filepath.Join(cfg.Root, filepath.FromSlash(dest))
	stem := strings.TrimSuffix(path.Base(r.TaskID), path.Ext(r.TaskID))
	out := filepath.Join(dir, stem+".out")
	info, err := orca.ParseOutputFile(out)
	if err != nil {
		return
	}
	correlated := false
	for _, m := range info.Modules {
		for _, c := range correlatedModules {
			if strings.Contains(m, c) {
				correlated = true
			}
		}
	}
	gbw := fileExists(filepath.Join(dir, stem+".gbw"))
	if !correlated && gbw && info.Jobs <= 1 {
		if info.OptConverged {
			if roll(cfg.VerifyGradient) {
				want["grad"] = true
			}
		} else if roll(cfg.VerifySCF) {
			want["scf"] = true
		}
	}
	if info.HasFreq && fileExists(filepath.Join(dir, stem+".hess")) && info.Jobs <= 1 && roll(cfg.VerifyHessian) {
		want["hess"] = true
	}
	if roll(replication) || quarantined {
		want["replica"] = true
	}
	if len(want) == 0 {
		return
	}
	ck, err := s.createCheck(r.TaskID, dest, stem, ss.id, want, info, "", nil, nil)
	if err != nil {
		s.log.Warnf("cannot plan checks for %s: %v", r.TaskID, err)
		return
	}
	if ck != nil && quarantined && c.RecheckFresh > 0 {
		counted := false
		_ = s.reg.update(ss.id, func(c *ClientRec) {
			if c.RecheckFresh > 0 {
				c.RecheckFresh--
				counted = true
			}
		})
		if counted {
			s.store.mu.Lock()
			ck.Recheck = true // counted by recheckDone, like a re-verification
			s.store.touch()
			s.store.mu.Unlock()
		}
	}
}

// createCheck builds the internal sub-tasks of a check.
// activeClients counts the clients that may get work (not revoked, disabled or pending).
func (s *Server) activeClients() int {
	n := 0
	for _, c := range s.reg.list() {
		if c.Status != stRevoked && c.Status != stDisabled && c.Status != stPending {
			n++
		}
	}
	return n
}

func (s *Server) createCheck(taskID, dest, stem, producer string, want map[string]bool, info *orca.OutputInfo, confirmOf string, suspects, firstFail []string) (*Check, error) {
	cfg := s.conf()
	dir := filepath.Join(cfg.Root, filepath.FromSlash(dest))
	orig, err := s.taskInput(taskID)
	if err != nil {
		return nil, err
	}
	scf, err := orca.ParseSCF(filepath.Join(dir, stem+".out"))
	if err != nil {
		return nil, err
	}
	ck := &Check{ID: randomID(8), TaskID: taskID, ResultRel: dest, Client: producer, Subtasks: map[string]string{},
		Done: map[string]string{}, Verifiers: map[string]string{}, Created: time.Now(), ConfirmOf: confirmOf, Suspects: suspects, FirstFail: firstFail}
	ck.Expect.SCFTotal = scf.SCF
	ck.Expect.GradMax = optGradTol(info.EchoedInput)
	if len(info.FinalEnergies) > 0 {
		ck.Expect.Final, ck.Expect.HasFinal = info.FinalEnergies[len(info.FinalEnergies)-1], true
	}
	base := filepath.Join(cfg.Root, dState, "internal", ck.ID)
	add := func(purpose string, files map[string][]byte, copies map[string]string) error {
		d := filepath.Join(base, purpose)
		if err := os.MkdirAll(d, 0o750); err != nil {
			return err
		}
		for n, b := range files {
			if err := os.WriteFile(filepath.Join(d, n), b, 0o644); err != nil {
				return err
			}
		}
		for n, src := range copies {
			if err := copyFile(src, filepath.Join(d, n)); err != nil {
				return err
			}
		}
		id := "~check/" + ck.ID + "/" + purpose + ".inp"
		ck.Subtasks[id] = purpose
		return nil
	}
	geom := func(atoms []orca.Atom) []byte {
		var b strings.Builder
		fmt.Fprintf(&b, "%d\ngeometry\n", len(atoms))
		for _, a := range atoms {
			fmt.Fprintf(&b, "%-3s %18.10f %18.10f %18.10f\n", a.Sym, a.X, a.Y, a.Z)
		}
		return []byte(b.String())
	}
	// names inside internal inputs are neutral so they look like ordinary companion files
	for kind := range want {
		switch kind {
		case "scf", "grad":
			if len(scf.Final) == 0 || !scf.HasTotal {
				continue
			}
			kw := []string(nil)
			if kind == "grad" {
				kw = []string{"EnGrad"}
			}
			in, err := orca.BuildCheckInput(string(orig), "geom.xyz", "guess.gbw", kw, []string{fmt.Sprintf("%%scf MaxIter %d end", s.conf().CheckMaxSCFIter)})
			if err != nil {
				continue // unsupported input structure: skip silently
			}
			if err := add(kind, map[string][]byte{kind + ".inp": []byte(in), "geom.xyz": geom(scf.Final)},
				map[string]string{"guess.gbw": filepath.Join(dir, stem+".gbw")}); err != nil {
				return nil, err
			}
		case "hess":
			atoms, H, err := orca.ParseHess(filepath.Join(dir, stem+".hess"))
			if err != nil {
				continue
			}
			v := make([]float64, len(H))
			norm := 0.0
			rng := mrand.New(mrand.NewPCG(uint64(secureFloat()*(1<<53)), uint64(time.Now().UnixNano())))
			for i := range v {
				v[i] = rng.NormFloat64()
				norm += v[i] * v[i]
			}
			for i := range v {
				v[i] /= math.Sqrt(norm)
			}
			h := 0.005
			hv := make([]float64, len(v))
			for r := range H {
				for c := range H[r] {
					hv[r] += H[r][c] * v[c]
				}
			}
			ck.Expect.Hv, ck.Expect.V, ck.Expect.Step = hv, v, h
			for _, sg := range []struct {
				p string
				s float64
			}{{"hess_plus", 1}, {"hess_minus", -1}} {
				in, err := orca.BuildCheckInput(string(orig), "geom.xyz", "", []string{"EnGrad"}, nil)
				if err != nil {
					break
				}
				if err := add(sg.p, map[string][]byte{sg.p + ".inp": []byte(in), "geom.xyz": geom(orca.Displace(atoms, v, sg.s*h))}, nil); err != nil {
					return nil, err
				}
			}
		case "replica":
			if err := add("replica", map[string][]byte{"replica.inp": orig}, nil); err != nil {
				return nil, err
			}
		}
	}
	if len(ck.Subtasks) == 0 {
		return nil, nil // nothing applicable: no check registered
	}
	s.store.mu.Lock()
	if s.store.Checks == nil {
		s.store.Checks = map[string]*Check{}
	}
	s.store.Checks[ck.ID] = ck
	// the producer may verify its own result only on a single computer (Nacomline), and
	// never in a confirmation or a tie-break: there it would judge its own case (code review)
	sameHost := s.conf().SameHost && confirmOf == "" && len(suspects) == 0 && len(firstFail) == 0 && s.activeClients() <= 1
	for id, purpose := range ck.Subtasks {
		t := &TaskState{ID: id, Priority: internalPrio, Seq: randomSeq(), Added: time.Now(), Internal: true, CheckID: ck.ID, Purpose: purpose}
		if !sameHost {
			t.avoid(producer, avoidForever) // never verify a result on the host that produced it
		}
		for _, sus := range suspects {
			t.avoid(sus, avoidForever)
		}
		in, _ := os.ReadFile(filepath.Join(base, purpose, purpose+".inp"))
		t.InputSHA = sha256hex(in)
		s.store.Tasks[id] = t
	}
	s.store.touch()
	s.store.mu.Unlock()
	kinds := make([]string, 0, len(want))
	for k := range want {
		kinds = append(kinds, k)
	}
	sort.Strings(kinds)
	s.ledger.Append(ledger.Entry{Kind: "verify_planned", Task: taskID, Client: producer, Note: fmt.Sprintf("check %s: %s on %s", ck.ID, strings.Join(kinds, ","), dest)})
	s.log.Infof("verification %s planned for %s: %s", ck.ID, taskID, strings.Join(kinds, ","))
	return ck, nil
}

// internalBundle lists the files of an internal task (input first).
func (s *Server) internalBundle(t *TaskState) ([]string, []string, error) {
	d := filepath.Join(s.conf().Root, dState, "internal", t.CheckID, t.Purpose)
	entries, err := os.ReadDir(d)
	if err != nil {
		return nil, nil, err
	}
	names, paths := []string{t.Purpose + ".inp"}, []string{filepath.Join(d, t.Purpose+".inp")}
	for _, e := range entries {
		if e.Name() != t.Purpose+".inp" && e.Type().IsRegular() {
			names = append(names, e.Name())
			paths = append(paths, filepath.Join(d, e.Name()))
		}
	}
	return names, paths, nil
}

// internalResult evaluates the result of a verification sub-task.
func (s *Server) internalResult(ss *Session, r wire.Result, v *Verdict, stage string, t *TaskState) error {
	s.store.mu.Lock()
	ck := s.store.Checks[t.CheckID]
	delete(s.store.Attempts, r.AttemptID)
	s.store.touch()
	s.store.mu.Unlock()
	if ck == nil {
		s.dropInternal(t.ID)
		return nil
	}
	if r.Failure == "machine" || r.Failure == "timeout" || r.Failure == "returned" {
		if r.Failure != "returned" {
			s.store.mu.Lock()
			t.avoid(ss.id, 6*time.Hour)
			s.store.mu.Unlock()
		}
		// logged: a check cycling through hosts that cannot run it was invisible before
		// (a DLPNO replica filled six disks in a row in the BDE campaign)
		s.log.Warnf("verification sub-task %s (%s of %s) failed on %s (%s); requeued for another host", t.ID, t.Purpose, ck.TaskID, ss.name, r.Failure)
		return nil // requeued for another verifier
	}
	if !v.OK {
		// The VERIFIER returned something invalid (no ORCA run, forged output...). That is
		// its fault, not the producer's: before, it counted as a failed check and a
		// cheating verifier sent an honest result to weird/ (lab). Penalize the verifier and
		// give the sub-check to another host.
		s.log.Warnf("verification %s/%s of %s on %s: invalid verifier result (%s); reassigned", ck.ID, t.Purpose, ck.TaskID, ss.name, strings.Join(v.Reasons, "; "))
		s.clientFault(ss.id, true, "invalid verifier result")
		s.store.mu.Lock()
		t.avoid(ss.id, avoidForever)
		s.store.touch()
		s.store.mu.Unlock()
		return nil
	}
	res := s.evaluate(ck, t.Purpose, r, stage)
	s.log.Infof("verification %s/%s of %s on %s: %s", ck.ID, t.Purpose, ck.TaskID, ss.name, res)
	s.store.mu.Lock()
	ck.Done[t.Purpose] = res
	ck.Verifiers[t.Purpose] = ss.id
	complete := len(ck.Done) == len(ck.Subtasks)
	s.store.touch()
	s.store.mu.Unlock()
	s.dropInternal(t.ID)
	if complete {
		s.finalizeCheck(ck)
	}
	return nil
}

// evaluate compares one sub-task result with the expectation. Returns "ok" or "fail: ...".
func (s *Server) evaluate(ck *Check, purpose string, r wire.Result, stage string) string {
	stem := anonStem(r.AttemptID)
	outPath := filepath.Join(stage, stem+".out")
	info, _ := orca.ParseOutputFile(outPath)
	tol := s.conf().EnergyTolerance
	switch purpose {
	case "scf", "grad":
		// The iteration cap (3 at first) produced a false positive: honest 3-chlorophenol
		// orbitals needed 4 cycles. Not converging within the cap is inconclusive; the
		// energy and gradient comparisons are what expose forged results.
		inconclusive := fmt.Sprintf("inconclusive: the check SCF did not converge within %d iterations (verify.check_max_scf_iterations)", s.conf().CheckMaxSCFIter)
		if r.Failure == "orca" {
			return inconclusive
		}
		scf, err := orca.ParseSCF(outPath)
		if err != nil || !scf.HasTotal {
			return "fail: no SCF energy in the check output"
		}
		if len(scf.Cycles) > 0 && scf.Cycles[len(scf.Cycles)-1] > s.conf().CheckMaxSCFIter {
			return inconclusive
		}
		if d := math.Abs(scf.SCF - ck.Expect.SCFTotal); d > math.Max(tol, 1e-6) {
			return fmt.Sprintf("fail: SCF energy %.9f differs from the reported %.9f by %.2e Eh", scf.SCF, ck.Expect.SCFTotal, d)
		}
		if purpose == "grad" && !scf.HasGrad {
			// MP2 (and other non-SCF) gradients print no "Norm"/"MAX gradient" summary in the
			// output; the .engrad file always has the vector (honest MP2 optimizations were
			// reported as "no gradient" before)
			if _, g, err := orca.ParseEngrad(filepath.Join(stage, stem+".engrad")); err == nil && len(g) > 0 {
				var sum float64
				for _, x := range g {
					sum += x * x
					scf.GradMax = math.Max(scf.GradMax, math.Abs(x))
				}
				scf.GradNorm, scf.HasGrad = math.Sqrt(sum), true
			}
		}
		if purpose == "grad" {
			if !scf.HasGrad {
				return "fail: no gradient in the check output"
			}
			if scf.GradMax > 0 && ck.Expect.GradMax > 0 && scf.GradMax > ck.Expect.GradMax {
				return fmt.Sprintf("fail: max gradient %.2e at the reported final geometry exceeds %.1e (not converged to the requested level)", scf.GradMax, ck.Expect.GradMax)
			}
			if scf.GradMax == 0 && scf.GradNorm > gradTolerance {
				return fmt.Sprintf("fail: gradient norm %.2e at the reported final geometry (not a stationary point)", scf.GradNorm)
			}
		}
		return "ok"
	case "hess_plus", "hess_minus":
		if r.Failure != "" {
			return "fail: gradient calculation failed"
		}
		// keep the engrad file until both gradients are available
		dst := filepath.Join(s.conf().Root, dState, "internal", ck.ID, purpose+".engrad")
		if err := copyFile(filepath.Join(stage, stem+".engrad"), dst); err != nil {
			return "fail: missing .engrad"
		}
		return "ok"
	case "replica":
		if r.Failure == "orca" {
			return "fail: ORCA failed on the replica while the original succeeded"
		}
		if info == nil || len(info.FinalEnergies) == 0 || !ck.Expect.HasFinal {
			return "fail: replica produced no final energy"
		}
		e := info.FinalEnergies[len(info.FinalEnergies)-1]
		if d := math.Abs(e - ck.Expect.Final); d > tol {
			return fmt.Sprintf("fail: replica energy %.9f differs from %.9f by %.2e Eh", e, ck.Expect.Final, d)
		}
		return "ok"
	case "canary":
		return s.evaluateCanary(ck, r, outPath)
	}
	return "fail: unknown check " + purpose
}

// finalizeCheck decides the fate of the verified result once every sub-task reported.
func (s *Server) finalizeCheck(ck *Check) {
	// combine the two halves of a Hessian probe
	if _, ok := ck.Done["hess_plus"]; ok && ck.Done["hess_plus"] == "ok" && ck.Done["hess_minus"] == "ok" {
		d := filepath.Join(s.conf().Root, dState, "internal", ck.ID)
		_, gp, e1 := orca.ParseEngrad(filepath.Join(d, "hess_plus.engrad"))
		_, gm, e2 := orca.ParseEngrad(filepath.Join(d, "hess_minus.engrad"))
		if e1 != nil || e2 != nil || len(gp) != len(ck.Expect.V) || len(gm) != len(ck.Expect.V) {
			ck.Done["hess"] = "fail: unreadable probe gradients"
		} else {
			num, den := 0.0, 0.0
			for i := range ck.Expect.Hv {
				fd := (gp[i] - gm[i]) / (2 * ck.Expect.Step)
				num += (ck.Expect.Hv[i] - fd) * (ck.Expect.Hv[i] - fd)
				den += ck.Expect.Hv[i] * ck.Expect.Hv[i]
			}
			rel := math.Sqrt(num) / math.Max(math.Sqrt(den), 1e-6)
			s.log.Infof("hessian probe of %s: relative error %.5f (tolerance %.3f)", ck.TaskID, rel, s.conf().HessTolerance)
			if rel > s.conf().HessTolerance {
				ck.Done["hess"] = fmt.Sprintf("fail: Hessian does not match the probe gradients (relative error %.3f)", rel)
			} else {
				ck.Done["hess"] = fmt.Sprintf("ok (relative error %.4f)", rel)
			}
		}
		delete(ck.Done, "hess_plus")
		delete(ck.Done, "hess_minus")
	}
	var fails []string
	purposes := make([]string, 0, len(ck.Done))
	for p := range ck.Done {
		purposes = append(purposes, p)
	}
	sort.Strings(purposes)
	var unsure []string
	for _, p := range purposes {
		switch {
		case strings.HasPrefix(ck.Done[p], "fail") || strings.HasPrefix(ck.Done[p], "invalid"):
			fails = append(fails, p+": "+ck.Done[p])
		case strings.HasPrefix(ck.Done[p], "inconclusive"):
			unsure = append(unsure, p+": "+ck.Done[p])
		}
	}
	s.forgetCheck(ck)
	if ck.Recheck {
		defer s.recheckDone(ck.Client, len(fails) > 0)
	}

	if ck.Expect.Canary {
		s.finishCanary(ck, fails)
		return
	}
	if ck.Rescue {
		s.finishRescue(ck, fails, unsure)
		return
	}
	if len(fails) == 0 && len(unsure) > 0 {
		// deterministic and not evidence of cheating: no third host, no penalty, but the
		// result is not left in output/ unverified either
		s.condemn(ck, unsure, dErrors, false)
		return
	}
	if len(fails) == 0 && ck.ConfirmOf != "" && len(ck.FirstFail) > 0 {
		// One verifier says "forged", another says "fine": a dispute. Before, the later
		// host won and the first verifiers were penalized; in the lab a colluding cheater
		// took the confirmation, its partner's forged result was accepted and two honest
		// verifiers were blamed. Now nobody wins by a single vote: weird/, no penalty.
		s.condemn(ck, append(append([]string{"disputed: the first verifiers reported"}, ck.FirstFail...),
			"an independent host found no problem (no client penalized; review and accept or reject)"), dWeird, false)
		return
	}
	if len(fails) == 0 && ck.ConfirmOf == "" && s.conf().SecondOpinion {
		// every sub-check ran on ONE host (a colluder could approve its partner's result):
		// repeat the cheapest one on another independent host before accepting
		// With second_opinion_unavailable = weird the second opinion is planned even when no
		// independent host is connected right now, as long as one is registered: it waits,
		// and expires (-> weird/) only if none comes within the check's stall limit. Sending
		// the result to weird/ at once left two colluding cheaters, who had approved each
		// other while the honest hosts were offline, unchecked and unpenalized (Windows
		// adversarial round, collude).
		ex := append([]string{ck.Client}, uniqueValues(ck.Verifiers)...)
		if v := uniqueValues(ck.Verifiers); len(v) == 1 && (s.independentHost(ex) || s.conf().NoSecondOpinion == "weird" && s.independentKnown(ex)) {
			if want := cheapestPurpose(ck.Done); want != "" {
				stem := strings.TrimSuffix(path.Base(ck.TaskID), path.Ext(ck.TaskID))
				info, _ := orca.ParseOutputFile(filepath.Join(s.conf().Root, filepath.FromSlash(ck.ResultRel), stem+".out"))
				if info != nil && func() bool {
					// (nil, nil): nothing applicable, so no second opinion was planned
					next, err := s.createCheck(ck.TaskID, ck.ResultRel, stem, ck.Client, map[string]bool{want: true}, info, ck.ID, v, nil)
					return err == nil && next != nil
				}() {
					s.log.Infof("verification of %s passed on a single host; second opinion (%s) on another", ck.TaskID, want)
					return
				}
			}
		}
		if v := uniqueValues(ck.Verifiers); len(v) == 1 && s.conf().NoSecondOpinion == "weird" {
			s.condemn(ck, []string{"unconfirmed: passed on a single host and no independent host could give a second opinion (verify.second_opinion_unavailable = weird)"}, dWeird, false)
			return
		}
	}
	if len(fails) == 0 {
		s.ledger.Append(ledger.Entry{Kind: "verify", Task: ck.TaskID, Client: ck.Client, Verdict: "passed", Note: "check " + ck.ID + ": " + strings.Join(purposes, ",")})
		if top, _ := splitSpool(ck.ResultRel); top == dOutput {
			s.retireWeird(ck.TaskID) // the accepted result is final now
		}
		_ = s.reg.update(ck.Client, func(c *ClientRec) { c.Verified++ })
		s.log.Infof("verification of %s passed (%s)", ck.TaskID, strings.Join(purposes, ","))
		s.addCanarySource(ck)
		return
	}
	if ck.ConfirmOf == "" {
		// confirm on a third host before condemning
		want := map[string]bool{}
		for _, f := range fails {
			p, _, _ := strings.Cut(f, ":")
			if strings.HasPrefix(p, "hess") {
				p = "hess"
			}
			want[p] = true
		}
		// Only the hosts that reported a failure are excluded: one that passed another
		// sub-check is still independent of the accusation. Before, every verifier was
		// excluded, and in a 4-host lab one lying verifier left nobody to confirm, so the
		// honest result was condemned "unconfirmed".
		accusers := map[string]bool{}
		for sub, who := range ck.Verifiers {
			p := sub
			if strings.HasPrefix(p, "hess") {
				p = "hess"
			}
			if want[p] {
				accusers[who] = true
			}
		}
		suspects := make([]string, 0, len(accusers))
		for who := range accusers {
			suspects = append(suspects, who)
		}
		sort.Strings(suspects)
		stem := strings.TrimSuffix(path.Base(ck.TaskID), path.Ext(ck.TaskID))
		info, _ := orca.ParseOutputFile(filepath.Join(s.conf().Root, filepath.FromSlash(ck.ResultRel), stem+".out"))
		if !s.independentHost(append([]string{ck.Client}, suspects...)) {
			// Nobody left to confirm (small network): the failed check stands. Before,
			// the confirmation waited forever, expired, and the forged result stayed in
			// output/ (lab: 3 hosts, every forged energy unconfirmable).
			s.condemn(ck, append(fails, "unconfirmed: no independent host available to confirm"), dWeird, true)
			return
		}
		if info != nil {
			if c, err := s.createCheck(ck.TaskID, ck.ResultRel, stem, ck.Client, want, info, ck.ID, suspects, fails); err == nil && c != nil {
				s.log.Warnf("verification of %s failed (%s); confirming on another host", ck.TaskID, strings.Join(fails, "; "))
				return
			}
		}
	}
	if ck.ConfirmOf != "" && len(ck.FirstFail) == 0 {
		// the first host passed it, the second opinion failed it: also a dispute
		s.condemn(ck, append(append([]string{"disputed: a second opinion failed"}, fails...),
			"the first verifier passed it (no client penalized; review and accept or reject)"), dWeird, false)
		return
	}
	s.condemn(ck, fails, dWeird, true)
}

// cheapestPurpose picks the least expensive sub-check that ran (for a second opinion).
func cheapestPurpose(done map[string]string) string {
	for _, p := range []string{"scf", "grad", "replica"} {
		if _, ok := done[p]; ok {
			return p
		}
	}
	return ""
}

func uniqueValues(m map[string]string) []string {
	seen := map[string]bool{}
	var out []string
	for _, v := range m {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	sort.Strings(out)
	return out
}

// condemn moves a result that failed active verification from output/ to dir: weird/ for
// failed checks, errors/ for inconclusive ones (the verdict file says which and why).
func (s *Server) condemn(ck *Check, fails []string, dir string, penalize bool) {
	s.moveMu.Lock()
	defer s.moveMu.Unlock()
	cfg := s.conf()
	top, rest := splitSpool(ck.ResultRel)
	if top != dOutput {
		return
	}
	src := filepath.Join(cfg.Root, filepath.FromSlash(ck.ResultRel))
	if !fileExists(src) {
		return
	}
	writeVerdict(src, &Verdict{Checks: []string{"active:" + ck.ID}, Reasons: fails})
	// bring the input along for the review, unless the result has its own copy: that is the
	// one the client ran (e.g. with "%pal nprocs 1" added), signed in its manifest; writing
	// the original over it made verify report it modified (found by the lab campaign)
	if in := filepath.Join(src, path.Base(ck.TaskID)); !fileExists(in) {
		copyFile(filepath.Join(cfg.Root, dCompleted, filepath.FromSlash(ck.TaskID)), in)
	}
	dstRel := freeName(cfg.Root, path.Join(dir, rest))
	dst := filepath.Join(cfg.Root, filepath.FromSlash(dstRel))
	if err := moveFile(src, dst); err != nil {
		s.log.Errorf("moving %s to %s/: %v", ck.ResultRel, dir, err)
		return
	}
	if dir == dErrors {
		s.ledgerDir("verify_inconclusive", dstRel, "check "+ck.ID+" inconclusive; moved from "+ck.ResultRel+": "+strings.Join(fails, "; "))
		s.hookResult(ck.TaskID, dstRel)
		s.log.Warnf("result %s: verification INCONCLUSIVE -> %s: %s", ck.TaskID, dstRel, strings.Join(fails, "; "))
		s.alertf("errors", "result %s moved to %s: verification inconclusive: %s", ck.TaskID, dstRel, strings.Join(fails, "; "))
		return
	}
	s.ledgerDir("verify_failed", dstRel, "check "+ck.ID+" failed; moved from "+ck.ResultRel+": "+strings.Join(fails, "; "))
	s.hookResult(ck.TaskID, dstRel)
	s.ledger.Append(ledger.Entry{Kind: "verify", Task: ck.TaskID, Client: ck.Client, Verdict: "failed", Note: "moved to " + dstRel})
	if !penalize || unconfirmed(fails) {
		defer s.rescueWeird(ck, dstRel)
	} else {
		defer s.retryWeird(ck.TaskID, ck.Client, dstRel)
	}
	if penalize {
		s.clientFault(ck.Client, true, "failed active verification of "+ck.TaskID)
	}
	s.log.Warnf("result %s FAILED active verification -> %s: %s", ck.TaskID, dstRel, strings.Join(fails, "; "))
	s.alertf("verify_failed", "result %s from client %s failed active verification: %s", ck.TaskID, ck.Client, strings.Join(fails, "; "))
	if penalize && cfg.FailurePolicy == "quarantine" {
		s.quarantine(ck.Client, "failed active verification of "+ck.TaskID)
	}
}

// dropInternal removes a finished internal task from the queue.
// forgetCheck removes a check: its sub-tasks, its record and its files.
func (s *Server) forgetCheck(ck *Check) {
	for id := range ck.Subtasks {
		s.dropInternal(id)
	}
	s.store.mu.Lock()
	delete(s.store.Checks, ck.ID)
	s.store.touch()
	s.store.mu.Unlock()
	os.RemoveAll(filepath.Join(s.conf().Root, dState, "internal", ck.ID))
}

func (s *Server) dropInternal(id string) {
	s.store.mu.Lock()
	delete(s.store.Tasks, id)
	s.store.touch()
	s.store.mu.Unlock()
}

// expireChecks forgets checks that could not complete (e.g. no independent host).
func (s *Server) expireChecks() {
	online := s.onlineClients()
	now := time.Now()
	s.store.mu.Lock()
	var old, done []*Check
	for _, ck := range s.store.Checks {
		if now.Sub(ck.Created) > checkLifetime {
			old = append(old, ck)
			continue
		}
		// its files are removed only when it is finished: a check without them was
		// finished before the server stopped without saving its state (the ledger has
		// the outcome). Its sub-tasks could only fail ("cannot read task") and end in
		// errors/ (Windows chaos test), so drop it.
		if _, err := os.Stat(filepath.Join(s.conf().Root, dState, "internal", ck.ID)); os.IsNotExist(err) {
			s.log.Warnf("check %s of %s was already finished (its files are gone; state saved before the end); dropped", ck.ID, ck.TaskID)
			done = append(done, ck)
			continue
		}
		switch stalled := s.checkStalled(ck, online); {
		case stalled && ck.Stalled.IsZero():
			ck.Stalled = now
			s.store.touch()
		case stalled && now.Sub(ck.Stalled) > stallLimit:
			s.log.Warnf("check %s of %s: no connected host may run its sub-tasks for %s; giving up", ck.ID, ck.TaskID, stallLimit)
			old = append(old, ck)
		case !stalled && !ck.Stalled.IsZero():
			ck.Stalled = time.Time{}
			s.store.touch()
		}
	}
	s.store.mu.Unlock()
	for _, ck := range done { // nothing to decide any more: no verdict, no penalty
		s.forgetCheck(ck)
		if ck.Recheck {
			// a quarantine re-check: count it as done (not as bad), or the client's
			// re-check round never ends and nobody is told (found by code review); the
			// count stops at zero if it was already counted before the unclean stop
			s.recheckDone(ck.Client, false)
		}
	}
	for _, ck := range old {
		s.forgetCheck(ck)
		s.ledger.Append(ledger.Entry{Kind: "verify", Task: ck.TaskID, Verdict: "unverified", Note: "check " + ck.ID + " expired (no independent host completed it)"})
		if ck.ConfirmOf != "" && len(ck.FirstFail) > 0 {
			// a failed check that could not be confirmed must not leave the result accepted
			s.condemn(ck, append(ck.FirstFail, "unconfirmed: the confirmation check expired"), dWeird, true)
		}
		if ck.Rescue {
			s.retryWeird(ck.TaskID, ck.Client, ck.ResultRel) // the tie-break never ran: recompute
		}
		if ck.ConfirmOf != "" && len(ck.FirstFail) == 0 && s.conf().NoSecondOpinion == "weird" {
			// a second opinion that never ran (verify.second_opinion_unavailable = weird)
			s.condemn(ck, []string{"unconfirmed: the second opinion expired (verify.second_opinion_unavailable = weird)"}, dWeird, false)
		}
		if c := s.reg.get(ck.Client); ck.ConfirmOf == "" && !ck.Rescue && !ck.Expect.Canary && c != nil && (c.Status == stQuarantined || c.Status == stRevoked) {
			// an unverified result of a client already known to cheat must not stay accepted
			// (dodge round: 4 forged results stayed in output/ when their checks stalled)
			s.condemn(ck, []string{"unverified: the check of a result from a " + c.Status + " client expired"}, dWeird, false)
		}
		if ck.Recheck {
			s.recheckDone(ck.Client, false) // could not be re-checked: do not count it as bad
		}
	}
}

// onlineClients maps the clients connected now to their registry status.
func (s *Server) onlineClients() map[string]string {
	out := map[string]string{}
	s.sessMu.Lock()
	ids := make([]string, 0, len(s.sessions))
	for id := range s.sessions {
		ids = append(ids, id)
	}
	s.sessMu.Unlock()
	for _, id := range ids {
		if c := s.reg.get(id); c != nil {
			out[id] = c.Status
		}
	}
	return out
}

// checkStalled reports whether none of a check's pending sub-tasks can run: none is
// running and no connected client may take any of them. Caller holds store.mu.
func (s *Server) checkStalled(ck *Check, online map[string]string) bool {
	pending := false
	for id := range ck.Subtasks {
		t := s.store.Tasks[id]
		if t == nil {
			continue
		}
		pending = true
		for _, a := range s.store.Attempts {
			if a.TaskID == id && !a.Lost {
				return false
			}
		}
		for cid, st := range online {
			if t.avoided(cid) || (t.OnlyFor != "" && t.OnlyFor != cid) {
				continue
			}
			if st == stActive || (st == stQuarantined && t.OnlyFor == cid) {
				return false
			}
		}
	}
	return pending
}

// independentHost reports whether an active client outside excluded is connected now. A
// registered client that is offline does not count: checks planned for it could wait for
// days (stallLimit covers a host that leaves after the check is planned).
// independentKnown reports whether a registered active client outside excluded exists,
// connected or not.
func (s *Server) independentKnown(excluded []string) bool {
	ex := map[string]bool{}
	for _, id := range excluded {
		ex[id] = true
	}
	for _, c := range s.reg.list() {
		if c.Status == stActive && !ex[c.ID] {
			return true
		}
	}
	return false
}

func (s *Server) independentHost(excluded []string) bool {
	ex := map[string]bool{}
	for _, id := range excluded {
		ex[id] = true
	}
	for id, st := range s.onlineClients() {
		if st == stActive && !ex[id] {
			return true
		}
	}
	return false
}

func sha256hex(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// unconfirmed reports whether a failure was never confirmed by a second independent host.
func unconfirmed(fails []string) bool {
	for _, f := range fails {
		if strings.HasPrefix(f, "unconfirmed:") {
			return true
		}
	}
	return false
}

// rescueWeird handles a result that went to weird/ WITHOUT proof of cheating (a dispute
// between verifiers, or a failed check nobody could confirm). Recomputing the whole job
// is the expensive way out; instead the cheapest check (SCF re-check when orbitals were
// returned, otherwise a replica) runs on a host that took no part so far. It passes -> the
// result goes back to output/ (two hosts against one); it fails -> the task is requeued as
// usual (retryWeird). verify.rescue_weird = false requeues at once.
func (s *Server) rescueWeird(ck *Check, weirdRel string) {
	cfg := s.conf()
	exclude := append(append([]string{}, ck.Suspects...), uniqueValues(ck.Verifiers)...)
	if !cfg.RescueWeird || !s.independentHost(append([]string{ck.Client}, exclude...)) {
		s.retryWeird(ck.TaskID, ck.Client, weirdRel)
		return
	}
	stem := strings.TrimSuffix(path.Base(ck.TaskID), path.Ext(ck.TaskID))
	dir := filepath.Join(cfg.Root, filepath.FromSlash(weirdRel))
	info, err := orca.ParseOutputFile(filepath.Join(dir, stem+".out"))
	if err != nil {
		s.retryWeird(ck.TaskID, ck.Client, weirdRel)
		return
	}
	want := map[string]bool{"replica": true}
	if fileExists(filepath.Join(dir, stem+".gbw")) {
		want = map[string]bool{"scf": true}
	}
	rc, err := s.createCheck(ck.TaskID, weirdRel, stem, ck.Client, want, info, "", exclude, nil)
	if err != nil || rc == nil {
		s.retryWeird(ck.TaskID, ck.Client, weirdRel)
		return
	}
	// who made the accusation: the first-round verifiers of a dispute settled against
	// them, otherwise the hosts of this check whose sub-check failed
	accusers := ck.Suspects
	if ck.ConfirmOf == "" || len(ck.FirstFail) == 0 {
		accusers = nil
		for sub, who := range ck.Verifiers {
			p := sub
			if strings.HasPrefix(p, "hess") {
				p = "hess"
			}
			if d := ck.Done[p]; strings.HasPrefix(d, "fail") || strings.HasPrefix(d, "invalid") {
				accusers = append(accusers, who)
			}
		}
	}
	s.store.mu.Lock()
	rc.Rescue = true
	rc.Accusers = accusers
	s.store.touch()
	s.store.mu.Unlock()
	s.ledger.Append(ledger.Entry{Kind: "rescue_weird", Task: ck.TaskID, Client: ck.Client, Note: "tie-break check " + rc.ID + " on a new host before recomputing"})
	s.log.Infof("result %s in weird/ without proof of cheating: tie-break check %s before recomputing", ck.TaskID, rc.ID)
}

// finishRescue concludes a tie-break check of a result in weird/.
func (s *Server) finishRescue(ck *Check, fails, unsure []string) {
	if len(fails) > 0 || len(unsure) > 0 {
		s.log.Warnf("tie-break of %s failed (%s); recomputing the task", ck.TaskID, strings.Join(append(fails, unsure...), "; "))
		s.retryWeird(ck.TaskID, ck.Client, ck.ResultRel)
		return
	}
	s.moveMu.Lock()
	defer s.moveMu.Unlock()
	cfg := s.conf()
	top, _ := splitSpool(ck.ResultRel)
	if top != dWeird || !fileExists(filepath.Join(cfg.Root, filepath.FromSlash(ck.ResultRel))) {
		return // reviewed (accepted or rejected) by the admin meanwhile
	}
	// as a manual accept (the same rules: one result per task, a queued retry dropped)
	_, dstRel, err := s.acceptWeirdLocked(ck.ResultRel, "automatically accepted (tie-break check "+ck.ID+" passed)")
	if err != nil {
		s.log.Errorf("tie-break of %s passed but the result stays in weird/: %v", ck.TaskID, err)
		return
	}
	s.pruneSpool()
	s.ledger.Append(ledger.Entry{Kind: "verify", Task: ck.TaskID, Client: ck.Client, Verdict: "passed", Note: "tie-break " + ck.ID + ": back in " + dstRel})
	s.log.Infof("result %s rescued from weird/ by a tie-break check -> %s", ck.TaskID, dstRel)
	// the producer and two independent hosts agree against the accuser: count it as a
	// fault (a lying verifier is a cheater; quarantine stays forgiving and reversible)
	for _, id := range uniqueStrings(ck.Accusers) {
		s.clientFault(id, true, "reported a false verification failure of "+ck.TaskID+" (overruled by a tie-break)")
		if cfg.FailurePolicy == "quarantine" {
			s.quarantine(id, "false verification failure of "+ck.TaskID)
		}
	}
	s.alertf("verify_failed", "result %s rescued from weird/: a new independent host confirmed it (-> %s)", ck.TaskID, dstRel)
}

func uniqueStrings(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, v := range in {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}

// reputationMultiplier scales the check fractions of a client past probation by its record.
func reputationMultiplier(cfg *Config, c *ClientRec, now time.Time) float64 {
	if !cfg.Reputation {
		return 1
	}
	if !c.LastFailure.IsZero() && now.Sub(c.LastFailure) < cfg.RecentFailureWindow {
		return cfg.RecentFailureMultiplier
	}
	if c.Verified >= cfg.TrustedAfter {
		return cfg.TrustedMultiplier
	}
	return 1
}
