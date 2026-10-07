// Command matriline-client is the Matriline helper-host agent: it connects out to the
// central server, runs ORCA jobs in a sandbox and returns signed results.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"github.com/agaloya/matriline/common/build"
	"github.com/agaloya/matriline/common/conf"
	"github.com/agaloya/matriline/common/conhost"
	"github.com/agaloya/matriline/common/i18n"
	"github.com/agaloya/matriline/common/orca"
	"github.com/agaloya/matriline/common/pager"
	"github.com/agaloya/matriline/common/parent"
	"io/fs"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/agaloya/matriline/common/ident"
	"github.com/agaloya/matriline/common/lock"
	"github.com/agaloya/matriline/common/orcafind"
	"github.com/agaloya/matriline/common/projects"
	"github.com/agaloya/matriline/common/release"
	"io"
)

const usage = `matriline-client - helper host of the Matriline distributed ORCA system

Usage: matriline-client [-c client.conf] <command>

  init [dir] [--credential file] [--language code]
                    create a directory with a documented client.conf; finds ORCA by itself.
                    With the credential file from the server admin it also installs it and
                    applies the settings the admin suggested in it (one command, nothing else).
                    --language: en, es, fr, pt or ar (default: this computer's language)
  run               connect to the server and compute (normally run by the system service)
  join              enrollment = register: create a key and a credential from a join token
                      --server host:port --server-key <base64> --token <token> [--name n] [--relay host:port]
  status            show local jobs and whether this computer is paused
  pause [duration] [--now]  stop lending this computer (e.g. "pause 2h"; without a duration
                    until 'resume'); running jobs finish, or with --now they are stopped and
                    given back to the server
  resume            lend it again
  enable            start again after the server admin disabled this client and enabled
                    it again (clients enable); the next 'run' or service start connects
  service install|remove
                    start the client by itself when you log in (Linux: systemd user
                    service; Windows: scheduled task, no window; macOS: launchd agent),
                    or stop doing so
  doctor            check ORCA, the sandbox and the credential, and that the server answers
                    (a plain network test: it does not log in)
  fingerprint [orca-dir]  print the ORCA installation's fingerprint (JSON) for the server
                    admin to approve (orca.accepted_fingerprints); needed for ORCA builds
                    that print no GIT hash, e.g. the arm64 build
  console           the commands above from a numbered menu
  version
`

// commandUsage is the part of the usage text about one command ('help <command>').
func commandUsage(cmd string) string {
	var b strings.Builder
	in := false
	for _, l := range strings.Split(i18n.T(usage), "\n") {
		if strings.HasPrefix(l, "  ") && !strings.HasPrefix(l, "   ") { // a command's first line
			f := strings.Fields(l)
			in = len(f) > 0 && f[0] == cmd
		}
		if in {
			b.WriteString(l + "\n")
		}
	}
	return b.String()
}

func main() {
	parent.Watch() // started by Nacomline: stop when it is gone
	args := os.Args[1:]
	if len(args) > 0 && args[0] == "sandbox-exec" { // internal helper, see sandbox_linux.go
		sandboxExecMain(args[1:])
		return
	}
	cfgPath := os.Getenv("MATRILINE_CLIENT_CONFIG")
	explicit := cfgPath != ""
	if cfgPath == "" {
		cfgPath = "client.conf"
	}
	if len(args) >= 2 && (args[0] == "-c" || args[0] == "--config") {
		cfgPath, args, explicit = args[1], args[2:], true
	}
	if !explicit && len(args) > 0 && needsClient(args[0]) {
		cfgPath = chooseClient(cfgPath)
	}
	if cf, err := conf.Load(cfgPath); err == nil {
		i18n.Choose(cf.String("general.language", ""))
	} else {
		i18n.Choose("")
	}
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, i18n.T(usage))
		os.Exit(2) // no command: a usage error
	}
	if args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		if len(args) > 1 {
			if t := commandUsage(args[1]); t != "" {
				fmt.Print(t)
				return
			}
			fmt.Fprintf(os.Stderr, "no command %q ('matriline-client help' lists them)\n", args[1])
			os.Exit(2)
		}
		pager.Print(i18n.T(usage))
		return
	}
	cfgPath, _ = filepath.Abs(cfgPath)
	var err error
	switch args[0] {
	case "version":
		fmt.Printf("%s (commit %s)%s\n", agentVersion, commit, build.Label)
	case "init":
		dir, cred := ".", ""
		for i := 1; i < len(args); i++ {
			if args[i] == "--credential" && i+1 < len(args) {
				cred = args[i+1]
				i++
			} else if args[i] == "--language" && i+1 < len(args) {
				initLanguage = args[i+1]
				i++
			} else if strings.HasPrefix(args[i], "--language=") {
				initLanguage = strings.TrimPrefix(args[i], "--language=")
			} else if args[i] == "--settings" && i+1 < len(args) {
				initSettings = args[i+1]
				i++
			} else {
				dir = args[i]
			}
		}
		if cred != "" {
			err = cmdInitWith(dir, cred)
		} else if initSettings != "" {
			err = errors.New("--settings goes with --credential")
		} else {
			err = cmdInit(dir)
		}
	case "run":
		err = cmdRun(cfgPath)
	case "join":
		err = cmdJoin(cfgPath, args[1:])
	case "status":
		err = cmdStatus(cfgPath)
	case "pause":
		err = cmdPause(cfgPath, args[1:])
	case "resume":
		err = cmdResume(cfgPath)
	case "enable":
		err = cmdEnable(cfgPath)
	case "console":
		err = cmdConsole(cfgPath)
	case "service":
		err = cmdService(cfgPath, args[1:])
	case "doctor":
		err = cmdDoctor(cfgPath)
	case "fingerprint":
		dir := ""
		if len(args) > 1 {
			dir = args[1]
		}
		err = cmdFingerprint(cfgPath, dir)
	default:
		err = fmt.Errorf("unknown command %q", args[0])
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func cmdInit(dir string) error {
	// INSTALL.md writes /path/to/project/directory: a copied command must not create it
	// (on Windows it would, as C:\path\to\...)
	if strings.Contains(filepath.ToSlash(dir), "path/to/") {
		return fmt.Errorf("%s is the example from the instructions: replace it with your own (empty) folder", dir)
	}
	// a working directory should hold only Matriline's files: easier to back up, move
	// and clean, and nothing else gets mixed with inputs, results or state
	if es, err := os.ReadDir(dir); err == nil && len(es) > 0 {
		fmt.Fprintln(os.Stderr, i18n.Tf("note: %s is not empty (%d entries); an empty directory is recommended", dir, len(es)))
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	p := filepath.Join(dir, "client.conf")
	if _, err := os.Stat(p); err == nil {
		return fmt.Errorf("%s already exists", p)
	}
	text, err := initConfigText()
	if err != nil {
		return err
	}
	if dirs := orcafind.Dirs(); len(dirs) > 0 {
		text, _ = setConfValue(text, "orca", "paths", strings.Join(dirs, ", "))
		fmt.Println(i18n.Tf("ORCA found: %s", strings.Join(dirs, ", ")))
	} else {
		fmt.Println(i18n.T("WARNING: no ORCA installation found; install ORCA and set orca.paths in client.conf"))
	}
	if err := os.WriteFile(p, []byte(text), 0o600); err != nil {
		return err
	}
	projects.Add("client", p) // found from any folder from now on (user)
	fmt.Println(i18n.Tf("created %s\nPut the credential file from the server admin next to it as credential.conf, then run: %s -c %s run", p, selfPath(), p))
	return nil
}

// cmdEnable undoes a disable (state/DISABLED). The server must have enabled the client
// too, or the next connection disables it again; a wiped client needs a new credential.
func cmdEnable(cfgPath string) error {
	cfg, _, err := loadConfig(cfgPath)
	if err != nil {
		return err
	}
	disabled := filepath.Join(cfg.StateDir, "DISABLED")
	if _, err := os.Stat(disabled); err != nil {
		fmt.Println(i18n.T("the client is not disabled"))
		return nil
	}
	_, kerr := os.Stat(filepath.Join(cfg.StateDir, "client.key"))
	if _, cerr := os.Stat(cfg.Credential); kerr != nil || cerr != nil {
		return fmt.Errorf("the key and credential of this client were deleted: ask the server admin for a new credential (matriline-client init --credential <file>)")
	}
	if err := os.Remove(disabled); err != nil {
		return err
	}
	fmt.Println(i18n.T("enabled: it connects at the next 'run' or service start (the server admin must have run 'clients enable' too)"))
	return nil
}

func cmdRun(cfgPath string) error {
	// a disabled client (end date, or switched off by the admin, which also deletes its
	// credential) exits with status 0 before anything else: an error here made a service
	// manager with Restart=on-failure restart it in a loop
	if cfg, _, err := loadConfig(cfgPath); err == nil {
		disabled := filepath.Join(cfg.StateDir, "DISABLED")
		if b, err := os.ReadFile(disabled); err == nil {
			fmt.Fprintf(os.Stderr, "matriline-client is disabled: %s\ndelete %s to enable it again (after getting a new credential if needed)\n",
				strings.TrimSpace(string(b)), disabled)
			return nil
		}
	}
	a, err := newAgent(cfgPath)
	if err != nil {
		return err
	}
	// one client per state directory: two instances ran at once in the lab
	// an old instance still ending (service restarted at once, update) gets 20 s
	lk, err := lock.AcquireWait(a.cfg.StateDir, "client.lock", 20*time.Second)
	if err != nil {
		return err
	}
	defer lk.Close()
	if exe, err := selfExe(); err == nil {
		release.CleanDownloads(exe) // left by an update that was interrupted
	}
	// checked again under the lock (lock.Acquire does not wait)
	disabled := filepath.Join(a.cfg.StateDir, "DISABLED")
	if b, err := os.ReadFile(disabled); err == nil {
		a.log.Warnf("client is disabled (%s); delete %s to enable it again", string(b), disabled)
		return nil
	}
	if pager.Interactive() {
		fmt.Println(i18n.T("Running in this terminal: Ctrl+C stops it (closing the window too). To keep it running without a terminal: 'service install'."))
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	conhost.Watch(func() {
		a.log.Warnf("the console host ended (scheduled task stopped?): stopping")
		cancel()
	})
	a.log.Infof("%s starting, client id %s", agentVersion, a.key.ID())
	err = a.run(ctx)
	var rs *restartErr
	if errors.As(err, &rs) {
		lk.Close()
		a.log.Infof("restarting into %s", rs.exe)
		err = release.Restart(rs.exe) // returns only on error
		return fmt.Errorf("restart after the update: %v (start the client again)", err)
	}
	if ctx.Err() != nil && err == nil {
		a.log.Infof("stopped (signal); unfinished jobs continue from where they were at the next start")
	}
	return err
}

func cmdJoin(cfgPath string, args []string) error {
	fs := flag.NewFlagSet("join", flag.ContinueOnError)
	server := fs.String("server", "", "server address host:port")
	skey := fs.String("server-key", "", "server public key (base64)")
	token := fs.String("token", "", "one-time join token")
	name := fs.String("name", "", "name shown to the admin")
	relay := fs.String("relay", "", "relay address host:port (relay mode)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *server == "" || *skey == "" || *token == "" {
		return fmt.Errorf("--server, --server-key and --token are required")
	}
	pub, err := ident.DecodeKey(*skey)
	if err != nil || len(pub) != 32 {
		return fmt.Errorf("bad --server-key")
	}
	cfg, _, err := loadConfig(cfgPath)
	if err != nil {
		return err
	}
	// a second join would silently replace the first credential (and the name the admin
	// is about to approve; a macOS checklist)
	if _, err := os.Stat(cfg.Credential); err == nil {
		return errors.New(i18n.Tf("%s already exists: this client has joined a server; to join again, move it away first", cfg.Credential))
	}
	if err := os.MkdirAll(cfg.StateDir, 0o700); err != nil {
		return err
	}
	k, _, err := ident.LoadOrCreate(filepath.Join(cfg.StateDir, "client.key"))
	if err != nil {
		return err
	}
	cred := &ident.Credential{Name: *name, ServerPub: pub, ServerAddress: *server, RelayAddress: *relay, JoinToken: *token}
	if err := ident.WriteCredential(cfg.Credential, cred); err != nil {
		return err
	}
	fmt.Printf("client key %s created; credential written to %s\nStart the client; the admin must approve it with: matriline-server clients approve %s\n", k.ID(), cfg.Credential, k.ID())
	return nil
}

func cmdStatus(cfgPath string) error {
	cfg, _, err := loadConfig(cfgPath)
	if err != nil {
		return err
	}
	fmt.Println(connectionText(cfg))
	if p, on := readPause(cfg); on {
		fmt.Println(p.shown() + " " + i18n.T("('resume' ends it)"))
	}
	entries, _ := os.ReadDir(cfg.ScratchDir)
	n := 0
	for _, e := range entries {
		j, err := loadJob(filepath.Join(cfg.ScratchDir, e.Name()))
		if err != nil {
			continue
		}
		n++
		age := ""
		if !j.Started.IsZero() {
			age = time.Since(j.Started).Round(time.Second).String()
		}
		fmt.Printf("%-40s %-9s %-8s %s %s\n", j.Task.TaskID, j.State, age, j.Failure, j.FailText)
	}
	if n == 0 {
		fmt.Println(i18n.T("no local jobs"))
	}
	return nil
}

func cmdDoctor(cfgPath string) error {
	if _, err := os.Stat(cfgPath); errors.Is(err, fs.ErrNotExist) {
		// before init (INSTALL section 2 sends people here to find their ORCA): say what
		// is found, then how to go on
		dirs := orcafind.Dirs()
		if len(dirs) == 0 {
			fmt.Println(i18n.T("ORCA: none found in the usual folders (INSTALL.md, section 2)"))
		}
		for _, d := range dirs {
			fmt.Println(i18n.Tf("ORCA found: %s", d))
		}
	}
	a, err := openAgent(cfgPath, false)
	if err != nil {
		return err
	}
	if b, err := os.ReadFile(filepath.Join(a.cfg.StateDir, "DISABLED")); err == nil {
		return errors.New(i18n.Tf("this client is disabled by the server admin: %s ('matriline-client enable' after the admin's 'clients enable')", strings.TrimSpace(string(b))))
	}
	if caches, _ := filepath.Glob(filepath.Join(a.cfg.StateDir, "fpcache-*")); len(caches) == 0 {
		fmt.Println(i18n.T("checking ORCA and the sandbox (the first time, fingerprinting ORCA's files takes a minute or two)..."))
	} else {
		fmt.Println(i18n.T("checking ORCA and the sandbox..."))
	}
	if err := a.prepare(); err != nil {
		return err
	}
	addr := a.cred.ServerAddress
	if a.cred.RelayAddress != "" {
		addr = a.cred.RelayAddress
	}
	if addr == "" {
		fmt.Println(i18n.Tf("credential: none yet (%s): the server checks are skipped; ORCA and the sandbox are checked below", a.cfg.Credential))
	} else if c, err := net.DialTimeout("tcp", addr, 10*time.Second); err != nil {
		fmt.Println(i18n.Tf("network:   NOT OK: %s", unreachableHint(a.cred.ServerAddress, a.cred.RelayAddress, err)))
	} else {
		c.Close()
		fmt.Println(i18n.Tf("network:   OK, %s answers", addr))
	}
	srv := a.cred.ServerAddress + " (" + a.serverID + ")"
	if addr == "" {
		srv = "-"
	}
	id := "-"
	if a.key != nil {
		id = a.key.ID()
	}
	fmt.Printf("client id: %s\nserver:    %s\nslots:     %d x %d MB\nsandbox:   %s\n", id, srv, a.slots, a.memPerSlotMB, a.sandboxDesc)
	for _, in := range a.installs {
		fmt.Printf("ORCA:      %s version %s build %s tree %s\n", in.dir, in.version, in.git, in.fp.TreeHash[:16])
	}
	ok, why := a.accepting()
	fmt.Printf("accepting: %v %s\n", ok, why)
	return nil
}

// cmdFingerprint prints the fingerprint of an ORCA installation (the first configured one
// by default): every file's SHA-256 and the tree hash. The server admin approves it with
// orca.accepted_fingerprints, so the server can check every program a result says it ran
// even for builds it cannot run itself or that print no GIT hash.
func cmdFingerprint(cfgPath, dir string) error {
	cfg, _, err := loadConfig(cfgPath)
	if err != nil && dir == "" {
		return err
	}
	if dir == "" {
		if len(cfg.OrcaPaths) == 0 {
			return errors.New("no ORCA installation configured ([orca] paths); give its directory")
		}
		dir = cfg.OrcaPaths[0]
	}
	fp, err := orca.FingerprintTree(dir, "", true)
	if err != nil {
		return err
	}
	shown := *fp
	shown.Root = filepath.Base(fp.Root) // the full path would tell the admin the user name and home
	out, err := json.MarshalIndent(&shown, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(out))
	fmt.Fprintf(os.Stderr, "%s: %d files, tree %s\nGive this JSON to the server admin (orca.accepted_fingerprints).\n", dir, len(fp.Files), fp.TreeHash)
	return nil
}

// needsClient: commands that work on a client folder (not init, help, version...).
func needsClient(cmd string) bool {
	switch cmd {
	case "init", "help", "-h", "--help", "version":
		return false
	}
	return true
}

// chooseClient finds the client folder when the command was not given one and the current
// folder has none (user): the only one on this computer, or the one the person picks.
func chooseClient(def string) string {
	i18n.Choose("")
	var in io.Reader
	if pager.Interactive() {
		in = os.Stdin
	}
	p, err := projects.Resolve("client", def, in, os.Stderr, projects.Texts{
		Which:   i18n.T("Which client folder? (its number and Enter)"),
		Several: i18n.T("several client folders on this computer: run the command inside one, or choose it with -c <folder>/client.conf:"),
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	return p
}
