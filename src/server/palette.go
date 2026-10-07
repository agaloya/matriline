package main

import (
	"fmt"
	"sort"
	"strings"
)

// The web page's colour palettes ([web] in server.conf; user, 2026-10-06): five colours,
// everything else is derived from them.

var paletteKeys = []string{"white", "black", "gray", "dark", "light"}

// In a dark palette "white" is still the background and "black" the text (a dark
// background, light text).
var palettes = map[string]map[string]string{
	"A":      {"white": "f0f7f4", "black": "32292f", "gray": "795d56", "dark": "4d868a", "light": "b9eae5"},
	"B":      {"white": "d1dede", "black": "1d201f", "gray": "ead2ac", "dark": "ae5a51", "light": "eab8b6"},
	"A-DARK": {"white": "1f1a1e", "black": "f0f7f4", "gray": "a78b84", "dark": "4d868a", "light": "b9eae5"},
	"B-DARK": {"white": "1d201f", "black": "d1dede", "gray": "b9a27f", "dark": "ae5a51", "light": "eab8b6"},
}

// darkPalette: the background is dark (the 3D view's background is then black).
func darkPalette(c map[string]string) bool { return lum(c["white"]) < 128 }

func lum(hex string) float64 {
	var r, g, b int
	fmt.Sscanf(hex, "%02x%02x%02x", &r, &g, &b)
	return 0.299*float64(r) + 0.587*float64(g) + 0.114*float64(b)
}

func paletteNames() []string {
	var n []string
	for k := range palettes {
		n = append(n, k)
	}
	sort.Strings(n)
	return n
}

func hexColor(v string) bool {
	if len(v) != 6 {
		return false
	}
	return strings.Trim(strings.ToLower(v), "0123456789abcdef") == ""
}

// webColors are the five colours for the web page: the palette, then the replacements.
func webColors(cfg *Config) map[string]string {
	out := map[string]string{}
	p, ok := palettes[cfg.WebPalette]
	if !ok {
		p = palettes["A"]
	}
	for k, v := range p {
		out[k] = v
	}
	for k, v := range cfg.WebColors {
		if hexColor(v) {
			out[k] = strings.ToLower(v)
		}
	}
	return out
}

// on is the text colour readable on a background: the palette's white or black, by the
// background's brightness (text on black is white, as the user asked).
func on(bg string, c map[string]string) string {
	light, dark := c["white"], c["black"]
	if darkPalette(c) {
		light, dark = c["black"], c["white"]
	}
	if lum(bg) < 140 {
		return light
	}
	return dark
}

// paletteCSS defines the page's colour variables from the five colours.
func paletteCSS(c map[string]string) string {
	head := c["black"] // the header: the text colour in a light palette, darker than the background in a dark one
	if darkPalette(c) {
		head = "0e0c0d"
	}
	card := "color-mix(in srgb,var(--white) 55%,#fff)" // boxes: a little lighter than the background
	if darkPalette(c) {
		card = "color-mix(in srgb,var(--white) 88%,#fff)"
	}
	return fmt.Sprintf(`:root{--white:#%s;--black:#%s;--gray:#%s;--dark:#%s;--light:#%s;--head:#%s;--on-head:#%s;
--on-black:#%s;--on-dark:#%s;--on-light:#%s;
--bg:var(--white);--fg:var(--black);--mut:color-mix(in srgb,var(--black) 72%%,var(--gray));
--card:%s;--line:color-mix(in srgb,var(--gray) 40%%,var(--white));--acc:var(--dark)}
`, c["white"], c["black"], c["gray"], c["dark"], c["light"], head, on(head, c), on(c["black"], c), on(c["dark"], c), on(c["light"], c), card)
}
