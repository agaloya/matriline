package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agaloya/matriline/common/ledger"
	"github.com/agaloya/matriline/common/manifest"
	"github.com/agaloya/matriline/common/wire"
)

// A slim ledger entry (D61) checks as much as a full one: a changed result file, a
// changed manifest and a deleted manifest are all reported.
func TestSlimLedgerVerifies(t *testing.T) {
	for _, tamper := range []string{"none", "out", "manifest", "nomanifest", "names"} {
		for _, top := range []string{"output", "weird"} {
			dir := t.TempDir()
			if err := cmdInit(dir, "en"); err != nil {
				t.Fatal(err)
			}
			s, err := openServer(filepath.Join(dir, "server.conf"))
			if err != nil {
				t.Fatal(err)
			}
			root := s.conf().Root
			res := filepath.Join(root, top, "x", "a")
			os.MkdirAll(res, 0o755)
			files := map[string]string{"job1234567890.out": "energy -1.0\n", "job1234567890.gbw": "orbitals"}
			var metas []wire.FileMeta
			for anon, body := range files {
				h := sha256.Sum256([]byte(body))
				metas = append(metas, wire.FileMeta{Name: anon, Size: int64(len(body)), SHA256: hex.EncodeToString(h[:])})
				os.WriteFile(filepath.Join(res, strings.Replace(anon, "job1234567890", "a", 1)), []byte(body), 0o644)
			}
			body, _ := json.Marshal(manifest.Manifest{Format: 1, Files: metas})
			signed, _ := json.Marshal(manifest.Signed{Body: body})
			os.WriteFile(filepath.Join(res, manifestFile), signed, 0o644)
			os.WriteFile(filepath.Join(res, namesFile), []byte("job1234567890.out=a.out\njob1234567890.gbw=a.gbw\n"), 0o644)
			os.WriteFile(filepath.Join(res, "a.inp"), []byte("! HF\n"), 0o644) // the server's copy: not in the manifest
			s.ledgerDir("accept", top+"/x/a", "test")
			es, _ := ledger.Read(filepath.Join(root, dState, "ledger.log"), s.key.Pub)
			last := es[len(es)-1]
			if last.FromManifest != top+"/x/a/"+manifestFile || len(last.Files) != 3 { // manifest, names, input
				t.Fatalf("not slim: %+v", last)
			}
			switch tamper {
			case "out":
				os.WriteFile(filepath.Join(res, "a.out"), []byte("energy -2.0\n"), 0o644)
			case "manifest":
				os.WriteFile(filepath.Join(res, manifestFile), append(signed, ' '), 0o644)
			case "nomanifest":
				os.Remove(filepath.Join(res, manifestFile))
			case "names":
				os.WriteFile(filepath.Join(res, namesFile), []byte("job1234567890.out=a.gbw\njob1234567890.gbw=a.out\n"), 0o644)
			}
			out, _ := verifySpool(root, s.key)
			consistent := strings.Contains(out, "RESULT: spool consistent")
			if consistent != (tamper == "none") {
				t.Errorf("%s in %s: %s", tamper, top, out)
			}
			if tamper == "none" && !strings.Contains(out, "5 intact") {
				t.Errorf("not every file checked: %s", out)
			}
		}
	}
}
