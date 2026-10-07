// Package i18n translates the texts people read (help, console menus, web pages, alerts)
// from catalogs embedded in the programs: lang/<code>.txt, made from lang/strings.txt.
//
// A catalog is a list of entries separated by blank lines; '#' starts a comment line:
//
//	en: pause the inputs of %s
//	es: pausa las entradas de %s
//
// A text of several lines continues on lines that start with "  |" (two spaces and a bar;
// what follows the bar is kept as it is, spaces included). The key is the English text
// itself. A text without a translation is shown in English.
package i18n

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
	"sync"
)

//go:embed lang
var langFS embed.FS

var (
	mu   sync.RWMutex
	cur  map[string]string
	code = "en"
)

// Entry is one text and its translation.
type Entry struct{ En, Tr string }

// Parse reads a catalog; lang is the code of the translation lines ("es").
func Parse(text, lang string) ([]Entry, error) {
	var out []Entry
	var e *Entry
	var field *string
	for n, line := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		switch {
		case strings.HasPrefix(line, "#"):
		case strings.TrimSpace(line) == "":
			e, field = nil, nil
		case strings.HasPrefix(line, "  |"):
			if field == nil {
				return nil, fmt.Errorf("line %d: a continuation line (  |) without en: or %s: before it", n+1, lang)
			}
			*field += "\n" + line[3:]
		case strings.HasPrefix(line, "en:"):
			out = append(out, Entry{En: strings.TrimPrefix(strings.TrimPrefix(line, "en:"), " ")})
			e = &out[len(out)-1]
			field = &e.En
		case strings.HasPrefix(line, lang+":"):
			if e == nil {
				return nil, fmt.Errorf("line %d: %s: without en: before it", n+1, lang)
			}
			e.Tr = strings.TrimPrefix(strings.TrimPrefix(line, lang+":"), " ")
			field = &e.Tr
		default:
			return nil, fmt.Errorf("line %d: expected en:, %s:, a continuation (  |), a comment (#) or a blank line", n+1, lang)
		}
	}
	return out, nil
}

// Available lists the languages with a catalog, English first.
func Available() []string {
	out := []string{"en"}
	es, _ := langFS.ReadDir("lang")
	for _, e := range es {
		if c, ok := strings.CutSuffix(e.Name(), ".txt"); ok && c != "strings" {
			out = append(out, c)
		}
	}
	return out
}

// Set chooses the language ("en", "es", or a locale such as "es_MX.UTF-8"). An unknown one
// is an error and leaves English.
func Set(lang string) error {
	c := Normalize(lang)
	mu.Lock()
	defer mu.Unlock()
	cur, code = nil, "en"
	if c == "en" {
		return nil
	}
	b, err := langFS.ReadFile("lang/" + c + ".txt")
	if err != nil {
		return fmt.Errorf("language %q: no translation (available: %s)", lang, strings.Join(Available(), ", "))
	}
	es, err := Parse(string(b), c)
	if err != nil {
		return fmt.Errorf("lang/%s.txt: %v", c, err)
	}
	m := make(map[string]string, len(es))
	for _, e := range es {
		if e.Tr != "" {
			m[e.En] = e.Tr
		}
	}
	cur, code = m, c
	return nil
}

// Normalize turns a locale ("es_MX.UTF-8", "es-MX") into a language code ("es"); empty,
// "C" and "POSIX" are English.
func Normalize(lang string) string {
	l := strings.ToLower(strings.TrimSpace(lang))
	if i := strings.IndexAny(l, "_-.@"); i >= 0 {
		l = l[:i]
	}
	if l == "" || l == "c" || l == "posix" {
		return "en"
	}
	return l
}

// FromEnv is the language of the environment (LC_ALL, LC_MESSAGES, LANG), or "" if none.
func FromEnv() string {
	for _, v := range []string{"LC_ALL", "LC_MESSAGES", "LANG"} {
		if s := os.Getenv(v); s != "" {
			return Normalize(s)
		}
	}
	return ""
}

// Template is a configuration template (name "server.conf" or "client.conf") in the
// language in use: lang/<name>.<code>, a translation of the whole file, else the English
// one given. Its first line records the English template it translates ("# source <sum>").
func Template(name, english string) string {
	c := Lang()
	if c == "en" {
		return english
	}
	b, err := langFS.ReadFile("lang/" + name + "." + c)
	if err != nil {
		return english
	}
	_, rest, _ := strings.Cut(string(b), "\n")
	return rest
}

// TemplateSource is the "# source <sum>" a translated template must start with: the
// SHA-256 (first 16 hex digits) of the English template it translates.
func TemplateSource(english string) string {
	s := sha256.Sum256([]byte(english))
	return "# source " + hex.EncodeToString(s[:8])
}

// Translations lists the translated templates of a name: language code -> text (with its
// source line).
func Translations(name string) map[string]string {
	out := map[string]string{}
	es, _ := langFS.ReadDir("lang")
	for _, e := range es {
		if c, ok := strings.CutPrefix(e.Name(), name+"."); ok {
			if b, err := langFS.ReadFile("lang/" + e.Name()); err == nil {
				out[c] = string(b)
			}
		}
	}
	return out
}

// Lang is the language in use.
func Lang() string {
	mu.RLock()
	defer mu.RUnlock()
	return code
}

// Dir is the writing direction of the language in use for HTML: "rtl" for Arabic, else "ltr".
func Dir() string {
	if Lang() == "ar" {
		return "rtl"
	}
	return "ltr"
}

// T translates a text.
func T(s string) string {
	mu.RLock()
	defer mu.RUnlock()
	if t, ok := cur[s]; ok {
		return t
	}
	return s
}

// N marks a text for translation where it is not translated at once (it returns it as it
// is): i18nextract lists it, and the code translates it later with T or Tf.
func N(s string) string { return s }

// Tf translates a format and fills it like fmt.Sprintf.
func Tf(format string, a ...any) string { return fmt.Sprintf(T(format), a...) }

// System is the language of this computer that has a translation, "" if none (English):
//  1. LANGUAGE, the GNU list of preferred languages ("es_MX:es:en"): the first translated;
//  2. LC_ALL, LC_MESSAGES or LANG, else the Windows or macOS display language;
//  3. the installed keyboards: English first if there is an English keyboard, then
//     Spanish, French, Portuguese (user: a computer in another language whose owner types
//     Spanish gets Spanish).
func System() string {
	return pick(os.Getenv("LANGUAGE"), FromEnv(), uiLanguage(), keyboardLanguages())
}

// unset: no language given, or C/POSIX (services, minimal systems: nothing about the person).
func unset(l string) bool {
	l = strings.ToLower(strings.TrimSpace(l))
	if i := strings.IndexAny(l, ".@"); i >= 0 {
		l = l[:i]
	}
	return l == "" || l == "c" || l == "posix"
}

// keyboardOrder is the priority among the languages of the installed keyboards (user).
var keyboardOrder = []string{"en", "es", "fr", "pt", "ar"}

func pick(languageList, env, ui string, keyboards []string) string {
	has := map[string]bool{}
	for _, c := range Available() {
		has[c] = true
	}
	for _, l := range strings.Split(languageList, ":") {
		if c := Normalize(l); l != "" && has[c] {
			return c
		}
	}
	for _, l := range []string{env, ui} {
		if c := Normalize(l); !unset(l) && has[c] {
			return c
		}
	}
	kb := map[string]bool{}
	for _, k := range keyboards {
		kb[k] = true
	}
	for _, c := range keyboardOrder {
		if kb[c] && has[c] {
			return c
		}
	}
	return ""
}

// keyboardLanguage maps a keyboard layout name (XKB code such as "us", "latam", "br",
// "ca"; or a macOS layout name such as "Spanish - ISO", "Brazilian", "ABC") to a language
// with a translation ("en", "es", "fr", "pt", "ar"), "" for others.
func keyboardLanguage(layout string) string {
	l := strings.ToLower(strings.TrimSpace(layout))
	if i := strings.IndexAny(l, "(:"); i >= 0 { // "us(intl)", "es:nodeadkeys"
		l = strings.TrimSpace(l[:i])
	}
	switch l {
	case "us", "gb", "uk", "au", "nz", "ie", "za", "en", "abc", "u.s.", "british", "us international - pc", "australian", "irish", "canadian english":
		return "en"
	case "es", "latam", "la", "spanish", "spanish - iso", "latin american":
		return "es"
	case "fr", "be", "ca", "fr-ch", "french", "french - pc", "french - numerical", "belgian", "canadian french - csa", "canadian french - pc", "swiss french":
		return "fr"
	case "br", "pt", "br-abnt2", "brazilian", "brazilian - pc", "brazilian abnt2", "portuguese":
		return "pt"
	case "ara", "ar", "arabic", "arabic - pc", "arabic - qwerty", "arabic - azerty":
		return "ar"
	}
	return ""
}

// Choose sets the configured language, or this computer's when none is configured;
// English when there is no translation for it.
func Choose(configured string) {
	if configured == "" {
		configured = System()
	}
	if Set(configured) != nil {
		Set("en")
	}
}
