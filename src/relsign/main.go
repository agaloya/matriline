// relsign - the maintainer's release signing tool (D66). Not shipped to users.
//
//	relsign keygen <key-file>        create the release key (keep it off GitHub and off any
//	                                 server; a USB stick or a password manager) and print the
//	                                 public key line for src/common/release/keys.go
//	relsign sign <key-file> <dist>   sign the binaries tools/release.sh built in <dist>:
//	                                 writes release.json and release.json.sig there
//	relsign verify <dist>            check <dist> as a program built from this source would
//
// Publishing: upload every file of <dist> as assets of a GitHub release (gh release create
// v<version> <dist>/*). Servers with update.mode = alert or auto find it at
// .../releases/latest/download/.
package main

import (
	"bufio"
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/agaloya/matriline/common/ident"
	"github.com/agaloya/matriline/common/release"
)

func main() {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "usage: relsign keygen <key-file> | sign <key-file> <dist> | verify <dist>")
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "keygen":
		err = keygen(os.Args[2])
	case "sign":
		if len(os.Args) < 4 {
			err = fmt.Errorf("usage: relsign sign <key-file> <dist>")
		} else {
			err = sign(os.Args[2], os.Args[3])
		}
	case "verify":
		err = verify(os.Args[2])
	default:
		err = fmt.Errorf("unknown command %q", os.Args[1])
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func keygen(path string) error {
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("%s exists: a new key would make every installed program refuse your releases", path)
	}
	k, created, err := ident.LoadOrCreate(path)
	if err != nil {
		return err
	}
	if !created {
		return fmt.Errorf("%s was not created", path)
	}
	fmt.Printf("release key written to %s (keep a copy somewhere safe and off GitHub)\n", path)
	fmt.Printf("put this line in src/common/release/keys.go, TrustedKeys:\n\t%q,\n", ident.EncodeKey(k.Pub))
	return nil
}

// distInfo reads the VERSION file and the SHA256SUMS that tools/release.sh wrote.
func distInfo(dist string) (*release.Manifest, error) {
	now := time.Now().UTC()
	m := &release.Manifest{Files: map[string]string{}, Date: now.Format("2006-01-02"), Expires: now.Add(release.Validity).Format("2006-01-02")}
	b, err := os.ReadFile(filepath.Join(dist, "VERSION"))
	if err != nil {
		return nil, fmt.Errorf("%v (build with tools/release.sh)", err)
	}
	for _, l := range strings.Split(string(b), "\n") {
		if k, v, ok := strings.Cut(strings.TrimSpace(l), " "); ok {
			switch k {
			case "version":
				m.Version = v
			case "commit":
				m.Commit = v
			}
		}
	}
	f, err := os.Open(filepath.Join(dist, "SHA256SUMS"))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		sum, name, ok := strings.Cut(sc.Text(), "  ")
		if !ok || len(sum) != 64 {
			continue
		}
		m.Files[name] = sum
	}
	if m.Version == "" || m.Commit == "" || len(m.Files) == 0 {
		return nil, fmt.Errorf("%s: no version, commit or files", dist)
	}
	return m, sc.Err()
}

func sign(keyPath, dist string) error {
	if _, err := os.Stat(keyPath); err != nil {
		return err
	}
	k, _, err := ident.LoadOrCreate(keyPath)
	if err != nil {
		return err
	}
	m, err := distInfo(dist)
	if err != nil {
		return err
	}
	trusted := false
	for _, t := range release.TrustedKeys {
		trusted = trusted || t == ident.EncodeKey(k.Pub)
	}
	if !trusted {
		return fmt.Errorf("this key is not in src/common/release/keys.go: programs built from this source would refuse the release (add %q and rebuild first)", ident.EncodeKey(k.Pub))
	}
	body, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	sig := release.Sign(body, ed25519.PrivateKey(k.Priv))
	if err := os.WriteFile(filepath.Join(dist, release.ManifestName), body, 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dist, release.SigName), sig, 0o644); err != nil {
		return err
	}
	fmt.Printf("signed release %s (commit %s, %d files, installable until %s) in %s\nPublish: gh release create v%s %s/*\n", m.Version, m.Commit, len(m.Files), m.Expires, dist, m.Version, dist)
	return nil
}

func verify(dist string) error {
	body, err := os.ReadFile(filepath.Join(dist, release.ManifestName))
	if err != nil {
		return err
	}
	sig, err := os.ReadFile(filepath.Join(dist, release.SigName))
	if err != nil {
		return err
	}
	m, err := release.Verify(body, sig)
	if err != nil {
		return err
	}
	fmt.Printf("OK: release %s (commit %s, %s), %d files signed by a trusted key\n", m.Version, m.Commit, m.Date, len(m.Files))
	return nil
}
