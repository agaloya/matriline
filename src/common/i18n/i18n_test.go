package i18n

import (
	"regexp"
	"slices"
	"strings"
	"testing"
)

func TestParse(t *testing.T) {
	es, err := Parse("# comment\nen: one %s\nes: uno %s\n\nen: two\n  |  lines\nes: dos\n  |  líneas\n\nen: untranslated\nes:\n", "es")
	if err != nil {
		t.Fatal(err)
	}
	want := []Entry{{"one %s", "uno %s"}, {"two\n  lines", "dos\n  líneas"}, {"untranslated", ""}}
	if !slices.Equal(es, want) {
		t.Fatalf("got %q", es)
	}
	for _, bad := range []string{"es: sin en\n", "  |stray\n", "en: x\nfr: y\n"} {
		if _, err := Parse(bad, "es"); err == nil {
			t.Errorf("%q: no error", bad)
		}
	}
}

func TestNormalize(t *testing.T) {
	for in, want := range map[string]string{"es_MX.UTF-8": "es", "es-MX": "es", "EN": "en", "C": "en", "": "en", "POSIX": "en", "de_DE@euro": "de"} {
		if got := Normalize(in); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
	if err := Set("xx"); err == nil || Lang() != "en" || T("Status") != "Status" {
		t.Errorf("unknown language: err %v, lang %s", err, Lang())
	}
}

var verbs = regexp.MustCompile(`%[-+# 0-9.*]*[a-zA-Z%]`)

// TestCatalogs: every catalog parses, has no duplicate, and each translation keeps the
// English text's format verbs (%s %d %q ...) in the same order.
func TestCatalogs(t *testing.T) {
	for _, c := range Available()[1:] {
		b, err := langFS.ReadFile("lang/" + c + ".txt")
		if err != nil {
			t.Fatal(err)
		}
		es, err := Parse(string(b), c)
		if err != nil {
			t.Fatalf("%s.txt: %v", c, err)
		}
		seen := map[string]bool{}
		for _, e := range es {
			if seen[e.En] {
				t.Errorf("%s.txt: %q twice", c, e.En)
			}
			seen[e.En] = true
			if e.Tr == "" {
				continue
			}
			if a, b := strings.Join(verbs.FindAllString(e.En, -1), " "), strings.Join(verbs.FindAllString(e.Tr, -1), " "); a != b {
				t.Errorf("%s.txt: %q has verbs [%s], its translation [%s]", c, e.En, a, b)
			}
		}
		if err := Set(c); err != nil {
			t.Error(err)
		}
		Set("en")
	}
}

// TestPick: LANGUAGE list first, then the system language, then the keyboards with
// English, Spanish, French, Portuguese in that order; "" (English) when nothing matches.
func TestPick(t *testing.T) {
	for _, c := range []struct {
		list, env, ui string
		kb            []string
		want          string
	}{
		{"de:es:en", "de_DE.UTF-8", "", nil, "es"},
		{"", "es_MX.UTF-8", "", []string{"en"}, "es"},
		{"", "de_DE.UTF-8", "", []string{"es", "en"}, "en"},
		{"", "de_DE.UTF-8", "", []string{"pt", "fr"}, "fr"},
		{"", "de_DE.UTF-8", "", []string{"pt"}, "pt"},
		{"", "", "fr-CA", []string{"es"}, "fr"},
		{"", "de_DE.UTF-8", "", nil, ""},
		{"", "C", "", []string{"es"}, "es"},
	} {
		if got := pick(c.list, c.env, c.ui, c.kb); got != c.want {
			t.Errorf("pick(%q, %q, %q, %v) = %q, want %q", c.list, c.env, c.ui, c.kb, got, c.want)
		}
	}
}

func TestKeyboardLanguage(t *testing.T) {
	for in, want := range map[string]string{"us": "en", "us(intl)": "en", "latam": "es", "es:nodeadkeys": "es", "ca": "fr",
		"br": "pt", "Spanish - ISO": "es", "Brazilian": "pt", "ABC": "en", "de": "", "Latin American": "es", "ara": "ar", "Arabic - PC": "ar"} {
		if got := keyboardLanguage(in); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
}

// TestCatalogsComplete: a catalog that lost most of its entries (a careless rewrite once
// emptied all three) fails; a few new texts not yet translated are only listed.
func TestCatalogsComplete(t *testing.T) {
	b, err := langFS.ReadFile("lang/strings.txt")
	if err != nil {
		t.Fatal(err)
	}
	all, err := Parse(string(b), "es")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range Available()[1:] {
		b, _ := langFS.ReadFile("lang/" + c + ".txt")
		es, _ := Parse(string(b), c)
		have := map[string]bool{}
		for _, e := range es {
			if e.Tr != "" {
				have[e.En] = true
			}
		}
		missing := 0
		for _, e := range all {
			if !have[e.En] {
				missing++
			}
		}
		if missing > len(all)/10 {
			t.Errorf("%s.txt: %d of %d texts without a translation", c, missing, len(all))
		} else if missing > 0 {
			t.Logf("%s.txt: %d texts not translated yet (go run ./i18nextract -check %s)", c, missing, c)
		}
	}
}
