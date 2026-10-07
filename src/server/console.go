package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"unicode"

	"github.com/agaloya/matriline/common/i18n"
	"github.com/agaloya/matriline/common/pager"
)

// cmdConsole is the interactive terminal: every command of the catalogue from a numbered
// menu (it asks for each argument with its explanation and confirms the risky ones), or
// typed directly as on the command line. It runs the same runCommand as the command line.
func cmdConsole(cfgPath string) error {
	in := bufio.NewReader(os.Stdin)
	fmt.Println(i18n.Tf("Matriline server console, %s", versionText()))
	if run := captureRemote(cfgPath, []string{"version"}); run == "" {
		fmt.Println(i18n.T("The server is not running: only local commands will work (start it with the system service)."))
	} else if strings.TrimSpace(run) != versionText() {
		fmt.Println(i18n.Tf("WARNING: the running server is %s, this console %s; restart the server to use this build.", strings.TrimSpace(run), versionText()))
	}
	fmt.Println(i18n.T(`Type a number from the menu, a command as on the command line (e.g. "status input/1list"),
"menu", "help <command>" or "quit".`))
	fmt.Println(i18n.T("To leave: type q and press Enter (or Ctrl+C)."))
	menu := consoleMenu()
	printMenu(os.Stdout, menu)
	for {
		line, ok := prompt(in, "matriline> ")
		if !ok {
			fmt.Println()
			return nil
		}
		args, err := splitLine(line)
		if err != nil {
			fmt.Println("error:", err)
			continue
		}
		if len(args) == 0 {
			continue
		}
		switch args[0] {
		case "quit", "exit", "q":
			return nil
		case "menu", "m":
			var b strings.Builder
			printMenu(&b, menu)
			pager.Print(b.String())
			continue
		case "help", "?", "h":
			if len(args) == 1 {
				pager.Print(usageText())
			} else if c, ok := lookup(args[1:]); ok {
				var b strings.Builder
				printHelp(&b, c)
				pager.Print(b.String())
			} else {
				fmt.Println(i18n.Tf("no command %q", strings.Join(args[1:], " ")))
			}
			continue
		}
		var c cmdSpec
		if n, err := strconv.Atoi(args[0]); err == nil {
			if n < 1 || n > len(menu) {
				fmt.Println(i18n.Tf("choose 1-%d", len(menu)))
				continue
			}
			c = menu[n-1]
			printHelp(os.Stdout, c)
			vals := make([]string, len(c.Args))
			cancelled := false
			for i, a := range c.Args {
				q := fmt.Sprintf("  %s (%s): ", a.Name, i18n.T(a.Help))
				if a.Flag != "" {
					q = "  " + i18n.Tf("%s? %s [y/N]: ", a.Flag, i18n.T(a.Help))
				} else if a.Optional {
					q = "  " + i18n.Tf("%s, optional (%s): ", a.Name, i18n.T(a.Help))
				}
				v, ok := prompt(in, q)
				if !ok {
					cancelled = true
					break
				}
				if a.Flag != "" {
					v = map[bool]string{true: "yes", false: ""}[yes(v)]
				}
				vals[i] = v
			}
			if cancelled {
				fmt.Println()
				continue
			}
			if args, err = c.commandLine(vals); err != nil {
				fmt.Println("error:", err)
				continue
			}
			fmt.Println("  "+i18n.T("command:")+" matriline-server", quoteLine(args))
		} else {
			var found bool
			if c, found = lookup(args); !found {
				if !isCommandWord(args[0]) {
					fmt.Println(i18n.Tf("unknown command %q: type \"menu\" or \"help\"", args[0]))
					continue
				}
				c = cmdSpec{Words: args[0]} // e.g. bare "clients": the server knows it
			}
		}
		if c.Words == "console" || c.Words == "run" || c.Words == "web" || c.Words == "watch" {
			fmt.Println(i18n.Tf("'%s' does not run inside the console; use it from a shell.", c.Words))
			continue
		}
		if c.Confirm {
			if v, ok := prompt(in, "  "+i18n.T("run it? [y/N]: ")); !ok || !yes(v) {
				fmt.Println("  " + i18n.T("not run"))
				continue
			}
		}
		if err := runCommand(cfgPath, args); err != nil {
			fmt.Println("error:", err)
		}
	}
}

// isCommandWord: the first word of some catalogue command.
func isCommandWord(w string) bool {
	for _, c := range catalogue {
		if strings.Fields(c.Words)[0] == w {
			return true
		}
	}
	return false
}

// consoleMenu lists the commands in menu order.
func consoleMenu() []cmdSpec {
	var m []cmdSpec
	for _, g := range groups {
		for _, c := range catalogue {
			if c.Group == g {
				m = append(m, c)
			}
		}
	}
	return m
}

func printMenu(w io.Writer, menu []cmdSpec) {
	g := ""
	for i, c := range menu {
		if c.Group != g {
			g = c.Group
			fmt.Fprintf(w, "%s\n", i18n.T(g))
		}
		fmt.Fprintf(w, "  %2d  %-20s %s\n", i+1, c.Words, i18n.T(c.Help))
	}
}

// prompt reads one line; false at end of input (Ctrl-D).
func prompt(in *bufio.Reader, q string) (string, bool) {
	fmt.Print(q)
	line, err := in.ReadString('\n')
	if err != nil && line == "" {
		return "", false
	}
	return strings.TrimSpace(line), true
}

// splitLine splits a command line into words like a shell: blanks separate words, '...'
// and "..." keep blanks, a backslash escapes the next character (outside '...').
func splitLine(s string) ([]string, error) {
	var words []string
	var w strings.Builder
	inWord := false
	var quote rune
	esc := false
	for _, r := range s {
		switch {
		case esc:
			w.WriteRune(r)
			esc = false
		case quote != 0:
			if r == quote {
				quote = 0
			} else if r == '\\' && quote == '"' {
				esc = true
			} else {
				w.WriteRune(r)
			}
		case r == '\\':
			esc, inWord = true, true
		case r == '"' || r == '\'':
			quote, inWord = r, true
		case unicode.IsSpace(r):
			if inWord {
				words = append(words, w.String())
				w.Reset()
				inWord = false
			}
		default:
			w.WriteRune(r)
			inWord = true
		}
	}
	if quote != 0 {
		return nil, fmt.Errorf("unclosed quote")
	}
	if inWord {
		words = append(words, w.String())
	}
	return words, nil
}

// shellUnsafe: anything but [A-Za-z0-9_./=:,+@%-] is quoted, so a shown command line
// can also be pasted into a shell (a file name "a;id" must not run id).
func shellUnsafe(r rune) bool {
	return !(r < 128 && (unicode.IsLetter(r) || unicode.IsDigit(r) || strings.ContainsRune("_./=:,+@%-", r)))
}

// quoteLine shows a command line so that splitLine reads it back the same.
func quoteLine(args []string) string {
	q := make([]string, len(args))
	for i, a := range args {
		if a == "" || strings.IndexFunc(a, shellUnsafe) >= 0 {
			a = "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
		}
		q[i] = a
	}
	return strings.Join(q, " ")
}

// yes: an answer that starts with y (yes), s (sí, sim) or o (oui).
func yes(v string) bool {
	v = strings.ToLower(strings.TrimSpace(v))
	return strings.HasPrefix(v, "y") || strings.HasPrefix(v, "s") || strings.HasPrefix(v, "o")
}
