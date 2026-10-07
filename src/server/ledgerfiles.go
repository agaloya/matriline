package main

import (
	"os"
	"path"
	"path/filepath"

	"github.com/agaloya/matriline/common/ledger"
	"github.com/agaloya/matriline/common/manifest"
	"github.com/agaloya/matriline/common/xfer"
)

// Smaller ledger, same check (D61, user 2026-10-05). A result directory's files are listed
// with their hashes in the client's signed manifest, stored in the directory; repeating
// every hash in the ledger made it ~2.8 KB per task (~285 MB for 100000). Now an entry
// lists only the files the manifest does not already vouch for with the same content (the
// manifest itself, matriline.names, the server's own files), and names the manifest in
// FromManifest. Verification rebuilds the full list from the manifest: the manifest and
// the names file keep their own hashes in the ledger, so changing either shows as MODIFIED,
// and without the manifest its files show as UNRECORDED: nothing goes unnoticed.

// manifestHashes: spool path -> SHA-256 of the files the signed manifest of result
// directory dir (spool-relative, slash separated) lists, under their names in that
// directory (matriline.names maps the client's anonymous names). nil without a manifest.
func manifestHashes(root, dir string) map[string]string {
	abs := filepath.Join(root, filepath.FromSlash(dir))
	raw, err := os.ReadFile(filepath.Join(abs, manifestFile))
	if err != nil {
		return nil
	}
	m, err := manifest.Peek(raw)
	if err != nil {
		return nil
	}
	names := readNames(abs)
	out := map[string]string{}
	for _, f := range m.Files {
		n := f.Name
		if r, ok := names[n]; ok {
			n = r
		}
		out[path.Join(dir, n)] = f.SHA256
	}
	return out
}

// dirHashes hashes the files of result directory dir for a ledger entry, leaving out
// those its manifest vouches for (slimFiles).
func dirHashes(root, dir string) ([]ledger.FileHash, string) {
	var files []ledger.FileHash
	entries, _ := os.ReadDir(filepath.Join(root, filepath.FromSlash(dir)))
	for _, e := range entries {
		if !e.Type().IsRegular() {
			continue
		}
		p := path.Join(dir, e.Name())
		if _, h, err := xfer.HashFile(filepath.Join(root, filepath.FromSlash(p))); err == nil {
			files = append(files, ledger.FileHash{Path: p, SHA256: h})
		}
	}
	return slimFiles(root, dir, files)
}

// slimFiles leaves out the files of result directory dir that its manifest lists with the
// same hash; from is the manifest's path when something was left out.
func slimFiles(root, dir string, files []ledger.FileHash) (kept []ledger.FileHash, from string) {
	listed := manifestHashes(root, dir)
	if listed == nil {
		return files, ""
	}
	for _, f := range files {
		if h, ok := listed[f.Path]; !ok || h != f.SHA256 {
			kept = append(kept, f)
		}
	}
	if len(kept) == len(files) {
		return files, ""
	}
	return kept, path.Join(dir, manifestFile)
}

// entryFiles is every file an entry vouches for: its own list and, for a slim entry, the
// files its manifest lists (read now from the directory).
func entryFiles(root string, e ledger.Entry) []ledger.FileHash {
	if e.FromManifest == "" {
		return e.Files
	}
	all := append([]ledger.FileHash{}, e.Files...)
	for p, h := range manifestHashes(root, path.Dir(e.FromManifest)) {
		all = append(all, ledger.FileHash{Path: p, SHA256: h})
	}
	return all
}
