package main

import (
	"encoding/base64"
	"fmt"
	"image"
	"os"
	"strings"
)

// Pictures in the terminal, for 'live' and 'watch': the protocols of kitty, iTerm2 (also
// WezTerm) and sixel (Konsole, Windows Terminal 1.22+, foot, mlterm, xterm -ti 340).
// A terminal that shows none of them gets the braille drawing. MATRILINE_IMAGES=off turns
// pictures off, =kitty|iterm|sixel forces one (detection reads the usual variables).

func imageProtocol() string {
	if v := strings.ToLower(os.Getenv("MATRILINE_IMAGES")); v != "" {
		if v == "kitty" || v == "iterm" || v == "sixel" {
			return v
		}
		return ""
	}
	if st, err := os.Stdout.Stat(); err != nil || st.Mode()&os.ModeCharDevice == 0 {
		return "" // not a terminal (a pipe, a file)
	}
	term, prog := os.Getenv("TERM"), os.Getenv("TERM_PROGRAM")
	if os.Getenv("TMUX") != "" || os.Getenv("ZELLIJ") != "" || strings.HasPrefix(term, "screen") || term == "dumb" || term == "" {
		return "" // multiplexers do not pass pictures through by default; dumb terminals
	}
	switch {
	case term == "xterm-kitty": // (not KITTY_WINDOW_ID alone: other programs started from kitty inherit it)
		return "kitty"
	case prog == "iTerm.app" || prog == "WezTerm":
		return "iterm"
	case os.Getenv("WT_SESSION") != "" || os.Getenv("KONSOLE_VERSION") != "" || strings.HasPrefix(term, "foot") || strings.HasPrefix(term, "mlterm"):
		return "sixel"
	}
	return ""
}

// termImage is the escape sequence that shows img in the terminal, "" if it cannot.
func termImage(img image.Image, proto string) string {
	switch proto {
	case "kitty":
		data := base64.StdEncoding.EncodeToString(pngBytes(img))
		var b strings.Builder
		for i := 0; i < len(data); i += 4096 {
			chunk, more := data[i:min(i+4096, len(data))], 0
			if i+4096 < len(data) {
				more = 1
			}
			if i == 0 { // one image id, the previous picture deleted: 'watch' does not pile them up
				fmt.Fprintf(&b, "\x1b_Ga=d,d=I,i=7471,q=2\x1b\\\x1b_Ga=T,f=100,i=7471,q=2,m=%d;%s\x1b\\", more, chunk)
			} else {
				fmt.Fprintf(&b, "\x1b_Gm=%d;%s\x1b\\", more, chunk)
			}
		}
		return b.String() + "\n"
	case "iterm":
		p := pngBytes(img)
		return fmt.Sprintf("\x1b]1337;File=inline=1;size=%d;preserveAspectRatio=1:%s\a\n", len(p), base64.StdEncoding.EncodeToString(p))
	case "sixel":
		return sixel(img)
	}
	return ""
}

// sixel encodes img with a 4 x 4 x 4 colour cube (64 colours: enough for black lines,
// coloured atoms and their anti-aliased edges on white; the white is painted too, so the
// picture shows on dark terminals).
func sixel(img image.Image) string {
	r := img.Bounds()
	idx := func(x, y int) int {
		cr, cg, cb, _ := img.At(x, y).RGBA()
		q := func(v uint32) int { return int(v>>8) * 4 / 256 }
		return q(cr)*16 + q(cg)*4 + q(cb)
	}
	var b strings.Builder
	b.WriteString("\x1bP0;1;0q\"1;1;" + fmt.Sprint(r.Dx()) + ";" + fmt.Sprint(r.Dy()))
	for c := 0; c < 64; c++ {
		lv := func(v int) int { return v * 100 / 3 }
		fmt.Fprintf(&b, "#%d;2;%d;%d;%d", c, lv(c/16), lv(c/4%4), lv(c%4))
	}
	for y0 := r.Min.Y; y0 < r.Max.Y; y0 += 6 {
		used := map[int]bool{}
		for y := y0; y < min(y0+6, r.Max.Y); y++ {
			for x := r.Min.X; x < r.Max.X; x++ {
				used[idx(x, y)] = true
			}
		}
		first := true
		for c := 0; c < 64; c++ {
			if !used[c] {
				continue
			}
			if !first {
				b.WriteByte('$')
			}
			first = false
			fmt.Fprintf(&b, "#%d", c)
			run, last := 0, byte(0)
			flush := func() {
				if run > 3 {
					fmt.Fprintf(&b, "!%d%c", run, last)
				} else {
					b.WriteString(strings.Repeat(string(last), run))
				}
			}
			for x := r.Min.X; x < r.Max.X; x++ {
				var bits byte
				for dy := 0; dy < 6 && y0+dy < r.Max.Y; dy++ {
					if idx(x, y0+dy) == c {
						bits |= 1 << dy
					}
				}
				ch := 63 + bits
				if ch == last {
					run++
					continue
				}
				if run > 0 {
					flush()
				}
				last, run = ch, 1
			}
			flush()
		}
		b.WriteByte('-')
	}
	b.WriteString("\x1b\\\n")
	return b.String()
}

var lastPicture = map[string]string{}

// withPicture replaces the braille drawing of a 'live' answer by a picture when this
// terminal shows pictures (the server only knows text; the geometry comes from molxyz).
func withPicture(cfgPath, out string, sel int) string {
	proto := imageProtocol()
	enableVT() // Windows: escape sequences (sixel) are text otherwise
	i, j := strings.Index(out, "-- molecule --\n"), strings.Index(out, "-- output (latest lines) --")
	if proto == "" || i < 0 || j < i {
		return out
	}
	xyz, err := call(cfgPath, []string{"molxyz", fmt.Sprint(sel)})
	if err != nil {
		return out
	}
	lines := strings.Split(strings.TrimSpace(xyz), "\n")
	if len(lines) < 3 {
		return out
	}
	key := proto + "\n" + strings.Join(lines[2:], "\n")
	pic := lastPicture[key] // 'watch' asks every 2 s for the same job: drawn once
	if pic == "" {
		img := skeletalImage("* xyz 0 1\n"+strings.Join(lines[2:], "\n")+"\n*\n", 420, 300)
		if img == nil {
			return out
		}
		pic = termImage(img, proto)
		lastPicture = map[string]string{key: pic}
	}
	if proto == "sixel" {
		// sixel was guessed from the terminal's name (no version: Windows Terminal before
		// 1.22 ignores it): the braille drawing stays below the picture
		return out[:i] + "-- molecule --\n" + pic + out[i+len("-- molecule --\n"):]
	}
	return out[:i] + "-- molecule --\n" + pic + out[j:]
}
