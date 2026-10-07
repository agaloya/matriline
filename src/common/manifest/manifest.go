// Package manifest defines the signed result manifest: the client's signed statement of
// which task it ran, with which ORCA binaries, and which files it produced (with hashes).
// The signature binds every output file to the client identity, so the server (and anyone
// auditing the ledger later) can detect modified or substituted files.
package manifest

import (
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"

	"github.com/agaloya/matriline/common/ident"
	"github.com/agaloya/matriline/common/wire"
)

// Exec records one executable observed while the job ran.
type Exec struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Count  int    `json:"count"`
	System bool   `json:"system,omitempty"` // system shell used by ORCA's system(3) calls
	MPI    bool   `json:"mpi,omitempty"`    // part of the fingerprinted OpenMPI (multi-core jobs)
}

// SystemShellPaths are the only programs outside the ORCA tree a manifest may list.
var SystemShellPaths = map[string]bool{"/bin/sh": true, "/usr/bin/sh": true, "/bin/bash": true,
	"/usr/bin/bash": true, "/bin/dash": true, "/usr/bin/dash": true, "/bin/busybox": true, "/usr/bin/busybox": true}

var reWinShell = regexp.MustCompile(`(?i)^[a-z]:\\windows\\system32\\cmd\.exe$`)

// IsSystemShell: a Unix shell above, or Windows' cmd.exe (ORCA's system() calls run
// %ComSpec%; Windows paths are case-insensitive and the system drive may not be C:).
func IsSystemShell(path string) bool {
	return SystemShellPaths[path] || reWinShell.MatchString(path)
}

// Manifest is the signed content. Field order is fixed by the struct, so json.Marshal is
// deterministic (maps are not used).
type Manifest struct {
	Format      int             `json:"format"` // 1
	ServerID    string          `json:"server_id"`
	ClientID    string          `json:"client_id"`
	TaskID      string          `json:"task_id"`
	AttemptID   string          `json:"attempt_id"`
	InputName   string          `json:"input_name"`
	InputSHA256 string          `json:"input_sha256"`
	OrcaVersion string          `json:"orca_version"`
	OrcaGit     string          `json:"orca_git"`
	OrcaTree    string          `json:"orca_tree"`
	MaxcoreMB   int             `json:"maxcore_mb"`            // resources written into the input
	Nprocs      int             `json:"nprocs"`                // (see orca.Normalize)
	MPIVersion  string          `json:"mpi_version,omitempty"` // OpenMPI of a multi-core job
	MPITree     string          `json:"mpi_tree,omitempty"`    // its fingerprint (tree hash)
	Kind        string          `json:"kind"`                  // job, replica, verify, canary
	Execs       []Exec          `json:"execs"`
	Files       []wire.FileMeta `json:"files"`
	ExitCode    int             `json:"exit_code"`
	StartedUnix int64           `json:"started_unix"`
	EndedUnix   int64           `json:"ended_unix"`
}

// Signed is the wire form: manifest bytes + signature.
type Signed struct {
	Body []byte `json:"body"`
	Sig  []byte `json:"sig"`
}

// Normalize sorts lists so equal content always gives identical bytes.
func (m *Manifest) Normalize() {
	sort.Slice(m.Files, func(i, j int) bool { return m.Files[i].Name < m.Files[j].Name })
	sort.Slice(m.Execs, func(i, j int) bool { return m.Execs[i].Path < m.Execs[j].Path })
}

// Sign produces the signed wire form.
func Sign(m *Manifest, k *ident.Key) ([]byte, error) {
	m.Format = 1
	m.ClientID = k.ID()
	m.Normalize()
	body, err := json.Marshal(m)
	if err != nil {
		return nil, err
	}
	sig := ed25519.Sign(k.Priv, append([]byte("matriline-manifest-v1"), body...))
	return json.Marshal(Signed{Body: body, Sig: sig})
}

// Peek reads a signed manifest WITHOUT checking its signature: to learn who signed it (and
// so which key verifies it), or to read a field of a manifest already verified on arrival.
func Peek(raw []byte) (*Manifest, error) {
	var sg Signed
	if err := json.Unmarshal(raw, &sg); err != nil {
		return nil, err
	}
	var m Manifest
	if err := json.Unmarshal(sg.Body, &m); err != nil {
		return nil, err
	}
	return &m, nil
}

// Verify checks the signature with the expected client key and returns the manifest.
func Verify(signed []byte, pub ed25519.PublicKey) (*Manifest, error) {
	var s Signed
	if err := json.Unmarshal(signed, &s); err != nil {
		return nil, fmt.Errorf("manifest: %v", err)
	}
	if !ed25519.Verify(pub, append([]byte("matriline-manifest-v1"), s.Body...), s.Sig) {
		return nil, errors.New("manifest: bad signature")
	}
	var m Manifest
	if err := json.Unmarshal(s.Body, &m); err != nil {
		return nil, err
	}
	if m.ClientID != ident.IDOf(pub) {
		return nil, errors.New("manifest: client id does not match signing key")
	}
	return &m, nil
}

// MatchFiles checks that the files announced in the RESULT message are exactly the ones in
// the signed manifest (same names, sizes, hashes).
func (m *Manifest) MatchFiles(files []wire.FileMeta) error {
	if len(files) != len(m.Files) {
		return fmt.Errorf("manifest lists %d files, result carries %d", len(m.Files), len(files))
	}
	idx := map[string]wire.FileMeta{}
	for _, f := range m.Files {
		idx[f.Name] = f
	}
	for _, f := range files {
		g, ok := idx[f.Name]
		if !ok || g.Size != f.Size || g.SHA256 != f.SHA256 {
			return fmt.Errorf("file %q does not match signed manifest", f.Name)
		}
	}
	return nil
}
