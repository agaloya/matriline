package main

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/agaloya/matriline/common/conf"
	"github.com/agaloya/matriline/common/ident"
	"github.com/agaloya/matriline/common/ledger"
	"github.com/agaloya/matriline/common/manifest"
	"github.com/agaloya/matriline/common/wire"
	"github.com/agaloya/matriline/common/xfer"
)

// The admin interface is a local socket (state/admin.sock, mode 0600) speaking one JSON
// request and one JSON response per connection. The CLI and the future web front end both
// use it, so every action is implemented exactly once, here.

type adminReq struct {
	Cmd  string   `json:"cmd"`
	Args []string `json:"args"`
}

type adminResp struct {
	Out string `json:"out"`
	Err string `json:"err,omitempty"`
}

// adminSocket returns the socket path. Unix socket paths are limited to ~108 bytes, so for
// deep working directories a short name derived from the root is used in the temp dir.
func adminSocket(root string) string {
	p := filepath.Join(root, dState, "admin.sock")
	if len(p) < 100 {
		return p
	}
	h := sha256.Sum256([]byte(root))
	return filepath.Join(os.TempDir(), "matriline-"+hex.EncodeToString(h[:8])+".sock")
}

func (s *Server) startAdmin() (net.Listener, error) {
	p := adminSocket(s.conf().Root)
	os.Remove(p)
	ln, err := net.Listen("unix", p)
	if err != nil {
		return nil, fmt.Errorf("admin socket: %v", err)
	}
	os.Chmod(p, 0o600)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go s.serveAdmin(c)
		}
	}()
	return ln, nil
}

func (s *Server) serveAdmin(c net.Conn) {
	defer c.Close()
	c.SetDeadline(time.Now().Add(10 * time.Minute))
	var req adminReq
	if err := json.NewDecoder(bufio.NewReader(c)).Decode(&req); err != nil {
		return
	}
	out, err := s.admin(req.Cmd, req.Args)
	s.adminEvent(req.Cmd, req.Args, out, err)
	resp := adminResp{Out: out}
	if err != nil {
		resp.Err = err.Error()
	}
	json.NewEncoder(c).Encode(resp)
}

// admin dispatches one admin command.
func (s *Server) admin(cmd string, a []string) (string, error) {
	if rest, js := withoutJSON(a); js {
		return s.cmdJSON(cmd, rest)
	}
	need := func(n int, usage string) error {
		if len(a) < n {
			return fmt.Errorf("usage: %s", usage)
		}
		return nil
	}
	switch cmd {
	case "status":
		return s.cmdStatus(a)
	case "add":
		if err := need(1, "add <file-or-dir> [input/sub-dir]"); err != nil {
			return "", err
		}
		dst := dInput
		if len(a) > 1 {
			dst = a[1]
		}
		return s.cmdAdd(a[0], dst)
	case "priority":
		if err := need(2, "priority <path> <number>"); err != nil {
			return "", err
		}
		n, err := strconv.Atoi(a[1])
		if err != nil {
			return "", fmt.Errorf("priority must be an integer")
		}
		return s.cmdPriority(a[0], n, false)
	case "next":
		if err := need(1, "next <path>"); err != nil {
			return "", err
		}
		return s.cmdPriority(a[0], 0, true)
	case "pause":
		if err := need(1, "pause <path>|all"); err != nil {
			return "", err
		}
		return s.cmdMove(a[0], dInput, dPaused, "pause")
	case "resume":
		if err := need(1, "resume <path>|all"); err != nil {
			return "", err
		}
		return s.cmdMove(a[0], dPaused, dInput, "resume")
	case "cancel":
		if err := need(1, "cancel <path>"); err != nil {
			return "", err
		}
		top, _ := splitSpool(a[0])
		from := dInput
		if top == dPaused {
			from = dPaused
		}
		return s.cmdMove(a[0], from, dCancelled, "cancel")
	case "uncancel":
		if err := need(1, "uncancel <path>"); err != nil {
			return "", err
		}
		return s.cmdMove(a[0], dCancelled, dInput, "uncancel")
	case "retry":
		if err := need(1, "retry <errors/path>"); err != nil {
			return "", err
		}
		return s.cmdRequeue(a[0], dErrors)
	case "review":
		return s.cmdReview()
	case "accept":
		if err := need(1, "accept <weird/path>"); err != nil {
			return "", err
		}
		return s.cmdAccept(a[0])
	case "reject":
		if err := need(1, "reject <weird/path>"); err != nil {
			return "", err
		}
		return s.cmdRequeue(a[0], dWeird)
	case "check":
		return s.cmdCheck()
	case "update":
		return s.cmdUpdate(a)
	case "live":
		return s.cmdLive(a)
	case "molxyz": // the web page's 3D view (not a user command)
		return s.molXYZ(a)
	case "test-alert": // the same as "alerts test", easy to remember
		return s.cmdAlertsTest()
	case "events":
		n := 50
		if len(a) > 0 {
			v, err := strconv.Atoi(a[0])
			if err != nil || v <= 0 {
				return "", fmt.Errorf("usage: events [number of lines]")
			}
			n = v
		}
		return s.cmdEvents(n)
	case "redo":
		if err := need(1, "redo <output/path|completed/path>"); err != nil {
			return "", err
		}
		return s.cmdRedo(a[0])
	case "clean":
		dry := len(a) > 0 && a[0] == "--dry-run"
		if dry {
			a = a[1:]
		}
		if err := need(1, "clean [--dry-run] <glob> [spool path]"); err != nil {
			return "", err
		}
		where := ""
		if len(a) > 1 {
			where = a[1]
		}
		return s.cmdClean(a[0], where, dry)
	case "reverify":
		if err := need(1, "reverify <output/path>"); err != nil {
			return "", err
		}
		return s.cmdReverify(a[0])
	case "edited":
		if err := need(1, "edited <path>"); err != nil {
			return "", err
		}
		return s.cmdEdited(a[0])
	case "taskinfo":
		if err := need(1, "taskinfo <path>"); err != nil {
			return "", err
		}
		return s.cmdTaskInfo(a[0])
	case "history":
		if err := need(1, "history <path>"); err != nil {
			return "", err
		}
		return s.cmdHistory(a[0])
	case "stats":
		return s.cmdStats()
	case "model":
		return s.cmdModel()
	case "doctor":
		return s.cmdDoctor()
	case "verify":
		return verifySpool(s.conf().Root, s.key)
	case "backup":
		if err := need(1, "backup <destination-dir>"); err != nil {
			return "", err
		}
		return s.cmdBackup(a[0])
	case "config":
		return s.cmdConfig(a)
	case "keys":
		return s.cmdKeys(a)
	case "clients":
		return s.cmdClients(a)
	case "alerts":
		if len(a) == 0 || a[0] != "test" {
			return "", fmt.Errorf("usage: alerts test")
		}
		return s.cmdAlertsTest()
	case "bans":
		return s.cmdBans(a)
	case "block", "unblock":
		if err := need(1, cmd+" <ip>"); err != nil {
			return "", err
		}
		return s.cmdBlock(a[0], cmd == "block")
	case "version":
		return versionText(), nil
	case "rescan":
		s.rescan()
		return "rescanned input/", nil
	case "stop":
		select {
		case s.stopReq <- struct{}{}:
		default:
		}
		return "server stopping", nil
	case "restart":
		if _, _, err := loadConfig(s.cfgPath); err != nil { // never restart into a broken file
			return "", fmt.Errorf("server.conf is not valid, so the server would not start again: %v", err)
		}
		exe, err := selfExe()
		if err != nil {
			return "", err
		}
		s.event("admin", "restart (settings that need it are applied)")
		s.setRestart(exe)
		return "server restarting: back in a few seconds (clients reconnect by themselves)", nil
	}
	return "", fmt.Errorf("unknown command %q (see 'matriline-server help')", cmd)
}

func wireNotice(kind, msg string) (wire.MsgType, wire.Notice) {
	return wire.TNotice, wire.Notice{Kind: kind, Message: msg}
}

// readPreset loads client settings for 'keys issue --preset' (a file in client.conf format,
// e.g. "[resources]\ncores = 4"). The client checks the names when it applies them. The
// admin cannot preset where the client keeps its credential and state, nor switch off its
// sandbox: those stay the computer owner's decision (D27).
func readPreset(path string) (map[string]string, error) {
	cf, err := conf.Load(path)
	if err != nil {
		return nil, fmt.Errorf("--preset: %v", err)
	}
	p := map[string]string{}
	for _, k := range cf.Keys() {
		sec, _, _ := strings.Cut(k, ".")
		if sec == "client" || sec == "security" {
			return nil, fmt.Errorf("--preset: %s cannot be preset (the client's own files and sandbox are its owner's decision)", k)
		}
		p[k] = cf.String(k, "")
	}
	if len(p) == 0 {
		return nil, fmt.Errorf("--preset: %s holds no settings", path)
	}
	return p, nil
}

// ---------------------------------------------------------------------------------------

func (s *Server) cmdStatus(a []string) (string, error) {
	cfg := s.conf()
	var b strings.Builder
	counts := s.spoolCounts("")
	s.store.mu.Lock()
	running, lost := 0, 0
	for _, at := range s.store.Attempts {
		if at.Lost {
			lost++
		} else {
			running++
		}
	}
	queued, internal := 0, 0 // input files vs verification sub-tasks and canaries
	for _, t := range s.store.Tasks {
		if t.Internal {
			internal++
		} else {
			queued++
		}
	}
	s.store.mu.Unlock()
	fmt.Fprintf(&b, "server %s  spool %s\n", s.key.ID(), cfg.Root)
	fmt.Fprintf(&b, "tasks:   %d in input/ (%d attempts running, %d lost); %d verification sub-tasks\n", queued, running, lost, internal)
	fmt.Fprintf(&b, "results: %d output, %d weird, %d errors, %d outdated (not counted), %d of other ORCA versions\n", counts[dOutput], counts[dWeird], counts[dErrors], counts[dOutdated], counts["other-versions"])
	fmt.Fprintf(&b, "inputs:  %d completed, %d paused, %d cancelled\n", counts[dCompleted], counts[dPaused], counts[dCancelled])
	fmt.Fprintln(&b, chartLine("", queued, running, counts))
	s.sessMu.Lock()
	ids := make([]string, 0, len(s.sessions))
	for id := range s.sessions {
		ids = append(ids, id)
	}
	s.sessMu.Unlock()
	sort.Strings(ids)
	fmt.Fprintf(&b, "clients connected: %d\n", len(ids))
	tw := tabwriter.NewWriter(&b, 2, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "  NAME\tID\tSLOTS\tRUNNING\tACCEPTING\tPOWER\tLAST HEARTBEAT")
	for _, id := range ids {
		ss := s.session(id)
		if ss == nil {
			continue
		}
		n := 0
		s.store.mu.Lock()
		for _, at := range s.store.Attempts {
			if at.ClientID == id && !at.Lost {
				n++
			}
		}
		s.store.mu.Unlock()
		hb := "-"
		acc := ss.offer.Accepting
		if !ss.lastBeat.IsZero() {
			hb = time.Since(ss.lastBeat).Round(time.Second).String() + " ago"
			acc = ss.hbAccept
		}
		fmt.Fprintf(tw, "  %s\t%s\t%d\t%d\t%v\t%s\t%s\n", ss.name, short(id), ss.offer.Slots, n, acc, ss.powerText(), hb)
	}
	tw.Flush()
	for _, id := range ids {
		if ss := s.session(id); ss != nil && ss.outdated != "" {
			fmt.Fprintf(&b, "  %s has ORCA %s, not this campaign's %s (orca.other_versions = %s)\n", ss.name, ss.outdated, cfg.OrcaVersion, cfg.OtherVersions)
		}
	}
	if len(a) == 0 { // the overview: one chart per top-level sub-directory too (a few)
		subs := s.topSubdirs()
		if len(subs) > 1 {
			b.WriteString("\n")
			for i, sub := range subs {
				if i == maxSubCharts {
					fmt.Fprintf(&b, "(%d more sub-directories: 'status <sub-directory>' shows one)\n", len(subs)-maxSubCharts)
					break
				}
				fmt.Fprintln(&b, s.subChart(sub))
			}
		}
	}
	if len(a) > 0 {
		_, sub := splitSpool(a[0])
		if top, _ := splitSpool(a[0]); top == "" || !isSpoolDir(top) {
			sub = path.Clean(filepath.ToSlash(a[0]))
		}
		fmt.Fprintf(&b, "\n%s\n", s.subChart(sub))
		out, err := s.cmdTaskInfo(a[0])
		if err == nil {
			b.WriteString("\n" + out)
		}
	}
	return b.String(), nil
}

func fileExists(p string) bool { _, err := os.Stat(p); return err == nil }

// cmdAdd copies files or a directory tree from anywhere into input/ (or a sub-directory).
func (s *Server) cmdAdd(src, dst string) (string, error) {
	cfg := s.conf()
	top, _ := splitSpool(dst)
	if top != dInput {
		return "", fmt.Errorf("destination must be input/ or a sub-directory of it")
	}
	dstAbs, err := s.store.spoolPath(dst)
	if err != nil {
		return "", err
	}
	st, err := os.Stat(src)
	if err != nil {
		return "", err
	}
	n := 0
	var placed []string
	copyOne := func(p, rel string) error {
		if !hasExt(p, cfg.InputExt) && !companion(p) {
			return nil
		}
		t := filepath.Join(dstAbs, rel)
		if fileExists(t) {
			return fmt.Errorf("%s already exists in the spool", t)
		}
		if l := linkOnTheWay(filepath.Join(cfg.Root, dInput), t); l != "" {
			return fmt.Errorf("%s is a symbolic link: inputs are not added through it", l)
		}
		// copy to a hidden name first, then rename: the scanner never sees half files
		tmp := filepath.Join(filepath.Dir(t), "."+filepath.Base(t)+".adding")
		if err := copyFile(p, tmp); err != nil {
			return err
		}
		if hasExt(p, cfg.InputExt) {
			n++
			if r, err := filepath.Rel(filepath.Join(cfg.Root, dInput), t); err == nil {
				placed = append(placed, filepath.ToSlash(r))
			}
		}
		return os.Rename(tmp, t)
	}
	if st.IsDir() {
		err = filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
			if err != nil || !d.Type().IsRegular() {
				return err
			}
			rel, _ := filepath.Rel(src, p)
			return copyOne(p, filepath.Join(filepath.Base(src), rel))
		})
	} else {
		err = copyOne(src, filepath.Base(src))
	}
	s.store.markPlaced(placed)
	s.rescan()
	if err != nil {
		return fmt.Sprintf("added %d input(s) before the error", n), err
	}
	return fmt.Sprintf("added %d input file(s) to %s", n, dst), nil
}

// linkOnTheWay returns the first existing symbolic link between base and target (target
// itself included), "" when there is none: 'add' must not write outside the spool through
// one (code review).
func linkOnTheWay(base, target string) string {
	rel, err := filepath.Rel(base, target)
	if err != nil || !filepath.IsLocal(rel) {
		return target
	}
	p := base
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		p = filepath.Join(p, part)
		st, err := os.Lstat(p)
		if err != nil {
			return "" // the rest does not exist yet: 'add' creates it
		}
		if st.Mode()&os.ModeSymlink != 0 {
			return p
		}
	}
	return ""
}

// companion files travel with inputs (coordinates, orbitals, ...).
func companion(p string) bool {
	switch strings.ToLower(filepath.Ext(p)) {
	case ".xyz", ".gbw", ".hess", ".pc", ".bas", ".cmp", ".pdb", ".gzmt", ".opt", ".txt":
		return true
	}
	return false
}

// tasksUnder returns queued task ids at or below a spool path (input/...).
func (s *Server) tasksUnder(p string) []string {
	top, rest := splitSpool(p)
	if top != dInput {
		return nil
	}
	var out []string
	for id := range s.store.Tasks {
		if rest == "" || id == rest || strings.HasPrefix(id, strings.TrimSuffix(rest, "/")+"/") {
			out = append(out, id)
		}
	}
	return out
}

func (s *Server) cmdPriority(p string, n int, next bool) (string, error) {
	s.store.mu.Lock()
	defer s.store.mu.Unlock()
	ids := s.tasksUnder(p)
	if len(ids) == 0 {
		return "", fmt.Errorf("no queued task under %s", p)
	}
	if next {
		for _, t := range s.store.Tasks {
			if t.Priority >= n {
				n = t.Priority + 1
			}
		}
	}
	for _, id := range ids {
		s.store.Tasks[id].Priority = n
	}
	s.store.touch()
	return fmt.Sprintf("priority %d set on %d task(s)", n, len(ids)), nil
}

// cmdMove moves inputs between input/, paused/ and cancelled/ keeping the structure.
func (s *Server) cmdMove(p, from, to, verb string) (string, error) {
	s.moveMu.Lock()
	defer s.moveMu.Unlock()
	cfg := s.conf()
	rel := ""
	if p != "all" {
		top, rest := splitSpool(p)
		if top != from {
			return "", fmt.Errorf("%s works on %s/ paths (got %s)", verb, from, p)
		}
		rel = rest
	}
	srcRoot := filepath.Join(cfg.Root, from)
	src := filepath.Join(srcRoot, filepath.FromSlash(rel))
	st, err := os.Stat(src)
	if err != nil {
		return "", err
	}
	var files []string
	if st.IsDir() {
		filepath.WalkDir(src, func(q string, d fs.DirEntry, err error) error {
			if err == nil && d.Type().IsRegular() {
				files = append(files, q)
			}
			return nil
		})
	} else {
		files = []string{src}
	}
	moved := 0
	var ids, placed []string
	for _, f := range files {
		r, _ := filepath.Rel(srcRoot, f)
		dst := filepath.Join(cfg.Root, to, r)
		if fileExists(dst) {
			return fmt.Sprintf("%d file(s) moved", moved), fmt.Errorf("%s already exists", dst)
		}
		if err := moveFile(f, dst); err != nil {
			return fmt.Sprintf("%d file(s) moved", moved), err
		}
		moved++
		if from == dInput && hasExt(f, cfg.InputExt) {
			ids = append(ids, filepath.ToSlash(r))
		}
		if to == dInput && hasExt(f, cfg.InputExt) {
			placed = append(placed, filepath.ToSlash(r))
		}
	}
	if from == dInput {
		for _, id := range ids {
			past := map[string]string{"cancel": "cancelled", "pause": "paused"}[verb]
			if past == "" {
				past = verb + "d"
			}
			s.cancelAttemptsOf(id, "task "+past+" by the admin")
		}
		s.store.forget(ids) // moved by Matriline, not "left input/ outside Matriline"
	}
	if st.IsDir() && !s.keepsEmpty(from) {
		pruneEmpty(srcRoot)
	}
	s.pruneSpool()
	s.ledger.Append(ledger.Entry{Kind: "move", Note: fmt.Sprintf("%s %s: %s -> %s (%d files)", verb, p, from, to, moved)})
	s.store.markPlaced(placed)
	s.rescan()
	return fmt.Sprintf("%s: %d file(s) moved from %s/ to %s/", verb, moved, from, to), nil
}

// cmdRequeue sends a task from errors/ or weird/ back to input/.
func (s *Server) cmdRequeue(p, from string) (string, error) {
	s.moveMu.Lock()
	defer s.moveMu.Unlock()
	cfg := s.conf()
	top, rest := splitSpool(p)
	if top != from || rest == "" {
		return "", fmt.Errorf("expected a %s/<task> path", from)
	}
	dir := filepath.Join(cfg.Root, filepath.FromSlash(p))
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}
	var inp string
	for _, e := range entries {
		if hasExt(e.Name(), cfg.InputExt) {
			inp = e.Name()
		}
	}
	if inp == "" {
		return "", fmt.Errorf("no input file inside %s", p)
	}
	taskID := path.Join(path.Dir(rest), inp)
	dst := filepath.Join(cfg.Root, dInput, filepath.FromSlash(taskID))
	if fileExists(dst) {
		return "", fmt.Errorf("%s is already queued", taskID)
	}
	// a job stopped by tasks.max_job_time (or any failed one) with its orbitals: the next
	// host starts from them instead of from the beginning (ORCA's AutoStart)
	// only with tasks.reuse_checkpoints and from a host trusted like keepCheckpoint's; its
	// id is recorded so that an ORCA error from those orbitals is not blamed on the input
	resumed, donor := "", ""
	if from == dErrors && cfg.ReuseCheckpoints {
		gbw := filepath.Join(dir, strings.TrimSuffix(inp, path.Ext(inp))+".gbw")
		if raw, err := os.ReadFile(filepath.Join(dir, manifestFile)); err == nil {
			if m, err := manifest.Peek(raw); err == nil {
				donor = m.ClientID
			}
		}
		if info, err := os.Stat(gbw); err == nil && info.Size() > 0 && donor != "" && s.checkpointTrusted(donor) {
			cp := s.checkpointPath(taskID)
			if os.MkdirAll(filepath.Dir(cp), 0o700) == nil && copyFile(gbw, cp) == nil {
				resumed = " (it continues from the orbitals of the stopped run)"
			}
		}
	}
	// a rejected weird result: its producer must not compute the task again
	producer := ""
	if from == dWeird {
		if raw, err := os.ReadFile(filepath.Join(dir, manifestFile)); err == nil {
			if m, err := manifest.Peek(raw); err == nil {
				producer = m.ClientID
			}
		}
	}
	// set when the scan makes the task, before any host can take it (code review:
	// set after the rescan, a free host could get it first, from scratch or as the producer)
	s.store.presetTask(taskID, func(t *TaskState) {
		if resumed != "" {
			t.CheckpointFrom = donor
		}
		if producer != "" {
			t.avoid(producer, avoidForever)
		}
	})
	if err := copyFile(filepath.Join(dir, inp), dst); err != nil {
		s.store.presetTask(taskID, nil)
		return "", err
	}
	// keep the rejected result for the record, out of the way; a result computed again on
	// the admin's request goes to outdated/<same path>[.N] (user, 2026-10-05)
	arch := filepath.Join(cfg.Root, dState, "rejected", time.Now().Format("20060102-150405")+"-"+sanitize(rest))
	if from == dOutput {
		arch = filepath.Join(cfg.Root, filepath.FromSlash(freeName(cfg.Root, path.Join(dOutdated, rest))))
		if err := os.MkdirAll(filepath.Dir(arch), 0o750); err != nil {
			return "", err
		}
	}
	if err := moveFile(dir, arch); err != nil {
		return "", err
	}
	if from == dOutput { // an accepted task computed again: its completed/ input goes too
		done := filepath.Join(cfg.Root, dCompleted, filepath.FromSlash(taskID))
		if fileExists(done) {
			moveFile(done, filepath.Join(arch, "completed-"+inp))
		}
	}
	s.pruneSpool()
	s.ledger.Append(ledger.Entry{Kind: "requeue", Task: taskID, Client: producer, Note: p + " -> input/ (old result kept in " + arch + ")"})
	s.store.markPlaced([]string{taskID})
	s.rescan()
	note := ""
	if producer != "" {
		note = " (the client that produced the rejected result will not get it)"
	}
	return fmt.Sprintf("%s requeued as input/%s%s%s", p, taskID, note, resumed), nil
}

// cmdRedo computes an accepted task again (user, 2026-10-05): given its result in output/
// or its input in completed/, the result and the completed/ input move to outdated/ and the
// input goes back to input/.
func (s *Server) cmdRedo(p string) (string, error) {
	top, rest := splitSpool(p)
	switch {
	case top == dCompleted && rest != "":
		out := resultDir(dOutput, rest)
		if !fileExists(filepath.Join(s.conf().Root, filepath.FromSlash(out))) {
			return "", fmt.Errorf("no result %s for %s", out, p)
		}
		p = out
	case top != dOutput || rest == "":
		return "", fmt.Errorf("expected an output/<task> or completed/<input> path")
	}
	return s.cmdRequeue(p, dOutput)
}

func (s *Server) cmdReview() (string, error) {
	cfg := s.conf()
	var b strings.Builder
	filepath.WalkDir(filepath.Join(cfg.Root, dWeird), func(p string, d fs.DirEntry, err error) error {
		if err == nil && d.Name() == verdictFile {
			rel, _ := filepath.Rel(cfg.Root, filepath.Dir(p))
			v, _ := os.ReadFile(p)
			fmt.Fprintf(&b, "== %s\n%s\n", filepath.ToSlash(rel), strings.TrimSpace(string(v)))
		}
		return nil
	})
	if b.Len() == 0 {
		return "weird/ is empty", nil
	}
	return b.String() + "\nUse 'accept <path>' to move a result to output/ or 'reject <path>' to recompute it.", nil
}

func (s *Server) cmdAccept(p string) (string, error) {
	s.moveMu.Lock()
	defer s.moveMu.Unlock()
	top, rest := splitSpool(p)
	if top != dWeird || rest == "" {
		return "", fmt.Errorf("expected a weird/<task> path")
	}
	if !fileExists(filepath.Join(s.conf().Root, filepath.FromSlash(p))) {
		return "", fmt.Errorf("%s does not exist", p)
	}
	if s.checked(p) {
		return "", fmt.Errorf("a check is still running on %s: try again when it is done", p)
	}
	_, dst, err := s.acceptWeirdLocked(p, "manually accepted")
	if err != nil {
		return "", err
	}
	s.pruneSpool()
	return fmt.Sprintf("accepted: %s -> %s", p, dst), nil
}

// ledgerDir records the current hashes of every file in a result directory.
func (s *Server) ledgerDir(kind, rel, note string) {
	files, from := dirHashes(s.conf().Root, rel)
	s.ledger.Append(ledger.Entry{Kind: kind, Note: note, Files: files, FromManifest: from})
}

func (s *Server) cmdReverify(p string) (string, error) {
	cfg := s.conf()
	dir, err := s.store.spoolPath(p)
	if err != nil {
		return "", err
	}
	signed, err := os.ReadFile(filepath.Join(dir, manifestFile))
	if err != nil {
		return "", fmt.Errorf("no %s in %s", manifestFile, p)
	}
	peek, err := manifest.Peek(signed)
	if err != nil {
		return "", err
	}
	pub := s.clientPub(peek.ClientID)
	if pub == nil {
		return "", fmt.Errorf("unknown client %s", peek.ClientID)
	}
	m, err := manifest.Verify(signed, pub)
	if err != nil {
		return "FAIL: " + err.Error(), nil
	}
	var bad []string
	names := readNames(dir)
	deleted := s.deletedFiles()
	var cleaned []string
	for _, f := range m.Files {
		local := f.Name
		if r, ok := names[f.Name]; ok {
			local = r
		}
		n, h, err := xfer.HashFile(filepath.Join(dir, filepath.FromSlash(local)))
		if err != nil && deleted[path.Join(filepath.ToSlash(p), local)] {
			cleaned = append(cleaned, f.Name)
			continue
		}
		if err != nil || n != f.Size || h != f.SHA256 {
			bad = append(bad, f.Name)
		}
	}
	v := &Verdict{OK: true, Manifest: m}
	// consistency is checked against the anonymous names the client used
	tmp, err := os.MkdirTemp(filepath.Join(cfg.Root, dState), "reverify-")
	if err == nil {
		defer os.RemoveAll(tmp)
		for a, r := range names {
			if strings.HasSuffix(a, ".out") {
				os.Link(filepath.Join(dir, r), filepath.Join(tmp, a))
			}
		}
		taskID := peek.TaskID
		if t, ok := s.findTaskByResultDir(p); ok {
			taskID = t
		}
		s.checkConsistency(v, m, tmp, taskID) // always run on demand, whatever the default policy
	}
	var b strings.Builder
	fmt.Fprintf(&b, "manifest signature: OK (client %s)\n", m.ClientID)
	if len(bad) > 0 {
		fmt.Fprintf(&b, "files modified since signing: %s\n", strings.Join(bad, ", "))
	} else {
		fmt.Fprintf(&b, "files: %d, all match the signed hashes\n", len(m.Files)-len(cleaned))
	}
	if len(cleaned) > 0 {
		fmt.Fprintf(&b, "deleted by the admin with 'clean' (recorded in the ledger): %s\n", strings.Join(cleaned, ", "))
	}
	if v.OK {
		b.WriteString("output consistency: OK\n")
	} else {
		fmt.Fprintf(&b, "output consistency: FAIL: %s\n", strings.Join(v.Reasons, "; "))
	}
	return b.String(), nil
}

func (s *Server) cmdTaskInfo(p string) (string, error) {
	s.store.mu.Lock()
	defer s.store.mu.Unlock()
	ids := s.tasksUnder(p)
	if len(ids) == 0 {
		return "", fmt.Errorf("no queued task under %s", p)
	}
	sort.Slice(ids, func(i, j int) bool { return naturalLess(ids[i], ids[j]) })
	var b strings.Builder
	for _, id := range ids {
		t := s.store.Tasks[id]
		fmt.Fprintf(&b, "%s  priority=%d attempts=%d", id, t.Priority, t.Attempts)
		for _, a := range s.runningOf(id) {
			fmt.Fprintf(&b, " running-on=%s(%s, since %s)", short(a.ClientID), a.Kind, time.Since(a.Started).Round(time.Second))
		}
		if t.LastError != "" {
			fmt.Fprintf(&b, " last-error=%q", t.LastError)
		}
		b.WriteString("\n")
	}
	return b.String(), nil
}

func (s *Server) cmdEdited(p string) (string, error) {
	top, rest := splitSpool(p)
	abs, err := s.store.spoolPath(p)
	if err != nil {
		return "", err
	}
	_, h, err := xfer.HashFile(abs)
	if err != nil {
		return "", err
	}
	if top == dInput {
		s.store.mu.Lock()
		if t := s.store.Tasks[rest]; t != nil {
			t.InputSHA, t.Attempts, t.AvoidOn, t.OrcaFailOn = h, 0, nil, nil
			s.store.touch()
		}
		s.store.mu.Unlock()
		s.cancelAttemptsOf(rest, "input edited by the admin")
	}
	s.ledger.Append(ledger.Entry{Kind: "edit", Task: rest, Files: []ledger.FileHash{{Path: path.Clean(filepath.ToSlash(p)), SHA256: h}}})
	return "edit recorded in the ledger (sha256 " + short(h) + ")", nil
}

func (s *Server) cmdHistory(p string) (string, error) {
	_, rest := splitSpool(p)
	rest = strings.TrimSuffix(rest, "/")
	es, err := ledger.Read(filepath.Join(s.conf().Root, dState, "ledger.log"), s.key.Pub)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	for _, e := range es {
		if e.Task == "" || !(e.Task == rest || strings.HasPrefix(e.Task, rest) || strings.TrimSuffix(e.Task, path.Ext(e.Task)) == rest) {
			continue
		}
		fmt.Fprintf(&b, "%s  %-9s %s", time.Unix(e.Time, 0).Format("2006-01-02 15:04:05"), e.Kind, e.Task)
		if e.Client != "" {
			name := short(e.Client)
			if c := s.reg.get(e.Client); c != nil {
				name = c.Name
			}
			fmt.Fprintf(&b, " client=%s", name)
		}
		if e.Verdict != "" {
			fmt.Fprintf(&b, " verdict=%q", e.Verdict)
		}
		if e.Note != "" {
			fmt.Fprintf(&b, " note=%q", e.Note)
		}
		b.WriteString("\n")
	}
	if b.Len() == 0 {
		return "no ledger entries for " + p, nil
	}
	return b.String(), nil
}

func (s *Server) cmdStats() (string, error) {
	es, err := ledger.Read(filepath.Join(s.conf().Root, dState, "ledger.log"), s.key.Pub)
	if err != nil {
		return "", err
	}
	type cs struct{ acc, weird, errs int }
	per := map[string]*cs{}
	var first, last int64
	total := 0
	for _, e := range es {
		if e.Kind == "verify" && e.Verdict == "passed" && strings.HasPrefix(e.Note, "tie-break ") {
			// rescued from weird/ by a tie-break: accepted after all
			if c := per[e.Client]; c != nil && c.weird > 0 {
				c.weird--
				c.acc++
				total++
			}
			continue
		}
		if e.Kind == "verify" && e.Verdict == "failed" {
			// accepted on arrival, later moved to weird/ by an active check
			if c := per[e.Client]; c != nil && c.acc > 0 {
				c.acc--
				c.weird++
				total--
			}
			continue
		}
		if e.Kind != "result" {
			continue
		}
		if first == 0 {
			first = e.Time
		}
		last = e.Time
		c := per[e.Client]
		if c == nil {
			c = &cs{}
			per[e.Client] = c
		}
		switch {
		case e.Verdict == "accepted":
			c.acc++
			total++
		case strings.HasPrefix(e.Verdict, "weird"):
			c.weird++
		default:
			c.errs++
		}
	}
	var b strings.Builder
	s.store.mu.Lock()
	queued, internal := len(s.store.Tasks), 0
	for _, t := range s.store.Tasks {
		if t.Internal {
			internal++
		}
	}
	s.store.mu.Unlock()
	// "still queued" stays the last field (scripts read it) and keeps counting everything
	fmt.Fprintf(&b, "accepted results: %d, inputs queued: %d, verification sub-tasks: %d, still queued: %d\n", total, queued-internal, internal, queued)
	queued -= internal // the ETA below is about the inputs
	if last > first && total > 1 {
		rate := float64(total) / (float64(last-first) / 3600)
		fmt.Fprintf(&b, "throughput: %.1f results/hour", rate)
		if queued > 0 && rate > 0 {
			fmt.Fprintf(&b, ", ETA for the queue: %s", time.Duration(float64(queued)/rate*float64(time.Hour)).Round(time.Minute))
		}
		b.WriteString("\n")
	}
	if queued > 0 {
		if eta, n, ok := s.learnedETA(); ok {
			fmt.Fprintf(&b, "learned model: ETA %s with the clients connected now (%d task(s) predicted)\n", eta, n)
		}
	}
	tw := tabwriter.NewWriter(&b, 2, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "CLIENT\tACCEPTED\tWEIRD\tERRORS\tSTATUS")
	for id, c := range per {
		name, st := short(id), "?"
		if r := s.reg.get(id); r != nil {
			name, st = r.Name, r.Status
		}
		fmt.Fprintf(tw, "%s\t%d\t%d\t%d\t%s\n", name, c.acc, c.weird, c.errs, st)
	}
	tw.Flush()
	return b.String(), nil
}

func (s *Server) cmdDoctor() (string, error) {
	cfg := s.conf()
	var b strings.Builder
	ok := func(cond bool, msg string) {
		mark := "OK  "
		if !cond {
			mark = "FAIL"
		}
		fmt.Fprintf(&b, "[%s] %s\n", mark, msg)
	}
	_, warns, err := loadConfig(s.cfgPath)
	ok(err == nil, "configuration file is valid")
	for _, w := range warns {
		fmt.Fprintf(&b, "       %s\n", w)
	}
	s.refMu.RLock()
	ok(len(s.refTrees) > 0, fmt.Sprintf("reference ORCA fingerprints loaded (%d)", len(s.refTrees)))
	if len(cfg.OrcaPaths) > 0 {
		ok(len(s.refGit) > 0, fmt.Sprintf("reference ORCA builds known: %v", keys(s.refGit)))
	}
	s.refMu.RUnlock()
	for _, d := range append(spoolDirs, dState) {
		f, err := os.CreateTemp(filepath.Join(cfg.Root, d), ".doctor-*")
		if err == nil {
			f.Close()
			os.Remove(f.Name())
		}
		ok(err == nil, "spool directory writable: "+d+"/")
	}
	_, err = ledger.Read(filepath.Join(cfg.Root, dState, "ledger.log"), s.key.Pub)
	ok(err == nil, "integrity ledger chain verifies")
	if cfg.Connection == "direct" {
		for _, l := range cfg.Listen {
			c, err := net.DialTimeout("tcp", localize(l), 2*time.Second)
			if err == nil {
				c.Close()
			}
			_, port, _ := net.SplitHostPort(localize(l))
			ok(err == nil, "listening on "+l+" (local check; reachability from outside depends on your router/firewall)"+firewallHint(port))
		}
	}
	if cfg.StorageLimit > 0 {
		used := dirSize(cfg.Root)
		ok(used < cfg.StorageLimit, fmt.Sprintf("storage: %d of %d bytes used", used, cfg.StorageLimit))
	}
	ok(!cfg.MailEnabled || (cfg.MailServer != "" && len(cfg.MailTo) > 0), "e-mail alerts configured (or disabled)")
	return b.String(), nil
}

func localize(addr string) string {
	if strings.HasPrefix(addr, ":") {
		return "127.0.0.1" + addr
	}
	return addr
}

func keys(m map[string]bool) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func (s *Server) cmdBackup(dest string) (string, error) {
	s.moveMu.Lock()
	defer s.moveMu.Unlock()
	if err := s.store.flush(); err != nil {
		return "", err
	}
	cfg := s.conf()
	if !filepath.IsAbs(dest) {
		return "", fmt.Errorf("backup destination must be an absolute path")
	}
	target := filepath.Join(dest, "matriline-backup-"+time.Now().Format("20060102-150405"))
	for _, d := range backupDirs {
		src := filepath.Join(cfg.Root, d)
		if _, err := os.Stat(src); os.IsNotExist(err) {
			continue
		}
		if err := copyTree(src, filepath.Join(target, d)); err != nil {
			return "", err
		}
	}
	os.Remove(filepath.Join(target, dState, "admin.sock"))
	copyFile(s.cfgPath, filepath.Join(target, "server.conf"))
	s.ledger.Append(ledger.Entry{Kind: "backup", Note: target})
	return "backup written to " + target + "\nrestore with: matriline-server restore " + target + " (server stopped)", nil
}

func (s *Server) cmdConfig(a []string) (string, error) {
	if len(a) == 0 {
		return "", fmt.Errorf("usage: config show|validate|reload")
	}
	switch a[0] {
	case "show":
		b, err := os.ReadFile(s.cfgPath)
		return string(b), err
	case "validate":
		_, warns, err := loadConfig(s.cfgPath)
		out := strings.Join(warns, "\n")
		if err != nil {
			return out, err
		}
		return out + "\nconfiguration is valid", nil
	case "reload":
		cfg, warns, err := loadConfig(s.cfgPath)
		out := strings.Join(warns, "\n")
		if err != nil {
			return out, fmt.Errorf("NOT applied: %v", err)
		}
		old := s.conf()
		cfg.Root = old.Root // [restart] options keep their running value
		cfg.Listen = old.Listen
		var notes []string
		if cfg.Mode != old.Mode {
			notes = append(notes, "security mode change applies to NEW connections only")
		}
		if strings.Join(cfg.OrcaPaths, ",") != strings.Join(old.OrcaPaths, ",") || cfg.OrcaVersion != old.OrcaVersion {
			defer s.loadReference()
		}
		s.cfgMu.Lock()
		s.cfg = cfg
		s.cfgMu.Unlock()
		if cfg.Language != old.Language {
			setLanguage(s.cfgPath)
		}
		if w := s.noteOrcaVersion(); w != "" {
			notes = append(notes, w)
		}
		s.ledger.Append(ledger.Entry{Kind: "config", Note: "configuration reloaded"})
		s.log.Infof("configuration reloaded")
		return strings.TrimSpace(out+"\n"+strings.Join(notes, "\n")) + "\nconfiguration applied", nil
	}
	return "", fmt.Errorf("unknown config action %q", a[0])
}

func (s *Server) cmdKeys(a []string) (string, error) {
	if len(a) == 0 {
		return "", fmt.Errorf("usage: keys issue <name> <output-file> | keys token [hours] | keys revoke <client>")
	}
	cfg := s.conf()
	switch a[0] {
	case "issue":
		// --uses N: one ticket for N computers (an admin enrolling a few machines); each
		// one still gets its own device key and name (name-1, name-2, ...)
		uses := 1
		var preset map[string]string
		var rest []string
		for i := 0; i < len(a); i++ {
			if a[i] == "--preset" && i+1 < len(a) {
				p, err := readPreset(a[i+1])
				if err != nil {
					return "", err
				}
				preset = p
				i++
				continue
			}
			if a[i] == "--uses" && i+1 < len(a) {
				n, err := strconv.Atoi(a[i+1])
				if err != nil || n < 1 || n > 1000 {
					return "", fmt.Errorf("--uses needs a number from 1 to 1000")
				}
				uses = n
				i++
				continue
			}
			rest = append(rest, a[i])
		}
		a = rest
		if len(a) < 3 {
			return "", fmt.Errorf("usage: keys issue <name> <output-file> [address] [--uses N]")
		}
		name := sanitize(a[1])
		if name == "" || len(name) > 64 {
			return "", fmt.Errorf("give the client a name (letters, digits, - and _, up to 64)")
		}
		for _, c := range s.reg.list() {
			if c.Name == name && c.Status != stRevoked {
				return "", fmt.Errorf("a client named %q already exists (revoke it first, or choose another name)", name)
			}
		}
		if s.reg.pendingEnroll(name) {
			return "", fmt.Errorf("a credential for %q was already issued and not used yet", name)
		}
		addr := cfg.Advertise
		if len(a) > 3 {
			addr = a[3]
		}
		if addr == "" && cfg.Connection == "relay" {
			addr = "relay" // clients dial the relay (relay_address below); this is only shown
		}
		if addr == "" {
			return "", fmt.Errorf("set network.advertise in the config (or pass the address as 4th argument)")
		}
		// The file holds no private key, only a one-time enrollment token: on its first
		// connection the client creates its own device key (which never leaves that
		// computer) and the server binds it to this name and burns the token. A copy of
		// the file made after that is useless.
		tok, err := s.reg.newEnrollToken(name, cfg.EnrollTTL, uses)
		if err != nil {
			return "", err
		}
		cred := &ident.Credential{Name: name, ServerPub: s.key.Pub, ServerAddress: addr, JoinToken: tok, Preset: preset}
		if cfg.Connection == "relay" {
			cred.RelayAddress = cfg.RelayAddr
		}
		if err := ident.WriteCredential(a[2], cred); err != nil {
			return "", err
		}
		s.ledger.Append(ledger.Entry{Kind: "key_issued", Note: fmt.Sprintf("%s: enrollment ticket for %d computer(s), valid %s", name, uses, cfg.EnrollTTL)})
		if uses > 1 {
			return fmt.Sprintf("issued a credential for %d computers named %s-1 ... %s-%d -> %s (valid %s)\n"+
				"Each computer that uses it creates its own key; after %d enrollments the file stops working.\n"+
				"Revoke one computer with 'keys revoke %s-2', the unused rest with 'keys revoke %s'",
				uses, name, name, uses, a[2], humanDuration(cfg.EnrollTTL), uses, name, name), nil
		}
		return fmt.Sprintf("issued a one-time credential for %s -> %s (valid %s, single use)\n"+
			"Give this file to the user. On its first connection the client creates its own key and the file stops working.\n"+
			"Check with 'matriline-server clients' that %s enrolled from the expected address; revoke with: matriline-server keys revoke %s",
			name, a[2], humanDuration(cfg.EnrollTTL), name, name), nil
	case "token":
		h := 24
		if len(a) > 1 {
			h, _ = strconv.Atoi(a[1])
		}
		tok, err := s.reg.newToken(time.Duration(h) * time.Hour)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("one-time join token (valid %dh): %s\nserver key: %s\nThe client runs: matriline-client join --server <address> --server-key %s --token %s",
			h, tok, ident.EncodeKey(s.key.Pub), ident.EncodeKey(s.key.Pub), tok), nil
	case "revoke":
		if len(a) < 2 {
			return "", fmt.Errorf("usage: keys revoke <client>")
		}
		cancelled := s.reg.cancelEnroll(a[1])
		out, err := s.setClientStatus(a[1], stRevoked, "revoked by admin")
		if err != nil && cancelled > 0 {
			return fmt.Sprintf("cancelled the unused credential issued for %s", a[1]), nil
		}
		if cancelled > 0 {
			out += fmt.Sprintf("\nalso cancelled %d unused credential(s) issued for %s", cancelled, a[1])
		}
		return out, err
	}
	return "", fmt.Errorf("unknown keys action %q", a[0])
}

func (s *Server) setClientStatus(q, status, note string) (string, error) {
	c, err := s.reg.resolve(q)
	if err != nil {
		return "", err
	}
	id := c.ID
	if err := s.reg.update(id, func(c *ClientRec) { c.Status = status; c.Note = note }); err != nil {
		return "", err
	}
	s.ledger.Append(ledger.Entry{Kind: "client_" + status, Client: id, Note: note})
	if ss := s.session(id); ss != nil {
		switch status {
		case stRevoked:
			ss.conn.Send(wireNotice("revoked", "access revoked by the server admin"))
			ss.conn.Close()
		case stDisabled:
			ss.conn.Send(wireNotice("disabled", disabledText(note)))
			ss.conn.Close()
		case stQuarantined:
			ss.conn.Send(wireNotice("warn", "client quarantined: every result will be verified"))
		case stDraining:
			ss.conn.Send(wireNotice("drain", "finish running tasks; no new tasks will be sent"))
		case stActive: // release/approve: the client must leave "draining" (it stayed there forever)
			ss.conn.Send(wireNotice("active", "client active: tasks will be sent again"))
		}
	}
	if status == stQuarantined {
		go s.recheckClient(id, 1) // same re-verification (and automatic release) as automatic quarantine
	}
	// a revoked or disabled client's running attempts are dropped and their tasks go back
	// to the queue. Only ITS attempts: cancelling every attempt of those tasks also stopped
	// other clients working on them (found by code review). A quarantined client keeps
	// computing (every result it returns is checked), as after an automatic quarantine.
	if status == stRevoked || status == stDisabled {
		s.store.mu.Lock()
		for aid, a := range s.store.Attempts {
			if a.ClientID == id {
				delete(s.store.Attempts, aid)
				s.store.touch()
			}
		}
		s.store.mu.Unlock()
	}
	return fmt.Sprintf("client %s (%s) is now %s", c.Name, id, status), nil
}

func (s *Server) cmdClients(a []string) (string, error) {
	if len(a) == 0 || a[0] == "list" {
		var b strings.Builder
		tw := tabwriter.NewWriter(&b, 2, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "NAME\tHOST\tID\tSTATUS\tRESULTS\tFAILED CHECKS\tORCA ERRORS\tLAST SEEN\tADDRESS")
		for _, c := range s.reg.list() {
			seen := "never"
			if !c.LastSeen.IsZero() {
				seen = c.LastSeen.Format("2006-01-02 15:04")
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%d\t%d\t%d\t%s\t%s\n", c.Name, nz(c.Hostname, "-"), c.ID, c.Status, c.Results, c.Failures, c.OrcaFails, seen, c.LastAddr)
		}
		tw.Flush()
		return b.String(), nil
	}
	if len(a) < 2 {
		return "", fmt.Errorf("usage: clients list | approve|quarantine|release|drain|enable <client> | disable <client> [--wipe] [reason] | rename <client> <new name>")
	}
	switch a[0] {
	case "approve", "release":
		return s.setClientStatus(a[1], stActive, a[0]+"d by admin")
	case "quarantine":
		return s.setClientStatus(a[1], stQuarantined, "quarantined by admin")
	case "drain":
		return s.setClientStatus(a[1], stDraining, "draining")
	case "rename":
		// a clearer name for a computer (e.g. "lab-pc-3" -> "office-left"): shown in status,
		// clients, live and the ledger from now on; its key and history stay
		if len(a) < 3 {
			return "", fmt.Errorf("usage: clients rename <client> <new name>")
		}
		c, err := s.reg.resolve(a[1])
		if err != nil {
			return "", err
		}
		name := sanitize(strings.Join(a[2:], "-"))
		if name == "" || len(name) > 64 {
			return "", fmt.Errorf("a name of letters, digits, - and _ (up to 64)")
		}
		if s.reg.pendingEnroll(name) {
			return "", fmt.Errorf("a credential for %q was issued and not used yet", name)
		}
		old, err := s.reg.rename(c.ID, name)
		if err != nil {
			return "", err
		}
		if ss := s.session(c.ID); ss != nil {
			ss.name = name
		}
		s.ledger.Append(ledger.Entry{Kind: "client_renamed", Client: c.ID, Note: old + " -> " + name})
		return fmt.Sprintf("client %s (%s) is now called %s", old, c.ID, name), nil
	case "disable":
		// the project ended, or a volunteer wants to leave and does not know how: the
		// client stops, returns its tasks, deletes its job data and exits until it is
		// enabled again (works offline too: it is told at its next connection). --wipe
		// also deletes its key and credential (it can only come back with a new one).
		var words []string
		wipe := false
		for _, w := range a[2:] {
			if w == "--wipe" {
				wipe = true
			} else {
				words = append(words, w)
			}
		}
		reason := strings.TrimSpace(strings.Join(words, " "))
		if reason == "" {
			reason = "disabled by admin"
		}
		if wipe {
			reason += wipeMark
		}
		c, err := s.reg.resolve(a[1])
		if err != nil {
			return "", err
		}
		out, err := s.setClientStatus(c.ID, stDisabled, reason)
		if err == nil && s.session(c.ID) == nil {
			out += "\nthe client is not connected: it stops at its next connection"
		}
		return out, err
	case "enable":
		c, err := s.reg.resolve(a[1])
		if err != nil {
			return "", err
		}
		if c.Status != stDisabled {
			return "", fmt.Errorf("client %s is %s, not disabled", c.Name, c.Status)
		}
		if strings.Contains(c.Note, wipeMark) {
			return "", fmt.Errorf("client %s was disabled with --wipe: its key is gone; issue a new credential (keys issue)", c.Name)
		}
		out, err := s.setClientStatus(a[1], stActive, "enabled by admin")
		if err == nil {
			out += "\non that computer, run: matriline-client enable (then it starts as usual)"
		}
		return out, err
	}
	return "", fmt.Errorf("unknown clients action %q", a[0])
}

func (s *Server) cmdBlock(ip string, block bool) (string, error) {
	if net.ParseIP(ip) == nil {
		return "", fmt.Errorf("%q is not an IP address", ip)
	}
	s.cfgMu.Lock()
	cfg := *s.cfg
	var list []string
	for _, b := range cfg.BlockedIPs {
		if b != ip {
			list = append(list, b)
		}
	}
	if block {
		list = append(list, ip)
	}
	cfg.BlockedIPs = list
	s.cfg = &cfg
	s.cfgMu.Unlock()
	if err := setConfigValue(s.cfgPath, "network", "blocked_ips", strings.Join(list, ", ")); err != nil {
		return "", err
	}
	if block {
		return ip + " blocked (saved in the config file)", nil
	}
	return ip + " unblocked", nil
}

// setConfigValue rewrites one "key = value" line inside a section, preserving comments.
func setConfigValue(path, section, key, value string) error {
	if _, err := conf.Quote(value); err != nil {
		return err
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return ident.WriteFileAtomic(path, []byte(setConfigText(string(b), section, key, value)), 0o600)
}

// setConfigText sets key in [section] of a configuration text.
// quoteValue writes a value the way conf.Load reads it back (as it is when it cannot be).
func quoteValue(v string) string {
	if q, err := conf.Quote(v); err == nil {
		return q
	}
	return v
}

func setConfigText(text, section, key, value string) string {
	lines := strings.Split(text, "\n")
	// a section may appear more than once (server.conf has BASIC and ADVANCED parts): the
	// key is replaced wherever it is; only a key found in no part is added, at the end of
	// the section's first part. Inserting it in the first part while it was set in the
	// second made the file invalid ("duplicate key") and stopped every command (block)
	cur, firstEnd, inSection := "", -1, false
	for i, ln := range lines {
		t := strings.TrimSpace(ln)
		if strings.HasPrefix(t, "[") && strings.HasSuffix(t, "]") {
			if inSection && firstEnd < 0 {
				firstEnd = i
			}
			cur = strings.Trim(t, "[]")
			inSection = cur == section
			continue
		}
		if cur == section && !strings.HasPrefix(t, "#") {
			if eq := strings.IndexByte(t, '='); eq > 0 && strings.TrimSpace(t[:eq]) == key {
				lines[i] = key + " = " + quoteValue(value)
				return strings.Join(lines, "\n")
			}
		}
	}
	switch {
	case firstEnd >= 0:
		lines = append(lines[:firstEnd], append([]string{key + " = " + value}, lines[firstEnd:]...)...)
	case inSection: // the section is the last one in the file
		lines = append(lines, key+" = "+value)
	default:
		lines = append(lines, "["+section+"]", key+" = "+value)
	}
	return strings.Join(lines, "\n")
}

// ---------------------------------------------------------------------------------------
// Offline spool verification against the ledger

// verifySpool checks the ledger chain and that every result file recorded in it still has
// the recorded hash (modifications made on the server after receipt are reported).
func verifySpool(root string, key *ident.Key) (string, error) {
	es, err := ledger.Read(filepath.Join(root, dState, "ledger.log"), key.Pub)
	var b strings.Builder
	if err != nil {
		fmt.Fprintf(&b, "LEDGER CHAIN BROKEN: %v\n", err)
	} else {
		fmt.Fprintf(&b, "ledger: %d entries, chain and signatures OK\n", len(es))
	}
	expect := map[string]string{}
	vouching := map[string]bool{} // manifests that slim entries rely on (D61)
	for _, e := range es {
		switch e.Kind {
		case "result", "accept", "verify_failed", "verify_inconclusive", "outdated":
			if i := strings.Index(e.Note, "from "); e.Kind == "accept" && i >= 0 {
				dropPrefix(expect, e.Note[i+5:])
			}
			if i := strings.Index(e.Note, "moved from "); e.Kind != "accept" && e.Kind != "result" && i >= 0 {
				from, _, _ := strings.Cut(e.Note[i+11:], ": ")
				dropPrefix(expect, from)
			}
			for _, f := range entryFiles(root, e) {
				expect[f.Path] = f.SHA256
			}
			if e.FromManifest != "" {
				vouching[e.FromManifest] = true
			}
		case "requeue":
			if i := strings.Index(e.Note, " -> "); i > 0 {
				dropPrefix(expect, e.Note[:i])
			}
		case "delete": // removed on purpose by "clean"; the ledger keeps their hashes
			for _, f := range e.Files {
				delete(expect, f.Path)
			}
		}
	}
	modified, missing, ok := 0, 0, 0
	for p, h := range expect {
		top, _ := splitSpool(p)
		if top != dOutput && top != dWeird && top != dErrors {
			continue
		}
		abs := filepath.Join(root, filepath.FromSlash(p))
		_, got, err := xfer.HashFile(abs)
		switch {
		case err != nil && vouching[p]:
			// the other files of its directory can no longer be checked
			modified++
			fmt.Fprintf(&b, "MANIFEST MISSING: %s (its directory's files cannot be checked)\n", p)
		case err != nil:
			// moved by accept/reject: only report if no newer location exists
			missing++
			fmt.Fprintf(&b, "missing:  %s\n", p)
		case got != h:
			modified++
			fmt.Fprintf(&b, "MODIFIED: %s\n", p)
		default:
			ok++
		}
	}
	// files present in output/ but never recorded
	unrec := 0
	filepath.WalkDir(filepath.Join(root, dOutput), func(p string, d fs.DirEntry, err error) error {
		if err == nil && d.Type().IsRegular() {
			rel, _ := filepath.Rel(root, p)
			if _, known := expect[filepath.ToSlash(rel)]; !known {
				unrec++
				fmt.Fprintf(&b, "UNRECORDED: %s\n", filepath.ToSlash(rel))
			}
		}
		return nil
	})
	fmt.Fprintf(&b, "files: %d intact, %d modified, %d missing (moved or deleted), %d unrecorded\n", ok, modified, missing, unrec)
	if err == nil && modified == 0 && unrec == 0 {
		b.WriteString("RESULT: spool consistent with the ledger\n")
	} else {
		b.WriteString("RESULT: INTEGRITY PROBLEMS FOUND\n")
	}
	return b.String(), nil
}

// dropPrefix forgets expected files below a directory that was moved away on purpose.
func dropPrefix(m map[string]string, dir string) {
	dir = strings.TrimSuffix(path.Clean(dir), "/") + "/"
	for k := range m {
		if strings.HasPrefix(k, dir) {
			delete(m, k)
		}
	}
}

// findTaskByResultDir maps output/<dir>/<stem> back to the task id using the ledger.
func (s *Server) findTaskByResultDir(rel string) (string, bool) {
	es, err := ledger.Read(filepath.Join(s.conf().Root, dState, "ledger.log"), s.key.Pub)
	if err != nil {
		return "", false
	}
	rel = path.Clean(filepath.ToSlash(rel))
	for i := len(es) - 1; i >= 0; i-- {
		for _, f := range es[i].Files {
			if path.Dir(f.Path) == rel && es[i].Task != "" {
				return es[i].Task, true
			}
		}
	}
	return "", false
}

// cmdClean deletes files matching a glob (base name) under one spool path, or under every
// result directory, and records each one (path + hash) in the ledger, so "verify" and
// "reverify" know they were removed by the admin and not tampered with. input/, state/ and
// Matriline's own bookkeeping files (matriline.*) are never touched.
func (s *Server) cmdClean(glob, where string, dry bool) (string, error) {
	if _, err := path.Match(glob, "x"); err != nil {
		return "", fmt.Errorf("bad pattern %q: %v", glob, err)
	}
	s.moveMu.Lock()
	defer s.moveMu.Unlock()
	root := s.conf().Root
	allowed := map[string]bool{dOutput: true, dWeird: true, dErrors: true, dCompleted: true, dCancelled: true}
	var tops []string
	if where == "" {
		tops = []string{dOutput, dWeird, dErrors, dCompleted, dCancelled}
	} else {
		top, _ := splitSpool(where)
		if !allowed[top] {
			return "", fmt.Errorf("clean works on output/ weird/ errors/ completed/ cancelled/ (got %s)", where)
		}
		tops = []string{path.Clean(filepath.ToSlash(where))}
	}
	var files []ledger.FileHash
	var bytes int64
	for _, t := range tops {
		filepath.WalkDir(filepath.Join(root, filepath.FromSlash(t)), func(p string, d fs.DirEntry, err error) error {
			if err != nil || !d.Type().IsRegular() || strings.HasPrefix(d.Name(), "matriline.") {
				return nil
			}
			if ok, _ := path.Match(glob, d.Name()); !ok {
				return nil
			}
			rel, _ := filepath.Rel(root, p)
			n, h, err := xfer.HashFile(p)
			if err != nil {
				return nil
			}
			files = append(files, ledger.FileHash{Path: filepath.ToSlash(rel), SHA256: h})
			bytes += n
			return nil
		})
	}
	if len(files) == 0 {
		return fmt.Sprintf("nothing matches %q", glob), nil
	}
	if dry {
		return fmt.Sprintf("would delete %d file(s), %.1f MB (run again without --dry-run)", len(files), float64(bytes)/1e6), nil
	}
	if _, err := s.ledger.Append(ledger.Entry{Kind: "delete", Note: fmt.Sprintf("clean %s %s", glob, where), Files: files}); err != nil {
		return "", fmt.Errorf("ledger: %v (nothing deleted)", err)
	}
	n := 0
	for _, f := range files {
		if os.Remove(filepath.Join(root, filepath.FromSlash(f.Path))) == nil {
			n++
		}
	}
	s.log.Infof("clean %s %s: deleted %d file(s), %.1f MB", glob, where, n, float64(bytes)/1e6)
	return fmt.Sprintf("deleted %d file(s), %.1f MB freed (recorded in the ledger)", n, float64(bytes)/1e6), nil
}

// deletedFiles lists every spool path removed with "clean".
func (s *Server) deletedFiles() map[string]bool {
	out := map[string]bool{}
	es, err := ledger.Read(filepath.Join(s.conf().Root, dState, "ledger.log"), s.key.Pub)
	if err != nil {
		return out
	}
	for _, e := range es {
		if e.Kind == "delete" {
			for _, f := range e.Files {
				out[f.Path] = true
			}
		}
	}
	return out
}

// spoolCounts counts inputs (files) and results (directories) per spool directory,
// optionally only below sub (a path relative to the spool directories, e.g. "bde_sp3").
func (s *Server) spoolCounts(sub string) map[string]int {
	cfg := s.conf()
	counts := map[string]int{}
	for _, d := range spoolDirs {
		n := 0
		filepath.WalkDir(filepath.Join(cfg.Root, d, filepath.FromSlash(sub)), func(p string, e fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if d == dOutput || d == dWeird || d == dErrors || d == dOutdated {
				if e.IsDir() && (fileExists(filepath.Join(p, manifestFile)) || fileExists(filepath.Join(p, verdictFile))) {
					n++
				}
			} else if e.Type().IsRegular() && hasExt(e.Name(), cfg.InputExt) {
				n++
			}
			return nil
		})
		counts[d] = n
	}
	// results of other ORCA versions (orca.other_versions = separate):
	// other-versions/<version>/<output|weird|errors>/<sub>/<task>/
	if vers, err := os.ReadDir(filepath.Join(cfg.Root, "other-versions")); err == nil {
		for _, v := range vers {
			for _, kind := range []string{dOutput, dWeird, dErrors} {
				filepath.WalkDir(filepath.Join(cfg.Root, "other-versions", v.Name(), kind, filepath.FromSlash(sub)), func(p string, e fs.DirEntry, err error) error {
					if err == nil && e.IsDir() && fileExists(filepath.Join(p, manifestFile)) {
						counts["other-versions"]++
					}
					return nil
				})
			}
		}
	}
	return counts
}

// maxSubCharts: the overview shows at most this many sub-directory charts.
const maxSubCharts = 8

// subChart is the chart line of one sub-directory (in every spool folder).
func (s *Server) subChart(sub string) string {
	q, r, c := s.subCounts(sub)
	return chartLine(sub, q, r, c)
}

// subCounts: the tasks of a sub-directory in input/ (queued, running included) and running,
// and its counts in every spool folder.
func (s *Server) subCounts(sub string) (int, int, map[string]int) {
	c := s.spoolCounts(sub)
	s.store.mu.Lock()
	q, r := 0, 0
	for _, t := range s.store.Tasks {
		if !t.Internal && strings.HasPrefix(t.ID, sub+"/") {
			q++
		}
	}
	for _, at := range s.store.Attempts {
		if !at.Lost && strings.HasPrefix(at.TaskID, sub+"/") {
			r++
		}
	}
	s.store.mu.Unlock()
	return q, r, c
}

// topSubdirs lists the top-level sub-directories of the spool (input/drugs, output/drugs
// ... give "drugs"), sorted naturally; a result folder itself (output/<task>/, for an
// input at the top of input/) is not one.
func (s *Server) topSubdirs() []string {
	root := s.conf().Root
	seen := map[string]bool{}
	for _, d := range []string{dInput, dPaused, dCancelled, dOutput, dWeird, dErrors} {
		es, _ := os.ReadDir(filepath.Join(root, d))
		for _, e := range es {
			p := filepath.Join(root, d, e.Name())
			if !e.IsDir() || strings.HasPrefix(e.Name(), ".") || fileExists(filepath.Join(p, manifestFile)) || fileExists(filepath.Join(p, verdictFile)) {
				continue
			}
			seen[e.Name()] = true
		}
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Slice(out, func(i, j int) bool { return naturalLess(out[i], out[j]) })
	return out
}

// chartLine is a machine-readable summary the command line draws as a coloured bar.
func chartLine(sub string, queued, running int, c map[string]int) string {
	label := "chart"
	if sub != "" {
		label = "chart " + sub
	}
	// outdated/ is left out: those tasks are counted where they are now (user)
	return fmt.Sprintf("%s: queued=%d running=%d output=%d weird=%d errors=%d other-versions=%d paused=%d cancelled=%d",
		label, max(queued-running, 0), running, c[dOutput], c[dWeird], c[dErrors], c["other-versions"], c[dPaused], c[dCancelled])
}

func isSpoolDir(d string) bool {
	for _, x := range spoolDirs {
		if x == d {
			return true
		}
	}
	return false
}

// disabledText is what a disabled client logs before it cleans up and exits.
// wipeMark in a disabled client's note: also delete its key and credential (clients
// disable --wipe). The client recognizes it in the notice text.
const wipeMark = " (credential withdrawn)"

func disabledText(reason string) string {
	wipe := strings.Contains(reason, wipeMark)
	reason = strings.TrimSpace(strings.Replace(reason, wipeMark, "", 1))
	t := "disabled by the server admin"
	if reason != "" && reason != "disabled by admin" {
		t += " (" + reason + ")"
	}
	if wipe {
		return t + wipeMark + ". Running tasks are stopped and returned to the server; the job data, the key and the credential on this computer are deleted. To join again, ask the admin for a new credential."
	}
	return t + ". Running tasks are stopped and returned to the server and the job data on this computer is deleted; the key and the credential stay. To come back, the admin runs 'clients enable', then you run 'matriline-client enable'."
}

// humanDuration prints 48h instead of 48h0m0s.
func humanDuration(d time.Duration) string {
	t := d.String()
	t = strings.TrimSuffix(t, "0s")
	t = strings.TrimSuffix(t, "0m")
	return t
}
