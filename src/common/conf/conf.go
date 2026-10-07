// Package conf implements the small, dependency-free configuration format used by every
// Matriline program. The format is deliberately close to INI/TOML so that each option can
// carry long explanatory comments and warnings:
//
//	# comment
//	[section]
//	key = value          # trailing comments are allowed
//	name = "quoted value with # inside"
//	list = a, b, c
//
// Keys are addressed as "section.key". Values are typed on access (String, Int, Bool,
// Duration, Size, List). Unknown or unused keys can be reported with Unused().
package conf

import (
	"bufio"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// Conf holds parsed key/value pairs.
type Conf struct {
	vals map[string]string
	line map[string]int
	used map[string]bool
	Path string
}

// New returns an empty configuration (all accessors return their defaults).
func New() *Conf {
	return &Conf{vals: map[string]string{}, line: map[string]int{}, used: map[string]bool{}}
}

// Load parses a configuration file.
func Load(path string) (*Conf, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	c := New()
	c.Path = path
	sc := bufio.NewScanner(f)
	section := ""
	n := 0
	for sc.Scan() {
		n++
		raw := strings.TrimSpace(sc.Text())
		if raw == "" || raw[0] == '#' || raw[0] == ';' {
			continue
		}
		if raw[0] == '[' {
			end := strings.IndexByte(raw, ']')
			if end < 0 {
				return nil, fmt.Errorf("%s:%d: unterminated section header", path, n)
			}
			section = strings.TrimSpace(raw[1:end])
			continue
		}
		eq := strings.IndexByte(raw, '=')
		if eq < 0 {
			return nil, fmt.Errorf("%s:%d: expected key = value", path, n)
		}
		key := strings.TrimSpace(raw[:eq])
		val, err := parseValue(strings.TrimSpace(raw[eq+1:]))
		if err != nil {
			return nil, fmt.Errorf("%s:%d: %v", path, n, err)
		}
		if section != "" {
			key = section + "." + key
		}
		if _, dup := c.vals[key]; dup {
			return nil, fmt.Errorf("%s:%d: duplicate key %q (first at line %d)", path, n, key, c.line[key])
		}
		c.vals[key] = val
		c.line[key] = n
	}
	return c, sc.Err()
}

// parseValue strips trailing comments and optional quotes: key = value, key = "value" and
// key = 'value' all mean the same (user: in other languages it was not clear what to
// write). Quoted text is taken literally, so Windows paths keep their backslashes.
func parseValue(s string) (string, error) {
	if s != "" && (s[0] == '"' || s[0] == '\'') {
		end := strings.LastIndexByte(s, s[0])
		if end == 0 {
			return "", fmt.Errorf("unterminated quoted value")
		}
		rest := strings.TrimSpace(s[end+1:])
		if rest != "" && rest[0] != '#' {
			return "", fmt.Errorf("unexpected text after quoted value")
		}
		return s[1:end], nil
	}
	if strings.HasPrefix(s, "#") {
		return "", nil
	}
	if i := strings.Index(s, " #"); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s), nil
}

// Set overrides a value (used by hot-reload and tests).
func (c *Conf) Set(key, val string) { c.vals[key] = val }

// Has reports whether the key is present.
func (c *Conf) Has(key string) bool { _, ok := c.vals[key]; return ok }

func (c *Conf) get(key string) (string, bool) {
	c.used[key] = true
	v, ok := c.vals[key]
	return v, ok
}

// String returns the value or def.
func (c *Conf) String(key, def string) string {
	if v, ok := c.get(key); ok {
		return v
	}
	return def
}

// Int returns an integer value or def. Malformed values are reported through Errs.
func (c *Conf) Int(key string, def int, errs *[]error) int {
	v, ok := c.get(key)
	if !ok || v == "" {
		return def
	}
	i, err := strconv.Atoi(v)
	if err != nil {
		*errs = append(*errs, c.err(key, "not an integer: %q", v))
		return def
	}
	return i
}

// Float returns a float value or def.
func (c *Conf) Float(key string, def float64, errs *[]error) float64 {
	v, ok := c.get(key)
	if !ok || v == "" {
		return def
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		*errs = append(*errs, c.err(key, "not a number: %q", v))
		return def
	}
	return f
}

// Bool accepts true/false/yes/no/on/off/1/0.
func (c *Conf) Bool(key string, def bool, errs *[]error) bool {
	v, ok := c.get(key)
	if !ok || v == "" {
		return def
	}
	switch strings.ToLower(v) {
	case "true", "yes", "on", "1":
		return true
	case "false", "no", "off", "0":
		return false
	}
	*errs = append(*errs, c.err(key, "not a boolean: %q", v))
	return def
}

// Duration accepts Go durations ("90s", "1h30m") and plain seconds.
func (c *Conf) Duration(key string, def time.Duration, errs *[]error) time.Duration {
	v, ok := c.get(key)
	if !ok || v == "" {
		return def
	}
	if n, err := strconv.Atoi(v); err == nil {
		return time.Duration(n) * time.Second
	}
	if n, err := strconv.Atoi(strings.TrimSuffix(v, "d")); err == nil && strings.HasSuffix(v, "d") {
		return time.Duration(n) * 24 * time.Hour // days: "1d", "3d"
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		*errs = append(*errs, c.err(key, "not a duration: %q", v))
		return def
	}
	return d
}

// Size parses byte sizes such as "512MB", "1.5GiB", "10G", "0" or "unlimited" (returns -1).
func (c *Conf) Size(key string, def int64, errs *[]error) int64 {
	v, ok := c.get(key)
	if !ok || v == "" {
		return def
	}
	n, err := ParseSize(v)
	if err != nil {
		*errs = append(*errs, c.err(key, "%v", err))
		return def
	}
	return n
}

// ParseSize converts a human size to bytes; "unlimited"/"none" gives -1.
func ParseSize(v string) (int64, error) {
	s := strings.ToLower(strings.TrimSpace(v))
	if s == "unlimited" || s == "none" || s == "-1" {
		return -1, nil
	}
	mult := 1.0
	units := []struct {
		suf string
		m   float64
	}{{"kib", 1 << 10}, {"mib", 1 << 20}, {"gib", 1 << 30}, {"tib", 1 << 40},
		{"kb", 1e3}, {"mb", 1e6}, {"gb", 1e9}, {"tb", 1e12},
		{"k", 1 << 10}, {"m", 1 << 20}, {"g", 1 << 30}, {"t", 1 << 40}, {"b", 1}}
	for _, u := range units {
		if strings.HasSuffix(s, u.suf) {
			mult = u.m
			s = strings.TrimSpace(strings.TrimSuffix(s, u.suf))
			break
		}
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || f < 0 {
		return 0, fmt.Errorf("not a size: %q", v)
	}
	return int64(f * mult), nil
}

// List splits a comma separated value; empty value gives def.
func (c *Conf) List(key string, def []string) []string {
	v, ok := c.get(key)
	if !ok {
		return def
	}
	if strings.TrimSpace(v) == "" {
		return nil
	}
	var out []string
	for _, p := range strings.Split(v, ",") {
		// "a", "b": parseValue took the outer quotes, the inner ones are left on the items
		if p = strings.Trim(strings.TrimSpace(p), `"'`); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// Unused returns keys present in the file that no accessor asked for (likely typos).
func (c *Conf) Unused() []string {
	var out []string
	for k := range c.vals {
		if !c.used[k] {
			out = append(out, fmt.Sprintf("%s (line %d)", k, c.line[k]))
		}
	}
	sort.Strings(out)
	return out
}

// Keys returns all keys, sorted.
func (c *Conf) Keys() []string {
	out := make([]string, 0, len(c.vals))
	for k := range c.vals {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func (c *Conf) err(key, format string, a ...any) error {
	return fmt.Errorf("%s:%d: %s: %s", c.Path, c.line[key], key, fmt.Sprintf(format, a...))
}

// Quote writes a value so that Load reads it back unchanged: as it is when it can, else
// between ' or " (whichever it does not contain; quoted text is literal, so no escapes).
// A value with " #", surrounding spaces or a leading quote needs them; one holding both
// quote characters and needing quotes cannot be written, nor one with a line break or
// another control character (it would start a new line: another setting or section).
func Quote(v string) (string, error) {
	if strings.ContainsFunc(v, unicode.IsControl) {
		return "", fmt.Errorf("a value with a line break or another control character cannot be written: %q", v)
	}
	needs := strings.Contains(v, "#") || strings.TrimSpace(v) != v || strings.HasPrefix(v, `"`) || strings.HasPrefix(v, "'")
	switch {
	case !needs:
		return v, nil
	case !strings.Contains(v, "'"):
		return "'" + v + "'", nil
	case !strings.Contains(v, `"`):
		return `"` + v + `"`, nil
	}
	return "", fmt.Errorf("a value with both ' and \" and a '#' or spaces at its ends cannot be written: %s", v)
}
