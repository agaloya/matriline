//go:build !windows && !darwin

package i18n

import (
	"os"
	"regexp"
	"strings"
)

// keyboardFiles are where Linux distributions record the keyboard layouts (Debian and
// Ubuntu; systemd's localectl writes the other two).
var keyboardFiles = []string{"/etc/default/keyboard", "/etc/vconsole.conf", "/etc/X11/xorg.conf.d/00-keyboard.conf"}

var reLayout = regexp.MustCompile(`(?m)^\s*(?:XKBLAYOUT|KEYMAP|XKB_LAYOUT)\s*=\s*"?([^"\n]*)"?|Option\s+"XkbLayout"\s+"([^"]*)"`)

// keyboardLanguages lists the languages of the configured keyboard layouts.
func keyboardLanguages() []string {
	var out []string
	for _, f := range keyboardFiles {
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		out = append(out, layoutLanguages(string(b))...)
	}
	return out
}

// layoutLanguages reads the layouts of one such file ("us,latam"; a console keymap such
// as "la-latin1" or "br-abnt2").
func layoutLanguages(text string) []string {
	var out []string
	for _, m := range reLayout.FindAllStringSubmatch(text, -1) {
		for _, l := range strings.Split(m[1]+m[2], ",") {
			c := keyboardLanguage(l)
			if c == "" {
				c = keyboardLanguage(strings.SplitN(l, "-", 2)[0]) // la-latin1 -> la
			}
			if c != "" {
				out = append(out, c)
			}
		}
	}
	return out
}
