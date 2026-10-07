package main

import (
	"math"
	"math/rand/v2"
	"path/filepath"
	"testing"
	"time"

	"github.com/agaloya/matriline/common/wire"
)

func TestFeaturize(t *testing.T) {
	phi, ok := featurize("! wB97M-V def2-TZVPP\n* xyz 0 2\nO 0 0 0\nH 0 0 1\n*\n")
	if !ok {
		t.Fatal("not featurized")
	}
	names := featureNames()
	get := func(n string) float64 {
		for i, x := range names {
			if x == n {
				return phi[i]
			}
		}
		t.Fatalf("no feature %s", n)
		return 0
	}
	if math.Abs(get("ln_electrons")-math.Log(9)) > 1e-9 || get("open_shell") != 1 ||
		get("method_range_separated") != 1 || get("basis_tz") != 1 || get("opt") != 0 {
		t.Fatalf("features %v", phi)
	}
	if _, ok := featurize("! B3LYP def2-SVP\n* xyzfile 0 1 geom.xyz\n"); ok {
		t.Fatal("xyzfile input must not be rated")
	}
}

// The fit must recover known client speed factors from noisy synthetic timings.
func TestFitRecoversClientSpeeds(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2))
	slow := map[string]float64{"fast": 1, "mid": 1.5, "slow": 3}
	var ss []sample
	for i := 0; i < 300; i++ {
		atoms := 2 + r.IntN(20)
		in := "! B3LYP def2-SVP\n* xyz 0 1\n"
		for a := 0; a < atoms; a++ {
			in += "C 0 0 0\n"
		}
		phi, _ := featurize(in + "*\n")
		for c, f := range slow {
			sec := 10 * math.Pow(float64(atoms), 2.5) * f * math.Exp(0.05*r.NormFloat64())
			ss = append(ss, sample{phi, c, sec})
		}
	}
	m := fit(ss)
	for c, f := range slow {
		if got := math.Exp(m.Host[c] - m.Host["fast"]); math.Abs(got-f)/f > 0.05 {
			t.Errorf("%s: speed factor %.2f, want %.2f", c, got, f)
		}
	}
}

// Earliest finish: a long job is held for a faster client that will finish it sooner, but
// not for one busy for too long, nor for one that cannot run it.
func TestEarliestFinishPick(t *testing.T) {
	dir := t.TempDir()
	if err := cmdInit(dir, "en"); err != nil {
		t.Fatal(err)
	}
	s, err := openServer(filepath.Join(dir, "server.conf"))
	if err != nil {
		t.Fatal(err)
	}
	n := len(featureNames())
	theta := make([]float64, n)
	theta[0] = 1 // base = phi[0] = ln(seconds on the fastest client)
	phi := func(sec float64) []float64 { p := make([]float64, n); p[0] = math.Log(sec); return p }
	long := &TaskState{ID: "long.inp", InputSHA: "L"}
	short := &TaskState{ID: "short.inp", InputSHA: "S"}
	running := &TaskState{ID: "run.inp", InputSHA: "R"}
	for _, x := range []*TaskState{long, short, running} {
		s.store.Tasks[x.ID] = x
	}
	s.learn.feats = map[string][]float64{"L": phi(3600), "S": phi(600), "R": phi(3600)}
	s.learn.featsOK = map[string]bool{"L": true, "S": true, "R": true}
	s.reg.add(&ClientRec{ID: "fast", Name: "fast", Status: stActive})
	s.reg.add(&ClientRec{ID: "slow", Name: "slow", Status: stActive})
	s.sessions = map[string]*Session{"fast": {s: s, id: "fast", name: "fast", hbAccept: true, offer: wire.OfferRes{Slots: 1, MemPerSlotMB: 1024}}}
	pick := func(slowness, fastBusyFor float64) string {
		s.learn.m = &Model{Theta: theta, Host: map[string]float64{"fast": 0, "slow": math.Log(slowness)}, N: 100}
		s.store.Attempts = map[string]*Attempt{"a": {ID: "a", TaskID: running.ID, ClientID: "fast",
			Started: time.Now().Add(-time.Duration(3600-fastBusyFor) * time.Second)}}
		s.store.mu.Lock()
		defer s.store.mu.Unlock()
		return s.learnedPick("slow", []*TaskState{long, short}).ID
	}
	// fast is free in 100 s: the long job finishes there at ~3700 s, on slow (3x) at 10800 s
	if got := pick(3, 100); got != "short.inp" {
		t.Errorf("fast free soon: slow got %s, want short.inp", got)
	}
	// fast busy for 3600 s more: long there at 7200 s, on slow (1.5x) at 5400 s
	if got := pick(1.5, 3600); got != "long.inp" {
		t.Errorf("fast busy long: slow got %s, want long.inp", got)
	}
	// fast cannot run the long job (memory): never held for it
	long.MemMB = 4000
	if got := pick(3, 100); got != "long.inp" {
		t.Errorf("fast lacks memory: slow got %s, want long.inp", got)
	}
}

// Cost at scale: fitting on maxSamples results with 50 clients, and one pick among 2000
// queued tasks with 50 connected clients and 400 running attempts.
func BenchmarkLearnedScale(b *testing.B) {
	n := len(featureNames())
	ss := make([]sample, maxSamples)
	for i := range ss {
		p := make([]float64, n)
		p[0], p[1], p[2] = 1, rand.Float64()*4, rand.Float64()*3
		ss[i] = sample{p, string(rune('A' + i%50)), 60 + rand.Float64()*3600}
	}
	b.Run("fit", func(b *testing.B) {
		for range b.N {
			fit(ss)
		}
	})
	dir := b.TempDir()
	cmdInit(dir, "en")
	s, _ := openServer(filepath.Join(dir, "server.conf"))
	m := fit(ss)
	m.N = maxSamples
	s.learn.m = m
	s.learn.feats, s.learn.featsOK = map[string][]float64{}, map[string]bool{}
	var cands []*TaskState
	for i := range 2000 {
		t := &TaskState{ID: string(rune(i)), InputSHA: string(rune(i))}
		s.learn.feats[t.InputSHA] = ss[i].phi
		s.store.Tasks[t.ID] = t
		cands = append(cands, t)
	}
	s.sessions = map[string]*Session{}
	for c := range 50 {
		id := string(rune('A' + c))
		s.reg.add(&ClientRec{ID: id, Name: id, Status: stActive})
		s.sessions[id] = &Session{s: s, id: id, hbAccept: true, offer: wire.OfferRes{Slots: 8}}
		for k := range 8 {
			a := &Attempt{ID: id + string(rune(k)), TaskID: cands[c*8+k].ID, ClientID: id, Started: time.Now()}
			s.store.Attempts[a.ID] = a
		}
	}
	slowest, worst := "", -1e9
	for id, h := range m.Host {
		if h > worst {
			slowest, worst = id, h
		}
	}
	b.Run("pick", func(b *testing.B) {
		for range b.N {
			s.learnedPick(slowest, cands)
		}
	})
}
