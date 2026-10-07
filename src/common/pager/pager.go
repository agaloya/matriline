// Package pager shows long text a screen at a time when it goes to a terminal (some
// terminals cannot scroll back, e.g. tmux without its copy mode): the system's pager,
// $PAGER or "less -FRX" (Unix) / "more" (Windows); space or Enter goes on, q quits. Text
// that fits on the screen, or that goes to a file or a pipe, is printed as it is.
package pager

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"

	"github.com/agaloya/matriline/common/i18n"
)

// Print writes text to standard output, paged when it is longer than the terminal.
func Print(text string) {
	if !terminal() || strings.Count(text, "\n") < rows()-1 {
		fmt.Print(text)
		return
	}
	cmd := command()
	if cmd == nil {
		fmt.Print(text)
		return
	}
	cmd.Stdin = strings.NewReader(text) // the pager reads keys from the terminal itself
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		fmt.Print(text)
	}
}

// Interactive reports whether a person types into this program and reads its output (both
// ends are terminals): then it is worth saying how to leave a screen or stop a program.
func Interactive() bool {
	for _, f := range []*os.File{os.Stdin, os.Stdout} {
		st, err := f.Stat()
		if err != nil || st.Mode()&os.ModeCharDevice == 0 {
			return false
		}
	}
	return true
}

func terminal() bool {
	if os.Getenv("NOPAGER") != "" || os.Getenv("TERM") == "dumb" {
		return false
	}
	st, err := os.Stdout.Stat()
	return err == nil && st.Mode()&os.ModeCharDevice != 0
}

// rows is the terminal's height: $LINES, "stty size" (Unix), otherwise 24.
func rows() int {
	if n, err := strconv.Atoi(os.Getenv("LINES")); err == nil && n > 5 {
		return n
	}
	if runtime.GOOS != "windows" {
		cmd := exec.Command("stty", "size")
		cmd.Stdin = os.Stdin
		if out, err := cmd.Output(); err == nil {
			if f := strings.Fields(string(out)); len(f) == 2 {
				if n, err := strconv.Atoi(f[0]); err == nil && n > 5 {
					return n
				}
			}
		}
	}
	return 24
}

func command() *exec.Cmd {
	if p := strings.Fields(os.Getenv("PAGER")); len(p) > 0 {
		if _, err := exec.LookPath(p[0]); err == nil {
			return exec.Command(p[0], p[1:]...)
		}
	}
	if runtime.GOOS == "windows" {
		return exec.Command("more")
	}
	if _, err := exec.LookPath("less"); err == nil {
		// the prompt says how to go on and how to leave (not everybody knows less)
		c := exec.Command("less", "-FRX", "-Ps"+lessEscape(i18n.T("space: more   b: back   q: quit")))
		c.Env = append(os.Environ(), "LESS=") // our options, not the user's (e.g. no -F would wait for q)
		return c
	}
	if _, err := exec.LookPath("more"); err == nil {
		return exec.Command("more")
	}
	return nil
}

// lessEscape quotes the characters less gives a meaning in prompts (less(1), PROMPTS).
func lessEscape(s string) string {
	var b strings.Builder
	for _, r := range s {
		if strings.ContainsRune(`?:.%\\`, r) {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}
