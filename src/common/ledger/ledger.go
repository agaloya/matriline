// Package ledger implements the server's append-only, hash-chained and signed integrity log.
// Every accepted result, file move and manual edit is recorded with the SHA-256 of the files
// involved. Because each entry includes the hash of the previous one and is signed by the
// server key, deleting, reordering or editing past entries breaks the chain, and comparing
// the latest recorded hashes with the spool reveals files modified on disk afterwards.
//
// The design follows the classic hash-chain / tamper-evident log idea (Haber & Stornetta,
// "How to time-stamp a digital document", J. Cryptology 3, 1991; Crosby & Wallach,
// "Efficient data structures for tamper-evident logging", USENIX Security 2009).
package ledger

import (
	"bufio"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/agaloya/matriline/common/ident"
)

// FileHash is a stored file path (relative to the spool root) and its SHA-256.
type FileHash struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

// Entry is one ledger record.
type Entry struct {
	Seq      uint64     `json:"seq"`
	Time     int64      `json:"time"`
	Kind     string     `json:"kind"` // result, accept, reject, move, edit, config, verify
	Task     string     `json:"task,omitempty"`
	Attempt  string     `json:"attempt,omitempty"`
	Client   string     `json:"client,omitempty"`
	Manifest string     `json:"manifest,omitempty"` // SHA-256 of the signed manifest bytes
	Verdict  string     `json:"verdict,omitempty"`
	Note     string     `json:"note,omitempty"`
	Files    []FileHash `json:"files,omitempty"`
	// FromManifest: the path of a result's signed manifest whose listed files (under their
	// names in the directory's matriline.names) belong to this entry too; they are not
	// repeated in Files (D61). Absent in older entries, which list every file.
	FromManifest string `json:"from_manifest,omitempty"`
	Prev         string `json:"prev"`
	Hash         string `json:"hash"`
	Sig          []byte `json:"sig"`
}

func (e *Entry) digest() string {
	c := *e
	c.Hash, c.Sig = "", nil
	b, _ := json.Marshal(c)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// Ledger is an open ledger file.
type Ledger struct {
	mu   sync.Mutex
	f    *os.File
	key  *ident.Key
	last string
	seq  uint64
}

// Open opens (or creates) the ledger and verifies the existing chain.
func Open(path string, key *ident.Key) (*Ledger, error) {
	if err := truncateTornTail(path); err != nil {
		return nil, err
	}
	entries, err := Read(path, key.Pub)
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	l := &Ledger{f: f, key: key, last: "genesis"}
	if n := len(entries); n > 0 {
		l.last, l.seq = entries[n-1].Hash, entries[n-1].Seq
	}
	return l, nil
}

// Append adds an entry durably and returns its hash.
func (l *Ledger) Append(e Entry) (string, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.seq++
	e.Seq, e.Time, e.Prev = l.seq, time.Now().Unix(), l.last
	e.Hash = e.digest()
	e.Sig = ed25519.Sign(l.key.Priv, []byte(e.Hash))
	b, err := json.Marshal(e)
	if err != nil {
		return "", err
	}
	if _, err := l.f.Write(append(b, '\n')); err != nil {
		return "", err
	}
	if err := l.f.Sync(); err != nil {
		return "", err
	}
	l.last = e.Hash
	return e.Hash, nil
}

// Close closes the file.
func (l *Ledger) Close() error { return l.f.Close() }

// Read loads and fully verifies a ledger. A truncated final line (power cut while writing)
// is tolerated and reported through the returned entries (it is simply ignored).
func Read(path string, pub ed25519.PublicKey) ([]Entry, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []Entry
	prev := "genesis"
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 64<<20)
	line := 0
	for sc.Scan() {
		line++
		var e Entry
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
			if !sc.Scan() { // last line only: torn write
				break
			}
			return out, fmt.Errorf("ledger line %d: corrupt entry", line)
		}
		if e.Prev != prev {
			return out, fmt.Errorf("ledger line %d: chain broken (entry removed or reordered)", line)
		}
		if e.digest() != e.Hash {
			return out, fmt.Errorf("ledger line %d: entry content modified", line)
		}
		if !ed25519.Verify(pub, []byte(e.Hash), e.Sig) {
			return out, errors.New("ledger: bad server signature at line " + fmt.Sprint(line))
		}
		prev = e.Hash
		out = append(out, e)
	}
	return out, sc.Err()
}

// truncateTornTail removes a partial last line left by a power cut during Append.
func truncateTornTail(path string) error {
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if len(b) == 0 || b[len(b)-1] == '\n' {
		return nil
	}
	i := len(b) - 1
	for i >= 0 && b[i] != '\n' {
		i--
	}
	return os.Truncate(path, int64(i+1))
}
