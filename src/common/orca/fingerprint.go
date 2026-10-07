package orca

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
)

// Fingerprint describes an ORCA installation tree.
type Fingerprint struct {
	Root     string            `json:"root"`
	TreeHash string            `json:"tree_hash"` // SHA-256 over "relpath\tsha256\n" lines, sorted
	Files    map[string]string `json:"files"`     // relpath -> sha256
	Bytes    int64             `json:"bytes"`
}

type cacheEntry struct {
	Size  int64  `json:"size"`
	MTime int64  `json:"mtime"`
	SHA   string `json:"sha"`
}

// FingerprintTree hashes every regular file below root (executables, shared libraries,
// basis-set/data files: all of them can change results). Results are cached by
// (size, mtime) in cachePath so restarts are cheap; pass deep=true to ignore the cache.
// Symbolic links are recorded by target text, never followed outside the tree. A top-level
// "setup" (the .run installer's helper) is left out, so installed and unpacked builds match.
func FingerprintTree(root, cachePath string, deep bool) (*Fingerprint, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	cache := map[string]cacheEntry{}
	if !deep && cachePath != "" {
		if b, err := os.ReadFile(cachePath); err == nil {
			_ = json.Unmarshal(b, &cache)
		}
	}
	type job struct {
		rel  string
		path string
		info fs.FileInfo
	}
	var jobs []job
	links := map[string]string{}
	err = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		rel = filepath.ToSlash(rel)
		switch {
		case d.Type()&fs.ModeSymlink != 0:
			t, _ := os.Readlink(p)
			links[rel] = "link:" + t
		case d.Type().IsRegular() && rel == "setup":
			// the self-extracting installer's helper: some installs keep it, the archives
			// and the other installs have none; ORCA never runs it
		case d.Type().IsRegular():
			info, err := d.Info()
			if err != nil {
				return err
			}
			jobs = append(jobs, job{rel, p, info})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	fp := &Fingerprint{Root: root, Files: make(map[string]string, len(jobs)+len(links))}
	for k, v := range links {
		fp.Files[k] = v
	}
	var mu sync.Mutex
	var firstErr error
	work := make(chan job)
	var wg sync.WaitGroup
	workers := runtime.NumCPU()
	if workers > 8 {
		workers = 8
	}
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range work {
				sz, mt := j.info.Size(), j.info.ModTime().UnixNano()
				mu.Lock()
				c, ok := cache[j.rel]
				mu.Unlock()
				sum := c.SHA
				if !ok || c.Size != sz || c.MTime != mt {
					s, err := hashFile(j.path)
					if err != nil {
						mu.Lock()
						if firstErr == nil {
							firstErr = err
						}
						mu.Unlock()
						continue
					}
					sum = s
				}
				mu.Lock()
				fp.Files[j.rel] = sum
				fp.Bytes += sz
				cache[j.rel] = cacheEntry{sz, mt, sum}
				mu.Unlock()
			}
		}()
	}
	for _, j := range jobs {
		work <- j
	}
	close(work)
	wg.Wait()
	if firstErr != nil {
		return nil, firstErr
	}
	fp.TreeHash = TreeHashOf(fp.Files)
	if cachePath != "" {
		if b, err := json.Marshal(cache); err == nil {
			_ = os.MkdirAll(filepath.Dir(cachePath), 0o700)
			_ = os.WriteFile(cachePath, b, 0o600)
		}
	}
	return fp, nil
}

func hashFile(p string) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// Lookup returns the recorded hash of an absolute path inside the tree.
func (f *Fingerprint) Lookup(abs string) (string, bool) {
	rel, err := filepath.Rel(f.Root, abs)
	if err != nil || strings.HasPrefix(rel, "..") {
		return "", false
	}
	s, ok := f.Files[filepath.ToSlash(rel)]
	return s, ok
}

// TreeHashOf is the tree hash of a file list: SHA-256 over sorted "relpath\tsha256\n" lines.
func TreeHashOf(files map[string]string) string {
	keys := make([]string, 0, len(files))
	for k := range files {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	h := sha256.New()
	for _, k := range keys {
		io.WriteString(h, k+"\t"+files[k]+"\n")
	}
	return hex.EncodeToString(h.Sum(nil))
}
