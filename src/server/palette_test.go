package main

import (
	"strings"
	"testing"
)

// Every palette produces all its colour variables, in the right places.
func TestPaletteCSS(t *testing.T) {
	for name := range palettes {
		css := paletteCSS(webColors(&Config{WebPalette: name}))
		for _, v := range []string{"--white:#", "--black:#", "--gray:#", "--dark:#", "--light:#", "--head:#", "--on-head:#",
			"--on-black:#", "--on-dark:#", "--on-light:#", "--card:color-mix("} {
			if !strings.Contains(css, v) {
				t.Errorf("%s: %s missing:\n%s", name, v, css)
			}
		}
		if strings.Contains(css, "%!") {
			t.Errorf("%s: format error:\n%s", name, css)
		}
	}
	c := webColors(&Config{WebPalette: "A", WebColors: map[string]string{"dark": "123456"}})
	if c["dark"] != "123456" || c["light"] != "b9eae5" {
		t.Errorf("replacement: %v", c)
	}
}
