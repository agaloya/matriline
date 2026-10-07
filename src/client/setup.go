package main

import (
	"fmt"
	"github.com/agaloya/matriline/common/conf"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/agaloya/matriline/common/i18n"
	"github.com/agaloya/matriline/common/ident"
	"github.com/agaloya/matriline/common/orcafind"
	"github.com/agaloya/matriline/common/projects"
)

// Zero-configuration setup: 'matriline-client init <dir> --credential <file>' copies the
// credential the admin sent, applies the client settings the admin suggested in it (its
// [preset] section) and finds ORCA (and OpenMPI) on this computer, so a user who knows
// nothing about the configuration runs one command. Every value can be changed later in
// client.conf: the computer's owner has the last word (D27).

// orcaExe is ORCA's main program in an installation directory (orca.exe on Windows).
func orcaExe(dir string) string {
	if runtime.GOOS == "windows" {
		return filepath.Join(dir, "orca.exe")
	}
	return filepath.Join(dir, "orca")
}

// setConfValue sets key = value inside [section] of a client.conf text by replacing the
// line that sets it (the template lists every setting). ok is false for an unknown one.
func setConfValue(text, section, key, value string) (string, bool) {
	lines := strings.Split(text, "\n")
	in := false
	q, err := conf.Quote(value) // read back unchanged (quoted text is literal: no %q escapes)
	if err != nil {
		return text, false
	}
	value = q
	for i, ln := range lines {
		t := strings.TrimSpace(ln)
		if strings.HasPrefix(t, "[") && strings.HasSuffix(t, "]") {
			if in {
				break
			}
			in = t == "["+section+"]"
			continue
		}
		if !in {
			continue
		}
		if k, _, ok := strings.Cut(t, "="); ok && !strings.HasPrefix(t, "#") && strings.TrimSpace(k) == key {
			lines[i] = key + " = " + value
			return strings.Join(lines, "\n"), true
		}
	}
	return text, false
}

// cmdInitWith is init with a credential: copy it, apply its preset, detect ORCA/OpenMPI.
func cmdInitWith(dir, credPath string) error {
	if strings.Contains(filepath.ToSlash(dir), "path/to/") {
		return fmt.Errorf("%s is the example from the instructions: replace it with your own (empty) folder", dir)
	}
	cred, err := ident.ReadCredential(credPath)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	cfgPath := filepath.Join(dir, "client.conf")
	if _, err := os.Stat(cfgPath); err == nil {
		return fmt.Errorf("%s already exists", cfgPath)
	}
	text, err := initConfigText()
	if err != nil {
		return err
	}
	var notes []string
	keys := make([]string, 0, len(cred.Preset))
	for k := range cred.Preset {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	preset := map[string]bool{}
	for _, k := range keys {
		sec, key, ok := strings.Cut(k, ".")
		if !ok || sec == "client" || sec == "security" {
			notes = append(notes, fmt.Sprintf("ignored preset %s (not a setting the admin may suggest)", k))
			continue
		}
		var set bool
		if text, set = setConfValue(text, sec, key, cred.Preset[k]); !set {
			notes = append(notes, fmt.Sprintf("ignored preset %s (no such setting)", k))
			continue
		}
		preset[k] = true
		notes = append(notes, fmt.Sprintf("admin's setting: %s = %s", k, cred.Preset[k]))
	}
	if initSettings != "" {
		sf, err := conf.Load(initSettings)
		if err != nil {
			return fmt.Errorf("settings %s: %v", initSettings, err)
		}
		for _, k := range sf.Keys() {
			sec, key, ok := strings.Cut(k, ".")
			v := sf.String(k, "")
			if !ok || sec == "client" || sec == "security" {
				notes = append(notes, fmt.Sprintf("ignored setting %s (not one a kit may set)", k))
				continue
			}
			var set bool
			if text, set = setConfValue(text, sec, key, v); !set {
				notes = append(notes, fmt.Sprintf("ignored setting %s (no such setting)", k))
				continue
			}
			preset[k] = true
			notes = append(notes, fmt.Sprintf("kit setting: %s = %s", k, v))
		}
	}
	if !preset["orca.paths"] {
		if dirs := orcafind.Dirs(); len(dirs) > 0 {
			text, _ = setConfValue(text, "orca", "paths", strings.Join(dirs, ", "))
			notes = append(notes, "ORCA found: "+strings.Join(dirs, ", "))
		} else {
			notes = append(notes, "WARNING: no ORCA installation found; install ORCA and set orca.paths in client.conf")
		}
	}
	// the credential first: a client.conf is what marks a folder as set up, so an
	// interruption between the two must not leave a client without its credential (the
	// kit carries on with such a folder; code review)
	b, err := os.ReadFile(credPath)
	if err != nil {
		return err
	}
	credDst := filepath.Join(dir, "credential.conf")
	if err := ident.WriteFileAtomic(credDst, b, 0o600); err != nil {
		return err
	}
	if err := os.WriteFile(cfgPath, []byte(text), 0o600); err != nil {
		return err
	}
	projects.Add("client", cfgPath) // found from any folder from now on (user)
	cfg, warns, err := loadConfig(cfgPath)
	if err != nil {
		os.Remove(cfgPath)
		os.Remove(credDst)
		return fmt.Errorf("the resulting configuration is not valid (%v); nothing was written", err)
	}
	notes = append(notes, warns...)
	if cfg.MaxCoresJob > 1 && cfg.MPIPath == "auto" {
		if m, err := findMPI(cfg); err == nil {
			text, _ = setConfValue(text, "orca", "mpi_path", m.dir)
			os.WriteFile(cfgPath, []byte(text), 0o600)
			notes = append(notes, "OpenMPI "+m.version+" found: "+m.dir)
		} else {
			notes = append(notes, "multi-core jobs need OpenMPI 4.1: "+err.Error())
		}
	}
	fmt.Printf("created %s for %s (server %s)\n", cfgPath, cred.Name, cred.ServerAddress)
	for _, n := range notes {
		fmt.Println("  " + n)
	}
	self := selfPath()
	fmt.Println(i18n.T("ORCA's license (EULA) applies to what this computer computes: your ORCA must be your own licensed copy,\n  and lending this computer is allowed only for academic (or private) projects whose admin also holds an ORCA license\n  (docs/ORCA_LICENSE.md in Matriline's source)."))
	// the service first (user): it runs without a terminal, starts by itself and survives
	// restarts; 'run' in a terminal is only for a quick try
	fmt.Printf("check it with:  %s -c %s doctor\nthen leave it running as a service:  %s -c %s service install\n(the log is %s)\n",
		self, cfgPath, self, cfgPath, filepath.Join(filepath.Dir(cfgPath), "state", "client.log"))
	return nil
}

// selfPath is how to call this program: its full path, since its directory may not be in
// PATH (e.g. ~/.local/bin on some systems).
func selfPath() string {
	if p, err := os.Executable(); err == nil {
		return p
	}
	return "matriline-client"
}
