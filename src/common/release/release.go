// Package release: signed Matriline releases and self-update (D66).
//
// A release is published (e.g. as GitHub release assets) as the binaries, release.json
// (version, commit, date and the SHA-256 of every binary) and release.json.sig, an ed25519
// signature by a key listed in TrustedKeys. That key belongs to the maintainer and never
// sits on GitHub or on a server: whoever takes over the repository, the release page or a
// Matriline server still cannot make any computer install a program it did not sign.
// Programs built without a trusted key never update themselves.
package release

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/agaloya/matriline/common/ident"
)

// Manifest is release.json.
type Manifest struct {
	Version string            `json:"version"` // "0.1.1"
	Commit  string            `json:"commit"`
	Date    string            `json:"date"`    // YYYY-MM-DD
	Expires string            `json:"expires"` // YYYY-MM-DD: refused after this day (no replay of old releases)
	Files   map[string]string `json:"files"`   // asset name -> SHA-256 (hex)
}

// DefaultURL is where clients fetch releases unless their owner sets another place
// (security.update_url): never a place chosen by the server (code review).
const DefaultURL = "https://github.com/agaloya/matriline/releases"

// Validity is how long a signed release can be installed (relsign writes Expires).
const Validity = 120 * 24 * time.Hour

const (
	ManifestName = "release.json"
	SigName      = "release.json.sig"
	sigContext   = "matriline-release-v1"
	maxManifest  = 1 << 20
	maxBinary    = 200 << 20
)

// Sign returns the signature of release.json's bytes (relsign).
func Sign(body []byte, priv ed25519.PrivateKey) []byte {
	return ed25519.Sign(priv, append([]byte(sigContext), body...))
}

// Verify checks release.json's signature against the trusted keys and parses it.
func Verify(body, sig []byte) (*Manifest, error) {
	if len(TrustedKeys) == 0 {
		return nil, errors.New("this program has no release key built in: it does not update itself")
	}
	ok := false
	for _, k := range TrustedKeys {
		pub, err := ident.DecodeKey(k)
		if err == nil && len(pub) == ed25519.PublicKeySize && ed25519.Verify(pub, append([]byte(sigContext), body...), sig) {
			ok = true
			break
		}
	}
	if !ok {
		return nil, errors.New("release.json is not signed by a trusted release key")
	}
	var m Manifest
	if err := json.Unmarshal(body, &m); err != nil {
		return nil, fmt.Errorf("release.json: %v", err)
	}
	if _, ok := parseVersion(m.Version); !ok || len(m.Files) == 0 {
		return nil, fmt.Errorf("release.json: bad version %q or no files", m.Version)
	}
	exp, err := time.Parse("2006-01-02", m.Expires)
	if err != nil {
		return nil, fmt.Errorf("release.json: no valid expiry date")
	}
	if time.Now().After(exp.Add(24 * time.Hour)) {
		return nil, fmt.Errorf("release %s expired on %s (an old release is not installed: the maintainer signs a new one)", m.Version, m.Expires)
	}
	return &m, nil
}

// Enabled reports whether this build can verify releases at all.
func Enabled() bool { return len(TrustedKeys) > 0 }

// URLOK refuses update addresses that would leak or misdirect: https, or plain http to
// this computer (tests).
func URLOK(u string) error {
	if strings.HasPrefix(u, "https://") || strings.HasPrefix(u, "http://127.0.0.1") || strings.HasPrefix(u, "http://localhost") {
		return nil
	}
	return fmt.Errorf("update address %q: only https is used", u)
}

// AssetName is the published file name of a program for a platform, as tools/release.sh
// names it: matriline-client-linux-amd64, matriline-server-windows-amd64.exe...
func AssetName(prog, goos, goarch string) string {
	n := "matriline-" + prog + "-" + goos + "-" + goarch
	if goos == "windows" {
		n += ".exe"
	}
	return n
}

// Platform is this program's "os-arch".
func Platform() string { return runtime.GOOS + "-" + runtime.GOARCH }

// VersionOf takes the version from an agent string ("matriline-server/0.1.0" -> "0.1.0").
func VersionOf(agent string) string {
	if i := strings.LastIndexByte(agent, '/'); i >= 0 {
		agent = agent[i+1:]
	}
	if i := strings.IndexByte(agent, ' '); i >= 0 {
		agent = agent[:i]
	}
	return agent
}

// parseVersion accepts exactly "major.minor.patch": digits only, no leading zeros, no
// "v" (a typo such as "v0.2.0" would point at download/vv0.2.0/).
func parseVersion(v string) ([3]int, bool) {
	var out [3]int
	parts := strings.Split(v, ".")
	if len(parts) != 3 {
		return out, false
	}
	for i, p := range parts {
		if p == "" || len(p) > 6 || (len(p) > 1 && p[0] == '0') || strings.Trim(p, "0123456789") != "" {
			return out, false
		}
		out[i], _ = strconv.Atoi(p)
	}
	return out, true
}

// Newer reports whether version a is later than b (both "major.minor.patch"; anything
// unparsable is never newer: no update on a malformed version).
func Newer(a, b string) bool {
	va, okA := parseVersion(a)
	vb, okB := parseVersion(b)
	if !okA || !okB {
		return false
	}
	for i := range va {
		if va[i] != vb[i] {
			return va[i] > vb[i]
		}
	}
	return false
}

var httpClient = &http.Client{Timeout: 10 * time.Minute}

func get(ctx context.Context, url string, limit int64, w io.Writer) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: %s", url, resp.Status)
	}
	n, err := io.Copy(w, io.LimitReader(resp.Body, limit+1))
	if err == nil && n > limit {
		err = fmt.Errorf("%s: larger than %d bytes", url, limit)
	}
	return err
}

// Fetch downloads the newest release.json and its signature from base (GitHub's releases
// page, https://github.com/<owner>/<repo>/releases, or a mirror with the same layout) and
// verifies them.
func Fetch(ctx context.Context, base string) (body, sig []byte, m *Manifest, err error) {
	var b, s strings.Builder
	if err = get(ctx, LatestURL(base, ManifestName), maxManifest, &b); err != nil {
		return
	}
	if err = get(ctx, LatestURL(base, SigName), 1024, &s); err != nil {
		return
	}
	body, sig = []byte(b.String()), []byte(s.String())
	m, err = Verify(body, sig)
	return
}

// FetchVersion downloads and verifies the release.json of one version (not the newest):
// a server's helper kit carries the clients of the server's own version.
func FetchVersion(ctx context.Context, base, version string) (*Manifest, error) {
	var b, s strings.Builder
	if err := get(ctx, AssetURL(base, version, ManifestName), maxManifest, &b); err != nil {
		return nil, err
	}
	if err := get(ctx, AssetURL(base, version, SigName), 1024, &s); err != nil {
		return nil, err
	}
	m, err := Verify([]byte(b.String()), []byte(s.String()))
	if err != nil {
		return nil, err
	}
	if m.Version != version {
		return nil, fmt.Errorf("release v%s holds a release.json of version %s", version, m.Version)
	}
	return m, nil
}

// Download fetches asset name of release m from base into a new file in dir, checks its
// SHA-256 against the signed manifest and returns the file's path.
func Download(ctx context.Context, base string, m *Manifest, name, dir string) (string, error) {
	want, ok := m.Files[name]
	if !ok {
		return "", fmt.Errorf("release %s has no %s", m.Version, name)
	}
	ext := ""
	if strings.HasSuffix(name, ".exe") {
		ext = ".exe" // Windows runs it for the preflight
	}
	f, err := os.CreateTemp(dir, ".update-*"+ext)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	err = get(ctx, AssetURL(base, m.Version, name), maxBinary, io.MultiWriter(f, h))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil && hex.EncodeToString(h.Sum(nil)) != want {
		err = fmt.Errorf("%s: SHA-256 differs from the signed release.json", name)
	}
	if err == nil {
		err = os.Chmod(f.Name(), 0o755)
	}
	if err == nil {
		err = syncFile(f.Name()) // on disk before it replaces the program (a power cut)
	}
	if err != nil {
		os.Remove(f.Name())
		return "", err
	}
	return f.Name(), nil
}

// Install puts the downloaded program newPath in place of the running one (exe), after
// Preflight. The running program stays as <exe>.previous (to go back by hand: rename it
// over exe). On Unix there is no moment without a program: a hard link keeps the old one
// as .previous, then one atomic rename puts the new one in place. A running program
// cannot be replaced on Windows, only renamed: there it is two renames.
func Install(newPath, exe string) error {
	prev := exe + ".previous"
	os.Remove(prev)
	if runtime.GOOS != "windows" {
		if err := os.Link(exe, prev); err != nil {
			// no hard links here (vfat, some network file systems, protected_hardlinks):
			// a copy, on disk before the rename
			if err := copySync(exe, prev); err != nil {
				return err
			}
		}
		if err := os.Rename(newPath, exe); err != nil {
			return err
		}
		syncDir(filepath.Dir(exe))
		return nil
	}
	if err := os.Rename(exe, prev); err != nil {
		return err
	}
	if err := os.Rename(newPath, exe); err != nil {
		os.Rename(prev, exe)
		return err
	}
	return nil
}

// ErrDoesNotRun: the new program ran and failed, or reported another version. Only this
// is a verdict on the release; any other Preflight error (it could not be started, timed
// out, the file vanished, the computer is busy) is worth another try later.
var ErrDoesNotRun = errors.New("the new program does not run here")

var preflightTimeout = 60 * time.Second

// Preflight runs the downloaded program once ("version --plain": no configuration, no
// connections) before it replaces anything: it must report the release's version. The
// file is hashed again first. A binary that does not run here is never installed; there
// is no automatic rollback to go wrong (code reviews).
func Preflight(ctx context.Context, path string, m *Manifest, name string) error {
	if err := checkHash(path, m.Files[name]); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, preflightTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, "version", "--plain")
	cmd.Dir = filepath.Dir(path)
	cmd.WaitDelay = 5 * time.Second // a child holding its output cannot block us
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	var exit *exec.ExitError
	switch {
	case ctx.Err() != nil:
		return fmt.Errorf("the new program did not answer in time (busy computer?): %v", ctx.Err())
	case errors.As(err, &exit):
		return fmt.Errorf("%w: %v %s", ErrDoesNotRun, err, strings.TrimSpace(tailOf(stderr.String(), 300)))
	case err != nil && (strings.Contains(err.Error(), "exec format error") || strings.Contains(err.Error(), "not a valid Win32 application")):
		return fmt.Errorf("%w: %v (another processor or system?)", ErrDoesNotRun, err)
	case err != nil:
		return fmt.Errorf("could not start the new program: %v", err) // e.g. a virus scanner holding it
	}
	first, _, _ := strings.Cut(string(out), "\n")
	if VersionOf(strings.Fields(first + " x")[0]) != m.Version {
		return fmt.Errorf("%w: it reports %q, not version %s", ErrDoesNotRun, strings.TrimSpace(first), m.Version)
	}
	return nil
}

func tailOf(s string, n int) string {
	if len(s) > n {
		return s[len(s)-n:]
	}
	return s
}

func checkHash(path, want string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	if hex.EncodeToString(h.Sum(nil)) != want {
		return fmt.Errorf("%s changed after the download", filepath.Base(path))
	}
	return nil
}

func copySync(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	if _, err = io.Copy(out, in); err == nil {
		err = out.Sync()
	}
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	return err
}

// CleanDownloads removes downloads left a day ago or more by an interrupted update in the
// program's folder (several clients may share that folder: recent files may be another
// one's download in progress).
func CleanDownloads(exe string) {
	if fs, err := filepath.Glob(filepath.Join(filepath.Dir(exe), ".update-*")); err == nil {
		for _, f := range fs {
			if st, err := os.Stat(f); err == nil && time.Since(st.ModTime()) > 24*time.Hour {
				os.Remove(f)
			}
		}
	}
}

// AssetURL is where a release's file is: <base>/download/v<version>/<name>, GitHub's
// layout. Always the version's own folder, never "latest", so a newer release published
// meanwhile cannot replace the file the signed list describes.
func AssetURL(base, version, name string) string {
	return strings.TrimRight(base, "/") + "/download/v" + version + "/" + name
}

// LatestURL is where the newest release.json is: <base>/latest/download/<name>.
func LatestURL(base, name string) string {
	return strings.TrimRight(base, "/") + "/latest/download/" + name
}

func syncFile(p string) error {
	f, err := os.OpenFile(p, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}

func syncDir(d string) {
	if f, err := os.Open(d); err == nil {
		f.Sync() // not possible on Windows: harmless
		f.Close()
	}
}

// Writable reports whether the running program can replace itself (its folder writable).
func Writable() bool {
	exe, err := Executable()
	if err != nil {
		return false
	}
	f, err := os.CreateTemp(filepath.Dir(exe), ".update-test-*")
	if err != nil {
		return false
	}
	f.Close()
	os.Remove(f.Name())
	return true
}

// Executable is the running program's path, links resolved.
func Executable() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(exe)
}

// Updated is set in the environment of a program restarted by an update: it waits a
// moment for the old process to release its lock (see AcquireRetry users).
const Updated = "MATRILINE_UPDATED"
