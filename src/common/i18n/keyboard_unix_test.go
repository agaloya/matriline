//go:build !windows && !darwin

package i18n

import (
	"slices"
	"testing"
)

func TestLayoutLanguages(t *testing.T) {
	for text, want := range map[string][]string{
		"XKBMODEL=\"pc105\"\nXKBLAYOUT=\"us,latam\"\n": {"en", "es"},
		"KEYMAP=la-latin1\n":                           {"es"},
		"KEYMAP=br-abnt2\n":                            {"pt"},
		"Section \"InputClass\"\n  Option \"XkbLayout\" \"fr,de\"\nEndSection\n": {"fr"},
	} {
		if got := layoutLanguages(text); !slices.Equal(got, want) {
			t.Errorf("%q: %v, want %v", text, got, want)
		}
	}
}
