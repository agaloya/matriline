package main

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"html/template"
	"math"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/agaloya/matriline/common/i18n"
	"github.com/agaloya/matriline/common/ident"
)

// cmdWeb serves the web interface: a front end of the command catalogue, nothing more.
// Every form builds a command line and runs it through the admin socket exactly as the
// command line does; commands that need a terminal (edit, config edit, restore) or would
// stop the server under the page are left out.
//
// Only this computer can reach it (loopback address; from elsewhere use an SSH tunnel), and
// only with the session token printed at start: other local users and other web sites
// cannot drive it (cookie SameSite=Strict, a token field in every form, Host header checked
// against DNS rebinding). No JavaScript, no external files.
func cmdWeb(cfgPath, addr string) error {
	auto := addr == ""
	if auto {
		addr = defaultWebAddr()
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("address %q: %v (e.g. 127.0.0.1:8484)", addr, err)
	}
	if ip := net.ParseIP(host); host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return fmt.Errorf("the web interface only listens on this computer (127.0.0.1, ::1); from another computer use an SSH tunnel: ssh -L %s:127.0.0.1:%s <server>", port, port)
	}
	ln, err := net.Listen("tcp", addr)
	for p := 8485; err != nil && auto && p < 8500; p++ { // another web page uses it (several servers)
		port = strconv.Itoa(p)
		ln, err = net.Listen("tcp", net.JoinHostPort(host, port))
	}
	if err != nil {
		return err
	}
	w := newWebUI(cfgPath, port)
	fmt.Printf("Matriline web interface: open\n  http://%s/login?t=%s\n(valid for %v; stop it with Ctrl-C)\n", ln.Addr(), w.loginTok, webSessionTTL)
	srv := &http.Server{Handler: w.routes(), ReadHeaderTimeout: 10 * time.Second}
	return srv.Serve(ln)
}

// defaultWebAddr is a random address in 127.0.0.0/8 on Linux (every one of them is local
// there): browsers send a cookie to every port of its host, so on the shared 127.0.0.1 a
// page served by another local user on another port could receive the session cookie
// (security review of D52). Elsewhere only 127.0.0.1 is configured by default.
func defaultWebAddr() string {
	if runtime.GOOS != "linux" {
		return "127.0.0.1:8484"
	}
	b := make([]byte, 3)
	rand.Read(b)
	return fmt.Sprintf("127.%d.%d.%d:8484", 1+int(b[0])%254, b[1], 1+int(b[2])%254)
}

// webSessionTTL: the login link and the session end after this time; restart 'web' then.
const webSessionTTL = 12 * time.Hour

type webUI struct {
	cfgPath, port string
	// editPath: the file the configuration page edits (server.conf, or MATRILINE_WEB_CONF of
	// a program that embeds Matriline); apply: that program (MATRILINE_WEB_APPLY), run as
	// "<apply> web-config-check <file>" and "<apply> web-config-apply" instead of Matriline's
	// own validation and 'config reload'
	editPath, apply string
	css             string // the colour variables of the configured palette, then webCSS
	molRotate       string // the 3D view: seconds per turn ("0" still), background colour
	molBg           string
	// three separate secrets: the printed login link, the session cookie and the form
	// field; a leaked cookie does not reveal the form token, nor the reverse
	loginTok, session, csrf string
	expires                 time.Time
	confMu                  sync.Mutex // one configuration save at a time
}

func newWebUI(cfgPath, port string) *webUI {
	r := func() string {
		b := make([]byte, 24)
		rand.Read(b)
		return hex.EncodeToString(b)
	}
	w := &webUI{cfgPath: cfgPath, editPath: cfgPath, port: port, loginTok: r(), session: r(), csrf: r(), expires: time.Now().Add(webSessionTTL)}
	cfg := &Config{WebPalette: "A"}
	if c, _, err := loadConfig(cfgPath); err == nil {
		cfg = c
	}
	colors := webColors(cfg)
	w.css = paletteCSS(colors) + webCSS
	w.molRotate = strconv.FormatFloat(cfg.MolRotation.Seconds(), 'f', -1, 64)
	switch {
	case cfg.MolBackground != "" && cfg.MolBackground != "auto":
		w.molBg = "#" + cfg.MolBackground
	case darkPalette(colors):
		w.molBg = "#000000"
	default:
		w.molBg = "#ffffff"
	}
	if e, a := os.Getenv("MATRILINE_WEB_CONF"), os.Getenv("MATRILINE_WEB_APPLY"); e != "" && a != "" {
		w.editPath, w.apply = e, a
	}
	return w
}

// webCookie is per port: browsers send a cookie of 127.0.0.1 to every port, so two web
// pages (two servers on one computer) would replace each other's session.
func (w *webUI) cookie() string { return "matriline_web_" + w.port }

func (w *webUI) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /login", w.login)
	mux.HandleFunc("GET /style.css", func(rw http.ResponseWriter, r *http.Request) {
		rw.Header().Set("Content-Type", "text/css")
		rw.Write([]byte(w.css))
	})
	mux.HandleFunc("GET /{$}", w.auth(w.home))
	mux.HandleFunc("GET /cmd", w.auth(w.form))
	mux.HandleFunc("GET /g", w.auth(w.groupPage))
	mux.HandleFunc("GET /live", w.auth(w.livePage))
	mux.HandleFunc("GET /mol", w.auth(w.molPage))
	mux.HandleFunc("GET /3dmol.js", w.auth(serveJS(lib3Dmol)))
	mux.HandleFunc("GET /viewer.js", w.auth(serveJS([]byte(viewerJS))))
	mux.HandleFunc("GET /add.js", w.auth(serveJS(addJS)))
	mux.HandleFunc("GET /picker.js", w.auth(serveJS(pickerJS)))
	mux.HandleFunc("GET /3dmol-license.txt", func(rw http.ResponseWriter, r *http.Request) {
		rw.Header().Set("Content-Type", "text/plain; charset=utf-8")
		rw.Write(license3Dmol)
	})
	mux.HandleFunc("POST /run", w.auth(w.run))
	mux.HandleFunc("GET /add", w.auth(w.addPage))
	mux.HandleFunc("POST /add", w.auth(w.addSave))
	mux.HandleFunc("GET /config", w.auth(w.settingsPage))
	mux.HandleFunc("POST /config", w.auth(w.configSave)) // the text editor
	mux.HandleFunc("POST /settings", w.auth(w.settingsSave))
	mux.HandleFunc("POST /restart", w.auth(w.restartServer))
	return w.guard(mux)
}

// guard sets the security headers and refuses requests addressed to another host name
// (a page on the Internet that resolves its own name to 127.0.0.1).
func (w *webUI) guard(h http.Handler) http.Handler {
	return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		hn, p, err := net.SplitHostPort(r.Host)
		if err != nil && w.port == "80" { // browsers leave out the default port
			hn, p, err = strings.Trim(r.Host, "[]"), "80", nil
		}
		if err != nil || p != w.port || (hn != "localhost" && !isLoopbackText(hn)) {
			http.Error(rw, "wrong host", http.StatusMisdirectedRequest)
			return
		}
		hd := rw.Header()
		hd.Set("Content-Security-Policy", "default-src 'none'; style-src 'self'; img-src 'self' data:; form-action 'self'; frame-ancestors 'none'")
		hd.Set("X-Content-Type-Options", "nosniff")
		hd.Set("Referrer-Policy", "no-referrer")
		hd.Set("Cache-Control", "no-store")
		h.ServeHTTP(rw, r)
	})
}

func isLoopbackText(h string) bool {
	ip := net.ParseIP(strings.Trim(h, "[]"))
	return ip != nil && ip.IsLoopback()
}

func eqSecret(a, b string) bool { return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1 }

func (w *webUI) live() bool { return time.Now().Before(w.expires) }

func (w *webUI) login(rw http.ResponseWriter, r *http.Request) {
	if !w.live() || !eqSecret(r.URL.Query().Get("t"), w.loginTok) {
		http.Error(rw, "wrong or old link: use the one printed by 'matriline-server web'", http.StatusForbidden)
		return
	}
	http.SetCookie(rw, &http.Cookie{Name: w.cookie(), Value: w.session, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode, Expires: w.expires})
	http.Redirect(rw, r, "/", http.StatusSeeOther)
}

func (w *webUI) auth(h http.HandlerFunc) http.HandlerFunc {
	return func(rw http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(w.cookie())
		if err != nil || !w.live() || !eqSecret(c.Value, w.session) {
			http.Error(rw, "not logged in: use the link printed by 'matriline-server web'", http.StatusForbidden)
			return
		}
		h(rw, r)
	}
}

// ---------------------------------------------------------------------------------------

type webNavGroup struct {
	Name string
	Cmds []cmdSpec
}

type webPage struct {
	Stat                    *statView // Status tab, as HTML
	StatText                string    // the same, as the terminal shows it
	LiveV                   *liveView // Live tab, as HTML
	Title, Version, Warning string
	Nav                     []webNavGroup
	Token                   string
	Refresh                 int       // seconds; 0 = no automatic reload
	Settings                bool      // the configuration page edits another file (MATRILINE_WEB_CONF)
	Tab                     string    // the highlighted tab: Status, Live, a group, Settings
	Group                   []cmdSpec // a group's page: its commands
	Dirs                    []string  // the add page: input/ and its folders
	XYZ, XYZName            string    // the 3D page: the molecule (XYZ) and its task
	MolRotate, MolBg        string    // its rotation (s per turn) and background
	Jobs                    []liveJob // the running jobs to pick from
	// the settings form
	ConfName     string
	SettingsForm []settingGroup
	NeedRestart  bool // a changed option is applied by a restart
	Restarting   bool
	// the live view: job numbers to pick from, the one shown, whole input or not
	Live     []int
	LiveSel  int
	LiveFull bool
	// a command form or a result
	Cmd     *cmdSpec
	Line    string
	Out     string
	Err     string
	Charts  []webChart
	Values  []string
	Lists   [][]string // suggested values per argument
	Running bool
	// the configuration editor
	Conf, ConfSum string
	Notes         []string
}

type webChart struct {
	Title string
	Segs  []webSeg
	Total int
}

type webSeg struct {
	Name, Color, Path string
	N                 int
}

func (w *webUI) page(title string) *webPage {
	p := &webPage{Title: title, Version: versionText(), Token: w.csrf, Settings: w.editPath != w.cfgPath}
	run, err := call(w.cfgPath, []string{"version"})
	switch {
	case err != nil:
		p.Warning = "The server is not running: start it with the system service."
	case strings.TrimSpace(run) != versionText():
		p.Warning = fmt.Sprintf("The running server is %s and this interface %s: restart the server (or this interface) so both are the same build.", strings.TrimSpace(run), versionText())
	default:
		p.Running = true
	}
	for _, g := range groups {
		ng := webNavGroup{Name: g}
		for _, c := range catalogue {
			if c.Group == g && !c.NoWeb {
				ng.Cmds = append(ng.Cmds, c)
			}
		}
		if len(ng.Cmds) > 0 {
			p.Nav = append(p.Nav, ng)
		}
	}
	return p
}

func (w *webUI) home(rw http.ResponseWriter, r *http.Request) {
	p := w.page("Status")
	p.Tab = "Status"
	p.Refresh = 15
	if p.Running {
		js, err := call(w.cfgPath, []string{"status", "--json"})
		if err == nil {
			p.Stat, err = parseStatus(js)
		}
		if err != nil {
			p.Err = err.Error()
		}
		if out, err := call(w.cfgPath, []string{"status"}); err == nil {
			p.StatText, _ = chartsOf(out)
		}
	}
	w.render(rw, p)
}

// livePage shows the live command's view (the same as 'matriline-server watch').
func (w *webUI) livePage(rw http.ResponseWriter, r *http.Request) {
	p := w.page("Live")
	p.Tab = "Live"
	p.Refresh = 5
	p.LiveSel, _ = strconv.Atoi(r.URL.Query().Get("n"))
	p.LiveSel = max(p.LiveSel, 1)
	p.LiveFull = r.URL.Query().Get("full") != ""
	if p.Running {
		args := []string{"live", strconv.Itoa(p.LiveSel)}
		if p.LiveFull {
			args = append(args, "full")
		}
		out, err := call(w.cfgPath, args)
		if err != nil {
			p.Err = err.Error()
		}
		// the table is drawn from live --json; the text keeps the selected job's details
		if i := strings.Index(out, "\n== "); i >= 0 {
			p.Out = strings.TrimLeft(out[i:], "\n")
		} else if !strings.Contains(out, "job(s) running") {
			p.Out = out // e.g. "nothing runs right now"
		}
		if js, err := call(w.cfgPath, []string{"live", "--json"}); err == nil {
			if v, err := parseLive(js); err == nil {
				p.LiveV = v
				for i := range v.Rows {
					p.Live = append(p.Live, v.Rows[i].N)
				}
			}
		}
	}
	w.render(rw, p)
}

func (w *webUI) form(rw http.ResponseWriter, r *http.Request) {
	c, ok := lookup(strings.Fields(r.URL.Query().Get("c")))
	if !ok || c.NoWeb || c.Words != r.URL.Query().Get("c") {
		http.Error(rw, "no such command on the web interface", http.StatusNotFound)
		return
	}
	if c.Local { // config edit: its own page
		http.Redirect(rw, r, "/config", http.StatusSeeOther)
		return
	}
	if c.Words == "add" {
		http.Redirect(rw, r, "/add", http.StatusSeeOther)
		return
	}
	p := w.page(c.Words)
	p.Tab = c.Group
	p.Cmd = &c
	p.Values = make([]string, len(c.Args))
	p.Lists = w.suggestions(c)
	pickerCSP(rw)
	w.render(rw, p)
}

// suggestions lists, for each argument, existing paths of the spool directories it names
// (breadth first, a few levels, at most 400), for the form's datalist.
func (w *webUI) suggestions(c cmdSpec) [][]string {
	lists := make([][]string, len(c.Args))
	root, err := rootOf(w.cfgPath)
	if err != nil {
		return lists
	}
	for i, a := range c.Args {
		var l []string
		if a.Name == "client" { // the clients this server knows
			if out, err := call(w.cfgPath, []string{"clients"}); err == nil {
				for _, ln := range strings.Split(out, "\n")[1:] {
					if f := strings.Fields(ln); len(f) > 0 {
						l = append(l, f[0])
					}
				}
			}
			lists[i] = l
			continue
		}
		queue := append([]string(nil), a.Suggest...)
		for len(queue) > 0 && len(l) < 2000 {
			d := queue[0]
			queue = queue[1:]
			es, err := os.ReadDir(filepath.Join(root, filepath.FromSlash(d)))
			if err != nil {
				continue
			}
			for _, e := range es {
				if strings.HasPrefix(e.Name(), ".") || len(l) >= 2000 {
					continue
				}
				p := d + "/" + e.Name()
				if !e.IsDir() {
					l = append(l, p)
					continue
				}
				l = append(l, p+"/") // a folder (pickTree)
				if strings.Count(p, "/") < 6 {
					queue = append(queue, p)
				}
			}
		}
		if len(l) > 0 && strings.HasSuffix(a.Name, "|all") {
			l = append(l, "all")
		}
		lists[i] = l
	}
	return lists
}

func (w *webUI) run(rw http.ResponseWriter, r *http.Request) {
	if !eqSecret(r.FormValue("token"), w.csrf) {
		http.Error(rw, "form token missing or wrong", http.StatusForbidden)
		return
	}
	c, ok := lookup(strings.Fields(r.FormValue("c")))
	if !ok || c.NoWeb || c.Local || c.Words != r.FormValue("c") {
		http.Error(rw, "no such command on the web interface", http.StatusNotFound)
		return
	}
	p := w.page(c.Words)
	p.Tab = c.Group
	p.Cmd = &c
	p.Values = make([]string, len(c.Args))
	for i := range c.Args {
		p.Values[i] = r.FormValue("a" + strconv.Itoa(i))
	}
	p.Lists = w.suggestions(c)
	pickerCSP(rw)
	args, err := c.commandLine(p.Values)
	switch {
	case err != nil:
		p.Err = err.Error()
	case c.Confirm && r.FormValue("confirm") != "yes":
		p.Err = "tick \"yes, run it\" to run this command"
	default:
		args = absArgs(args)
		p.Line = "matriline-server " + quoteLine(args)
		var out string
		out, err = call(w.cfgPath, args)
		p.Out, p.Charts = chartsOf(out)
		if err != nil {
			p.Err = err.Error()
		}
	}
	w.render(rw, p)
}

// configPage shows server.conf for editing ('config edit' of the command line).
func (w *webUI) configPage(rw http.ResponseWriter, r *http.Request) {
	p := w.page("config edit")
	p.Tab = "Settings"
	b, err := os.ReadFile(w.editPath)
	if err != nil {
		p.Err = err.Error()
	}
	p.Conf, p.ConfSum = string(b), sumHex(b)
	w.render(rw, p)
}

// configSave validates the edited text like 'config edit' does and, when asked and valid,
// saves it and applies it with 'config reload'. An edit made meanwhile by someone else
// (terminal or another tab) is never overwritten.
func (w *webUI) configSave(rw http.ResponseWriter, r *http.Request) {
	if !eqSecret(r.FormValue("token"), w.csrf) {
		http.Error(rw, "form token missing or wrong", http.StatusForbidden)
		return
	}
	w.confMu.Lock() // check, validate and write as one step
	defer w.confMu.Unlock()
	p := w.page("config edit")
	text := strings.ReplaceAll(r.FormValue("conf"), "\r\n", "\n")
	p.Conf, p.ConfSum = text, r.FormValue("sum")
	cur, err := os.ReadFile(w.editPath)
	if err != nil {
		p.Err = err.Error()
		w.render(rw, p)
		return
	}
	name := filepath.Base(w.editPath)
	if sumHex(cur) != p.ConfSum {
		p.Err = name + " was changed by someone else since this page was opened: copy your changes, reload the page and apply them again"
		w.render(rw, p)
		return
	}
	// a private copy next to the file (a relative spool root resolves the same way)
	f, err := os.CreateTemp(filepath.Dir(w.editPath), "."+name+".web-*")
	if err == nil {
		_, err = f.WriteString(text)
		f.Close()
		defer os.Remove(f.Name())
	}
	if err != nil {
		p.Err = err.Error()
		w.render(rw, p)
		return
	}
	var warns []string
	if w.apply != "" { // the embedding program checks its own file
		out, cerr := exec.Command(w.apply, "web-config-check", f.Name()).CombinedOutput()
		if cerr != nil {
			err = fmt.Errorf("%s", strings.TrimSpace(string(out)))
		} else if t := strings.TrimSpace(string(out)); t != "" {
			warns = strings.Split(t, "\n")
		}
	} else {
		_, warns, err = loadConfig(f.Name())
	}
	p.Notes = warns
	switch {
	case err != nil:
		p.Err = err.Error()
	case r.FormValue("save") == "":
		p.Out = "valid; nothing saved yet"
	default:
		if err := ident.WriteFileAtomic(w.editPath, []byte(text), 0o600); err != nil {
			p.Err = err.Error()
			break
		}
		p.ConfSum = sumHex([]byte(text))
		if w.apply != "" {
			p.Line = filepath.Base(w.apply) + " web-config-apply"
			out, err := exec.Command(w.apply, "web-config-apply").CombinedOutput()
			p.Out = "saved. " + strings.TrimSpace(string(out))
			if err != nil {
				p.Err = "saved, but not applied: " + strings.TrimSpace(string(out))
			}
			break
		}
		p.Line = "matriline-server config reload"
		out, err := call(w.cfgPath, []string{"config", "reload"})
		p.Out = "saved. " + out
		if err != nil {
			p.Err = "saved; it will be applied when the server starts: " + err.Error()
		}
	}
	w.render(rw, p)
}

func sumHex(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func (w *webUI) render(rw http.ResponseWriter, p *webPage) {
	rw.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := webTmpl.Execute(rw, p); err != nil {
		fmt.Fprintf(rw, "<p>template error: %s</p>", template.HTMLEscapeString(err.Error()))
	}
}

// chartColors and chartOrder: the spool states as the charts draw them.
var (
	chartColors = map[string]string{"queued": "#9aa5b1", "running": "#3b82f6", "output": "#22a06b",
		"weird": "#e2b203", "errors": "#e34935", "other-versions": "#ec4899",
		"paused": "#a855f7", "cancelled": "#6b7280"}
	chartOrder = []string{"queued", "running", "output", "weird", "errors", "other-versions", "paused", "cancelled"}
)

// makeChart is a pie chart of spool states, as SVG paths (a full circle when one state has
// everything).
func makeChart(title string, vals map[string]int) webChart {
	ch := webChart{Title: title}
	for _, k := range chartOrder {
		ch.Segs = append(ch.Segs, webSeg{Name: k, Color: chartColors[k], N: vals[k]})
		ch.Total += vals[k]
	}
	a := -math.Pi / 2
	for i := range ch.Segs {
		s := &ch.Segs[i]
		if s.N == 0 {
			continue
		}
		if s.N == ch.Total {
			s.Path = "M 50 5 A 45 45 0 1 1 49.99 5 Z"
			break
		}
		b := a + 2*math.Pi*float64(s.N)/float64(ch.Total)
		large := 0
		if b-a > math.Pi {
			large = 1
		}
		s.Path = fmt.Sprintf("M 50 50 L %.2f %.2f A 45 45 0 %d 1 %.2f %.2f Z",
			50+45*math.Cos(a), 50+45*math.Sin(a), large, 50+45*math.Cos(b), 50+45*math.Sin(b))
		a = b
	}
	return ch
}

// chartsOf takes the "chart ...: k=v ..." lines out of a status answer.
func chartsOf(out string) (string, []webChart) {
	var keep []string
	var charts []webChart
	for _, ln := range strings.Split(out, "\n") {
		head, kv, ok := strings.Cut(ln, ": ")
		if !ok || !strings.HasPrefix(head, "chart") {
			keep = append(keep, ln)
			continue
		}
		vals := map[string]int{}
		for _, f := range strings.Fields(kv) {
			k, v, _ := strings.Cut(f, "=")
			vals[k], _ = strconv.Atoi(v)
		}
		title := strings.TrimSpace(strings.TrimPrefix(head, "chart"))
		if title == "" {
			title = "all inputs"
		}
		charts = append(charts, makeChart(title, vals))
	}
	return strings.Join(keep, "\n"), charts
}

var webTmpl = template.Must(template.New("page").Funcs(template.FuncMap{
	"syn":  func(c cmdSpec) string { return c.synopsis() },
	"T":    i18n.T,
	"Tf":   i18n.Tf,
	"lang": i18n.Lang,
	"dir":  i18n.Dir,
	"name": func(i int) string { return "a" + strconv.Itoa(i) },
	"idx":  func(v []string, i int) string { return v[i] },
	"tree": pickTree,
	"list": func(v [][]string, i int) []string {
		if i < len(v) {
			return v[i]
		}
		return nil
	},
}).Parse(webHTML + pickHTML))

// pickHTML is a path picker's tree (picker.go): a folder's name chooses it, its arrow
// opens it.
const pickHTML = `{{define "pick"}}{{range .}}{{if .Kids}}<details{{if .Open}} open{{end}}><summary><button type="button" class="pk" data-p="{{.Path}}">{{.Name}}/</button></summary><div>{{template "pick" .Kids}}</div></details>
{{else}}<div class="lf"><button type="button" class="pk" data-p="{{.Path}}">{{.Name}}{{if .Dir}}/{{end}}</button></div>
{{end}}{{end}}{{end}}`

const webHTML = `<!doctype html>
<html lang="{{lang}}" dir="{{dir}}"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
{{if .Restarting}}<meta http-equiv="refresh" content="4;url=/">{{else if .Refresh}}<meta http-equiv="refresh" content="{{.Refresh}}">{{end}}
<title>Matriline · {{T .Title}}</title><link rel="stylesheet" href="/style.css"></head>
<body>
<header><a class="brand" href="/">Matriline</a><span class="ver">{{.Version}}</span></header>
<nav class="tabs">
<a href="/"{{if eq .Tab "Status"}} class="on"{{end}}>{{T "Status"}}</a><a href="/live"{{if eq .Tab "Live"}} class="on"{{end}}>{{T "Live"}}</a>
{{range .Nav}}<a href="/g?name={{.Name}}"{{if eq $.Tab .Name}} class="on"{{end}}>{{T .Name}}</a>{{end}}
<a href="/config"{{if eq .Tab "Settings"}} class="on"{{end}}>{{T "Settings"}}</a>
</nav>
{{range .Nav}}{{if eq $.Tab .Name}}<nav class="sub">{{range .Cmds}}<a href="/cmd?c={{.Words}}"{{if $.Cmd}}{{if eq $.Cmd.Words .Words}} class="on"{{end}}{{end}}>{{.Words}}</a>{{end}}</nav>{{end}}{{end}}
{{if .Warning}}<div class="warn">{{.Warning}}</div>{{end}}
<main>
{{if .Restarting}}
<h1>{{T "Restarting the server"}}</h1><p class="help">{{T "This page comes back to the status by itself when the server answers again (a few seconds)."}}</p>
{{else if .SettingsForm}}
<h1>{{T "Settings"}}</h1>
<p class="help">{{.ConfName}}: {{T "one field per option with its explanation."}} <a href="/config?text=1">{{T "Edit it as text instead"}}</a>.</p>
<form method="post" action="/settings" class="settings">
<input type="hidden" name="token" value="{{.Token}}"><input type="hidden" name="sum" value="{{.ConfSum}}">
<div class="bar"><button type="submit" name="action" value="check" class="plain">{{T "Check"}}</button>
<button type="submit" name="action" value="save">{{T "Save"}}</button>
<button type="submit" name="action" value="exit">{{T "Save and close"}}</button>
<button type="submit" name="action" value="cancel" class="plain" formnovalidate>{{T "Cancel"}}</button></div>
{{if .Err}}<pre class="err">{{.Err}}</pre>{{end}}{{if .Out}}<pre>{{.Out}}</pre>{{end}}{{range .Notes}}<p class="note">{{.}}</p>{{end}}
{{if .NeedRestart}}<p class="warn2">{{T "A changed option is applied when the server restarts."}}</p>{{end}}
{{range .SettingsForm}}<fieldset><legend>[{{.Section}}]</legend>
{{range .Items}}<label class="set"><span class="key">{{.Key}}{{if .Restart}} <em class="rs">{{T "restart"}}</em>{{end}}</span>
{{if .ReadOnly}}<input type="text" value="{{.Value}}" disabled title="{{T "changed only in the file"}}"><span class="hint">{{T "read-only here: edit it in the file"}}</span>
{{else if .Bool}}<select name="{{.Field}}"><option value="true"{{if eq .Value "true"}} selected{{end}}>true</option><option value="false"{{if eq .Value "false"}} selected{{end}}>false</option></select>
{{else}}<input type="text" name="{{.Field}}" value="{{.Value}}">{{end}}
<span class="hint">{{.Help}}{{if .More}}<details><summary>{{T "more"}}</summary>{{.More}}</details>{{end}}</span></label>
{{end}}</fieldset>{{end}}
</form>
{{if .NeedRestart}}<form method="post" action="/restart" class="inline"><input type="hidden" name="token" value="{{.Token}}"><button type="submit" class="second">{{T "Restart the server now"}}</button></form>{{end}}
{{else if .ConfSum}}
<h1>{{T "Settings as text"}}</h1>
<p class="help">{{T "The file with its explanations. \"Check\" validates it; \"Save and apply\" also writes it and applies it (some options need a restart; the warnings say which)."}} <a href="/config">{{T "Back to the form"}}</a>.</p>
<form method="post" action="/config" class="wide">
<input type="hidden" name="token" value="{{.Token}}"><input type="hidden" name="sum" value="{{.ConfSum}}">
<textarea name="conf" rows="30" spellcheck="false">{{.Conf}}</textarea>
<button type="submit">{{T "Check"}}</button> <button type="submit" name="save" value="yes" class="second">{{T "Save and apply"}}</button>
</form>
{{range .Notes}}<p class="note">{{.}}</p>{{end}}
{{else if eq .Title "Live 3D"}}
<h1>{{T "Live 3D"}}</h1>
{{if .Jobs}}<nav class="jobs">{{range .Jobs}}<a href="/mol?n={{.N}}"{{if eq .N $.LiveSel}} class="on"{{end}}><b>{{.N}}</b> {{.Client}} · {{.Task}}</a>{{end}}</nav>{{end}}
{{if .XYZ}}<p class="hint">{{.XYZName}} ({{T "job"}} {{.LiveSel}}): {{T "its input geometry. Drag to turn, scroll to zoom."}} <a href="/live?n={{.LiveSel}}">{{T "Back to live"}}</a></p>
<div class="molbox"><div id="mol3d" class="mol3d" data-xyz="{{.XYZ}}" data-rotate="{{.MolRotate}}" data-bg="{{.MolBg}}"></div><span class="mark">3Dmol.js</span></div>
<script src="/3dmol.js"></script><script src="/viewer.js"></script>
<p class="note">{{T "3D view:"}} <a href="/3dmol-license.txt">3Dmol.js</a> {{T "(BSD license), served by this page; nothing is fetched from the Internet."}}</p>{{end}}
{{else if .Dirs}}
<h1>add</h1>
<p class="help">{{T "Inputs (*.inp) into a folder of input/: choose or drop files and whole folders (their sub-folders are kept), or give a folder or file on this computer."}}</p>
<form method="post" action="/add" enctype="multipart/form-data">
<input type="hidden" name="token" value="{{.Token}}">
<label><span class="arg">{{T "folder"}}</span><select name="dest">{{range .Dirs}}<option value="{{.}}">{{.}}/</option>{{end}}</select></label>
<label><span class="arg">{{T "new sub-folder"}} <em>{{T "(optional)"}}</em></span><input type="text" name="newdir" placeholder="{{T "e.g. batch-3"}}"></label>
<label class="drop" data-count="{{T "file(s) ready to add"}}" data-sending="{{T "sending..."}}" data-max="268435456" data-toolarge="{{T "too large: at most 256 MB at once; add the rest in another upload, or give a folder on this computer"}}"><span class="arg">{{T "files or folders"}}</span><input type="file" name="files" multiple accept=".inp,.xyz"><span class="hint">{{T "choose, or drop files and folders here; they add up"}}</span></label>
<label><span class="arg">{{T "a whole folder"}} <em>{{T "(optional)"}}</em></span><input type="file" name="folder" webkitdirectory multiple></label>
<label><span class="arg">{{T "or a path"}} <em>{{T "(optional)"}}</em></span><input type="text" name="path" placeholder="{{T "a file or folder on this computer"}}"></label>
<button type="submit">{{T "Add"}}</button>
</form>
<script src="/add.js"></script>
{{else if .Group}}
<h1>{{T .Tab}}</h1>
<div class="cards">{{range .Group}}<a class="card" href="/cmd?c={{.Words}}"><b>{{.Words}}</b><span>{{T .Help}} {{T .More}}</span></a>{{end}}</div>
{{else}}{{with .Cmd}}
<h1>{{.Words}}</h1>
<p class="help">{{T .Help}} {{T .More}}</p>
<p class="syn"><code>matriline-server {{syn .}}</code></p>
<form method="post" action="/run">
<input type="hidden" name="token" value="{{$.Token}}"><input type="hidden" name="c" value="{{.Words}}">
{{range $i, $a := .Args}}
<label>{{if $a.Flag}}<input type="checkbox" name="{{name $i}}" value="yes"{{if idx $.Values $i}} checked{{end}}> {{$a.Flag}} <span class="hint">{{T $a.Help}}</span>
{{else}}<span class="arg">{{if $a.Option}}{{$a.Option}} {{end}}{{$a.Name}}{{if $a.Optional}} <em>{{T "(optional)"}}</em>{{end}}</span>
<input type="text" name="{{name $i}}" value="{{idx $.Values $i}}"{{if not $a.Optional}} required{{end}}><span class="hint">{{T $a.Help}}</span>{{end}}</label>
{{with list $.Lists $i}}<div class="picker" data-for="{{name $i}}" role="group" aria-label="{{$a.Name}}">{{template "pick" (tree .)}}</div>{{end}}
{{end}}
{{if .Confirm}}<label class="confirm"><input type="checkbox" name="confirm" value="yes"> {{T "yes, run it"}}</label>{{end}}
<button type="submit">{{T "Run"}}</button>
</form>
{{if $.Lists}}<script src="/picker.js"></script>{{end}}
{{else}}{{if .Live}}<h1>{{T "Live"}}</h1><p class="hint">{{Tf "Refreshes every %d s." .Refresh}}</p>
{{with .LiveV}}<div class="tiles">
<div class="tile"><b>{{len .Rows}}</b><span>{{T "calculations running"}}</span></div>
<div class="tile"><b class="c-starting">{{index .Phases "starting"}}</b><span>{{T "starting (input received)"}}</span></div>
<div class="tile"><b class="c-computing">{{index .Phases "computing"}}</b><span>{{T "computing"}}</span></div>
<div class="tile"><b class="c-uploading">{{index .Phases "uploading"}}</b><span>{{T "sending the result"}}</span></div>
</div>
<table class="grid"><thead><tr><th>#</th><th>{{T "computer"}}</th><th>{{T "calculation"}}</th><th>{{T "kind"}}</th><th>{{T "phase"}}</th><th>{{T "time"}}</th><th>{{T "output so far"}}</th></tr></thead><tbody>
{{range .Rows}}<tr{{if eq .N $.LiveSel}} class="sel"{{end}}><td><a href="/live?n={{.N}}">{{.N}}</a></td><td>{{.Client}}</td><td>{{.Task}}</td><td>{{T .Kind}}</td><td><span class="ph ph-{{.Phase}}">{{T .Phase}}</span></td><td class="num">{{.Time}}</td><td class="num">{{.Size}}</td></tr>
{{end}}</tbody></table>{{end}}
<p class="hint">{{Tf "Job %d:" .LiveSel}} <a href="/live?n={{.LiveSel}}{{if not .LiveFull}}&amp;full=1{{end}}">{{if .LiveFull}}{{T "first input lines only"}}{{else}}{{T "whole input"}}{{end}}</a>
· <a href="/mol?n={{.LiveSel}}"><b>{{T "view the molecule in 3D"}}</b></a></p>
{{else if eq .Title "Live"}}<h1>{{T "Live"}}</h1><p class="hint">{{Tf "Refreshes every %d s." .Refresh}}</p>
{{else}}<h1>{{T "Status"}}</h1><p class="hint">{{Tf "Refreshes every %d s." 15}}</p>
{{with .Stat}}<div class="tiles">
<div class="tile big"><b>{{.Pct}}%</b><span>{{T "of the project done"}}</span><svg class="pbar" viewBox="0 0 100 4" preserveAspectRatio="none" aria-hidden="true">{{range .All.Bar}}<rect x="{{printf "%.3f" .X}}" width="{{printf "%.3f" .W}}" height="4" fill="{{.Color}}"><title>{{T .Name}} {{.N}}</title></rect>{{end}}</svg><small>{{Tf "%d of %d calculations" .Done .Total}}</small></div>
<div class="tile"><b class="c-queued">{{.Waiting}}</b><span>{{T "waiting"}}</span></div>
<div class="tile"><b class="c-running">{{.Running}}</b><span>{{Tf "running on %d computers" (len .Clients)}}</span></div>
<div class="tile"><b class="c-output">{{.Results.Output}}</b><span>{{T "done"}}</span></div>
<div class="tile"><b class="c-weird">{{.Results.Weird}}</b><span>{{T "set aside for review"}}</span></div>
<div class="tile"><b class="c-errors">{{.Results.Errors}}</b><span>{{T "failed"}}</span></div>
</div>
{{if .Projects}}<h2>{{T "Projects"}}</h2><div class="projs">{{range .Projects}}<div class="proj"><svg viewBox="0 0 100 100" width="84" height="84" aria-hidden="true">{{range .Chart.Segs}}{{if .Path}}<path d="{{.Path}}" fill="{{.Color}}"/>{{end}}{{end}}</svg>
<div class="pinfo"><b>{{.Name}}</b> <span class="pct">{{.Pct}}%</span><svg class="pbar" viewBox="0 0 100 4" preserveAspectRatio="none" aria-hidden="true">{{range .Bar}}<rect x="{{printf "%.3f" .X}}" width="{{printf "%.3f" .W}}" height="4" fill="{{.Color}}"/>{{end}}</svg>
<small>{{range .Bar}}<span class="lg"><svg width="10" height="10" aria-hidden="true"><rect width="10" height="10" rx="2" fill="{{.Color}}"/></svg> {{T .Name}} {{.N}}</span> {{end}}</small></div></div>{{end}}</div>{{end}}
<h2>{{T "Computers"}}</h2>
{{if .Clients}}<table class="grid"><thead><tr><th>{{T "computer"}}</th><th>{{T "calculations"}}</th><th>{{T "taking work"}}</th><th>{{T "power"}}</th><th>{{T "last report"}}</th></tr></thead><tbody>
{{range .Clients}}<tr><td>{{.Name}}</td><td><svg class="mini" viewBox="0 0 100 4" preserveAspectRatio="none" aria-hidden="true"><rect width="100" height="4" fill="#d6dbe0"/><rect width="{{.Bar}}" height="4" fill="#3b82f6"/></svg> {{.Running}} / {{.Slots}}</td><td>{{if .Accepting}}{{T "yes"}}{{else}}{{T "paused"}}{{end}}</td><td>{{.Power}}</td><td class="num">{{.Seen}}</td></tr>
{{end}}</tbody></table>
<p class="hint">{{Tf "%d of %d places in use" .Busy .Slots}}</p>{{else}}<p class="hint">{{T "No computer is connected."}}</p>{{end}}
{{end}}
{{if .StatText}}<details class="astext"><summary>{{T "as text (as the terminal shows it)"}}</summary><pre>{{.StatText}}</pre></details>{{end}}{{end}}{{end}}{{end}}
{{if not .SettingsForm}}
{{if .Line}}<p class="line"><code>{{.Line}}</code></p>{{end}}
{{if .Err}}<pre class="err">{{.Err}}</pre>{{end}}
{{if .Charts}}<div class="charts">{{range .Charts}}<figure><svg viewBox="0 0 100 100" width="150" height="150" role="img" aria-label="{{.Title}}">
{{if .Total}}{{range .Segs}}{{if .Path}}<path d="{{.Path}}" fill="{{.Color}}"><title>{{.Name}} {{.N}}</title></path>{{end}}{{end}}{{else}}<circle cx="50" cy="50" r="45" fill="#e5e7eb"/>{{end}}
</svg><figcaption><b>{{.Title}}</b>{{range .Segs}}{{if .N}}<span><svg width="11" height="11" aria-hidden="true"><rect width="11" height="11" rx="2" fill="{{.Color}}"/></svg> {{.Name}} {{.N}}</span>{{end}}{{end}}</figcaption></figure>{{end}}</div>{{end}}
{{if .Out}}<pre>{{.Out}}</pre>{{end}}
{{end}}
</main></body></html>`

const webCSS = `[dir=rtl] pre,[dir=rtl] code,[dir=rtl] svg,[dir=rtl] input,[dir=rtl] textarea{direction:ltr;unicode-bidi:isolate}.tiles{display:flex;flex-wrap:wrap;gap:.8em;margin:.6em 0 1.2em}.tile{background:var(--card);border:1px solid var(--line);border-radius:10px;padding:.7em 1em;min-width:9em;display:flex;flex-direction:column}
.tile b{font-size:1.9em;line-height:1.1}.tile span{opacity:.8}.tile.big{min-width:18em;flex:1}.tile small{opacity:.7;margin-top:.3em}
.pbar{display:block;width:100%;height:.8em;border-radius:.4em;background:var(--line);margin-top:.5em}
.c-queued{color:#7b8794}.c-running,.c-computing{color:#3b82f6}.c-output{color:#22a06b}.c-weird{color:#c99a00}.c-errors{color:#e34935}.c-starting{color:#a855f7}.c-uploading{color:#0d9488}
.projs{display:grid;grid-template-columns:repeat(auto-fill,minmax(21em,1fr));gap:.8em;margin-bottom:1.2em}.proj{display:flex;gap:.9em;align-items:center;background:var(--card);border:1px solid var(--line);border-radius:10px;padding:.7em}
.pinfo{flex:1;min-width:0}.pct{float:right;font-weight:700}.lg{white-space:nowrap;font-size:.85em;margin-right:.6em}
table.grid{border-collapse:collapse;width:100%;max-width:70em;background:var(--card);border:1px solid var(--line);border-radius:10px;overflow:hidden}
table.grid th,table.grid td{padding:.35em .7em;text-align:left;border-bottom:1px solid var(--line)}table.grid th{font-weight:600;opacity:.8}table.grid tr.sel{outline:2px solid var(--dark)}td.num{text-align:right;font-variant-numeric:tabular-nums}
.mini{display:inline-block;width:4em;height:.55em;border-radius:.3em;vertical-align:middle}
.ph{padding:.1em .55em;border-radius:1em;font-size:.85em;color:#fff}.ph-starting{background:#a855f7}.ph-computing{background:#3b82f6}.ph-uploading{background:#0d9488}.ph-lost{background:#6b7280}
details.astext{margin-top:1em}h2{font-size:1.15em;margin:1em 0 .5em}
*{box-sizing:border-box}body{margin:0;font:15px/1.45 system-ui,sans-serif;background:var(--bg);color:var(--fg)}
header{display:flex;gap:1em;align-items:baseline;padding:.6em 1em;background:var(--head);color:var(--on-head)}
.brand{font-weight:700;color:var(--on-head);text-decoration:none;font-size:1.15em}.ver{opacity:.75;font-size:.85em}
nav.tabs{display:flex;flex-wrap:wrap;gap:.3em;padding:.4em .8em 0;background:var(--head)}
nav.tabs a{padding:.45em .9em;color:var(--on-light);background:var(--light);text-decoration:none;border-radius:6px 6px 0 0;opacity:.7;border-bottom:3px solid transparent}
nav.tabs a.on{background:var(--dark);color:var(--on-dark);font-weight:700;opacity:1;border-bottom-color:var(--white)}nav.tabs a:hover{opacity:1}
nav.sub{display:flex;flex-wrap:wrap;gap:.3em;padding:.5em 1em;border-bottom:3px solid var(--dark);background:var(--card)}
nav.sub a{padding:.15em .7em;border:1px solid var(--light);border-radius:999px;color:var(--fg);text-decoration:none;font-size:.9em;background:var(--bg)}
nav.sub a.on{background:var(--dark);color:var(--on-dark);border-color:var(--dark)}nav.sub a:hover{border-color:var(--dark)}
.warn{background:var(--light);color:var(--on-light);padding:.6em 1em}
.warn2{background:var(--light);color:var(--on-light);padding:.5em .8em;border-radius:6px}
.note{color:var(--mut);font-size:.85em}main{padding:1em 1.5em;max-width:80em}h1{margin:.2em 0 .4em;font-size:1.4em}
.help,.hint{color:var(--mut)}.hint{font-size:.85em;margin-left:.5em}a{color:var(--dark)}
form{background:var(--card);border:1px solid var(--line);border-radius:8px;padding:1em;max-width:52em}
form.inline{display:inline-block;border:0;padding:0;background:none}
label{display:block;margin:.5em 0}.arg{display:inline-block;min-width:13em;font-family:ui-monospace,monospace}
input[type=text],select{width:24em;max-width:100%;padding:.35em .5em;border:1px solid var(--line);border-radius:5px;background:var(--bg);color:var(--fg)}
.picker{position:relative;max-height:16em;overflow:auto;width:32em;max-width:100%;margin:.1em 0 .9em;padding:.3em .4em;border:1px solid var(--line);border-radius:6px;background:var(--card);font:13px/1.5 ui-monospace,monospace;direction:ltr;text-align:left}
.picker details{margin:0}.picker details>div{padding-inline-start:1.3em}.picker summary{color:var(--fg)}.picker .lf{padding-inline-start:1.1em}
.pk{margin:0;padding:.05em .4em;border:0;border-radius:4px;background:none;color:var(--fg);font:inherit;font-weight:400;cursor:pointer}.pk:hover{background:var(--light);color:var(--on-light)}
.pk.sel{background:var(--dark);color:var(--on-dark);font-weight:600}
.picked{max-height:14em;overflow:auto;margin:.3em 0 .8em;font-size:.9em}.drop input[type=file]{padding:1.2em;border:2px dashed var(--gray);border-radius:8px;width:24em;max-width:100%}
button{margin-top:.6em;padding:.45em 1.4em;border:0;border-radius:5px;background:var(--dark);color:var(--on-dark);font-weight:600;cursor:pointer}
button:disabled{background:var(--light);color:var(--on-light);cursor:not-allowed;opacity:.7}
button.plain{background:var(--bg);color:var(--fg);border:1px solid var(--gray)}button.second{background:var(--black);color:var(--on-black)}
.confirm{color:var(--dark);font-weight:600}form.wide,form.settings{max-width:none}
.settings .bar{position:sticky;top:0;background:var(--card);padding:.3em 0 .6em;border-bottom:1px solid var(--line);z-index:1}
fieldset{border:1px solid var(--line);border-radius:8px;margin:1em 0;padding:.4em 1em}legend{font-family:ui-monospace,monospace;color:var(--dark);font-weight:600}
label.set{display:grid;grid-template-columns:16em 24em 1fr;gap:.2em 1em;align-items:start;margin:.6em 0}
.key{font-family:ui-monospace,monospace}.rs{font-size:.75em;color:var(--on-light);background:var(--light);font-style:normal;border-radius:4px;padding:0 .3em}
label.set .hint{margin-left:0}details{margin-top:.2em}summary{cursor:pointer;color:var(--dark)}
textarea{width:100%;font:13px/1.4 ui-monospace,monospace;padding:.6em;border:1px solid var(--line);border-radius:5px;background:var(--bg);color:var(--fg)}
pre{background:var(--card);border:1px solid var(--line);border-radius:8px;padding:1em;overflow-x:auto;font-size:13px}
pre.err{border-color:#c0392b;color:#c0392b}.line code,.syn code{background:var(--card);padding:.2em .4em;border-radius:4px}
.cards{display:grid;grid-template-columns:repeat(auto-fill,minmax(18em,1fr));gap:.8em}
.card{display:block;padding:.8em 1em;background:var(--card);border:1px solid var(--line);border-left:4px solid var(--dark);border-radius:8px;color:var(--fg);text-decoration:none}
.card:hover{border-color:var(--dark)}.card b{display:block;font-family:ui-monospace,monospace;color:var(--dark)}.card span{color:var(--mut);font-size:.9em}
.charts{display:flex;flex-wrap:wrap;gap:1.5em;margin:1em 0}figure{margin:0;display:flex;gap:.8em;align-items:center}
figcaption span{display:block;font-size:.85em}figcaption svg{vertical-align:-1px}
.molbox{position:relative;max-width:60em}.mol3d{position:relative;width:100%;height:32em;border:1px solid var(--line);border-radius:8px;overflow:hidden}
.mark{position:absolute;right:.6em;bottom:.4em;font-size:.75em;opacity:.55;color:#888;pointer-events:none}
nav.jobs{display:flex;flex-wrap:wrap;gap:.4em;margin:.2em 0 .8em}nav.jobs a{padding:.25em .7em;border:1px solid var(--light);border-radius:6px;color:var(--fg);text-decoration:none;font-size:.9em;background:var(--card)}
nav.jobs a.on{background:var(--dark);color:var(--on-dark);border-color:var(--dark)}
@media (max-width:760px){label.set{grid-template-columns:1fr}main{padding:.8em}}`
