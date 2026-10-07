package main

import (
	"bufio"
	"net"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"
)

// fakeSMTP accepts one message on localhost (plain SMTP, no TLS, no auth) and returns
// the envelope and data it received.
func fakeSMTP(t *testing.T) (addr string, got chan string) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	got = make(chan string, 1)
	go func() {
		defer ln.Close()
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		c.SetDeadline(time.Now().Add(10 * time.Second))
		r := bufio.NewReader(c)
		say := func(s string) { c.Write([]byte(s + "\r\n")) }
		say("220 fake ESMTP")
		var b strings.Builder
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			cmd := strings.ToUpper(strings.TrimSpace(line))
			switch {
			case strings.HasPrefix(cmd, "EHLO"), strings.HasPrefix(cmd, "HELO"):
				say("250 fake")
			case strings.HasPrefix(cmd, "MAIL FROM"), strings.HasPrefix(cmd, "RCPT TO"):
				b.WriteString(strings.TrimSpace(line) + "\n")
				say("250 ok")
			case cmd == "DATA":
				say("354 go on")
				for {
					l, err := r.ReadString('\n')
					if err != nil || l == ".\r\n" {
						break
					}
					b.WriteString(l)
				}
				say("250 queued")
			case cmd == "QUIT":
				say("221 bye")
				got <- b.String()
				return
			default:
				say("250 ok")
			}
		}
	}()
	return ln.Addr().String(), got
}

func TestMailAlert(t *testing.T) {
	addr, got := fakeSMTP(t)
	cfg := &Config{MailServer: addr, MailFrom: "matriline@example.org", MailTo: []string{"admin@example.org", "second@example.org"}}
	if err := sendMail(cfg, "[matriline] storage", "disk 95 % full"); err != nil {
		t.Fatal(err)
	}
	m := <-got
	for _, want := range []string{"MAIL FROM:<matriline@example.org>", "RCPT TO:<admin@example.org>", "RCPT TO:<second@example.org>",
		"Subject: [matriline] storage", "disk 95 % full"} {
		if !strings.Contains(m, want) {
			t.Errorf("missing %q in:\n%s", want, m)
		}
	}
}

// Only the events chosen in alerts.events are mailed.
func TestMailOnlyChosenEvents(t *testing.T) {
	dir := t.TempDir()
	if err := cmdInit(dir, "en"); err != nil {
		t.Fatal(err)
	}
	s, err := openServer(dir + "/server.conf")
	if err != nil {
		t.Fatal(err)
	}
	addr, got := fakeSMTP(t)
	cfg := *s.conf()
	cfg.MailEnabled, cfg.MailServer, cfg.MailFrom, cfg.MailTo = true, addr, "m@example.org", []string{"a@example.org"}
	cfg.AlertModes = map[string]string{"quarantine": "now", "ban": "off"}
	s.cfg = &cfg
	s.alert("ban", "not chosen")
	s.alert("quarantine", "client x quarantined")
	select {
	case m := <-got:
		if strings.Contains(m, "not chosen") || !strings.Contains(m, "client x quarantined") {
			t.Errorf("mail:\n%s", m)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no mail for a chosen event")
	}
}

// "alerts test" mails a test alert at once, whatever alerts.events says.
func TestAlertsTestCommand(t *testing.T) {
	dir := t.TempDir()
	if err := cmdInit(dir, "en"); err != nil {
		t.Fatal(err)
	}
	s, err := openServer(dir + "/server.conf")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.admin("alerts", []string{"test"}); err == nil {
		t.Error("no channel configured, but no error")
	}
	addr, got := fakeSMTP(t)
	cfg := *s.conf()
	cfg.MailEnabled, cfg.MailServer, cfg.MailFrom, cfg.MailTo, cfg.AlertModes = true, addr, "m@example.org", []string{"a@example.org"}, map[string]string{}
	s.cfg = &cfg
	out, err := s.admin("alerts", []string{"test"})
	if err != nil || !strings.Contains(out, "e-mail sent from m@example.org to a@example.org") {
		t.Fatalf("%q %v", out, err)
	}
	if m := <-got; !strings.Contains(m, "Subject: [matriline] test") {
		t.Errorf("mail:\n%s", m)
	}
}

// E-mail on with missing settings: a warning from the configuration check.
func TestMailIncompleteWarns(t *testing.T) {
	c := &Config{MailEnabled: true, MailServer: "smtp.example.org:587"}
	found := false
	for _, w := range c.validate() {
		if strings.Contains(w, "alerts.from, alerts.to not set") {
			found = true
		}
	}
	if !found {
		t.Errorf("no warning for incomplete e-mail settings: %v", c.validate())
	}
}

// summary alerts are kept and sent together; a quiet summary says all is well.
func TestMailSummary(t *testing.T) {
	addr, got := fakeSMTP(t)
	s := mailServer(t, addr)
	alertMu.Lock()
	alertLast = map[string]time.Time{}
	alertMu.Unlock()
	s.alert("errors", "task a.inp moved to errors/")
	s.alert("errors", "task b.inp moved to errors/")
	s.alert("enrolled", "client x joined")
	select {
	case m := <-got:
		t.Fatalf("a summary alert was e-mailed at once:\n%s", m)
	case <-time.After(300 * time.Millisecond):
	}
	s.sendSummary(false)
	select {
	case m := <-got:
		for _, want := range []string{"3 alert(s)", "errors 2", "enrolled 1", "a.inp", "b.inp", "client x joined", "tasks:"} {
			if !strings.Contains(m, want) {
				t.Errorf("summary lacks %q:\n%s", want, m)
			}
		}
	case <-time.After(10 * time.Second):
		t.Fatal("no summary e-mail")
	}
	addr2, got2 := fakeSMTP(t)
	s.cfg.MailServer, s.cfg.SummaryQuiet = addr2, true
	s.sendSummary(false)
	select {
	case m := <-got2:
		if !strings.Contains(m, "all is well") || !strings.Contains(m, "Nothing to report") {
			t.Errorf("quiet summary:\n%s", m)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("no quiet summary")
	}
}

// Files from before the per-alert modes keep their meaning.
func TestAlertsOldConfig(t *testing.T) {
	text := "[alerts]\nevents = storage, errors\ndigest = 6h\n"
	vals := map[string]string{}
	for _, l := range strings.Split(text, "\n") {
		if k, v, ok := strings.Cut(l, " = "); ok {
			vals["alerts."+k] = v
		}
	}
	c := &Config{}
	has := func(k string) bool { _, ok := vals[k]; return ok }
	str := func(k, d string) string {
		if v, ok := vals[k]; ok {
			return v
		}
		return d
	}
	dur := func(k string, d time.Duration) time.Duration {
		if v, ok := vals[k]; ok {
			x, _ := time.ParseDuration(v)
			return x
		}
		return d
	}
	if w := alertConfig(c, has, str, dur); len(w) > 0 {
		t.Fatal(w)
	}
	if c.AlertModes["storage"] != "summary" || c.AlertModes["errors"] != "summary" || c.AlertModes["ban"] != "off" || c.AlertSummarySpec != "every 6h0m0s" {
		t.Errorf("old config: %v %q", c.AlertModes, c.AlertSummarySpec)
	}
}

// mailServer is a server whose alerts go by e-mail to addr (every event chosen).
func mailServer(t *testing.T, addr string) *Server {
	dir := t.TempDir()
	if err := cmdInit(dir, "en"); err != nil {
		t.Fatal(err)
	}
	s, err := openServer(dir + "/server.conf")
	if err != nil {
		t.Fatal(err)
	}
	cfg := *s.conf()
	cfg.MailEnabled, cfg.MailServer, cfg.MailFrom, cfg.MailTo = true, addr, "m@example.org", []string{"a@example.org"}
	cfg.AlertModes = map[string]string{"errors": "summary", "enrolled": "summary", "storage": "now"}
	s.cfg = &cfg
	return s
}

// The SMTP password from a file: it must not be readable by others.
func TestSMTPPasswordFile(t *testing.T) {
	p := t.TempDir() + "/pw"
	os.WriteFile(p, []byte("abcd efgh\n"), 0o644)
	if _, err := smtpPassword(&Config{MailPassFile: p}); err == nil && runtime.GOOS != "windows" {
		t.Error("a password file readable by others was used")
	}
	os.Chmod(p, 0o600)
	if pw, err := smtpPassword(&Config{MailPassFile: p}); err != nil || pw != "abcd efgh" {
		t.Errorf("password %q, %v", pw, err)
	}
}
