package main

import (
	"strings"
	"testing"

	"github.com/agaloya/matriline/common/i18n"

	"github.com/agaloya/matriline/common/i18n/i18ntest"
)

// TestConfTranslations: the translated server.conf templates match the English one option for
// option (common/i18n/lang/server.conf.<code>).
func TestConfTranslations(t *testing.T) { i18ntest.CheckTemplates(t, "server.conf", defaultConfig) }

// TestConfTranslationsSettingsPage: the settings page reads a translation like the English
// file: the same options, the same [restart] marks, help and "more" texts where English
// has them (a separator "# ---" line wrapped onto two lines made "ninguno" the help of
// alerts.command in the first Spanish file).
func TestConfTranslationsSettingsPage(t *testing.T) {
	en := parseSettings(defaultConfig)
	for code, text := range i18n.Translations("server.conf") {
		_, body, _ := strings.Cut(text, "\n")
		tr := parseSettings(body)
		if len(tr) != len(en) {
			t.Fatalf("server.conf.%s: %d sections on the settings page, English %d", code, len(tr), len(en))
		}
		for gi := range en {
			if len(tr[gi].Items) != len(en[gi].Items) {
				t.Fatalf("server.conf.%s [%s]: %d settings, English %d", code, en[gi].Section, len(tr[gi].Items), len(en[gi].Items))
			}
			for si, a := range en[gi].Items {
				b := tr[gi].Items[si]
				if a.Key != b.Key || a.Restart != b.Restart || (a.Help == "") != (b.Help == "") || (a.More == "") != (b.More == "") {
					t.Errorf("server.conf.%s %s.%s on the settings page: restart %v (English %v), help %q, more %v (English: help %v, more %v)",
						code, a.Section, a.Key, b.Restart, a.Restart, b.Help, b.More != "", a.Help != "", a.More != "")
				}
			}
		}
	}
}
