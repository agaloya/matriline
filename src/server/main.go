// Command matriline-server is the Matriline central server: the coordinating daemon
// ("run") and the administration command line (every other sub-command), in one binary.
package main

import (
	"bufio"
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/agaloya/matriline/common/build"
	"github.com/agaloya/matriline/common/conf"
	"github.com/agaloya/matriline/common/conhost"
	"github.com/agaloya/matriline/common/i18n"
	"github.com/agaloya/matriline/common/ident"
	"github.com/agaloya/matriline/common/lock"
	"github.com/agaloya/matriline/common/orcafind"
	"github.com/agaloya/matriline/common/pager"
	"github.com/agaloya/matriline/common/parent"
	"github.com/agaloya/matriline/common/projects"
	"github.com/agaloya/matriline/common/release"
	"io"
)

func main() {
	parent.Watch() // started by Nacomline: stop when it is gone
	args := os.Args[1:]
	cfgPath := os.Getenv("MATRILINE_SERVER_CONFIG")
	explicit := cfgPath != ""
	if cfgPath == "" {
		cfgPath = "server.conf"
	}
	if len(args) >= 2 && (args[0] == "-c" || args[0] == "--config") {
		cfgPath, args, explicit = args[1], args[2:], true
	}
	if !explicit && len(args) > 0 && needsProject(args[0]) {
		cfgPath = chooseProject("server", cfgPath)
	}
	setLanguage(cfgPath)
	if len(args) == 1 && args[0] == "detect-language" {
		// for programs that embed Matriline (Nacomline): the language a new installation
		// on this computer gets (i18n.System), so they need not detect it themselves
		fmt.Println(cmp.Or(i18n.System(), "en"))
		return
	}
	if len(args) == 0 || args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		if len(args) > 1 {
			if c, ok := lookup(args[1:]); ok {
				var b strings.Builder
				printHelp(&b, c)
				pager.Print(b.String())
				return
			}
			if len(args) == 2 { // 'help clients': the commands that start with that word
				var b strings.Builder
				for _, c := range catalogue {
					if w := strings.Fields(c.Words); len(w) > 1 && w[0] == args[1] {
						if syn := c.synopsis(); len(syn) > 31 { // the layout of 'help'
							fmt.Fprintf(&b, "  %s\n  %-31s %s\n", syn, "", i18n.T(c.Help))
						} else {
							fmt.Fprintf(&b, "  %-31s %s\n", syn, i18n.T(c.Help))
						}
					}
				}
				if b.Len() > 0 {
					pager.Print(b.String() + "\n" + i18n.Tf("details: matriline-server help %s <command>", args[1]) + "\n")
					return
				}
			}
			fmt.Fprintf(os.Stderr, "no command %q ('matriline-server help' lists them)\n", strings.Join(args[1:], " "))
			os.Exit(2)
		}
		pager.Print(usageText())
		return
	}
	cfgPath, _ = filepath.Abs(cfgPath)
	if err := runCommand(cfgPath, args); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

// runCommand runs one command line: the command line itself, the console and (through
// call) the web interface all come here.
func runCommand(cfgPath string, args []string) error {
	var err error
	args = absArgs(args)
	switch args[0] {
	case "service":
		err = cmdService(cfgPath, args[1:])
	case "console":
		err = cmdConsole(cfgPath)
	case "kit":
		err = cmdKit(cfgPath, args[1:])
	case "web":
		addr := ""
		if len(args) > 1 {
			addr = args[1]
		}
		err = cmdWeb(cfgPath, addr)
	case "version":
		fmt.Println(versionText())
		if len(args) > 1 && args[1] == "--plain" {
			break // just this program (an update's preflight): no configuration, no connection
		}
		if run := captureRemote(cfgPath, []string{"version"}); run != "" && strings.TrimSpace(run) != versionText() {
			fmt.Printf("WARNING: the running server is %s; restart it to run this build\n", strings.TrimSpace(run))
		}
	case "init":
		dir, lang := ".", ""
		for i := 1; i < len(args); i++ {
			switch {
			case args[i] == "--language" && i+1 < len(args):
				lang = args[i+1]
				i++
			case strings.HasPrefix(args[i], "--language="):
				lang = strings.TrimPrefix(args[i], "--language=")
			default:
				dir = args[i]
			}
		}
		err = cmdInit(dir, lang)
	case "doctor":
		err = cmdDoctorAny(cfgPath)
	case "run":
		err = cmdRun(cfgPath)
	case "watch":
		err = cmdWatch(cfgPath)
	case "edit":
		if len(args) < 2 {
			err = fmt.Errorf("usage: edit <path>")
			break
		}
		err = cmdEdit(cfgPath, args[1])
	case "restore":
		if len(args) < 2 {
			err = fmt.Errorf("usage: restore <backup-dir>")
			break
		}
		err = cmdRestore(cfgPath, args[1])
	case "config":
		if len(args) > 1 && args[1] == "edit" {
			err = cmdConfigEdit(cfgPath)
			break
		}
		err = remote(cfgPath, args)
	case "verify":
		if remote(cfgPath, args) != nil {
			err = offlineVerify(cfgPath)
		}
	case "live":
		// in a terminal that shows pictures, the molecule as a picture (no pager: it would
		// break the picture's escape sequence)
		if _, js := withoutJSON(args); !js && imageProtocol() != "" {
			sel := 1 // as cmdLive reads it: the number anywhere ('live full 3')
			for _, w := range args[1:] {
				if n, e := strconv.Atoi(w); e == nil {
					sel = n
				}
			}
			var out string
			if out, err = call(cfgPath, args); err == nil {
				fmt.Println(withPicture(cfgPath, out, sel))
			}
			break
		}
		err = remote(cfgPath, args)
	default:
		err = remote(cfgPath, args)
	}
	return err
}

// absArgs resolves the local files of a command on the caller's side: the server runs in
// its own working directory.
func absArgs(args []string) []string {
	args = append([]string(nil), args...)
	switch {
	case (args[0] == "add" || args[0] == "backup") && len(args) > 1:
		args[1], _ = filepath.Abs(args[1])
	case args[0] == "keys" && len(args) > 3 && args[1] == "issue":
		// the file is the second positional argument after "issue" (--uses N and
		// --preset FILE may be anywhere; the preset file is resolved here too)
		pos := 0
		for i := 2; i < len(args); i++ {
			if args[i] == "--uses" {
				i++
				continue
			}
			if args[i] == "--preset" {
				if i+1 < len(args) {
					args[i+1], _ = filepath.Abs(args[i+1])
				}
				i++
				continue
			}
			if pos++; pos == 2 {
				args[i], _ = filepath.Abs(args[i])
				break
			}
		}
	}
	return args
}

func versionText() string {
	return fmt.Sprintf("%s (commit %s)%s", agentVersion, commit, build.Label)
}

func rootOf(cfgPath string) (string, error) {
	cfg, _, err := loadConfig(cfgPath)
	if err != nil {
		return "", err
	}
	root := cfg.Root
	if !filepath.IsAbs(root) {
		root = filepath.Join(filepath.Dir(cfgPath), root)
	}
	return filepath.Clean(root), nil
}

// remote sends a command to the running server through the admin socket and prints the answer.
func remote(cfgPath string, args []string) error {
	out, err := call(cfgPath, args)
	if out != "" {
		if args[0] == "status" {
			st, _ := os.Stdout.Stat()
			out = renderCharts(out, st != nil && st.Mode()&os.ModeCharDevice != 0 && os.Getenv("NO_COLOR") == "")
		}
		pager.Print(out + "\n")
	}
	return err
}

// call sends a command to the running server through the admin socket and returns its answer.
func call(cfgPath string, args []string) (string, error) {
	root, err := rootOf(cfgPath)
	if err != nil {
		return "", err
	}
	c, err := net.DialTimeout("unix", adminSocket(root), 3*time.Second)
	if err != nil {
		return "", fmt.Errorf("the server is not running (%v). Start it with 'matriline-server run' or the system service", err)
	}
	defer c.Close()
	if err := json.NewEncoder(c).Encode(adminReq{Cmd: args[0], Args: args[1:]}); err != nil {
		return "", err
	}
	var resp adminResp
	if err := json.NewDecoder(bufio.NewReader(c)).Decode(&resp); err != nil {
		return "", err
	}
	if resp.Err != "" {
		err = fmt.Errorf("%s", resp.Err)
	}
	return strings.TrimRight(resp.Out, "\n"), err
}

// cmdDoctorAny: the running server's checks, or, before the first 'run' (or while it is
// stopped), the same checks made here (a new admin runs 'doctor' first; Nacomline's
// doctor did, on a Mac). Only "listening" then needs the server.
func cmdDoctorAny(cfgPath string) error {
	out, err := call(cfgPath, []string{"doctor"})
	if err == nil || !strings.Contains(err.Error(), "not running") {
		if out != "" {
			fmt.Println(out)
		}
		return err
	}
	root, rerr := rootOf(cfgPath)
	if rerr != nil {
		return rerr
	}
	if lk, lerr := lock.Acquire(filepath.Join(root, dState), "server.lock"); lerr == nil {
		defer lk.Close() // the server cannot start meanwhile
	} else {
		return err // it is starting or stopping: try again in a moment
	}
	s, oerr := openServer(cfgPath)
	if oerr != nil {
		return oerr
	}
	fmt.Println(i18n.T("The server is not running: these checks were made here (start it, and 'listening' passes)."))
	s.loadReference()
	out, err = s.cmdDoctor()
	// nothing listens while the server is stopped: not a failure here
	fmt.Println(strings.ReplaceAll(out, "[FAIL] listening on ", "[SKIP] listening on "))
	return err
}

// setLanguage chooses the language of the texts people read: general.language of the
// configuration, otherwise the system's; English when there is no translation.
func setLanguage(cfgPath string) {
	lang := ""
	if cf, err := conf.Load(cfgPath); err == nil {
		lang = cf.String("general.language", "")
	}
	i18n.Choose(lang)
}

var languageLine = regexp.MustCompile(`(?m)^(\[general\][^\[]*?)language =[ \t]*\n`)

func cmdInit(dir, lang string) error {
	asked := lang
	if lang == "" {
		lang = i18n.System()
	}
	if i18n.Normalize(lang) != "en" && i18n.Set(lang) != nil {
		if asked != "" {
			return fmt.Errorf("language %q: no translation (available: %s)", asked, strings.Join(i18n.Available(), ", "))
		}
		lang = "en" // this computer's language has no translation yet
	}
	// INSTALL.md writes /path/to/project/directory: a copied command must not create it
	// (on Windows it would, as C:\path\to\...)
	if strings.Contains(filepath.ToSlash(dir), "path/to/") {
		return fmt.Errorf("%s is the example from the instructions: replace it with your own (empty) folder", dir)
	}
	// a working directory should hold only Matriline's files: easier to back up, move
	// and clean, and nothing else gets mixed with inputs, results or state
	if es, err := os.ReadDir(dir); err == nil && len(es) > 0 {
		fmt.Fprintf(os.Stderr, "note: %s is not empty (%d entries); an empty directory is recommended\n", dir, len(es))
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	cfgPath := filepath.Join(dir, "server.conf")
	if _, err := os.Stat(cfgPath); err == nil {
		return fmt.Errorf("%s already exists", cfgPath)
	}
	defer projects.Add("server", cfgPath) // found from any folder from now on (user)
	if err := os.WriteFile(cfgPath, []byte(withRootShown(withFoundOrca(withLanguage(i18n.Template("server.conf", defaultConfig), i18n.Normalize(lang))), dir)), 0o600); err != nil {
		return err
	}
	for _, d := range append(spoolDirs, dState) {
		mode := os.FileMode(0o750)
		if d == dState {
			mode = 0o700
		}
		if err := os.MkdirAll(filepath.Join(dir, d), mode); err != nil {
			return err
		}
	}
	k, _, err := ident.LoadOrCreate(filepath.Join(dir, dState, "server.key"))
	if err != nil {
		return err
	}
	abs, _ := filepath.Abs(dir)
	fmt.Printf("initialized %s\nserver id:  %s\nserver key: %s\nnext: edit %s (at least network.advertise), then run 'matriline-server -c %s run'\n",
		abs, k.ID(), ident.EncodeKey(k.Pub), cfgPath, cfgPath)
	fmt.Println(i18n.T("ORCA's license (EULA) applies to every use of Matriline: academic or private projects only, and\n  you and every helper must each hold an ORCA license (docs/ORCA_LICENSE.md in Matriline's source)."))
	return nil
}

// withLanguage writes the chosen language into the template's general.language (the
// first "language =" line, in the [general] section at the top of every template).
func withLanguage(text, lang string) string {
	return languageLine.ReplaceAllString(text, "${1}language = "+lang+"\n")
}

// withRootShown writes the project folder above "root = ." (user: "." alone did not say
// which folder it is). The value stays "." so the project still works when moved or
// restored elsewhere.
func withRootShown(text, dir string) string {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return text
	}
	return strings.Replace(text, "\nroot = .\n", "\n# "+i18n.Tf("\".\" is this folder: %s", abs)+"\nroot = .\n", 1)
}

// withFoundOrca sets orca.reference_paths (and orca.version when needed) to the ORCA
// installations found on this computer: the template's /opt/orca-6.1.1 means nothing on
// Windows (found when the server was first set up there). Installations of the template's
// version win; otherwise the newest version found becomes the campaign's.
func withFoundOrca(text string) string {
	const verLine, refLine = "\nversion = 6.1.1\n", "\nreference_paths = /opt/orca-6.1.1\n"
	byVer := map[string][]string{}
	var newest string
	for _, d := range orcafind.Dirs() {
		v, _, err := probeOrca(d, os.TempDir())
		if err != nil {
			fmt.Printf("  ORCA at %s does not run here: %v\n", d, err)
			continue
		}
		fmt.Printf("  ORCA found: %s (version %s)\n", d, v)
		byVer[v] = append(byVer[v], d)
		if newest == "" || newerVersion(v, newest) {
			newest = v
		}
	}
	want := "6.1.1"
	if len(byVer[want]) == 0 {
		if newest == "" {
			fmt.Println("  no ORCA on this computer: fine, the server needs none (orca.accepted_fingerprints = builtin\n  accepts the official builds); reference_paths stays empty")
			return strings.Replace(text, refLine, "\nreference_paths =\n", 1)
		}
		want = newest
		text = strings.Replace(text, verLine, "\nversion = "+want+"\n", 1)
	}
	if len(builtinFor(want)) > 0 {
		// every official build of this version is known by its fingerprint: a reference
		// would only cost a long first start (over 10 minutes on a Windows VM)
		fmt.Printf("  ORCA %s: the server knows every official build of it, so it needs no reference ORCA\n  (orca.accepted_fingerprints = builtin); reference_paths stays empty\n", want)
		return strings.Replace(text, refLine, "\nreference_paths =\n", 1)
	}
	return strings.Replace(text, refLine, "\nreference_paths = "+strings.Join(byVer[want], ", ")+"\n", 1)
}

// newerVersion compares dotted versions ("6.1.1" > "6.0.10" > "6.0.9").
func newerVersion(a, b string) bool {
	pa, pb := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(pa) && i < len(pb); i++ {
		x, _ := strconv.Atoi(pa[i])
		y, _ := strconv.Atoi(pb[i])
		if x != y {
			return x > y
		}
	}
	return len(pa) > len(pb)
}

func cmdRun(cfgPath string) error {
	// one server per spool, checked before anything is opened or written
	root, err := rootOf(cfgPath)
	if err != nil {
		return err
	}
	// an old instance still ending (service restarted at once, update) gets 20 s
	lk, err := lock.AcquireWait(filepath.Join(root, dState), "server.lock", 20*time.Second)
	if err != nil {
		return err
	}
	defer lk.Close()
	s, err := openServer(cfgPath)
	if err != nil {
		return err
	}
	if c, err := net.DialTimeout("unix", adminSocket(s.conf().Root), time.Second); err == nil {
		c.Close()
		return fmt.Errorf("another server is already running on this spool")
	}
	if exe, err := selfExe(); err == nil {
		release.CleanDownloads(exe) // left by an update that was interrupted
	}
	if pager.Interactive() {
		fmt.Println(i18n.T("Running in this terminal: Ctrl+C stops it (closing the window too). To keep it running without a terminal: 'service install'."))
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	conhost.Watch(func() { // its scheduled task was ended (common/conhost)
		s.log.Warnf("the console host ended (scheduled task stopped?): stopping")
		cancel()
	})
	if err := s.run(ctx); err != nil || s.restartTo() == "" {
		return err
	}
	lk.Close()
	err = release.Restart(s.restartTo()) // returns only on error
	return fmt.Errorf("restart after the update: %v (start the server again)", err)
}

// cmdEdit opens an input in an editor; the server records the new hash in the ledger and
// restarts the task if it was running (D23).
func cmdEdit(cfgPath, p string) error {
	root, err := rootOf(cfgPath)
	if err != nil {
		return err
	}
	top, _ := splitSpool(p)
	if top != dInput && top != dPaused && top != dCancelled {
		return fmt.Errorf("only inputs in input/, paused/ or cancelled/ can be edited (results are protected by the integrity ledger)")
	}
	abs := filepath.Join(root, filepath.FromSlash(p))
	if !filepath.IsLocal(filepath.FromSlash(p)) {
		return fmt.Errorf("path outside the working directory")
	}
	if _, err := os.Stat(abs); err != nil {
		return err
	}
	if top == dInput {
		info := captureRemote(cfgPath, []string{"taskinfo", p})
		if strings.Contains(info, "running-on=") {
			fmt.Printf("WARNING: this task is running now:\n%s\nSaving changes cancels the running attempt(s) and recomputes the task.\n", info)
		}
	}
	fmt.Println("WARNING: the edit will be recorded in the integrity ledger. Results computed from the old version are not affected.")
	before, _ := os.ReadFile(abs)
	editor := os.Getenv("EDITOR")
	if editor == "" {
		editor = defaultEditor
	}
	cmd := exec.Command(editor, abs)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("editor: %v", err)
	}
	after, _ := os.ReadFile(abs)
	if string(before) == string(after) {
		fmt.Println("no changes")
		return nil
	}
	return remote(cfgPath, []string{"edited", p})
}

func captureRemote(cfgPath string, args []string) string {
	root, err := rootOf(cfgPath)
	if err != nil {
		return ""
	}
	c, err := net.DialTimeout("unix", adminSocket(root), time.Second)
	if err != nil {
		return ""
	}
	defer c.Close()
	json.NewEncoder(c).Encode(adminReq{Cmd: args[0], Args: args[1:]})
	var resp adminResp
	json.NewDecoder(c).Decode(&resp)
	return resp.Out
}

func cmdConfigEdit(cfgPath string) error {
	orig, err := os.ReadFile(cfgPath)
	if err != nil {
		return err
	}
	tmp := cfgPath + ".editing"
	if err := os.WriteFile(tmp, orig, 0o600); err != nil {
		return err
	}
	defer os.Remove(tmp)
	editor := os.Getenv("EDITOR")
	if editor == "" {
		editor = defaultEditor
	}
	for {
		cmd := exec.Command(editor, tmp)
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
		if err := cmd.Run(); err != nil {
			return err
		}
		_, warns, err := loadConfig(tmp)
		for _, w := range warns {
			fmt.Println(w)
		}
		if err == nil {
			break
		}
		fmt.Printf("%v\nEdit again? [Y/n] ", err)
		var ans string
		fmt.Scanln(&ans)
		if strings.HasPrefix(strings.ToLower(ans), "n") {
			return fmt.Errorf("configuration not changed")
		}
	}
	b, _ := os.ReadFile(tmp)
	if err := ident.WriteFileAtomic(cfgPath, b, 0o600); err != nil {
		return err
	}
	if err := remote(cfgPath, []string{"config", "reload"}); err != nil {
		fmt.Println("saved; it will be applied when the server starts:", err)
	}
	return nil
}

func cmdRestore(cfgPath, backup string) error {
	root, err := rootOf(cfgPath)
	if err != nil {
		return err
	}
	if c, err := net.DialTimeout("unix", adminSocket(root), time.Second); err == nil {
		c.Close()
		return fmt.Errorf("stop the server first ('matriline-server stop')")
	}
	if _, err := os.Stat(filepath.Join(backup, dState, "ledger.log")); err != nil {
		return fmt.Errorf("%s does not look like a Matriline backup", backup)
	}
	keep := filepath.Join(root, "pre-restore-"+time.Now().Format("20060102-150405"))
	for _, d := range backupDirs {
		if _, err := os.Stat(filepath.Join(backup, d)); os.IsNotExist(err) {
			continue
		}
		if _, err := os.Stat(filepath.Join(root, d)); err == nil {
			if err := moveFile(filepath.Join(root, d), filepath.Join(keep, d)); err != nil {
				return err
			}
		}
		if err := copyTree(filepath.Join(backup, d), filepath.Join(root, d)); err != nil {
			return err
		}
	}
	fmt.Printf("restored %s\nprevious state kept in %s (delete it when satisfied)\n", backup, keep)
	return nil
}

func offlineVerify(cfgPath string) error {
	root, err := rootOf(cfgPath)
	if err != nil {
		return err
	}
	k, _, err := ident.LoadOrCreate(filepath.Join(root, dState, "server.key"))
	if err != nil {
		return err
	}
	out, err := verifySpool(root, k)
	fmt.Print(out)
	return err
}

// renderCharts replaces each "chart...: k=v ..." line of status with a 50-column bar,
// coloured on a terminal (ANSI; NO_COLOR disables it, https://no-color.org) and drawn
// with distinct characters otherwise.
func renderCharts(out string, color bool) string {
	type seg struct{ name, color, char string }
	segs := []seg{{"queued", "47", "."}, {"running", "44", ">"}, {"output", "42", "#"},
		{"weird", "43", "?"}, {"errors", "41", "!"}, {"other-versions", "104", "o"},
		{"paused", "45", "|"}, {"cancelled", "100", "x"}}
	lines := strings.Split(out, "\n")
	for i, ln := range lines {
		head, kv, ok := strings.Cut(ln, ": ")
		if !ok || !strings.HasPrefix(head, "chart") {
			continue
		}
		vals := map[string]int{}
		total := 0
		for _, f := range strings.Fields(kv) {
			k, v, _ := strings.Cut(f, "=")
			n, _ := strconv.Atoi(v)
			vals[k] = n
			total += n
		}
		var bar, legend strings.Builder
		width, used := 50, 0
		for j, sg := range segs {
			n := vals[sg.name]
			w := 0
			if total > 0 {
				w = int(math.Round(float64(n) * float64(width) / float64(total)))
			}
			if j == len(segs)-1 && total > 0 {
				w = max(width-used, 0)
			}
			if n > 0 && w == 0 && used < width {
				w = 1
			}
			w = min(w, width-used)
			used += w
			if color {
				bar.WriteString("\x1b[" + sg.color + "m" + strings.Repeat(" ", w) + "\x1b[0m")
				fmt.Fprintf(&legend, " \x1b[%sm  \x1b[0m %s %d", sg.color, sg.name, n)
			} else {
				bar.WriteString(strings.Repeat(sg.char, w))
				fmt.Fprintf(&legend, " %s %s %d", sg.char, sg.name, n)
			}
		}
		title := strings.TrimPrefix(strings.TrimPrefix(head, "chart"), " ")
		if title == "" {
			title = "spool"
		}
		lines[i] = fmt.Sprintf("%s [%s]\n %s", title, bar.String(), strings.TrimSpace(legend.String()))
	}
	return strings.Join(lines, "\n")
}

// needsProject: commands that work on a project (not init, help, version...).
func needsProject(cmd string) bool {
	switch cmd {
	case "init", "help", "-h", "--help", "version", "detect-language":
		return false
	}
	return true
}

// chooseProject finds the project when the command was not given one and the current
// folder has none (user): the only one made on this computer, or the one the person picks.
func chooseProject(kind, def string) string {
	i18n.Choose("")
	var in io.Reader
	if pager.Interactive() {
		in = os.Stdin
	}
	p, err := projects.Resolve(kind, def, in, os.Stderr, projects.Texts{
		Which:   i18n.T("Which project? (its number and Enter)"),
		Several: i18n.Tf("several projects on this computer: run the command inside one, or choose it with -c <folder>/%s.conf:", kind),
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	return p
}
