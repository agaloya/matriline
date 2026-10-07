package main

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/agaloya/matriline/common/i18n"
)

// Longer explanations and examples for 'help <command>' (the catalogue keeps one line).
var commandDocs = map[string]string{
	"init": `Creates the working directory: input/ output/ completed/ ... and state/ with this
server's key, and a server.conf that documents every option. Edit at least
network.advertise (the address clients dial) before 'run'.`,
	"run": `Runs the server in the foreground until Ctrl-C or 'stop'. Normally the system
service does it ('service install'). Only one server can run per working directory.`,
	"stop": `Stops the running server cleanly: it saves its state; clients keep computing and
reconnect when it is back.`,
	"service": `install: the server starts by itself (Linux: a systemd user unit, at logon; with
'loginctl enable-linger' also without logon. Windows: a scheduled task, at start-up
when installed by an administrator, otherwise at logon). remove: undoes it. Installing
twice replaces the earlier one; each working directory has its own service.`,
	"doctor": `Checks server.conf, the ORCA reference installation and its fingerprint, the free
disk, the network port and the ledger, and says what to fix.`,
	"add": `Copies .inp files from anywhere on this computer into input/ (keeping
sub-directories). The server notices them within scan_interval and queues them.
Copying files into input/ by hand works the same.`,
	"status": `Without a path: how many inputs are queued, running, done (output/), weird/,
errors/, per sub-directory of input/, and the connected clients. With a path (a file
or folder of the spool): where each task is and on which client it runs.`,
	"live": `What runs now on every client; for one job (number n, default 1): its input (first
lines; 'full' for all of it), a drawing of the molecule and the latest output lines
the client reported (they arrive with its heartbeats). 'watch' repeats it every 2 s.`,
	"pause": `Moves inputs from input/ to paused/: they are not given out; a running one is
stopped and its client freed. 'all' pauses every queued input. 'resume' brings them back.`,
	"cancel": `Moves inputs (queued or paused) to cancelled/; a running one is stopped.
'uncancel' brings them back to input/.`,
	"priority": `Higher numbers are served first (default 0). On a directory it applies to every
input in it. 'next' gives one file or directory the highest priority at once.`,
	"history": `Everything the ledger recorded about a task: queued, given to which client,
results, checks, moves, by whom and when.`,
	"review": `Lists the results in weird/ with the reason (failed or unconfirmed check,
inconsistent output, ORCA problem). Then 'accept' (it becomes the result in output/) or
'reject' (computed again).`,
	"accept": `Moves a result from weird/ to output/ after you reviewed it. If the task already
has a result in output/, that one moves to outdated/ (one result per task). Recorded in
the ledger.`,
	"reject": `Discards a result in weird/ (it moves to outdated/) and queues the task again.`,
	"redo": `Computes an accepted task again: its result moves to outdated/ (not counted in the
totals) and the input goes back to input/.`,
	"retry": `Queues a task in errors/ again (e.g. after fixing the input or the memory).`,
	"clean": `Deletes files that are no longer needed from results (e.g. "*.tmp" or large
"*.densities"), recorded in the ledger so 'verify' still agrees. Never touches input/ or
state/. --dry-run lists them first. Example: clean --dry-run "*.densities" output/`,
	"reverify": `Checks again a result in output/: the client's signature, the hash of every file and
the consistency of the ORCA output.`,
	"events": `The short log of what admins did and what the system noticed (results, alerts,
moves): state/events.log. Easier to read than the full log.`,
	"check": `Makes sure every task is in exactly one place (input/, output/, weird/, errors/,
paused/, cancelled/) and none was lost or duplicated. Also runs every hour.`,
	"verify": `Verifies the ledger's signed chain and the hash of every stored result file; lists
anything modified, missing or unrecorded.`,
	"backup": `Copies the spool, state and configuration into a new dated folder inside the given
directory (absolute path). 'restore' puts a backup back (server stopped).`,
	"alerts test": `Sends a test alert by e-mail and/or the alert program, so you know the [alerts]
settings work.`,
	"update status": `This program's version, the newest signed release found, and whether every client
can update (and why not). Automatic updates are EXPERIMENTAL (see server.conf).`,
	"update apply": `Installs the new signed release: clients first (each when its running jobs are
done), then this server, which restarts. Only if every client can update, unless
--force (clients that cannot keep their version).`,
	"config edit": `Opens server.conf in $EDITOR; when you close it, checks it and applies it ('config
reload'). Options marked [restart] need a restart.`,
	"clients disable": `Switches a client off remotely (now, or at its next connection if it is offline): it
stops, gives back its tasks, deletes its job data and stays off. 'clients enable' undoes
it (then the computer runs 'matriline-client enable'). --wipe also deletes its key and
credential: it can only come back with a new credential.`,
	"clients rename": `Gives a client a clearer name, e.g. the room or the owner of the computer, so it is
easy to tell which computer is which in status, clients, live and the web page. Its key,
results and history stay; the ledger records the change.`,
	"clients quarantine": `Every result of this client is checked on another computer until 'clients release'.`,
	"keys issue": `Writes a one-time credential file for a new client: on its first connection the client
creates its own key and the file stops working. --uses N lets N computers enroll with
one file (name-1, name-2, ...).`,
}

// printHelp is 'help <command>': what it does, its arguments, the details and notes.
func printHelp(w io.Writer, c cmdSpec) {
	prog := os.Getenv("MATRILINE_PROG") // the name a program that embeds Matriline is called by
	if prog == "" {
		prog = "matriline-server"
	}
	fmt.Fprintf(w, "%s %s\n\n  %s", prog, c.synopsis(), i18n.T(c.Help))
	if c.More != "" {
		fmt.Fprintf(w, " %s", i18n.T(c.More))
	}
	fmt.Fprintln(w)
	if d := i18n.T(commandDocs[c.Words]); d != "" {
		fmt.Fprintln(w)
		for _, l := range strings.Split(d, "\n") {
			fmt.Fprintln(w, "  "+l)
		}
	}
	if len(c.Args) > 0 {
		fmt.Fprintln(w, "\n  "+i18n.T("arguments:"))
	}
	for _, a := range c.Args {
		name := a.Name
		if a.Flag != "" {
			name = a.Flag
		} else if a.Option != "" {
			name = a.Option + " " + a.Name
		}
		opt := ""
		if a.Optional {
			opt = " " + i18n.T("(optional)")
		}
		fmt.Fprintf(w, "    %-16s %s%s\n", name, i18n.T(a.Help), opt)
	}
	var notes []string
	if c.Confirm {
		notes = append(notes, i18n.T("the console and the web page ask before running it"))
	}
	if c.Local {
		notes = append(notes, i18n.T("runs on this computer, in this terminal"))
	}
	if c.NoWeb {
		notes = append(notes, i18n.T("not on the web page"))
	}
	if len(notes) > 0 {
		fmt.Fprintf(w, "\n  %s\n", i18n.Tf("note: %s", strings.Join(notes, "; ")))
	}
}
