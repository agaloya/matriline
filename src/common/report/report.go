// Package report builds the per-run metadata report (D17): a light "key=value" text file,
// one entry per line, sorted keys, with sampled series summarized as statistics.
// Values that cannot be measured are written as "NA:<reason>" so missing data is explicit.
package report

import (
	"fmt"
	"math"
	"os"
	"sort"
	"strconv"
	"strings"
)

// Report is an ordered set of fields.
type Report struct {
	vals    map[string]string
	allowed func(string) bool
}

// New creates a report; fields lists the keys (or prefixes ending in '*') that the server
// asked for. nil or ["*"] means everything.
func New(fields []string) *Report {
	r := &Report{vals: map[string]string{}}
	if len(fields) == 0 || (len(fields) == 1 && fields[0] == "*") {
		r.allowed = func(string) bool { return true }
		return r
	}
	r.allowed = func(k string) bool {
		for _, f := range fields {
			if f == k || (strings.HasSuffix(f, "*") && strings.HasPrefix(k, strings.TrimSuffix(f, "*"))) {
				return true
			}
		}
		return false
	}
	return r
}

func clean(s string) string {
	s = strings.ReplaceAll(s, "\n", " ")
	return strings.TrimSpace(strings.ReplaceAll(s, "\r", " "))
}

// Set stores a string value.
func (r *Report) Set(k, v string) {
	if r.allowed(k) {
		r.vals[k] = clean(v)
	}
}

// SetInt stores an integer.
func (r *Report) SetInt(k string, v int64) { r.Set(k, strconv.FormatInt(v, 10)) }

// SetFloat stores a float with the given number of decimals.
func (r *Report) SetFloat(k string, v float64, dec int) {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		r.NA(k, "invalid_value")
		return
	}
	r.Set(k, strconv.FormatFloat(v, 'f', dec, 64))
}

// NA marks a value as unavailable with a short machine-readable reason.
func (r *Report) NA(k, reason string) { r.Set(k, "NA:"+reason) }

// Stats writes <k>.n, .min, .mean, .max and .pXX for the requested percentiles.
func (r *Report) Stats(k string, samples []float64, percentiles []int, dec int, naReason string) {
	if len(samples) == 0 {
		r.NA(k, naReason)
		return
	}
	s := append([]float64(nil), samples...)
	sort.Float64s(s)
	sum := 0.0
	for _, v := range s {
		sum += v
	}
	r.SetInt(k+".n", int64(len(s)))
	r.SetFloat(k+".min", s[0], dec)
	r.SetFloat(k+".mean", sum/float64(len(s)), dec)
	r.SetFloat(k+".max", s[len(s)-1], dec)
	for _, p := range percentiles {
		if p > 0 && p < 100 {
			r.SetFloat(fmt.Sprintf("%s.p%d", k, p), Percentile(s, float64(p)), dec)
		}
	}
}

// Percentile uses linear interpolation between closest ranks on sorted data.
func Percentile(sorted []float64, p float64) float64 {
	if len(sorted) == 1 {
		return sorted[0]
	}
	pos := p / 100 * float64(len(sorted)-1)
	lo := int(math.Floor(pos))
	hi := int(math.Ceil(pos))
	return sorted[lo] + (sorted[hi]-sorted[lo])*(pos-float64(lo))
}

// Bytes renders the report.
func (r *Report) Bytes() []byte {
	keys := make([]string, 0, len(r.vals))
	for k := range r.vals {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteString("# matriline run report v1 (key=value; NA:<reason> = not measurable)\n")
	for _, k := range keys {
		b.WriteString(k)
		b.WriteByte('=')
		b.WriteString(r.vals[k])
		b.WriteByte('\n')
	}
	return []byte(b.String())
}

// Write stores the report file.
func (r *Report) Write(path string) error { return os.WriteFile(path, r.Bytes(), 0o644) }

// Parse reads a report back (used by the server for statistics and checks).
func Parse(b []byte) map[string]string {
	out := map[string]string{}
	for _, ln := range strings.Split(string(b), "\n") {
		if ln == "" || ln[0] == '#' {
			continue
		}
		if i := strings.IndexByte(ln, '='); i > 0 {
			out[ln[:i]] = ln[i+1:]
		}
	}
	return out
}
