package main

import (
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math/big"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/agaloya/matriline/common/ident"
	"github.com/agaloya/matriline/common/xfer"
)

// Spool directory names (D19, D24).
const (
	dInput     = "input"
	dOutput    = "output"
	dCompleted = "completed"
	dCancelled = "cancelled"
	dPaused    = "paused"
	dWeird     = "weird"
	dErrors    = "errors"
	dOutdated  = "outdated" // results replaced by a recomputation the admin asked for (redo)
	dState     = "state"
)

var spoolDirs = []string{dInput, dOutput, dCompleted, dCancelled, dPaused, dWeird, dErrors, dOutdated}

// backupDirs: everything a backup copies and a restore puts back (the whole working
// directory but the configuration, copied apart): the spool, results of other ORCA
// versions (orca.other_versions = separate; they were missing) and the state.
var backupDirs = append(append([]string{}, spoolDirs...), "other-versions", dState)

// TaskState is the server's knowledge about one queued task (keyed by path under input/).
type TaskState struct {
	ID         string          `json:"id"`
	Priority   int             `json:"priority"`
	Seq        int64           `json:"seq"` // random order key / tie breaker
	Added      time.Time       `json:"added"`
	InputSHA   string          `json:"input_sha"`
	Attempts   int             `json:"attempts"`
	OrcaFailOn map[string]bool `json:"orca_fail_on,omitempty"`
	// QuickFailOn: hosts where an attempt failed as a "machine" failure within minutes
	QuickFailOn map[string]bool `json:"quick_fail_on,omitempty"`
	// CheckpointFrom: the client whose orbitals (kept from its failed attempt) wait for
	// the next host of this task
	CheckpointFrom string               `json:"checkpoint_from,omitempty"`
	AvoidOn        map[string]time.Time `json:"avoid_on,omitempty"` // client -> do not assign before
	LastError      string               `json:"last_error,omitempty"`
	DiskRefused    map[string]bool      `json:"disk_refused,omitempty"` // clients that refused it for scratch disk
	DiskAlerted    bool                 `json:"disk_alerted,omitempty"`
	MemMB          int                  `json:"mem_mb,omitempty"` // memory per core the task needs (learned from an ORCA MaxCore error)
	Procs          int                  `json:"procs,omitempty"`  // MPI processes the input asks for (0/1: one)
	// internal tasks (verification sub-tasks, canaries) never appear in the spool
	Internal bool   `json:"internal,omitempty"`
	CheckID  string `json:"check_id,omitempty"`
	Purpose  string `json:"purpose,omitempty"`
	OnlyFor  string `json:"only_for,omitempty"` // canaries target one client
}

// opaqueID is the task identifier shown to clients: it reveals neither the file name nor
// whether the task is a real job, a verification sub-task or a canary.
// opaqueID is keyed with a secret derived from the server's private key (setOpaqueKey):
// a client can only know the ids of tasks it was given. Before, it was a plain hash of the
// task path, so a client guessing a path ("batch1/phenol.inp") could re-adopt or answer a
// task it was never assigned.
func opaqueID(id string) string {
	m := hmac.New(sha256.New, opaqueKey)
	m.Write([]byte("matriline-task:" + id))
	return "t-" + hex.EncodeToString(m.Sum(nil)[:10])
}

var opaqueKey []byte

func setOpaqueKey(priv ed25519.PrivateKey) {
	h := sha256.Sum256(append([]byte("matriline-opaque-task-id:"), priv.Seed()...))
	opaqueKey = h[:]
}

// legacyOpaqueID is the unkeyed form of servers before 2026-10-04, accepted only for
// attempts this server itself recorded for the same client (results in flight at upgrade).
func legacyOpaqueID(id string) string {
	h := sha256.Sum256([]byte("matriline-task:" + id))
	return "t-" + hex.EncodeToString(h[:10])
}

// anonStem is the input/output base name used by clients for an attempt.
func anonStem(attemptID string) string { return "job" + attemptID[:10] }

// avoid marks a client as unsuitable for this task for a while; caller holds store.mu.
// avoidForever keeps a host off a task for good (in practice: ten years).
const avoidForever = 10 * 365 * 24 * time.Hour

func (t *TaskState) avoid(clientID string, d time.Duration) {
	if t.AvoidOn == nil {
		t.AvoidOn = map[string]time.Time{}
	}
	t.AvoidOn[clientID] = time.Now().Add(d)
}

// avoided reports whether the client must not receive the task now.
func (t *TaskState) avoided(clientID string) bool {
	until, ok := t.AvoidOn[clientID]
	return ok && time.Now().Before(until)
}

// Attempt is one assignment of a task to a client.
type Attempt struct {
	ID       string    `json:"id"`
	TaskID   string    `json:"task_id"`
	ClientID string    `json:"client_id"`
	Kind     string    `json:"kind"` // job, replica, verify, canary
	Started  time.Time `json:"started"`
	LastSeen time.Time `json:"last_seen"`
	Progress int64     `json:"progress"`
	Lost     bool      `json:"lost"`
	// FromCheckpoint: this attempt started from orbitals kept from that client's attempt
	FromCheckpoint string `json:"from_checkpoint,omitempty"`
	// Adopted: re-adopted from a client's report after the server lost track of it;
	// Started is then the re-adoption, not the assignment (no timing check)
	Adopted bool `json:"adopted,omitempty"`
	// Tail: the last output lines the client reported (live view; not saved)
	Tail string `json:"-"`
	// Phase: what the job is doing, for the live view (not saved): "" = starting (the
	// input was sent, no report yet), the client's state ("running", "finished"), or
	// "uploading" while the server receives its result
	Phase string `json:"-"`
}

// Store holds tasks and attempts and persists them in state/tasks.json.
type Store struct {
	mu       sync.Mutex
	root     string
	path     string
	Tasks    map[string]*TaskState `json:"tasks"`
	Attempts map[string]*Attempt   `json:"attempts"`
	Checks   map[string]*Check     `json:"checks,omitempty"`
	Canaries []CanarySource        `json:"canaries,omitempty"`
	dirty    bool
	placed   map[string]bool             // inputs written by the server itself: complete, no settle wait
	presets  map[string]func(*TaskState) // applied to the task when the scan makes it
	order    []string                    // cached queue order
	orderOK  bool
}

func openStore(root string) (*Store, error) {
	s := &Store{root: root, path: filepath.Join(root, dState, "tasks.json"),
		Tasks: map[string]*TaskState{}, Attempts: map[string]*Attempt{}}
	b, err := os.ReadFile(s.path)
	if err == nil {
		if err := json.Unmarshal(b, s); err != nil {
			return nil, fmt.Errorf("%s: %v", s.path, err)
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	if s.Tasks == nil {
		s.Tasks = map[string]*TaskState{}
	}
	if s.Attempts == nil {
		s.Attempts = map[string]*Attempt{}
	}
	return s, nil
}

// flush writes the store if it changed. Called periodically and on shutdown.
func (s *Store) flush() error {
	s.mu.Lock()
	if !s.dirty {
		s.mu.Unlock()
		return nil
	}
	b, err := json.Marshal(s)
	s.dirty = false
	s.mu.Unlock()
	if err != nil {
		return err
	}
	return ident.WriteFileAtomic(s.path, b, 0o600)
}

func (s *Store) touch() { s.dirty = true; s.orderOK = false }

func randomID(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func randomSeq() int64 {
	n, _ := rand.Int(rand.Reader, big.NewInt(1<<62))
	return n.Int64()
}

// ---------------------------------------------------------------------------------------
// Paths

// spoolPath joins a spool-relative path safely; it refuses anything escaping the root.
func (s *Store) spoolPath(rel string) (string, error) {
	rel = filepath.ToSlash(strings.TrimSpace(rel))
	rel = strings.TrimPrefix(rel, "./")
	if rel == "" || rel == "." {
		return s.root, nil
	}
	if !filepath.IsLocal(filepath.FromSlash(rel)) {
		return "", fmt.Errorf("path %q is outside the working directory", rel)
	}
	return filepath.Join(s.root, filepath.FromSlash(rel)), nil
}

// splitSpool returns (top-level dir, rest) of a spool-relative path.
func splitSpool(rel string) (string, string) {
	rel = path.Clean(filepath.ToSlash(rel))
	if i := strings.IndexByte(rel, '/'); i >= 0 {
		return rel[:i], rel[i+1:]
	}
	return rel, ""
}

// resultDir maps a task id "a/b/mol.inp" to "<top>/a/b/mol".
func resultDir(top, taskID string) string {
	stem := strings.TrimSuffix(taskID, path.Ext(taskID))
	return path.Join(top, stem)
}

// ---------------------------------------------------------------------------------------
// Natural ordering: "2list" < "10list", directories and files compared segment by segment.

func naturalLess(a, b string) bool {
	as, bs := strings.Split(a, "/"), strings.Split(b, "/")
	for i := 0; i < len(as) && i < len(bs); i++ {
		if as[i] != bs[i] {
			// a file directly in a directory sorts before deeper entries only by name
			return naturalSegLess(as[i], bs[i])
		}
	}
	return len(as) < len(bs)
}

func naturalSegLess(a, b string) bool {
	for a != "" && b != "" {
		ad, bd := unicode.IsDigit(rune(a[0])), unicode.IsDigit(rune(b[0]))
		if ad && bd {
			ai, bi := 0, 0
			for ai < len(a) && unicode.IsDigit(rune(a[ai])) {
				ai++
			}
			for bi < len(b) && unicode.IsDigit(rune(b[bi])) {
				bi++
			}
			na, nb := strings.TrimLeft(a[:ai], "0"), strings.TrimLeft(b[:bi], "0")
			if len(na) != len(nb) {
				return len(na) < len(nb)
			}
			if na != nb {
				return na < nb
			}
			a, b = a[ai:], b[bi:]
			continue
		}
		ca, cb := unicode.ToLower(rune(a[0])), unicode.ToLower(rune(b[0]))
		if ca != cb {
			return ca < cb
		}
		a, b = a[1:], b[1:]
	}
	return len(a) < len(b)
}

// ---------------------------------------------------------------------------------------
// Scanning input/

// scan synchronizes the task table with input/. New files must be older than settle.
// Returns added and removed task ids.
func (s *Store) scan(exts []string, settle time.Duration) (added, removed []string, err error) {
	inRoot := filepath.Join(s.root, dInput)
	seen := map[string]bool{}
	now := time.Now()
	err = filepath.WalkDir(inRoot, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !d.Type().IsRegular() {
			return nil
		}
		name := d.Name()
		if strings.HasPrefix(name, ".") || !hasExt(name, exts) {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(inRoot, p)
		id := filepath.ToSlash(rel)
		seen[id] = true
		s.mu.Lock()
		_, known := s.Tasks[id]
		s.mu.Unlock()
		s.mu.Lock()
		trusted := s.placed[id]
		delete(s.placed, id)
		s.mu.Unlock()
		if known || (now.Sub(info.ModTime()) < settle && !trusted) {
			return nil
		}
		_, sha, err := xfer.HashFile(p)
		if err != nil {
			return nil
		}
		s.mu.Lock()
		t := &TaskState{ID: id, Seq: randomSeq(), Added: now, InputSHA: sha}
		if f := s.presets[id]; f != nil {
			f(t)
			delete(s.presets, id)
		}
		s.Tasks[id] = t
		s.touch()
		s.mu.Unlock()
		added = append(added, id)
		return nil
	})
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, nil, err
	}
	s.mu.Lock()
	for id, t := range s.Tasks {
		if !seen[id] && !t.Internal {
			delete(s.Tasks, id)
			removed = append(removed, id)
			s.touch()
		}
	}
	s.mu.Unlock()
	return added, removed, nil
}

// markPlaced tells the next scan that these inputs were written completely by the server
// (add, resume, uncancel, ...), so they are queued at once instead of after settle_time.
// forget drops tasks whose inputs Matriline itself moved out of input/ (pause, cancel),
// so that the next scan does not report them as removed by hand.
func (s *Store) forget(ids []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, id := range ids {
		if t := s.Tasks[id]; t != nil && !t.Internal {
			delete(s.Tasks, id)
			s.touch()
		}
	}
}

func (s *Store) markPlaced(ids []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.placed == nil {
		s.placed = map[string]bool{}
	}
	for _, id := range ids {
		s.placed[id] = true
	}
}

func hasExt(name string, exts []string) bool {
	low := strings.ToLower(name)
	for _, e := range exts {
		if strings.HasSuffix(low, strings.ToLower(e)) {
			return true
		}
	}
	return false
}

// resolveOpaque maps a client-visible task id back to the real one; caller holds mu.
func (s *Store) resolveOpaque(opaque string) (string, bool) {
	for id := range s.Tasks {
		if opaqueID(id) == opaque {
			return id, true
		}
	}
	return "", false
}

// queueOrder returns task ids sorted by priority (desc) then natural order or random key.
func (s *Store) queueOrder(order string) []string {
	if s.orderOK {
		return s.order
	}
	ids := make([]string, 0, len(s.Tasks))
	for id := range s.Tasks {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		a, b := s.Tasks[ids[i]], s.Tasks[ids[j]]
		// verification sub-tasks and canaries always first, oldest first: "next" gives a
		// user task priority 1002, above internalPrio, and replicas waited 7 h (lab)
		if a.Internal != b.Internal {
			return a.Internal
		}
		if a.Internal && !a.Added.Equal(b.Added) && a.Priority == b.Priority {
			return a.Added.Before(b.Added)
		}
		if a.Priority != b.Priority {
			return a.Priority > b.Priority
		}
		if order == "random" {
			return a.Seq < b.Seq
		}
		return naturalLess(a.ID, b.ID)
	})
	s.order, s.orderOK = ids, true
	return ids
}

// ---------------------------------------------------------------------------------------
// Moving files inside the spool

// moveFile renames src to dst creating parents (both absolute). Falls back to copy+remove
// across file systems.
// freeName is rel, or rel.2, rel.3... the first that is not taken under root (a result
// or input arriving again keeps the earlier one). Anything Stat cannot read counts as
// free: the move that follows then fails with a clear error, where a loop waiting for
// "does not exist" never ended (a file where a directory belongs gives ENOTDIR).
func freeName(root, rel string) string {
	cand := rel
	for i := 2; fileExists(filepath.Join(root, filepath.FromSlash(cand))); i++ {
		cand = fmt.Sprintf("%s.%d", rel, i)
	}
	return cand
}

func moveFile(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o750); err != nil {
		return err
	}
	if _, err := os.Stat(dst); err == nil {
		return fmt.Errorf("destination %s already exists", dst)
	}
	if err := os.Rename(src, dst); err == nil {
		return ident.SyncDir(filepath.Dir(dst))
	}
	if err := copyTree(src, dst); err != nil {
		return err
	}
	return os.RemoveAll(src)
}

func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		t := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(t, 0o750)
		}
		if !d.Type().IsRegular() {
			return nil // symlinks and devices are never copied
		}
		return copyFile(p, t)
	})
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o750); err != nil {
		return err
	}
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Sync(); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// pruneEmpty removes empty directories below dir (not dir itself).
func pruneEmpty(dir string) {
	var dirs []string
	filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err == nil && d.IsDir() && p != dir {
			dirs = append(dirs, p)
		}
		return nil
	})
	sort.Slice(dirs, func(i, j int) bool { return len(dirs[i]) > len(dirs[j]) })
	for _, d := range dirs {
		os.Remove(d) // fails (and is ignored) when not empty
	}
}

// keepsEmpty: empty sub-directories of spool directory d stay (spool.keep_empty_dirs).
func (s *Server) keepsEmpty(d string) bool {
	for _, k := range s.conf().KeepEmpty {
		if strings.Trim(k, "/") == d {
			return true
		}
	}
	return false
}

// pruneSpool removes the sub-directories left empty in the spool directories, except in
// those listed in spool.keep_empty_dirs (input/ by default: the admin's own structure).
func (s *Server) pruneSpool() {
	cfg := s.conf()
	for _, d := range append(append([]string{}, spoolDirs...), "other-versions") {
		if !s.keepsEmpty(d) {
			pruneEmpty(filepath.Join(cfg.Root, d))
		}
	}
}

// dirSize returns the total size of regular files below dir.
func dirSize(dir string) int64 {
	var n int64
	filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err == nil && d.Type().IsRegular() {
			if info, err := d.Info(); err == nil {
				n += info.Size()
			}
		}
		return nil
	})
	return n
}

// presetTask registers settings for a task the scan has not made yet (nil removes them):
// they are in place before any host can be given it.
func (s *Store) presetTask(id string, f func(*TaskState)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.presets == nil {
		s.presets = map[string]func(*TaskState){}
	}
	if f == nil {
		delete(s.presets, id)
		return
	}
	s.presets[id] = f
}
