package main

// Learned scheduling (tasks.scheduler = learned). The server learns, from its own accepted
// results, how long a job takes from its input and how fast each client is:
//
//	ln(seconds) = θ·φ(input) + h[client]
//
// φ: bias, ln(electrons), ln(atoms), Opt, Freq, open shell, method family, basis family.
// h: per-client log slowness (one-hot). Fitted by ridge-regularised least squares (normal
// equations, Gaussian elimination; stdlib only, on the CPU, a fraction of a second). Inside
// the highest priority level the server then gives the longest predicted job first
// (longest-processing-time-first, within 4/3 of optimal on identical machines: Graham, SIAM
// J. Appl. Math. 17 (1969) 416, doi:10.1137/0117039), held back for a faster client when
// that one would finish it earlier (earliest finish time, as in list scheduling for
// machines of different speeds).

import (
	"bufio"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/agaloya/matriline/common/manifest"
)

// method and basis families (keyword substrings, lower case; first match wins)
var methodFamilies = []struct {
	name string
	keys []string
}{
	{"cc", []string{"ccsd", "dlpno", "qcisd"}},
	{"mp2", []string{"mp2"}},
	{"double_hybrid", []string{"b2plyp", "dsd-", "pwpb95", "b2gp"}},
	{"composite_3c", []string{"-3c"}},
	{"range_separated", []string{"wb97", "cam-b3lyp", "lc-"}},
	{"hybrid", []string{"b3lyp", "pbe0", "tpssh", "m06", "bhandhlyp", "b3pw91", "x3lyp", "r2scan0", "scan0"}},
	{"gga_mgga", []string{"pbe", "blyp", "bp86", "tpss", "r2scan", "scan", "revpbe", "pw91", "b97"}},
	{"hf", []string{"hf", "uhf", "rhf", "rohf"}},
	{"semiempirical", []string{"xtb", "pm3", "am1", "mndo"}},
}
var basisFamilies = []struct {
	name string
	keys []string
}{
	{"qz", []string{"qzv", "pvqz", "pcseg-3", "aug-cc-pvqz"}},
	{"tz", []string{"tzv", "pvtz", "pcseg-2"}},
	{"dz", []string{"svp", "sv(p)", "pvdz", "6-31", "pcseg-1", "dzp"}},
	{"min", []string{"sto-3g", "minix", "mini"}},
}

// Z for electron counting (symbols up to Kr cover nearly every organic job; others count 30)
var atomicZ = map[string]int{"H": 1, "He": 2, "Li": 3, "Be": 4, "B": 5, "C": 6, "N": 7, "O": 8, "F": 9, "Ne": 10,
	"Na": 11, "Mg": 12, "Al": 13, "Si": 14, "P": 15, "S": 16, "Cl": 17, "Ar": 18, "K": 19, "Ca": 20,
	"Br": 35, "I": 53, "Fe": 26, "Cu": 29, "Zn": 30, "Ni": 28, "Co": 27, "Mn": 25, "Cr": 24, "Ti": 22}

func featureNames() []string {
	n := []string{"bias", "ln_electrons", "ln_atoms", "opt", "freq", "open_shell"}
	for _, m := range methodFamilies {
		n = append(n, "method_"+m.name)
	}
	for _, b := range basisFamilies {
		n = append(n, "basis_"+b.name)
	}
	return n
}

// featurize reads an ORCA input. ok=false when the geometry cannot be counted (e.g.
// "* xyzfile" without the file): such tasks are scheduled in normal order.
func featurize(input string) ([]float64, bool) {
	var kw []string
	atoms, elec, charge, mult := 0, 0, 0, 1
	in := false
	sc := bufio.NewScanner(strings.NewReader(input))
	for sc.Scan() {
		t := strings.TrimSpace(sc.Text())
		low := strings.ToLower(t)
		switch {
		case strings.HasPrefix(t, "!"):
			kw = append(kw, strings.Fields(strings.ToLower(strings.TrimPrefix(t, "!")))...)
		case strings.HasPrefix(t, "*") && !in:
			f := strings.Fields(strings.TrimPrefix(t, "*"))
			if len(f) >= 3 && (strings.ToLower(f[0]) == "xyz" || strings.ToLower(f[0]) == "int") {
				charge, _ = strconv.Atoi(f[1])
				mult, _ = strconv.Atoi(f[2])
				in = true
			} else if len(f) >= 1 && strings.HasSuffix(strings.ToLower(f[0]), "file") {
				return nil, false
			}
		case in && strings.HasPrefix(t, "*"):
			in = false
		case in && t != "" && !strings.HasPrefix(low, "#"):
			sym := strings.Fields(t)[0]
			sym = strings.TrimRight(sym, "0123456789")
			if len(sym) > 1 {
				sym = sym[:1] + strings.ToLower(sym[1:])
			}
			z, ok := atomicZ[sym]
			if !ok {
				z = 30
			}
			atoms++
			elec += z
		}
	}
	if atoms == 0 {
		return nil, false
	}
	elec -= charge
	b := func(v bool) float64 {
		if v {
			return 1
		}
		return 0
	}
	has := func(keys []string) bool {
		for _, k := range kw {
			for _, s := range keys {
				if strings.Contains(k, s) {
					return true
				}
			}
		}
		return false
	}
	phi := []float64{1, math.Log(float64(max(elec, 1))), math.Log(float64(atoms)),
		b(has([]string{"opt", "optts"})), b(has([]string{"freq", "numfreq"})), b(mult > 1 || has([]string{"uks", "uhf"}))}
	found := false
	for _, m := range methodFamilies {
		v := !found && has(m.keys)
		found = found || v
		phi = append(phi, b(v))
	}
	found = false
	for _, bf := range basisFamilies {
		v := !found && has(bf.keys)
		found = found || v
		phi = append(phi, b(v))
	}
	return phi, true
}

// Model is the fitted predictor.
type Model struct {
	Theta  []float64          `json:"theta"`
	Host   map[string]float64 `json:"host"` // client id -> log slowness
	N      int                `json:"samples"`
	RMSLog float64            `json:"rms_log_error"`
	MedRel float64            `json:"median_relative_error"`
	Fitted time.Time          `json:"fitted"`
}

type sample struct {
	phi    []float64
	client string
	sec    float64
}

// learner owns the model; it is rebuilt from output/ periodically.
type learner struct {
	mu      sync.Mutex
	m       *Model
	feats   map[string][]float64
	featsOK map[string]bool
	// samples: read from output/ once, then one is added per accepted result (reading
	// every manifest again every 10 results would not scale to 100000 results)
	samples []sample
	loaded  bool
	added   int // samples added since the last fit
}

// maxSamples bounds memory and fitting time (the newest are kept): the fit is O(n p^2)
// with p ~ 20 features + one per client, a fraction of a second at this size.
const maxSamples = 20000

// collect reads every accepted result (manifest timing + the input stored with it).
func (s *Server) collectSamples() []sample {
	root := filepath.Join(s.conf().Root, dOutput)
	var out []sample
	filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || d.Name() != manifestFile {
			return nil
		}
		raw, err := os.ReadFile(p)
		if err != nil {
			return nil
		}
		peek, err := manifest.Peek(raw)
		if err != nil {
			return nil
		}
		m, err := manifest.Verify(raw, s.clientPub(peek.ClientID)) // only authentic timings
		if err != nil || m.EndedUnix <= m.StartedUnix || m.Kind != "job" && m.Kind != "" {
			return nil
		}
		dir := filepath.Dir(p)
		in, err := os.ReadFile(filepath.Join(dir, filepath.Base(dir)+".inp"))
		if err != nil {
			return nil
		}
		phi, ok := featurize(string(in))
		if !ok {
			return nil
		}
		out = append(out, sample{phi, m.ClientID, float64(m.EndedUnix - m.StartedUnix)})
		return nil
	})
	return out
}

// fit solves min Σ(ln t - θ·φ - h_c)² + λ|β|² (λ small, keeps h identifiable).
func fit(ss []sample) *Model {
	hosts := map[string]int{}
	for _, x := range ss {
		if _, ok := hosts[x.client]; !ok {
			hosts[x.client] = len(hosts)
		}
	}
	nf := len(featureNames())
	n := nf + len(hosts)
	A := make([][]float64, n)
	for i := range A {
		A[i] = make([]float64, n+1)
	}
	row := make([]float64, n)
	for _, x := range ss {
		for i := range row {
			row[i] = 0
		}
		copy(row, x.phi)
		row[nf+hosts[x.client]] = 1
		y := math.Log(math.Max(x.sec, 1))
		for i := 0; i < n; i++ {
			if row[i] == 0 {
				continue
			}
			for k := 0; k < n; k++ {
				A[i][k] += row[i] * row[k]
			}
			A[i][n] += row[i] * y
		}
	}
	for i := 0; i < n; i++ {
		A[i][i] += 1e-3
	}
	beta := solve(A)
	m := &Model{Theta: beta[:nf], Host: map[string]float64{}, N: len(ss), Fitted: time.Now()}
	for c, i := range hosts {
		m.Host[c] = beta[nf+i]
	}
	var se float64
	var rel []float64
	for _, x := range ss {
		e := m.logPredict(x.phi, x.client) - math.Log(math.Max(x.sec, 1))
		se += e * e
		rel = append(rel, math.Abs(math.Exp(e)-1))
	}
	if len(ss) > 0 {
		m.RMSLog = math.Sqrt(se / float64(len(ss)))
		sort.Float64s(rel)
		m.MedRel = rel[len(rel)/2]
	}
	return m
}

// crossValidate returns the median relative error of predictions for held-out samples
// (k folds, deterministic interleaving); clients unseen in a fold's training set are skipped.
func crossValidate(ss []sample, k int) (float64, int) {
	if len(ss) < 2*k {
		return 0, 0
	}
	var rel []float64
	for f := 0; f < k; f++ {
		var train, test []sample
		for i, x := range ss {
			if i%k == f {
				test = append(test, x)
			} else {
				train = append(train, x)
			}
		}
		m := fit(train)
		for _, x := range test {
			if _, ok := m.Host[x.client]; !ok {
				continue
			}
			e := m.logPredict(x.phi, x.client) - math.Log(math.Max(x.sec, 1))
			rel = append(rel, math.Abs(math.Exp(e)-1))
		}
	}
	if len(rel) == 0 {
		return 0, 0
	}
	sort.Float64s(rel)
	return rel[len(rel)/2], len(rel)
}

// solve does Gaussian elimination with partial pivoting on an augmented matrix.
func solve(A [][]float64) []float64 {
	n := len(A)
	for c := 0; c < n; c++ {
		p := c
		for r := c + 1; r < n; r++ {
			if math.Abs(A[r][c]) > math.Abs(A[p][c]) {
				p = r
			}
		}
		A[c], A[p] = A[p], A[c]
		if math.Abs(A[c][c]) < 1e-12 {
			continue
		}
		for r := c + 1; r < n; r++ {
			f := A[r][c] / A[c][c]
			for k := c; k <= n; k++ {
				A[r][k] -= f * A[c][k]
			}
		}
	}
	x := make([]float64, n)
	for r := n - 1; r >= 0; r-- {
		if math.Abs(A[r][r]) < 1e-12 {
			continue
		}
		v := A[r][n]
		for k := r + 1; k < n; k++ {
			v -= A[r][k] * x[k]
		}
		x[r] = v / A[r][r]
	}
	return x
}

func (m *Model) base(phi []float64) float64 {
	v := 0.0
	for i, t := range m.Theta {
		v += t * phi[i]
	}
	return v
}

func (m *Model) logPredict(phi []float64, client string) float64 {
	return m.base(phi) + m.Host[client]
}

// refresh refits the model when enough new results arrived (cheap: a few hundred samples).
func (s *Server) refreshModel(force bool) *Model {
	l := &s.learn
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.loaded {
		l.samples, l.loaded = s.collectSamples(), true
		if len(l.samples) > maxSamples {
			l.samples = l.samples[len(l.samples)-maxSamples:]
		}
		l.added = len(l.samples)
	}
	// refit after 10 new results, or 2 % of the samples when there are many
	if l.m != nil && !force && l.added < max(10, len(l.samples)/50) {
		return l.m
	}
	l.added = 0
	if len(l.samples) == 0 {
		l.m = nil
		return nil
	}
	l.m = fit(l.samples)
	return l.m
}

// learnFrom adds one accepted result to the samples (called when it is accepted).
func (s *Server) learnFrom(t *TaskState, m *manifest.Manifest) {
	if m == nil || m.EndedUnix <= m.StartedUnix || (m.Kind != "job" && m.Kind != "") {
		return
	}
	phi, ok := s.taskFeatures(t)
	if !ok {
		return
	}
	l := &s.learn
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.loaded {
		return // the first refresh reads output/, this result included
	}
	l.samples = append(l.samples, sample{phi, m.ClientID, float64(m.EndedUnix - m.StartedUnix)})
	if len(l.samples) > maxSamples {
		l.samples = l.samples[len(l.samples)-maxSamples:]
	}
	l.added++
}

// taskFeatures caches φ per input hash.
func (s *Server) taskFeatures(t *TaskState) ([]float64, bool) {
	l := &s.learn
	l.mu.Lock()
	if l.feats == nil {
		l.feats, l.featsOK = map[string][]float64{}, map[string]bool{}
	}
	if phi, ok := l.feats[t.InputSHA]; ok || l.featsOK[t.InputSHA] {
		l.mu.Unlock()
		return phi, phi != nil
	}
	l.mu.Unlock()
	in, err := s.taskInput(t.ID)
	var phi []float64
	ok := false
	if err == nil {
		phi, ok = featurize(string(in))
	}
	l.mu.Lock()
	l.featsOK[t.InputSHA] = true
	if ok {
		l.feats[t.InputSHA] = phi
	}
	l.mu.Unlock()
	return phi, ok
}

// learnedPick returns the candidate with the longest predicted run time (LPT: longest
// processing time first), whichever client asks; nil = use the normal order. Long jobs then
// start early and the short ones fill the gaps at the end. The first version matched each
// client's speed rank to a job length (fast hosts got long jobs, slow hosts short ones):
// the long jobs waited for a fast host and ran last; on the BDE campaign's run times it
// simulated at 9.30 h against 8.18 h for LPT (lower bound 8.16 h, random order 9.58 h), and a
// real 40-job A/B on the lab was not faster than the normal order.
// Called with store.mu held.
func (s *Server) learnedPick(clientID string, cands []*TaskState) *TaskState {
	cfg := s.conf()
	l := &s.learn
	l.mu.Lock()
	m := l.m
	l.mu.Unlock()
	if m == nil || m.N < cfg.LearnedMinSamples || len(cands) < 2 {
		return nil
	}
	type cand struct {
		t    *TaskState
		base float64 // ln of the predicted time on the fastest client
	}
	cs := make([]cand, 0, len(cands))
	for _, t := range cands {
		if phi, ok := s.taskFeatures(t); ok {
			cs = append(cs, cand{t, m.base(phi)})
		}
	}
	if len(cs) == 0 {
		return nil
	}
	sort.Slice(cs, func(i, j int) bool { return cs[i].base > cs[j].base })
	if len(cs) > 50 { // only the longest matter here; keeps a request cheap with huge queues
		cs = cs[:50]
	}
	// Earliest predicted finish (user's idea, 2026-10-05): LPT gives the longest job to
	// whichever client asks; but if a faster client will be free soon enough to finish it
	// earlier, the job is held for that client and this one gets the next. Each job held
	// adds to that client's queue, so several long jobs do not wait for the same one. A
	// faster client counts only if it is online, accepting and able to take the job
	// (memory, cores, not avoided): a job is never held for a client that cannot run it. If
	// that client disconnects, the next request no longer counts it.
	hc, known := m.Host[clientID]
	if !known {
		return cs[0].t
	}
	busy := s.fasterClients(m, clientID, hc)
	for _, c := range cs {
		mine := math.Exp(c.base + hc)
		best, bestAt := "", mine
		for _, f := range busy {
			if !f.canRun(cfg, c.t) {
				continue
			}
			if at := f.freeIn + math.Exp(c.base+f.h); at < bestAt {
				best, bestAt = f.id, at
			}
		}
		if best == "" {
			return c.t
		}
		for _, f := range busy { // held for best: it is busy with it for that long
			if f.id == best {
				f.freeIn = bestAt
			}
		}
	}
	return cs[len(cs)-1].t // every long job finishes earlier elsewhere: take the shortest
}

// fastClient is a connected client faster than the one asking, with the predicted time
// until one of its slots is free.
type fastClient struct {
	id     string
	h      float64 // ln slowness
	freeIn float64 // seconds
	mem    int     // memory per slot (MB)
	pool   bool    // memory pool (resources.memory_total)
	procs  int
}

func (f *fastClient) canRun(cfg *Config, t *TaskState) bool {
	if t.avoided(f.id) || (t.OnlyFor != "" && t.OnlyFor != f.id) || procsOf(cfg, t) > max(f.procs, 1) {
		return false
	}
	return f.pool || t.MemMB <= 0 || t.MemMB <= f.mem
}

// fasterClients lists the connected, active, accepting clients faster than the asking one
// and when each will have a slot free (running attempts' predicted remaining time). Caller
// holds store.mu.
func (s *Server) fasterClients(m *Model, asking string, hAsk float64) []*fastClient {
	var out []*fastClient
	s.sessMu.Lock()
	sessions := make([]*Session, 0, len(s.sessions))
	for _, ss := range s.sessions {
		sessions = append(sessions, ss)
	}
	s.sessMu.Unlock()
	for _, ss := range sessions {
		h, known := m.Host[ss.id]
		if ss.id == asking || !known || h >= hAsk || !ss.hbAccept && !ss.offer.Accepting || ss.outdated != "" {
			continue
		}
		if rec := s.reg.get(ss.id); rec == nil || rec.Status != stActive {
			continue
		}
		f := &fastClient{id: ss.id, h: h, mem: ss.offer.MemPerSlotMB, pool: ss.offer.MemTotalMB > 0, procs: ss.offer.MaxProcs}
		var remaining []float64
		for _, a := range s.store.Attempts {
			if a.ClientID != ss.id || a.Lost {
				continue
			}
			rem := 0.0
			if t := s.store.Tasks[a.TaskID]; t != nil {
				if phi, ok := s.taskFeatures(t); ok {
					rem = math.Max(0, math.Exp(m.base(phi)+h)-time.Since(a.Started).Seconds())
				}
			}
			remaining = append(remaining, rem)
		}
		if len(remaining) >= max(ss.offer.Slots, 1) {
			sort.Float64s(remaining)
			f.freeIn = remaining[len(remaining)-max(ss.offer.Slots, 1)] // first slot to free up
		}
		out = append(out, f)
	}
	return out
}

// cmdModel describes the fitted model for the admin.
func (s *Server) cmdModel() (string, error) {
	m := s.refreshModel(true)
	if m == nil {
		return "no accepted results with readable inputs yet", nil
	}
	var b strings.Builder
	mode := s.conf().Scheduler
	fmt.Fprintf(&b, "scheduler: %s (learned needs >= %d samples)\n", mode, s.conf().LearnedMinSamples)
	fmt.Fprintf(&b, "samples: %d   fit: rms log error %.2f, median relative error %.0f %%\n", m.N, m.RMSLog, 100*m.MedRel)
	if cv, n := crossValidate(s.collectSamples(), 5); n > 0 {
		fmt.Fprintf(&b, "5-fold cross-validation (honest error on unseen jobs): median relative error %.0f %% (%d predictions)\n", 100*cv, n)
	}
	b.WriteString("note: effects of features that always occur together (e.g. Opt and Freq) are not separable\n")
	fmt.Fprintf(&b, "\nclient speed (1.00 = fastest; time factor relative to it)\n")
	type hv struct {
		name string
		v    float64
	}
	var hs []hv
	for id, v := range m.Host {
		name := id
		if c := s.reg.get(id); c != nil {
			name = c.Name
		}
		hs = append(hs, hv{name, v})
	}
	sort.Slice(hs, func(i, j int) bool { return hs[i].v < hs[j].v })
	for _, h := range hs {
		fmt.Fprintf(&b, "  %-12s x%.2f\n", h.name, math.Exp(h.v-hs[0].v))
	}
	fmt.Fprintf(&b, "\nfeature effects (time factor when the feature is present / per e-fold)\n")
	for i, n := range featureNames() {
		if i == 0 {
			continue
		}
		fmt.Fprintf(&b, "  %-24s x%.2f\n", n, math.Exp(m.Theta[i]))
	}
	return b.String(), nil
}

// learnedETA estimates how long the queued tasks take with the clients connected now:
// the predicted run time of every task (scaled to the average connected client) over the
// slots of those clients weighted by their speed, and never less than the longest single
// task on the fastest client. ok = false without a usable model or a known client.
func (s *Server) learnedETA() (eta time.Duration, predicted int, ok bool) {
	cfg := s.conf()
	m := s.refreshModel(false)
	if m == nil || m.N < cfg.LearnedMinSamples {
		return 0, 0, false
	}
	type host struct {
		h     float64
		slots int
	}
	var hosts []host
	s.sessMu.Lock()
	for id, ss := range s.sessions {
		if h, known := m.Host[id]; known && ss.offer.Slots > 0 {
			hosts = append(hosts, host{h, ss.offer.Slots})
		}
	}
	s.sessMu.Unlock()
	if len(hosts) == 0 {
		return 0, 0, false
	}
	hbar, hmin := 0.0, math.Inf(1)
	for _, x := range hosts {
		hbar += x.h
		hmin = math.Min(hmin, x.h)
	}
	hbar /= float64(len(hosts))
	capacity := 0.0 // average-client slots
	for _, x := range hosts {
		capacity += float64(x.slots) * math.Exp(hbar-x.h)
	}
	s.store.mu.Lock()
	var ts []*TaskState
	for _, t := range s.store.Tasks {
		if !t.Internal {
			ts = append(ts, t)
		}
	}
	s.store.mu.Unlock()
	var known []float64
	unknown := 0
	for _, t := range ts {
		if phi, ok := s.taskFeatures(t); ok {
			known = append(known, m.base(phi))
		} else {
			unknown++
		}
	}
	if len(known) == 0 {
		return 0, 0, false
	}
	sort.Float64s(known)
	work, longest := 0.0, 0.0
	for _, b := range known {
		work += math.Exp(b + hbar)
		longest = math.Max(longest, math.Exp(b+hmin))
	}
	work += float64(unknown) * math.Exp(known[len(known)/2]+hbar) // unreadable inputs: median
	sec := math.Max(work/capacity, longest)
	return time.Duration(sec * float64(time.Second)).Round(time.Minute), len(known), true
}
