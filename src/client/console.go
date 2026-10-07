package main

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/agaloya/matriline/common/i18n"
)

// cmdConsole: the client's commands from a numbered menu, for people who do not use a
// terminal often (the server has the same, 'matriline-server console').
func cmdConsole(cfgPath string) error {
	type item struct {
		label string
		run   func(in *bufio.Reader) error
	}
	ask := func(in *bufio.Reader, q string) string {
		fmt.Print(q)
		l, _ := in.ReadString('\n')
		return strings.TrimSpace(l)
	}
	menu := []item{
		{i18n.T("status: connection, pause, local jobs"), func(*bufio.Reader) error { return cmdStatus(cfgPath) }},
		{i18n.T("pause: stop lending this computer (for a while or until resume)"), func(in *bufio.Reader) error {
			var args []string
			if d := ask(in, "  "+i18n.T("for how long (e.g. 2h, 45m; empty = until resume): ")); d != "" {
				args = append(args, d)
			}
			for {
				v := strings.ToLower(ask(in, "  "+i18n.T("stop the running jobs too? [y/N]: ")))
				if v == "y" || v == "yes" || v == "s" || v == "si" || v == "sí" || v == "sim" || v == "o" || v == "oui" {
					args = append(args, "--now")
				} else if v != "" && v != "n" && v != "no" && v != "não" && v != "nao" && v != "non" {
					continue // e.g. "q" typed by mistake: ask again
				}
				break
			}
			return cmdPause(cfgPath, args)
		}},
		{i18n.T("resume: lend it again"), func(*bufio.Reader) error { return cmdResume(cfgPath) }},
		{i18n.T("doctor: check ORCA, the sandbox, the credential and the server"), func(*bufio.Reader) error { return cmdDoctor(cfgPath) }},
		{i18n.T("service install: start by itself at log-in / start-up"), func(*bufio.Reader) error { return cmdService(cfgPath, []string{"install"}) }},
		{i18n.T("service remove: stop starting by itself"), func(*bufio.Reader) error { return cmdService(cfgPath, []string{"remove"}) }},
		{i18n.T("enable: start again after the admin disabled and re-enabled this client"), func(*bufio.Reader) error { return cmdEnable(cfgPath) }},
		{"version", func(*bufio.Reader) error {
			fmt.Printf("%s (commit %s)\n", agentVersion, commit)
			return nil
		}},
	}
	in := bufio.NewReader(os.Stdin)
	for {
		fmt.Println("\n" + i18n.Tf("Matriline client (%s)", cfgPath))
		for i, m := range menu {
			fmt.Printf("  %d  %s\n", i+1, m.label)
		}
		fmt.Println("  q  " + i18n.T("quit (q and Enter; or Ctrl+C)"))
		c := ask(in, "> ")
		if c == "q" || c == "quit" || c == "" && in.Buffered() == 0 && isEOF(in) {
			return nil
		}
		n, err := strconv.Atoi(c)
		if err != nil || n < 1 || n > len(menu) {
			fmt.Println(i18n.Tf("choose 1-%d or q", len(menu)))
			continue
		}
		if err := menu[n-1].run(in); err != nil {
			fmt.Println(i18n.T("error:"), err)
		}
	}
}

func isEOF(in *bufio.Reader) bool {
	_, err := in.Peek(1)
	return err != nil
}
