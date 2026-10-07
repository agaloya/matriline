package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/agaloya/matriline/common/i18n"
)

// The command catalogue: one description of every admin command, from which the help text,
// the interactive terminal ('console') and the web interface ('web') are built. The web and
// the console only fill in arguments and run the same command line through runCommand, so
// no interface can do anything the command line cannot (requirements, section 3).

type argSpec struct {
	Name     string   // shown as <name> or [name]
	Help     string   // one line, shown when asking for the value
	Optional bool     // may be left empty (then it is left out)
	Flag     string   // a yes/no option: its value is this flag (e.g. --dry-run) or nothing
	Suggest  []string // spool directories whose paths the web form suggests
	Option   string   // a named option: a value v is passed as "Option v" (e.g. --uses 3)
}

type cmdSpec struct {
	Group   string
	Words   string // "status", "config show", "clients approve"
	Args    []argSpec
	Help    string
	More    string // a second help line
	Local   bool   // runs in the calling process with the terminal (editor, foreground daemon)
	NoWeb   bool   // not offered on the web (needs a terminal, or stops the server under it)
	Confirm bool   // asks before running: hard to undo, or acts on many tasks at once
}

var (
	pathArg = func(help string, in ...string) argSpec { return argSpec{Name: "path", Help: help, Suggest: in} }
	cliArg  = argSpec{Name: "client", Help: "client name or id (see 'clients')"}
)

var groups = []string{"Setup and service", "Tasks", "Results", "Integrity and data", "Configuration", "Clients and keys"}

var catalogue = []cmdSpec{
	{Group: "Setup and service", Words: "init", Args: []argSpec{{Name: "dir", Help: "new working directory (default: here)", Optional: true},
		{Name: "code", Option: "--language", Help: "language of the menus, help, web page and alerts: en, es, fr, pt or ar (default: this computer's)", Optional: true}},
		Help: "create a working directory with a documented server.conf", Local: true, NoWeb: true},
	{Group: "Setup and service", Words: "run", Help: "start the server (normally done by the system service)", Local: true, NoWeb: true},
	{Group: "Setup and service", Words: "stop", Help: "stop the running server", Confirm: true, NoWeb: true},
	{Group: "Setup and service", Words: "service", Args: []argSpec{{Name: "install|remove", Help: "install: start the server by itself at logon; remove: stop doing so"}},
		Help: "run the server as a service (systemd user unit; Windows scheduled task)", Local: true, NoWeb: true},
	{Group: "Setup and service", Words: "restart", Help: "stop and start the server again (applies options marked [restart])", Confirm: true},
	{Group: "Setup and service", Words: "doctor", Help: "check configuration, ORCA reference, disk, port, ledger"},
	{Group: "Setup and service", Words: "version", Help: "program version and the commit it was built from"},
	{Group: "Setup and service", Words: "console", Help: "interactive terminal: every command from menus", Local: true, NoWeb: true},
	{Group: "Setup and service", Words: "web", Args: []argSpec{{Name: "address", Help: "loopback address (default 127.0.0.1:8484)", Optional: true}},
		Help: "web interface on this computer (a front end of these commands)", Local: true, NoWeb: true},

	{Group: "Tasks", Words: "add", Args: []argSpec{{Name: "file|dir", Help: "input file or directory, anywhere on this computer"},
		{Name: "input/sub", Help: "destination sub-directory of input/ (default input/)", Optional: true}},
		Help: "copy inputs from anywhere into input/ (or a sub-directory)"},
	{Group: "Tasks", Words: "status", Args: []argSpec{{Name: "path", Help: "a spool path for details (empty: overview)", Optional: true, Suggest: spoolDirs}},
		Help: "overview, connected clients; details for a path"},
	{Group: "Tasks", Words: "priority", Args: []argSpec{pathArg("input file or directory", dInput, dPaused), {Name: "n", Help: "priority, an integer (higher first)"}},
		Help: "set priority (higher first) of a file or directory"},
	{Group: "Tasks", Words: "next", Args: []argSpec{pathArg("input file or directory", dInput, dPaused)}, Help: "serve this file/directory before everything else"},
	{Group: "Tasks", Words: "pause", Args: []argSpec{{Name: "path|all", Help: "input file, directory, or all", Suggest: []string{dInput}}},
		Help: "move inputs from input/ to paused/ (running ones stop)", Confirm: true},
	{Group: "Tasks", Words: "resume", Args: []argSpec{{Name: "path|all", Help: "paused file, directory, or all", Suggest: []string{dPaused}}}, Help: "move inputs back from paused/ to input/"},
	{Group: "Tasks", Words: "cancel", Args: []argSpec{pathArg("input or paused file or directory", dInput, dPaused)}, Help: "move inputs to cancelled/", Confirm: true},
	{Group: "Tasks", Words: "uncancel", Args: []argSpec{pathArg("cancelled file or directory", dCancelled)}, Help: "move inputs from cancelled/ back to input/"},
	{Group: "Tasks", Words: "edit", Args: []argSpec{pathArg("input in input/, paused/ or cancelled/")},
		Help: "edit an input with $EDITOR (default nano); recorded in ledger", Local: true, NoWeb: true},
	{Group: "Tasks", Words: "history", Args: []argSpec{pathArg("task path", dInput, dCompleted, dOutput, dWeird, dErrors)}, Help: "ledger history of a task"},
	{Group: "Tasks", Words: "live", Args: []argSpec{{Name: "n", Help: "job number to show in detail (default 1)", Optional: true}, {Name: "full", Help: "the whole input", Flag: "full", Optional: true}},
		Help: "what runs now; one job's input, molecule and latest output", More: "('watch' repeats it every 2 s; the web has a live page)"},
	{Group: "Tasks", Words: "watch", Help: "live view in the terminal, refreshed every 2 s", Local: true, NoWeb: true},
	{Group: "Tasks", Words: "stats", Help: "throughput, ETA and per-client results"},
	{Group: "Tasks", Words: "model", Help: "learned scheduler: client speeds and job-time predictors"},
	{Group: "Tasks", Words: "rescan", Help: "rescan input/ now"},

	{Group: "Results", Words: "review", Help: "list results in weird/ with the reasons"},
	{Group: "Results", Words: "accept", Args: []argSpec{{Name: "weird/path", Help: "result in weird/", Suggest: []string{dWeird}}}, Help: "accept a weird result (moves it to output/)", Confirm: true},
	{Group: "Results", Words: "reject", Args: []argSpec{{Name: "weird/path", Help: "result in weird/", Suggest: []string{dWeird}}}, Help: "discard a weird result and compute the task again", Confirm: true},
	{Group: "Results", Words: "redo", Args: []argSpec{{Name: "output/path|completed/path", Help: "an accepted result, or its input in completed/", Suggest: []string{dOutput, dCompleted}}},
		Help: "compute an accepted task again (the old result moves to outdated/)", Confirm: true},
	{Group: "Results", Words: "retry", Args: []argSpec{{Name: "errors/path", Help: "task in errors/", Suggest: []string{dErrors}}}, Help: "requeue a failed task"},
	{Group: "Results", Words: "clean", Args: []argSpec{{Name: "--dry-run", Help: "only list what would be deleted", Flag: "--dry-run"},
		{Name: "glob", Help: `file name pattern, e.g. "*.tmp"`}, {Name: "path", Help: "spool path to limit it to", Optional: true, Suggest: []string{dOutput, dWeird, dErrors}}},
		Help: `delete files no longer needed (e.g. "*.tmp") from results;`, More: "recorded in the ledger, never touches input/ or state/", Confirm: true},
	{Group: "Results", Words: "reverify", Args: []argSpec{{Name: "output/path", Help: "result in output/", Suggest: []string{dOutput}}}, Help: "re-check signature, file hashes and output consistency"},

	{Group: "Configuration", Words: "update status", Help: "this version, the newest signed release and whether every client can update"},
	{Group: "Configuration", Words: "update check", Help: "look for a new signed release now (update.url)"},
	{Group: "Configuration", Words: "update apply", Args: []argSpec{{Name: "--force", Help: "do not wait for clients that cannot update", Flag: "--force", Optional: true}},
		Help: "install the new release: clients first (between jobs),", More: "then this server, which restarts; only if every client can (unless --force)", Confirm: true},
	{Group: "Configuration", Words: "alerts test", Help: "send a test alert now by e-mail, Telegram and/or the alert program", More: "(also: test-alert)"},
	{Group: "Integrity and data", Words: "events", Args: []argSpec{{Name: "lines", Help: "how many of the last lines (default 50)", Optional: true}},
		Help: "the short log of admin actions and of what the system noticed (state/events.log)"},
	{Group: "Integrity and data", Words: "check", Help: "every task in exactly one place, none lost (also runs every hour; findings in state/events.log)"},
	{Group: "Integrity and data", Words: "verify", Help: "verify the ledger chain and every stored result file"},
	{Group: "Integrity and data", Words: "backup", Args: []argSpec{{Name: "absolute-dir", Help: "destination directory (absolute path)"}},
		Help: "copy all spool directories, state and config"},
	{Group: "Integrity and data", Words: "restore", Args: []argSpec{{Name: "backup-dir", Help: "a directory written by 'backup'"}},
		Help: "restore a backup (server must be stopped)", Local: true, NoWeb: true, Confirm: true},

	{Group: "Configuration", Words: "config show", Help: "show server.conf as the running server reads it"},
	{Group: "Configuration", Words: "config validate", Help: "check server.conf without applying it"},
	{Group: "Configuration", Words: "config reload", Help: "apply server.conf while running (warnings shown)"},
	{Group: "Configuration", Words: "config edit", Help: "edit server.conf, validate it and apply it", Local: true},

	{Group: "Clients and keys", Words: "clients list", Help: "list clients"},
	{Group: "Clients and keys", Words: "clients approve", Args: []argSpec{cliArg}, Help: "approve a client waiting for approval"},
	{Group: "Clients and keys", Words: "clients quarantine", Args: []argSpec{cliArg}, Help: "stop trusting a client's results (all are checked)", Confirm: true},
	{Group: "Clients and keys", Words: "clients release", Args: []argSpec{cliArg}, Help: "end a quarantine"},
	{Group: "Clients and keys", Words: "clients drain", Args: []argSpec{cliArg}, Help: "finish running jobs, give it no new ones"},
	{Group: "Clients and keys", Words: "clients disable", Args: []argSpec{cliArg, {Name: "--wipe", Help: "also delete its key and credential", Flag: "--wipe", Optional: true},
		{Name: "why", Help: "reason shown to the client", Optional: true}},
		Help: "switch a client off remotely (now, or at its next", More: "connection): it stops, returns its tasks, deletes its job data and exits; --wipe also deletes its key and credential", Confirm: true},
	{Group: "Clients and keys", Words: "clients rename", Args: []argSpec{cliArg, {Name: "new name", Help: "letters, digits, - and _ (e.g. office-left)"}},
		Help: "give a client a clearer name (shown everywhere from now on)"},
	{Group: "Clients and keys", Words: "clients enable", Args: []argSpec{cliArg}, Help: "undo 'clients disable' (not --wipe); the client", More: "then runs 'matriline-client enable'"},
	{Group: "Clients and keys", Words: "kit", Args: []argSpec{{Name: "name", Help: "helper name"}, {Name: "folder", Help: "where to write the kit files"},
		{Name: "code", Option: "--language", Help: "en, es, fr, pt or ar for the helper", Optional: true},
		{Name: "file", Option: "--settings", Help: "client settings file (client.conf format)", Optional: true},
		{Name: "n", Option: "--uses", Help: "computers one credential may enroll", Optional: true},
		{Name: "unix|windows", Option: "--only", Help: "only one of the two files", Optional: true},
		{Name: "folder", Option: "--clients", Help: "client programs (tools/release.sh dist) instead of the signed release", Optional: true}},
		Help: "one file per helper that sets the computer up and installs the service (credential, client and settings inside)", Local: true, NoWeb: true},
	{Group: "Clients and keys", Words: "keys issue", Args: []argSpec{{Name: "name", Help: "client name"}, {Name: "file", Help: "where to write the credential file"},
		{Name: "N", Option: "--uses", Help: "how many computers may enroll with it (default 1)", Optional: true},
		{Name: "file", Option: "--preset", Help: "client settings to suggest (client.conf format), applied by 'init --credential'", Optional: true, Suggest: nil}, {Name: "addr", Help: "server address written in it (default: advertise)", Optional: true}},
		Help: "create a credential file for a new client"},
	{Group: "Clients and keys", Words: "keys token", Args: []argSpec{{Name: "hours", Help: "validity in hours", Optional: true}}, Help: "one-time join token (enrollment = register)"},
	{Group: "Clients and keys", Words: "keys revoke", Args: []argSpec{cliArg}, Help: "revoke a client immediately", Confirm: true},
	{Group: "Clients and keys", Words: "block", Args: []argSpec{{Name: "ip", Help: "IP address"}}, Help: "refuse connections from an IP address", Confirm: true},
	{Group: "Clients and keys", Words: "unblock", Args: []argSpec{{Name: "ip", Help: "IP address"}}, Help: "allow connections from an IP address again"},
	{Group: "Clients and keys", Words: "bans", Help: "automatic bans (network.ban_*)"},
	{Group: "Clients and keys", Words: "bans lift", Args: []argSpec{{Name: "address", Help: "banned address"}}, Help: "lift an automatic ban"},
}

// synopsis is "keys issue <name> <file> [--uses N] [addr]".
func (c cmdSpec) synopsis() string {
	s := c.Words
	for _, a := range c.Args {
		switch {
		case a.Flag != "":
			s += " [" + a.Flag + "]"
		case a.Option != "":
			s += " [" + a.Option + " " + a.Name + "]"
		case a.Optional:
			s += " [" + a.Name + "]"
		default:
			s += " <" + a.Name + ">"
		}
	}
	return s
}

// init applies MATRILINE_HIDE: a program that embeds Matriline (Nacomline, one computer)
// lists command words its users have no use for (e.g. "clients,keys,bans"); help, the
// console and the web page leave them out. The command line still reaches them.
func init() { hideCommands(os.Getenv("MATRILINE_HIDE")) }

func hideCommands(list string) {
	hide := strings.Split(list, ",")
	var kept []cmdSpec
	for _, c := range catalogue {
		drop := false
		for _, h := range hide {
			if h = strings.TrimSpace(h); h != "" && (c.Words == h || strings.HasPrefix(c.Words, h+" ")) {
				drop = true
			}
		}
		if !drop {
			kept = append(kept, c)
		}
	}
	catalogue = kept
}

// lookup finds the longest catalogue entry that the command line starts with.
func lookup(args []string) (cmdSpec, bool) {
	var best cmdSpec
	found := false
	for _, c := range catalogue {
		w := strings.Fields(c.Words)
		if len(w) > len(args) {
			continue
		}
		match := true
		for i := range w {
			if w[i] != args[i] {
				match = false
				break
			}
		}
		if match && (!found || len(w) > len(strings.Fields(best.Words))) {
			best, found = c, true
		}
	}
	return best, found
}

// commandLine turns the values given for c's arguments (in order; "" for an optional one
// left out, "yes" for a flag that is set) into the command line.
func (c cmdSpec) commandLine(vals []string) ([]string, error) {
	line := strings.Fields(c.Words)
	for i, a := range c.Args {
		v := ""
		if i < len(vals) {
			v = strings.TrimSpace(vals[i])
		}
		switch {
		case a.Flag != "":
			if v != "" && v != "no" {
				line = append(line, a.Flag)
			}
		case v == "" && a.Optional:
		case v == "":
			return nil, fmt.Errorf("%s: <%s> is required (%s)", c.Words, a.Name, a.Help)
		case a.Option != "":
			line = append(line, a.Option, v)
		default:
			line = append(line, v)
		}
	}
	return line, nil
}

// usageText builds 'matriline-server help' from the catalogue.
func usageText() string {
	var b strings.Builder
	b.WriteString(i18n.T(`matriline-server - central server of the Matriline distributed ORCA system

Usage: matriline-server [-c server.conf] <command> [arguments]
Paths are relative to the working (spool) directory, e.g. input/1list/mol1.inp
Every task command accepts a single file or a whole directory.
`))
	for _, g := range groups {
		header := false
		for _, c := range catalogue {
			if c.Group != g {
				continue
			}
			if !header {
				fmt.Fprintf(&b, "\n%s\n", i18n.T(g))
				header = true
			}
			syn := c.synopsis()
			if len(syn) > 31 {
				fmt.Fprintf(&b, "  %s\n  %-31s %s\n", syn, "", i18n.T(c.Help))
			} else {
				fmt.Fprintf(&b, "  %-31s %s\n", syn, i18n.T(c.Help))
			}
			if c.More != "" {
				fmt.Fprintf(&b, "  %-31s %s\n", "", i18n.T(c.More))
			}
		}
	}
	return b.String()
}
