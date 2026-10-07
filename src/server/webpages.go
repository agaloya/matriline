package main

import (
	_ "embed"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/textproto"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/agaloya/matriline/common/conf"
	"github.com/agaloya/matriline/common/ident"
)

// Pages of the web interface beyond a command's form: a group's commands, adding inputs
// (folders and uploaded files), the settings form and the restart.

// groupPage lists the commands of one group (a tab) with their explanations.
func (w *webUI) groupPage(rw http.ResponseWriter, r *http.Request) {
	g := r.URL.Query().Get("name")
	p := w.page(g)
	p.Tab = g
	for _, ng := range p.Nav {
		if ng.Name == g {
			p.Group = ng.Cmds
		}
	}
	if p.Group == nil {
		http.Error(rw, "no such group", http.StatusNotFound)
		return
	}
	w.render(rw, p)
}

// inputDirs lists input/ and its sub-directories (a few levels), for the add page.
func (w *webUI) inputDirs() []string {
	root, err := rootOf(w.cfgPath)
	if err != nil {
		return nil
	}
	out := []string{dInput}
	queue := []string{dInput}
	for len(queue) > 0 && len(out) < 300 {
		d := queue[0]
		queue = queue[1:]
		es, err := os.ReadDir(filepath.Join(root, filepath.FromSlash(d)))
		if err != nil {
			continue
		}
		for _, e := range es {
			if e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
				p := d + "/" + e.Name()
				out = append(out, p)
				if strings.Count(p, "/") < 4 {
					queue = append(queue, p)
				}
			}
		}
	}
	sort.Strings(out)
	return out
}

const maxUpload = 256 << 20 // also in the add page (data-max) and its text

//go:embed add.js
var addJS []byte

var uploadName = regexp.MustCompile(`(?i)^[A-Za-z0-9_-][A-Za-z0-9._-]{0,150}\.(inp|xyz)$`)

// windowsDevice: names Windows reserves for devices, whatever their extension ("NUL.inp"
// vanishes, a "CON" folder cannot be made; code review).
var windowsDevice = regexp.MustCompile(`(?i)^(con|prn|aux|nul|com[0-9]|lpt[0-9])(\.|$)`)
var uploadDir = regexp.MustCompile(`^[A-Za-z0-9_-][A-Za-z0-9._-]{0,150}$`)

// uploadedName is the file name exactly as the browser sent it, with its path inside a
// chosen or dropped folder: Go's Part.FileName keeps only the last part.
func uploadedName(h textproto.MIMEHeader, fallback string) string {
	if _, params, err := mime.ParseMediaType(h.Get("Content-Disposition")); err == nil && params["filename"] != "" {
		return params["filename"]
	}
	return fallback
}

// uploadPath checks an uploaded file's name, which may carry its path inside a chosen or
// dropped folder ("batch1/sub/mol.inp"): every folder a plain name (no "..", no drive or
// device, no stream), the file an .inp or .xyz, at most 8 levels. It returns the clean
// relative path.
func uploadPath(name string) (string, bool) {
	parts := strings.Split(strings.ReplaceAll(name, "\\", "/"), "/")
	if len(parts) > 8 || !uploadName.MatchString(parts[len(parts)-1]) {
		return "", false
	}
	for i, d := range parts {
		if windowsDevice.MatchString(d) {
			return "", false
		}
		// folders: plain names, not ending in '.' (Windows drops it: "a." would be "a")
		if i < len(parts)-1 && (!uploadDir.MatchString(d) || strings.Trim(d, ".") == "" || strings.HasSuffix(d, ".")) {
			return "", false
		}
	}
	return strings.Join(parts, "/"), true
}

// addPage shows input/'s folders and a form for files to add (chosen or dropped on the
// field: browsers accept dropped files on a file field, no script needed), or a path on
// this computer.
// offered: a command is on this web page (MATRILINE_HIDE can take it away).
func offered(words ...string) bool {
	_, ok := lookup(words)
	return ok
}

func (w *webUI) addPage(rw http.ResponseWriter, r *http.Request) {
	if !offered("add") {
		http.NotFound(rw, r)
		return
	}
	p := w.page("add")
	p.Tab = "Tasks"
	p.Dirs = w.inputDirs()
	addPageCSP(rw)
	w.render(rw, p)
}

// addPageCSP lets the add page run its own script (add.js) and send its form with it; the
// other pages keep running no script at all.
func addPageCSP(rw http.ResponseWriter) {
	rw.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; connect-src 'self'; style-src 'self'; img-src 'self' data:; form-action 'self'; frame-ancestors 'none'")
}

// addSave stores the uploaded files in a private temporary folder and adds them with
// the 'add' command (recorded in the ledger like any other add).
func (w *webUI) addSave(rw http.ResponseWriter, r *http.Request) {
	if !offered("add") {
		http.NotFound(rw, r)
		return
	}
	// read part by part, each file written as it arrives: ParseMultipartForm stops at 1000
	// parts (code review: a folder of 1100 small inputs failed as "too large")
	r.Body = http.MaxBytesReader(rw, r.Body, maxUpload)
	mr, err := r.MultipartReader()
	if err != nil {
		http.Error(rw, "not a form upload: "+err.Error(), http.StatusBadRequest)
		return
	}
	root, _ := rootOf(w.cfgPath)
	tmp, err := os.MkdirTemp(filepath.Join(root, dState), "web-upload-")
	if err != nil {
		http.Error(rw, err.Error(), http.StatusInternalServerError)
		return
	}
	defer os.RemoveAll(tmp)
	fields := map[string]string{}
	saved, skipped, files := 0, 0, 0
	refused := ""
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			var tooBig *http.MaxBytesError
			if errors.As(err, &tooBig) {
				http.Error(rw, fmt.Sprintf("upload too large: at most %d MB at once (add the rest in another upload, or give a folder on this computer)", maxUpload>>20), http.StatusRequestEntityTooLarge)
			} else {
				http.Error(rw, "upload broken: "+err.Error(), http.StatusBadRequest)
			}
			return
		}
		form := part.FormName()
		if part.FileName() == "" { // a plain field (token, dest, newdir, path)
			b, _ := io.ReadAll(io.LimitReader(part, 64<<10))
			fields[form] = string(b)
			part.Close()
			continue
		}
		if form != "files" && form != "folder" {
			part.Close()
			continue
		}
		files++
		name := uploadedName(part.Header, part.FileName())
		rel, ok := uploadPath(name)
		if !ok {
			if uploadName.MatchString(path.Base(strings.ReplaceAll(name, "\\", "/"))) || !strings.ContainsAny(name, `/\`) {
				// an input refused for its name or its path, or a single odd file: say so;
				// other files inside a folder (outputs, notes): skip quietly
				refused = fmt.Sprintf("%q refused: names of letters, digits, '.', '_' and '-' ending in .inp or .xyz, inside plain folders", name)
			} else {
				skipped++
			}
			part.Close()
			continue
		}
		dst := filepath.Join(tmp, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
			refused = err.Error()
			part.Close()
			continue
		}
		f, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			_, err = io.Copy(f, part)
			f.Close()
		}
		part.Close()
		if err != nil {
			refused = fmt.Sprintf("%s: %v", rel, err)
			continue
		}
		saved++
	}
	if !eqSecret(fields["token"], w.csrf) {
		http.Error(rw, "form token missing or wrong", http.StatusForbidden)
		return
	}
	p := w.page("add")
	p.Tab = "Tasks"
	p.Dirs = w.inputDirs()
	p.Err = refused
	dest := fields["dest"]
	if sub := strings.Trim(strings.TrimSpace(fields["newdir"]), "/"); sub != "" {
		if !filepath.IsLocal(filepath.FromSlash(sub)) {
			p.Err = "new folder: a name inside the chosen folder"
			addPageCSP(rw)
			w.render(rw, p)
			return
		}
		dest = dest + "/" + sub
	}
	if dest != dInput && !strings.HasPrefix(dest, dInput+"/") {
		p.Err = "choose a folder of input/"
		addPageCSP(rw)
		w.render(rw, p)
		return
	}
	var out []string
	if pth := strings.TrimSpace(fields["path"]); pth != "" { // a path on this computer
		o, err := call(w.cfgPath, absArgs([]string{"add", pth, dest}))
		out = append(out, o)
		if err != nil {
			p.Err = err.Error()
		}
	}
	if files > 0 {
		// each top-level file or folder through 'add' (a folder keeps its name and
		// sub-folders inside the destination)
		entries, _ := os.ReadDir(tmp)
		for _, e := range entries {
			o, err := call(w.cfgPath, []string{"add", filepath.Join(tmp, e.Name()), dest})
			out = append(out, o)
			if err != nil {
				p.Err = err.Error()
			}
		}
		note := ""
		if skipped > 0 {
			note = fmt.Sprintf(" (%d other file(s) skipped: only .inp and .xyz)", skipped)
		}
		out = append(out, fmt.Sprintf("%d uploaded file(s) received for %s/%s", saved, dest, note))
	}
	if len(out) == 0 && p.Err == "" {
		p.Err = "choose or drop files, or give a path on this computer"
	}
	p.Out = strings.Join(out, "\n")
	p.Dirs = w.inputDirs()
	addPageCSP(rw)
	w.render(rw, p)
}

// settingsPage is the settings form (configPage keeps the text editor, ?text=1).
func (w *webUI) settingsPage(rw http.ResponseWriter, r *http.Request) {
	if w.editPath == w.cfgPath && !offered("config", "edit") {
		http.NotFound(rw, r)
		return
	}
	if r.URL.Query().Get("text") != "" {
		w.configPage(rw, r)
		return
	}
	p := w.page("settings")
	p.Tab = "Settings"
	b, err := os.ReadFile(w.editPath)
	if err != nil {
		p.Err = err.Error()
	}
	p.ConfName, p.ConfSum = filepath.Base(w.editPath), sumHex(b)
	p.SettingsForm = parseSettings(string(b))
	w.render(rw, p)
}

// settingsSave applies the changed fields to the file's text (comments and order kept),
// checks it like 'config edit' and, if asked, saves and applies it. A field of an option
// marked [restart] changed: the page offers a restart.
func (w *webUI) settingsSave(rw http.ResponseWriter, r *http.Request) {
	if w.editPath == w.cfgPath && !offered("config", "edit") {
		http.NotFound(rw, r)
		return
	}
	if !eqSecret(r.FormValue("token"), w.csrf) {
		http.Error(rw, "form token missing or wrong", http.StatusForbidden)
		return
	}
	if r.FormValue("action") == "cancel" {
		http.Redirect(rw, r, "/", http.StatusSeeOther)
		return
	}
	w.confMu.Lock()
	defer w.confMu.Unlock()
	p := w.page("settings")
	p.Tab = "Settings"
	p.ConfName = filepath.Base(w.editPath)
	cur, err := os.ReadFile(w.editPath)
	if err != nil {
		p.Err = err.Error()
		w.render(rw, p)
		return
	}
	if sumHex(cur) != r.FormValue("sum") {
		p.Err = p.ConfName + " was changed by someone else since this page was opened: reload the page and apply your changes again"
		p.ConfSum, p.SettingsForm = sumHex(cur), parseSettings(string(cur))
		w.render(rw, p)
		return
	}
	text := string(cur)
	groups := parseSettings(text)
	var changed []string
	restart := false
	for gi := range groups {
		for si := range groups[gi].Items {
			s := &groups[gi].Items[si]
			v, ok := r.Form[s.Field()]
			if !ok || s.ReadOnly {
				continue
			}
			nv := strings.TrimSpace(strings.ReplaceAll(v[0], "\n", " "))
			if nv == s.Value {
				continue
			}
			if strings.ContainsAny(nv, "#[") {
				p.Err = s.Section + "." + s.Key + ": '#' and '[' cannot be used in a value"
				break
			}
			if _, err := conf.Quote(nv); err != nil {
				p.Err = s.Section + "." + s.Key + ": " + err.Error()
				break
			}
			text = setConfigText(text, s.Section, s.Key, nv)
			changed = append(changed, fmt.Sprintf("%s.%s = %s", s.Section, s.Key, nv))
			s.Value = nv
			restart = restart || s.Restart
		}
	}
	p.ConfSum, p.SettingsForm = sumHex(cur), groups
	if p.Err != "" {
		w.render(rw, p)
		return
	}
	notes, err := w.checkConf(text)
	p.Notes = notes
	switch {
	case err != nil:
		p.Err = err.Error()
	case len(changed) == 0:
		p.Out = "nothing changed"
	case r.FormValue("action") == "check":
		p.Out = "valid; nothing saved yet. Changes:\n  " + strings.Join(changed, "\n  ")
	default:
		if err := ident.WriteFileAtomic(w.editPath, []byte(text), 0o600); err != nil {
			p.Err = err.Error()
			break
		}
		p.ConfSum = sumHex([]byte(text))
		out, aerr := w.applyConf()
		p.Out = "saved:\n  " + strings.Join(changed, "\n  ") + "\n" + out
		if aerr != nil {
			p.Err = "saved, but not applied yet: " + aerr.Error()
		}
		p.NeedRestart = restart && w.apply == ""
		if r.FormValue("action") == "exit" && !p.NeedRestart && aerr == nil {
			http.Redirect(rw, r, "/", http.StatusSeeOther)
			return
		}
	}
	w.render(rw, p)
}

// checkConf validates a configuration text: Matriline's own check, or the embedding
// program's (MATRILINE_WEB_APPLY). It returns the warnings.
func (w *webUI) checkConf(text string) ([]string, error) {
	f, err := os.CreateTemp(filepath.Dir(w.editPath), "."+filepath.Base(w.editPath)+".web-*")
	if err != nil {
		return nil, err
	}
	defer os.Remove(f.Name())
	_, werr := f.WriteString(text)
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil { // a truncated copy must not pass for the text that will be saved
		return nil, fmt.Errorf("cannot check the settings: %v", werr)
	}
	if w.apply != "" {
		out, err := exec.Command(w.apply, "web-config-check", f.Name()).CombinedOutput()
		t := strings.TrimSpace(string(out))
		if err != nil {
			return nil, fmt.Errorf("%s", t)
		}
		if t == "" {
			return nil, nil
		}
		return strings.Split(t, "\n"), nil
	}
	_, warns, err := loadConfig(f.Name())
	return warns, err
}

func (w *webUI) applyConf() (string, error) {
	if w.apply != "" {
		out, err := exec.Command(w.apply, "web-config-apply").CombinedOutput()
		if err != nil {
			return "", fmt.Errorf("%s", strings.TrimSpace(string(out)))
		}
		return strings.TrimSpace(string(out)), nil
	}
	return call(w.cfgPath, []string{"config", "reload"})
}

// restartServer asks the server to restart; the page keeps working meanwhile (it is a
// separate program) and comes back to the status when the server answers again.
func (w *webUI) restartServer(rw http.ResponseWriter, r *http.Request) {
	if !offered("restart") {
		http.NotFound(rw, r)
		return
	}
	if !eqSecret(r.FormValue("token"), w.csrf) {
		http.Error(rw, "form token missing or wrong", http.StatusForbidden)
		return
	}
	p := w.page("restart")
	out, err := call(w.cfgPath, []string{"restart"})
	p.Out = out
	if err != nil {
		p.Err = err.Error()
	}
	p.Restarting = true
	p.Refresh = 4
	w.render(rw, p)
}
