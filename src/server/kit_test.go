package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/agaloya/matriline/common/ident"
	"github.com/agaloya/matriline/common/release"
)

// The kit takes the clients of this server's own version from its signed release, and
// refuses a file whose hash is not the signed one.
func TestKitClientsFromSignedRelease(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	saved := release.TrustedKeys
	release.TrustedKeys = []string{ident.EncodeKey(pub)}
	defer func() { release.TrustedKeys = saved }()
	version := release.VersionOf(agentVersion)
	files := map[string][]byte{}
	sums := map[string]string{}
	for _, t := range append(kitUnixTargets, [3]string{"windows", "amd64", ""}) {
		n := release.AssetName("client", t[0], t[1])
		files[n] = []byte("client for " + n)
		h := sha256.Sum256(files[n])
		sums[n] = hex.EncodeToString(h[:])
	}
	body, _ := json.Marshal(release.Manifest{Version: version, Commit: "test", Files: sums, Expires: time.Now().Add(30 * 24 * time.Hour).Format("2006-01-02")})
	sig := release.Sign(body, priv)
	tampered := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		prefix := "/download/v" + version + "/"
		name := strings.TrimPrefix(r.URL.Path, prefix)
		switch {
		case !strings.HasPrefix(r.URL.Path, prefix):
			http.NotFound(w, r)
		case name == release.ManifestName:
			w.Write(body)
		case name == release.SigName:
			w.Write(sig)
		case files[name] != nil:
			b := files[name]
			if tampered {
				b = []byte("something else")
			}
			w.Write(b)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	get, err := kitClientSource(&Config{UpdateURL: srv.URL}, "", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	b, err := get("linux", "riscv64")
	if err != nil || !bytes.Equal(b, files["matriline-client-linux-riscv64"]) {
		t.Fatalf("download: %q %v", b, err)
	}
	tampered = true
	if _, err := get("windows", "amd64"); err == nil {
		t.Fatal("a file that is not the signed one was accepted")
	}
}
