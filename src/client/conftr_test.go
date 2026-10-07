package main

import (
	"testing"

	"github.com/agaloya/matriline/common/i18n/i18ntest"
)

// TestConfTranslations: the translated client.conf templates match the English one option for
// option (common/i18n/lang/client.conf.<code>).
func TestConfTranslations(t *testing.T) { i18ntest.CheckTemplates(t, "client.conf", defaultConfig) }
