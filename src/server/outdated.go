package main

import (
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/agaloya/matriline/common/manifest"
)

// One result per task (user, 2026-10-06): once a task has an accepted result in output/,
// its earlier results that went to weird/ move to outdated/ (with their matriline.verdict:
// the evidence stays), and a weird result accepted by hand replaces the one in output/,
// which moves to outdated/ too. weird/ then holds only what still needs a decision.

// taskResultDirs lists the result directories of taskID under top: <top>/<sub>/<stem> and
// <stem>.2, .3... whose signed manifest names that task (results keep their manifest, but
// not always the input: results.include may leave it out), or, without a manifest (a
// failure filed by the server), that hold the task's input copy.
func (s *Server) taskResultDirs(top, taskID string) []string {
	root := s.conf().Root
	stemRel := resultDir(top, taskID)
	parent, base := path.Dir(stemRel), path.Base(stemRel)
	entries, _ := os.ReadDir(filepath.Join(root, filepath.FromSlash(parent)))
	var out []string
	for _, e := range entries {
		n := e.Name()
		if !e.IsDir() || (n != base && !(strings.HasPrefix(n, base+".") && reCopy.MatchString(n[len(base):]))) {
			continue
		}
		rel := path.Join(parent, n)
		abs := filepath.Join(root, filepath.FromSlash(rel))
		if raw, err := os.ReadFile(filepath.Join(abs, manifestFile)); err == nil {
			if m, err := manifest.Peek(raw); err == nil && (m.TaskID == opaqueID(taskID) || m.TaskID == legacyOpaqueID(taskID)) {
				out = append(out, rel)
			}
			continue
		}
		if fileExists(filepath.Join(abs, path.Base(taskID))) {
			out = append(out, rel)
		}
	}
	return out
}

// errChecked: a running check reads the directory, which must stay where it is.
var errChecked = errors.New("a check is still running on it")

// checked reports whether a running check reads result directory rel.
func (s *Server) checked(rel string) bool {
	s.store.mu.Lock()
	defer s.store.mu.Unlock()
	for _, ck := range s.store.Checks {
		if ck.ResultRel == rel {
			return true
		}
	}
	return false
}

// outdateLocked moves a result directory to outdated/ and records it; the caller holds
// moveMu. A directory a running check reads is not moved (errChecked).
func (s *Server) outdateLocked(rel, why string) error {
	if s.checked(rel) {
		return errChecked
	}
	root := s.conf().Root
	top, rest := splitSpool(rel)
	if top == "" || rest == "" {
		return fmt.Errorf("not a result directory: %s", rel)
	}
	dst := freeName(root, path.Join(dOutdated, rest))
	if err := moveFile(filepath.Join(root, filepath.FromSlash(rel)), filepath.Join(root, filepath.FromSlash(dst))); err != nil {
		return fmt.Errorf("moving %s to outdated/: %v", rel, err)
	}
	s.ledgerDir("outdated", dst, "moved from "+rel+": "+why)
	s.event("system", "%s -> %s (%s)", rel, dst, why)
	return nil
}

// retireWeird moves the weird/ results of a task that now has an accepted result.
func (s *Server) retireWeird(taskID string) {
	s.moveMu.Lock()
	defer s.moveMu.Unlock()
	s.retireWeirdLocked(taskID)
}

func (s *Server) retireWeirdLocked(taskID string) {
	for _, rel := range s.taskResultDirs(dWeird, taskID) {
		if err := s.outdateLocked(rel, "the task has an accepted result"); err != nil && err != errChecked {
			s.log.Errorf("%v", err)
		}
	}
	s.pruneSpool()
}

// acceptWeirdLocked makes result directory weirdRel (weird/...) the task's result in
// output/, for the admin's accept and for a passed tie-break; the caller holds moveMu. The
// task's result in output/ (if any) moves to outdated/: if it cannot (a check reads it, or
// the move fails) nothing is done, so output/ never holds two results of one task. A copy
// of the task still queued (the automatic retry) is dropped and its attempts cancelled, and
// the task's other weird results move to outdated/ too.
func (s *Server) acceptWeirdLocked(weirdRel, why string) (string, string, error) {
	cfg := s.conf()
	src := filepath.Join(cfg.Root, filepath.FromSlash(weirdRel))
	taskID := ""
	_, rest := splitSpool(weirdRel)
	if raw, err := os.ReadFile(filepath.Join(src, manifestFile)); err == nil {
		if m, err := manifest.Peek(raw); err == nil {
			taskID, _ = s.store.resolveOpaque(m.TaskID)
		}
	}
	entries, _ := os.ReadDir(src)
	for _, e := range entries {
		if taskID == "" && hasExt(e.Name(), cfg.InputExt) {
			taskID = path.Join(path.Dir(rest), e.Name())
		}
	}
	if taskID == "" {
		return "", "", fmt.Errorf("cannot tell which task %s belongs to", weirdRel)
	}
	for _, rel := range s.taskResultDirs(dOutput, taskID) {
		if err := s.outdateLocked(rel, "replaced by "+weirdRel+" ("+why+")"); err != nil {
			return "", "", fmt.Errorf("%s is the task's result now and cannot be replaced: %v", rel, err)
		}
	}
	dstRel := freeName(cfg.Root, resultDir(dOutput, taskID))
	// the result's input copy stays with it (it may be the client's own, signed in the
	// manifest); completed/ keeps the original
	if done := filepath.Join(cfg.Root, dCompleted, filepath.FromSlash(taskID)); !fileExists(done) {
		copyFile(filepath.Join(src, path.Base(taskID)), done)
	}
	if err := moveFile(src, filepath.Join(cfg.Root, filepath.FromSlash(dstRel))); err != nil {
		return "", "", err
	}
	s.ledgerDir("accept", dstRel, why+" from "+weirdRel)
	s.hookResult(taskID, dstRel)
	// the task is done: a queued copy (automatic retry) and its running attempts go
	s.store.mu.Lock()
	_, queued := s.store.Tasks[taskID]
	s.store.mu.Unlock()
	if queued {
		to := dCompleted
		if fileExists(filepath.Join(cfg.Root, dCompleted, filepath.FromSlash(taskID))) {
			to = "" // completed/ has the input already
		}
		s.finishTask(taskID, to)
		s.cancelAttemptsOf(taskID, "the task's result was accepted")
	}
	s.retireWeirdLocked(taskID)
	return taskID, dstRel, nil
}
