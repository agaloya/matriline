package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The settings form covers every key of server.conf, each with an explanation.
func TestParseSettings(t *testing.T) {
	dir := t.TempDir()
	if err := cmdInit(dir, "en"); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "server.conf"))
	groups := parseSettings(string(b))
	n, noHelp := 0, []string{}
	var listen, mail setting
	for _, g := range groups {
		for _, s := range g.Items {
			n++
			if s.Help == "" {
				noHelp = append(noHelp, s.Section+"."+s.Key)
			}
			if s.Key == "listen" {
				listen = s
			}
			if s.Key == "email_enabled" {
				mail = s
			}
		}
	}
	if n < 40 {
		t.Errorf("only %d settings found", n)
	}
	if len(noHelp) > 0 {
		t.Errorf("settings without an explanation: %s", strings.Join(noHelp, ", "))
	}
	if !listen.Restart || listen.Value != "44100" {
		t.Errorf("listen: %+v", listen)
	}
	if !mail.Bool || mail.Value != "false" || !strings.Contains(mail.Help, "SMTP") {
		t.Errorf("email_enabled: %+v", mail)
	}
	// a changed value lands in the right place, nothing else changes
	out := setConfigText(string(b), "alerts", "smtp_server", "smtp.example.org:587")
	if _, _, err := loadConfigText(t, out); err != nil {
		t.Fatal(err)
	}
	lines := 0
	for _, l := range strings.Split(out, "\n") {
		if strings.HasPrefix(strings.TrimSpace(l), "smtp_server =") {
			lines++
		}
	}
	if lines != 1 || !strings.Contains(out, "\nsmtp_server = smtp.example.org:587") {
		t.Error("smtp_server not set once")
	}
}

func loadConfigText(t *testing.T, text string) (*Config, []string, error) {
	p := filepath.Join(t.TempDir(), "server.conf")
	os.WriteFile(p, []byte(text), 0o600)
	return loadConfig(p)
}

func TestUploadNames(t *testing.T) {
	for name, ok := range map[string]bool{"water.inp": true, "a-b_c.1.xyz": true, "foo.inp:evil.inp": false, "CON": false,
		"x.exe": false, ".hidden.inp": false, "C:x.inp": false, "trail.inp.": false, "sp ace.inp": false} {
		if uploadName.MatchString(name) != ok {
			t.Errorf("%q accepted: %v", name, !ok)
		}
	}
	for _, k := range []string{"hooks.result", "alerts.command", "alerts.telegram_token_file"} {
		if !protectedSettings[k] {
			t.Errorf("%s editable on the web page", k)
		}
	}
}
