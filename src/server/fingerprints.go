package main

import (
	"embed"
	"path"
	"strings"
)

// Fingerprints of the official ORCA builds, compiled in (orca.accepted_fingerprints =
// builtin): the admin needs no ORCA, and no file of other systems' builds, to accept
// clients with any official build of the campaign's version. Only SHA-256 hashes of each
// build's files: no part of ORCA (see docs/ORCA_LICENSE.md). Made with
// "matriline-client fingerprint" from the unpacked official packages
// (fingerprints/README.md lists them).
//
//go:embed fingerprints/*.json
var builtinFingerprints embed.FS

// builtinFor returns name -> JSON of the built-in fingerprints of ORCA version v.
func builtinFor(v string) map[string][]byte {
	out := map[string][]byte{}
	entries, _ := builtinFingerprints.ReadDir("fingerprints")
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "orca-"+v+"-") {
			if raw, err := builtinFingerprints.ReadFile(path.Join("fingerprints", e.Name())); err == nil {
				out["builtin:"+e.Name()] = raw
			}
		}
	}
	return out
}
