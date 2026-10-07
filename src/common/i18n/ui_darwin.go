//go:build darwin

package i18n

import (
	"os/exec"
	"regexp"
	"strings"
)

// uiLanguage is the first of the user's preferred languages ("es-MX" -> "es").
func uiLanguage() string {
	out, err := exec.Command("/usr/bin/defaults", "read", "-g", "AppleLanguages").Output()
	if err != nil {
		return ""
	}
	for _, f := range strings.Fields(string(out)) {
		f = strings.Trim(f, `"(),`)
		if f != "" {
			return Normalize(f)
		}
	}
	return ""
}

var reMacLayout = regexp.MustCompile(`"KeyboardLayout Name"\s*=\s*"?([^";]+)"?;`)

// keyboardLanguages lists the languages of the enabled keyboard layouts.
func keyboardLanguages() []string {
	out, err := exec.Command("/usr/bin/defaults", "read", "com.apple.HIToolbox", "AppleEnabledInputSources").Output()
	if err != nil {
		return nil
	}
	var langs []string
	for _, m := range reMacLayout.FindAllStringSubmatch(string(out), -1) {
		if c := keyboardLanguage(m[1]); c != "" {
			langs = append(langs, c)
		}
	}
	return langs
}
