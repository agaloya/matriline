package main

import (
	"html/template"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// Every command of the catalogue that goes to the running server is known to its dispatcher.
func TestCatalogueMatchesDispatcher(t *testing.T) {
	dir := t.TempDir()
	if err := cmdInit(dir, "en"); err != nil {
		t.Fatal(err)
	}
	s, err := openServer(filepath.Join(dir, "server.conf"))
	if err != nil {
		t.Fatal(err)
	}
	s.stopReq = make(chan struct{}, 1)
	for _, c := range catalogue {
		if c.Local {
			continue
		}
		w := strings.Fields(c.Words)
		if _, err := s.admin(w[0], w[1:]); err != nil && strings.Contains(err.Error(), "unknown command") {
			t.Errorf("%s: %v", c.Words, err)
		}
		if got, ok := lookup(w); !ok || got.Words != c.Words {
			t.Errorf("lookup(%q) = %q", c.Words, got.Words)
		}
	}
}

func TestCommandLine(t *testing.T) {
	keys, _ := lookup([]string{"keys", "issue"})
	got, err := keys.commandLine([]string{"lab", "/tmp/lab.cred", "3", ""})
	if want := []string{"keys", "issue", "lab", "/tmp/lab.cred", "--uses", "3"}; err != nil || !reflect.DeepEqual(got, want) {
		t.Errorf("keys issue: %q %v", got, err)
	}
	clean, _ := lookup([]string{"clean"})
	got, _ = clean.commandLine([]string{"yes", "*.tmp", ""})
	if want := []string{"clean", "--dry-run", "*.tmp"}; !reflect.DeepEqual(got, want) {
		t.Errorf("clean: %q", got)
	}
	if _, err := clean.commandLine([]string{"", "", ""}); err == nil {
		t.Error("clean without a glob accepted")
	}
	if c, ok := lookup([]string{"clients", "disable", "x"}); !ok || c.Words != "clients disable" {
		t.Errorf("longest match: %q", c.Words)
	}
}

func TestSplitLine(t *testing.T) {
	for in, want := range map[string][]string{
		`status input/1list`:                 {"status", "input/1list"},
		`clean --dry-run "*.tmp"  x`:         {"clean", "--dry-run", "*.tmp", "x"},
		`add '/a b/c' input/my\ dir`:         {"add", "/a b/c", "input/my dir"},
		`clients disable lab "it's \"old\""`: {"clients", "disable", "lab", `it's "old"`},
		``:                                   nil,
	} {
		got, err := splitLine(in)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Errorf("splitLine(%q) = %q, %v", in, got, err)
		}
		if back, _ := splitLine(quoteLine(want)); len(want) > 0 && !reflect.DeepEqual(back, want) {
			t.Errorf("quoteLine(%q) = %q does not read back", want, quoteLine(want))
		}
	}
	for in, want := range map[string]string{"weird/a;id": `'weird/a;id'`, "a\nb": "'a\nb'", "input/x-1.inp": "input/x-1.inp"} {
		if got := quoteLine([]string{in}); got != want {
			t.Errorf("quoteLine(%q) = %s", in, got)
		}
	}
	if got := absArgs([]string{"keys", "issue", "--uses", "3", "alice", "f.cred"}); got[3] != "3" || !filepath.IsAbs(got[5]) {
		t.Errorf("absArgs keys issue: %q", got)
	}
	if _, err := splitLine(`add "x`); err == nil {
		t.Error("unclosed quote accepted")
	}
}

func TestWebAccess(t *testing.T) {
	w := newWebUI(filepath.Join(t.TempDir(), "server.conf"), "8484")
	w.loginTok, w.session, w.csrf = "tok123", "sess456", "csrf789"
	h := w.routes()
	do := func(method, target, host string, cookie bool, form url.Values) *httptest.ResponseRecorder {
		var r *http.Request
		if form != nil {
			r = httptest.NewRequest(method, target, strings.NewReader(form.Encode()))
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		} else {
			r = httptest.NewRequest(method, target, nil)
		}
		r.Host = host
		if cookie {
			r.AddCookie(&http.Cookie{Name: w.cookie(), Value: "sess456"})
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		return rec
	}
	const lo = "127.0.0.1:8484"
	if rec := do("GET", "/", lo, false, nil); rec.Code != http.StatusForbidden {
		t.Errorf("no cookie: %d", rec.Code)
	}
	if rec := do("GET", "/login?t=bad", lo, false, nil); rec.Code != http.StatusForbidden {
		t.Errorf("bad login: %d", rec.Code)
	}
	if rec := do("GET", "/login?t=tok123", lo, false, nil); rec.Code != http.StatusSeeOther || !strings.Contains(rec.Header().Get("Set-Cookie"), "sess456") || !strings.Contains(rec.Header().Get("Set-Cookie"), "SameSite=Strict") {
		t.Errorf("login: %d %q", rec.Code, rec.Header().Get("Set-Cookie"))
	}
	if rec := do("GET", "/", "evil.example:8484", true, nil); rec.Code != http.StatusMisdirectedRequest {
		t.Errorf("rebinding host: %d", rec.Code)
	}
	if rec := do("GET", "/", lo, true, nil); rec.Code != 200 || !strings.Contains(rec.Body.String(), "not running") {
		t.Errorf("home: %d", rec.Code)
	}
	if rec := do("GET", "/cmd?c=keys+issue", "localhost:8484", true, nil); rec.Code != 200 || !strings.Contains(rec.Body.String(), "--uses") {
		t.Errorf("form: %d", rec.Code)
	}
	if rec := do("GET", "/cmd?c=edit", lo, true, nil); rec.Code != http.StatusNotFound {
		t.Errorf("terminal-only command offered: %d", rec.Code)
	}
	if rec := do("GET", "/cmd?c=config+edit", lo, true, nil); rec.Code != http.StatusSeeOther {
		t.Errorf("config edit form: %d", rec.Code)
	}
	if rec := do("POST", "/run", lo, true, url.Values{"c": {"config edit"}, "token": {"csrf789"}}); rec.Code != http.StatusNotFound {
		t.Errorf("config edit through /run: %d", rec.Code)
	}
	if rec := do("POST", "/config", lo, true, url.Values{"conf": {"x"}}); rec.Code != http.StatusForbidden {
		t.Errorf("config save without form token: %d", rec.Code)
	}
	if rec := do("POST", "/run", lo, true, url.Values{"c": {"stats"}}); rec.Code != http.StatusForbidden {
		t.Errorf("post without form token: %d", rec.Code)
	}
	if rec := do("POST", "/run", lo, true, url.Values{"c": {"stop"}, "token": {"csrf789"}}); rec.Code != http.StatusNotFound {
		t.Errorf("stop from the web: %d", rec.Code)
	}
	rec := do("POST", "/run", lo, true, url.Values{"c": {"cancel"}, "token": {"csrf789"}, "a0": {"input/x"}})
	if !strings.Contains(rec.Body.String(), "yes, run it") || strings.Contains(rec.Body.String(), `class="line"`) {
		t.Error("risky command ran without confirmation")
	}
	rec = do("POST", "/run", lo, true, url.Values{"c": {"stats"}, "token": {"csrf789"}})
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "server is not running") {
		t.Errorf("run: %d %s", rec.Code, rec.Body.String())
	}
	// the session cookie is not the form token (a cookie leaked to another local port
	// must not be enough to post)
	if rec := do("POST", "/run", lo, true, url.Values{"c": {"stats"}, "token": {"sess456"}}); rec.Code != http.StatusForbidden {
		t.Errorf("cookie value accepted as form token: %d", rec.Code)
	}
	w.expires = time.Now().Add(-time.Second)
	if rec := do("GET", "/", lo, true, nil); rec.Code != http.StatusForbidden {
		t.Errorf("expired session: %d", rec.Code)
	}
}

func TestChartsOf(t *testing.T) {
	out, charts := chartsOf("tasks: 3\nchart: queued=2 running=1 output=3 weird=0 errors=0 paused=0 cancelled=0\nclients")
	if out != "tasks: 3\nclients" || len(charts) != 1 || charts[0].Total != 6 || charts[0].Segs[2].Path == "" || charts[0].Segs[3].Path != "" {
		t.Fatalf("%q %+v", out, charts)
	}
	_, charts = chartsOf("chart 1list: queued=0 running=0 output=4 weird=0 errors=0 paused=0 cancelled=0")
	if charts[0].Title != "1list" || !strings.HasPrefix(charts[0].Segs[2].Path, "M 50 5 A") {
		t.Fatalf("%+v", charts)
	}
}

// MATRILINE_HIDE: an embedding program leaves out commands (and whole groups).
func TestHideCommands(t *testing.T) {
	saved := catalogue
	defer func() { catalogue = saved }()
	hideCommands("clients, keys,config,block,unblock,bans,kit")
	for _, w := range [][]string{{"clients", "list"}, {"keys", "issue"}, {"config", "edit"}} {
		if _, ok := lookup(w); ok {
			t.Errorf("%v still listed", w)
		}
	}
	if _, ok := lookup([]string{"status"}); !ok {
		t.Error("status hidden")
	}
	if strings.Contains(usageText(), "Clients and keys") {
		t.Error("an empty group still in the help")
	}
}

// Every command meant for the web page appears in its menus and opens its own form (user:
// a release check). Commands that need a terminal are NoWeb and must not appear.
func TestWebShowsEveryCommand(t *testing.T) {
	dir := t.TempDir()
	if err := cmdInit(dir, "en"); err != nil {
		t.Fatal(err)
	}
	w := newWebUI(filepath.Join(dir, "server.conf"), "8484")
	w.session = "sess"
	h := w.routes()
	get := func(target string) string {
		r := httptest.NewRequest("GET", target, nil)
		r.Host = "127.0.0.1:8484"
		r.AddCookie(&http.Cookie{Name: w.cookie(), Value: "sess"})
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		if rec.Code != 200 && rec.Code != 303 {
			t.Errorf("%s: %d", target, rec.Code)
		}
		return rec.Body.String()
	}
	menus := ""
	for _, g := range groups {
		menus += get("/g?name=" + url.QueryEscape(g))
	}
	for _, c := range catalogue {
		inMenu := strings.Contains(menus, `href="/cmd?c=`+strings.ReplaceAll(c.Words, " ", "%20")+`"`)
		if c.NoWeb {
			if inMenu {
				t.Errorf("%q needs a terminal but is on the web page", c.Words)
			}
			continue
		}
		if !inMenu {
			t.Errorf("%q is not in the web page's menus", c.Words)
		}
		body := get("/cmd?c=" + url.QueryEscape(c.Words))
		if c.Words != "add" && !c.Local && !strings.Contains(body, "<h1>"+template.HTMLEscapeString(c.Words)+"</h1>") {
			t.Errorf("%q: its form does not open", c.Words)
		}
	}
}

// TestYes: the consoles' confirmations in every language (y/N, s/N, o/N).
func TestYes(t *testing.T) {
	for _, v := range []string{"y", "yes", "Y", "s", "sí", "sim", "o", "oui"} {
		if !yes(v) {
			t.Errorf("%q is not yes", v)
		}
	}
	for _, v := range []string{"", "n", "no", "não", "non", "N"} {
		if yes(v) {
			t.Errorf("%q is yes", v)
		}
	}
}

// Uploaded names may carry their path inside a dropped folder; nothing may climb out of it.
func TestUploadPath(t *testing.T) {
	for in, want := range map[string]string{
		"mol.inp": "mol.inp", "batch1/sub/mol.inp": "batch1/sub/mol.inp", `batch1\mol.xyz`: "batch1/mol.xyz",
		"../x.inp": "", "a/../../x.inp": "", "/etc/x.inp": "", "a/./x.inp": "", ".hidden/x.inp": "",
		"a/x.out": "", "C:/x.inp": "", "a/b/c/d/e/f/g/h/x.inp": "", "con:x.inp": "",
		"CON.inp": "", "nul.xyz": "", "CON/a.inp": "", "lpt1/a.inp": "", "a./b.inp": "", "a../b.inp": "",
		"MOL.INP": "MOL.INP", "console/a.inp": "console/a.inp",
	} {
		got, ok := uploadPath(in)
		if (want == "") == ok || got != want {
			t.Errorf("uploadPath(%q) = %q, %v; want %q", in, got, ok, want)
		}
	}
}

// A symbolic link inside input/ is not written through by 'add' (code review).
func TestLinkOnTheWay(t *testing.T) {
	base := t.TempDir()
	outside := t.TempDir()
	os.MkdirAll(filepath.Join(base, "real"), 0o755)
	if err := os.Symlink(outside, filepath.Join(base, "link")); err != nil {
		t.Skip("no symbolic links here")
	}
	if l := linkOnTheWay(base, filepath.Join(base, "link", "a.inp")); l != filepath.Join(base, "link") {
		t.Errorf("link not found: %q", l)
	}
	if l := linkOnTheWay(base, filepath.Join(base, "real", "new", "a.inp")); l != "" {
		t.Errorf("plain path refused: %q", l)
	}
}
