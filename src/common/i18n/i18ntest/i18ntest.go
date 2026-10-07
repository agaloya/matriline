// Package i18ntest checks translated configuration templates (for the programs' tests).
package i18ntest

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/agaloya/matriline/common/conf"
	"github.com/agaloya/matriline/common/i18n"
)

var marker = regexp.MustCompile(`(?m)^# >> (\S+)`)
var header = regexp.MustCompile(`(?m)^\[(\w+)\]`)
var listItem = regexp.MustCompile(`(?m)^#   +[a-z0-9_.]+ +- `)
var numbered = regexp.MustCompile(`(?m)^#\s+[0-9]\. `)
var indent = regexp.MustCompile(`^#( +)\S`)
var general = regexp.MustCompile(`(?m)^\[general\]\n(#[^\n]*\n)+language =\n`)

// CheckTemplates: every translation of the template name has the same sections, options,
// default values and explanation markers (# >> key) as the English one, in the same order;
// a translation of an older English template is reported (it is still used).
func CheckTemplates(t *testing.T, name, english string) {
	for code, text := range i18n.Translations(name) {
		first, body, _ := strings.Cut(text, "\n")
		if first != i18n.TemplateSource(english) {
			t.Logf("NOTE %s.%s translates an older %s template (%s, now %s): run 'go run ./i18nextract -check %s' and update it",
				name, code, name, first, i18n.TemplateSource(english), code)
		}
		if err := same(t, english, body); err != nil {
			t.Errorf("%s.%s: %v", name, code, err)
		}
	}
}

func same(t *testing.T, en, tr string) error {
	for _, re := range []*regexp.Regexp{header, marker} {
		a, b := all(re, en), all(re, tr)
		if !slices.Equal(a, b) {
			return fmt.Errorf("%s differ:\n  English:     %v\n  translation: %v", re, a, b)
		}
	}
	// the language option is written in every language, the same in every file: whoever
	// cannot read this file's language can still find it (user)
	if a, b := general.FindString(en), general.FindString(tr); a != b {
		return fmt.Errorf("the [general] language block differs from the English one (it is not translated):\n%s\n---\n%s", a, b)
	}
	// the layout of the explanations: the same lists ("#   tls  - ...") and numbered steps,
	// and no indentation English does not use (a reflow left one-word lines indented by 5
	// in server.conf.es, and another one merged the lists into paragraphs)
	for _, re := range []*regexp.Regexp{listItem, numbered} {
		if a, b := len(re.FindAllString(en, -1)), len(re.FindAllString(tr, -1)); a != b {
			return fmt.Errorf("%d lines like %q in English, %d in the translation (keep lists and numbered steps one item per line)", a, re, b)
		}
	}
	indents := map[int]bool{}
	for _, l := range strings.Split(en, "\n") {
		if m := indent.FindStringSubmatch(l); m != nil {
			indents[len(m[1])] = true
		}
	}
	for i, l := range strings.Split(tr, "\n") {
		if m := indent.FindStringSubmatch(l); m != nil && !indents[len(m[1])] {
			return fmt.Errorf("line %d indented by %d spaces, which English never uses: %q", i+2, len(m[1]), l)
		}
	}
	// words the settings page reads: keep them in English
	for _, w := range []string{"EXPLANATIONS =", "[restart]", "# >> "} {
		if a, b := strings.Count(en, w), strings.Count(tr, w); a != b {
			return fmt.Errorf("%q appears %d times in English, %d in the translation (keep it as it is)", w, a, b)
		}
	}
	dir := t.TempDir()
	load := func(n, text string) (*conf.Conf, error) {
		p := filepath.Join(dir, n)
		if err := os.WriteFile(p, []byte(text), 0o600); err != nil {
			return nil, err
		}
		return conf.Load(p)
	}
	ce, err := load("en", en)
	if err != nil {
		return err
	}
	ct, err := load("tr", tr)
	if err != nil {
		return fmt.Errorf("does not parse: %v", err)
	}
	if a, b := ce.Keys(), ct.Keys(); !slices.Equal(a, b) {
		return fmt.Errorf("options differ:\n  English:     %v\n  translation: %v", a, b)
	}
	for _, k := range ce.Keys() {
		if a, b := ce.String(k, ""), ct.String(k, ""); a != b {
			return fmt.Errorf("%s: default %q, translation %q (values stay as they are)", k, a, b)
		}
	}
	return nil
}

func all(re *regexp.Regexp, s string) []string {
	var out []string
	for _, m := range re.FindAllStringSubmatch(s, -1) {
		out = append(out, m[1])
	}
	return out
}
