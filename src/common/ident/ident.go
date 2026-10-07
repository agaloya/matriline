// Package ident manages Matriline node identities: Ed25519 key pairs, the short textual
// identity derived from a public key, self-signed TLS certificates carrying that key and
// credential files handed to clients.
package ident

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base32"
	"encoding/base64"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/agaloya/matriline/common/conf"
)

var b32 = base32.StdEncoding.WithPadding(base32.NoPadding)

// Key is a node key pair.
type Key struct {
	Priv ed25519.PrivateKey
	Pub  ed25519.PublicKey
}

// Generate creates a new random key.
func Generate() (*Key, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	return &Key{Priv: priv, Pub: pub}, nil
}

// FromSeed rebuilds a key from its 32-byte seed.
func FromSeed(seed []byte) (*Key, error) {
	if len(seed) != ed25519.SeedSize {
		return nil, errors.New("bad key seed length")
	}
	priv := ed25519.NewKeyFromSeed(seed)
	return &Key{Priv: priv, Pub: priv.Public().(ed25519.PublicKey)}, nil
}

// ID returns the textual identity of the key.
func (k *Key) ID() string { return IDOf(k.Pub) }

// IDOf derives "ml1-" + 26 base32 chars of SHA-256(pub).
func IDOf(pub []byte) string {
	h := sha256.Sum256(pub)
	return "ml1-" + strings.ToLower(b32.EncodeToString(h[:16]))
}

// EncodeKey / DecodeKey serialize keys for config files.
func EncodeKey(b []byte) string { return base64.StdEncoding.EncodeToString(b) }

// DecodeKey decodes base64 key material.
func DecodeKey(s string) ([]byte, error) {
	return base64.StdEncoding.DecodeString(strings.TrimSpace(s))
}

// LoadOrCreate reads a key file (one line: base64 seed) or creates it with mode 0600.
// Two processes that start together (a new client's service and its 'doctor') end with
// the same key: the file is never replaced once it exists, and the one that comes second
// reads the first one's key (a kit test: each kept its own, and the service
// could not connect after a restart).
func LoadOrCreate(path string) (*Key, bool, error) {
	if k, err := loadKey(path); err == nil || !os.IsNotExist(err) {
		return k, false, err
	}
	k, err := Generate()
	if err != nil {
		return nil, false, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, false, err
	}
	err = createOnly(path, []byte(EncodeKey(k.Priv.Seed())+"\n"))
	if os.IsExist(err) { // another process made it first: use its key
		k, err = loadKey(path)
		return k, false, err
	}
	if err != nil {
		return nil, false, err
	}
	return k, true, nil
}

func loadKey(path string) (*Key, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	seed, err := DecodeKey(string(b))
	if err != nil {
		return nil, fmt.Errorf("%s: %v", path, err)
	}
	return FromSeed(seed)
}

// createOnly writes a new file whole, with mode 0600, and fails with an "exists" error if
// path is already there: the content is written to a temporary file that is then
// hard-linked to path (a link never replaces a file), so a reader never sees it half
// written. Where links are not possible (some file systems), it is created exclusively.
func createOnly(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-"+filepath.Base(path)+"-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	_, err = tmp.Write(data)
	if err == nil {
		err = tmp.Chmod(0o600)
	}
	if err == nil {
		err = tmp.Sync()
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	if err = os.Link(name, path); err == nil || os.IsExist(err) {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

// Certificate builds a self-signed TLS certificate for the key. Trust is NOT based on the
// certificate chain but on the embedded public key (pinned / registry lookup).
func (k *Key) Certificate() (tls.Certificate, error) {
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: k.ID()},
		NotBefore:    time.Unix(0, 0),
		NotAfter:     time.Date(9999, 1, 1, 0, 0, 0, 0, time.UTC),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, k.Pub, k.Priv)
	if err != nil {
		return tls.Certificate{}, err
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: k.Priv}, nil
}

// PeerKey extracts the Ed25519 public key from a raw peer certificate.
func PeerKey(raw []byte) (ed25519.PublicKey, error) {
	cert, err := x509.ParseCertificate(raw)
	if err != nil {
		return nil, err
	}
	pub, ok := cert.PublicKey.(ed25519.PublicKey)
	if !ok {
		return nil, errors.New("peer certificate is not Ed25519")
	}
	return pub, nil
}

// Credential is what a client needs to join a network.
type Credential struct {
	Name          string // human label chosen by the admin
	Key           *Key   // client key (issued mode) - nil when the client generates its own
	ServerPub     ed25519.PublicKey
	ServerAddress string // host:port for direct mode
	RelayAddress  string // host:port of the relay (relay mode), may be empty
	JoinToken     string // register mode only
	// Preset: client settings suggested by the admin ("resources.cores" -> "4"), applied by
	// 'matriline-client init --credential' to a new client.conf; the client may change them.
	Preset map[string]string
}

// WriteCredential stores a credential file with mode 0600.
func WriteCredential(path string, c *Credential) error {
	var b strings.Builder
	switch {
	case c.Key != nil:
		b.WriteString("# Matriline client credential. KEEP SECRET: anyone holding this file can act as\n")
		b.WriteString("# this client. The server admin can revoke it at any time.\n")
	case c.JoinToken != "":
		b.WriteString("# Matriline one-time credential. Keep it private until it is used: on its first\n")
		b.WriteString("# connection the client creates its own key (state/client.key) and this ticket\n")
		b.WriteString("# stops working.\n")
	default:
		b.WriteString("# Matriline client credential: the server's identity and address. This client's\n")
		b.WriteString("# own key is in state/client.key: keep that file private.\n")
	}
	b.WriteString("[credential]\n")
	fmt.Fprintf(&b, "name = %q\n", c.Name)
	if c.Key != nil {
		fmt.Fprintf(&b, "client_id = %s\nprivate_key = %s\n", c.Key.ID(), EncodeKey(c.Key.Priv.Seed()))
	}
	fmt.Fprintf(&b, "server_id = %s\nserver_pubkey = %s\nserver_address = %s\n",
		IDOf(c.ServerPub), EncodeKey(c.ServerPub), c.ServerAddress)
	if c.RelayAddress != "" {
		fmt.Fprintf(&b, "relay_address = %s\n", c.RelayAddress)
	}
	if c.JoinToken != "" {
		fmt.Fprintf(&b, "join_token = %s\n", c.JoinToken)
	}
	if len(c.Preset) > 0 {
		b.WriteString("\n# Client settings suggested by the server admin, applied by\n")
		b.WriteString("# 'matriline-client init <dir> --credential <this file>'. You can change them later.\n")
		b.WriteString("[preset]\n")
		keys := make([]string, 0, len(c.Preset))
		for k := range c.Preset {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Fprintf(&b, "%s = %q\n", k, c.Preset[k])
		}
	}
	return WriteFileAtomic(path, []byte(b.String()), 0o600)
}

// ReadCredential parses a credential file.
func ReadCredential(path string) (*Credential, error) {
	cf, err := conf.Load(path)
	if err != nil {
		return nil, err
	}
	c := &Credential{
		Name:          cf.String("credential.name", ""),
		ServerAddress: cf.String("credential.server_address", ""),
		RelayAddress:  cf.String("credential.relay_address", ""),
		JoinToken:     cf.String("credential.join_token", ""),
	}
	if s := cf.String("credential.private_key", ""); s != "" {
		seed, err := DecodeKey(s)
		if err != nil {
			return nil, fmt.Errorf("%s: private_key: %v", path, err)
		}
		if c.Key, err = FromSeed(seed); err != nil {
			return nil, err
		}
	}
	pub, err := DecodeKey(cf.String("credential.server_pubkey", ""))
	if err != nil || len(pub) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("%s: missing or bad server_pubkey", path)
	}
	c.ServerPub = pub
	if id := cf.String("credential.server_id", ""); id != "" && id != IDOf(pub) {
		return nil, fmt.Errorf("%s: server_id does not match server_pubkey", path)
	}
	for _, k := range cf.Keys() {
		if name, ok := strings.CutPrefix(k, "preset."); ok {
			if c.Preset == nil {
				c.Preset = map[string]string{}
			}
			c.Preset[name] = cf.String(k, "")
		}
	}
	return c, nil
}

// WriteFileAtomic writes via a temporary file + fsync + rename, so a power cut never leaves
// a half-written file behind.
func WriteFileAtomic(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".tmp-"+filepath.Base(path)+"-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(name, path); err != nil {
		return err
	}
	return SyncDir(dir)
}

// SyncDir fsyncs a directory so a rename inside it is durable (no-op where unsupported).
func SyncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	_ = d.Sync()
	return nil
}
