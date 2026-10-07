package main

import (
	"bufio"
	"crypto/ed25519"
	"fmt"
	"net"
	"net/smtp"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/agaloya/matriline/common/i18n"
	"github.com/agaloya/matriline/common/ident"
	"github.com/agaloya/matriline/common/wire"
)

var (
	alertMu   sync.Mutex
	alertLast = map[string]time.Time{}
)

// alert logs an event and, as [alerts] says for its kind: sends it now (e-mail and the
// alert program; the same kind at most once per alerts.now_limit, so a misbehaving client
// cannot flood the admin's inbox or phone), keeps it for the next summary, or nothing.
func (s *Server) alert(kind, msg string) { s.alertText(kind, msg, i18n.T(msg)) }

// alertf: an alert from a format; the log and events.log get it in English, the e-mail
// and Telegram message in general.language.
func (s *Server) alertf(kind, format string, a ...any) {
	s.alertText(kind, fmt.Sprintf(format, a...), i18n.Tf(format, a...))
}

func (s *Server) alertText(kind, en, msg string) {
	s.log.Warnf("alert [%s] %s", kind, en)
	s.event("system", "alert [%s] %s", kind, en)
	cfg := s.conf()
	if !cfg.alertChannels() {
		return
	}
	mode := cfg.AlertModes[kind]
	if mode == "" {
		mode = alertModeDefault(kind)
	}
	switch mode {
	case "off":
		return
	case "summary":
		s.queueSummary(kind, msg)
		return
	}
	alertMu.Lock()
	if time.Since(alertLast[kind]) < cfg.AlertNowLimit {
		alertMu.Unlock()
		s.log.Infof("alert [%s] sent less than %s ago: kept for the next summary", kind, cfg.AlertNowLimit)
		s.queueSummary(kind, msg) // not lost: in the next summary, if there is one
		return
	}
	alertLast[kind] = time.Now()
	alertMu.Unlock()
	s.deliver("[matriline] "+kind, kind, msg)
}

// deliver sends one message through every configured channel, in the background.
func (s *Server) deliver(subject, kind, msg string) { s.send(subject, kind, msg, false) }

// send sends one message through every configured channel; wait: until done (the last
// summary when the server stops, which would otherwise be cut off by the exit).
func (s *Server) send(subject, kind, msg string, wait bool) {
	cfg := s.conf()
	var wg sync.WaitGroup
	run := func(f func()) {
		wg.Add(1)
		go func() { defer wg.Done(); f() }()
	}
	if cfg.MailEnabled {
		run(func() {
			if err := sendMail(cfg, subject, msg); err != nil {
				s.log.Errorf("alert e-mail failed: %v", err)
				s.event("system", "alert e-mail for [%s] failed: %v", kind, err)
			} else {
				s.log.Infof("alert [%s] e-mailed to %s", kind, strings.Join(cfg.MailTo, ", "))
			}
		})
	}
	if cfg.AlertCommand != "" {
		run(func() {
			if err := s.runAlertCommand(cfg.AlertCommand, kind, msg); err != nil {
				s.log.Errorf("alert command failed: %v", err)
			}
		})
	}
	if cfg.TelegramEnabled {
		run(func() {
			if err := sendTelegram(cfg, subject+"\n"+msg); err != nil {
				s.log.Errorf("alert by Telegram failed: %v", err)
				s.event("system", "alert by Telegram for [%s] failed: %v", kind, err)
			} else {
				s.log.Infof("alert [%s] sent by Telegram", kind)
			}
		})
	}
	if wait {
		wg.Wait()
	}
}

// alertChannels: some way to send alerts is configured.
func (c *Config) alertChannels() bool {
	return c.MailEnabled || c.AlertCommand != "" || c.TelegramEnabled
}

// The summary (alerts.summary): alerts kept and sent together on a schedule.
type summaryEntry struct {
	when       time.Time
	kind, text string
}

var (
	summaryMu    sync.Mutex
	summaryBuf   []summaryEntry
	summaryN     int       // alerts kept, including those beyond the list's cap
	summarySince time.Time // the previous summary (or the start)
)

const summaryMax = 200

func (s *Server) queueSummary(kind, msg string) {
	if s.conf().AlertSummary == nil || s.conf().AlertSummary.off {
		return
	}
	summaryMu.Lock()
	defer summaryMu.Unlock()
	summaryN++
	if len(summaryBuf) < summaryMax {
		summaryBuf = append(summaryBuf, summaryEntry{time.Now(), kind, msg})
	}
}

// sendSummary sends the kept alerts (or, with alerts.summary_when_quiet, a note that
// nothing happened), with the project's state.
func (s *Server) sendSummary(stopping bool) {
	cfg := s.conf()
	summaryMu.Lock()
	buf, n, since := summaryBuf, summaryN, summarySince
	summaryBuf, summaryN, summarySince = nil, 0, time.Now()
	summaryMu.Unlock()
	if n == 0 && (stopping || !cfg.SummaryQuiet) {
		return
	}
	kinds := map[string]int{}
	var b strings.Builder
	if n == 0 {
		b.WriteString(i18n.Tf("Nothing to report since %s.", since.Format("2006-01-02 15:04")) + "\n")
	} else {
		b.WriteString(i18n.Tf("%d alert(s) since %s:", n, since.Format("2006-01-02 15:04")) + "\n")
	}
	for _, e := range buf {
		kinds[e.kind]++
		fmt.Fprintf(&b, "%s  [%s] %s\n", e.when.Format("2006-01-02 15:04"), e.kind, e.text)
	}
	if n > len(buf) {
		b.WriteString(i18n.Tf("... and %d more (see state/events.log)", n-len(buf)) + "\n")
	}
	if st, err := s.cmdStatus(nil); err == nil { // the project's state, first lines
		l := strings.Split(st, "\n")
		b.WriteString("\n" + strings.Join(l[:min(4, len(l))], "\n") + "\n")
	}
	var ks []string
	for k, c := range kinds {
		ks = append(ks, fmt.Sprintf("%s %d", k, c))
	}
	sort.Strings(ks)
	subject := "[matriline] " + i18n.T("summary: all is well")
	if n > 0 {
		subject = "[matriline] " + i18n.Tf("summary: %d alert(s) (%s)", n, strings.Join(ks, ", "))
	}
	s.send(subject, "summary", b.String(), stopping)
}

func (s *Server) summaryLoop() {
	defer s.wg.Done()
	summaryMu.Lock()
	summarySince = time.Now()
	summaryMu.Unlock()
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	spec := ""
	var next time.Time
	for {
		select {
		case <-s.stop:
			s.sendSummary(true) // nothing kept is lost when the server stops
			return
		case now := <-t.C:
			cfg := s.conf()
			if cfg.AlertSummarySpec != spec { // a reload changed the schedule
				spec = cfg.AlertSummarySpec
				next = cfg.AlertSummary.next(now)
			}
			if !next.IsZero() && !now.Before(next) {
				s.sendSummary(false)
				next = cfg.AlertSummary.next(now)
			}
		}
	}
}

// runAlertCommand runs the admin's program without a shell: the message is an argument,
// never parsed as a command line.
// cmdAlertsTest sends one test alert through every configured channel now (no event
// filter, no hourly limit) and reports what happened, so the admin can check the e-mail
// settings or the alert program before something real happens.
func (s *Server) cmdAlertsTest() (string, error) {
	cfg := s.conf()
	host, _ := os.Hostname()
	msg := i18n.Tf("Test alert from the Matriline server %s on %s (%s). If you read this, alerts reach you.", s.key.ID(), host, time.Now().Format(time.RFC1123))
	var out []string
	if !cfg.alertChannels() {
		return "", fmt.Errorf("no alert channel configured: in server.conf set alerts.email_enabled (with the e-mail settings), alerts.telegram_enabled (with the bot's settings) or alerts.command; the explanations at the end of the file say how")
	}
	if cfg.MailEnabled {
		if err := sendMail(cfg, "[matriline] test", msg); err != nil {
			out = append(out, fmt.Sprintf("e-mail to %s via %s: FAILED: %v", strings.Join(cfg.MailTo, ", "), cfg.MailServer, err))
		} else {
			out = append(out, fmt.Sprintf("e-mail sent from %s to %s via %s", cfg.MailFrom, strings.Join(cfg.MailTo, ", "), cfg.MailServer))
		}
	}
	if cfg.AlertCommand != "" {
		if err := s.runAlertCommand(cfg.AlertCommand, "test", msg); err != nil {
			out = append(out, fmt.Sprintf("alert program %s: FAILED: %v", cfg.AlertCommand, err))
		} else {
			out = append(out, "alert program "+cfg.AlertCommand+" ran")
		}
	}
	if cfg.TelegramEnabled {
		if err := sendTelegram(cfg, "[matriline] test\n"+msg); err != nil {
			out = append(out, "Telegram: FAILED: "+err.Error())
		} else {
			out = append(out, "Telegram message sent to chat "+cfg.TelegramChat)
		}
	}
	return strings.Join(out, "\n"), nil
}

func (s *Server) runAlertCommand(prog, kind, msg string) error {
	out, err := s.runChild(prog, []string{kind, msg}, nil, s.conf().Root, 30*time.Second)
	if err != nil {
		return fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// smtpPassword reads the SMTP password from alerts.smtp_password_file (one line; it must
// not be readable by other users) or else the environment variable alerts.smtp_password_env.
func smtpPassword(cfg *Config) (string, error) {
	if cfg.MailPassFile == "" {
		return os.Getenv(cfg.MailPassEnv), nil
	}
	p, err := secretFile(cfg.MailPassFile, "SMTP password file")
	if appPassword.MatchString(p) { // Google shows app passwords in groups: "abcd efgh ijkl mnop"
		p = strings.ReplaceAll(p, " ", "")
	}
	return p, err
}

// sendMail uses net/smtp, which upgrades to STARTTLS when the server offers it.
func sendMail(cfg *Config, subject, body string) error {
	host, _, err := net.SplitHostPort(cfg.MailServer)
	if err != nil {
		return err
	}
	var auth smtp.Auth
	if cfg.MailUser != "" {
		pass, err := smtpPassword(cfg)
		if err != nil {
			return err
		}
		auth = smtp.PlainAuth("", cfg.MailUser, pass, host)
	}
	msg := fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: %s\r\nDate: %s\r\n\r\n%s\r\n",
		cfg.MailFrom, strings.Join(cfg.MailTo, ", "), subject, time.Now().Format(time.RFC1123Z), body)
	return smtp.SendMail(cfg.MailServer, auth, cfg.MailFrom, cfg.MailTo, []byte(msg))
}

// ---------------------------------------------------------------------------------------
// Relay mode (server side). The server keeps a control connection to the relay; for each
// client that asks for this server the relay sends "CONNECT <token>" and the server opens a
// data connection "MLRY1 DATA <token>", which is then handled exactly like a direct TCP
// connection (full MWP handshake end to end).

func (s *Server) relayLoop() {
	defer s.wg.Done()
	backoff := time.Second
	for {
		select {
		case <-s.stop:
			return
		default:
		}
		err := s.relaySession()
		s.log.Warnf("relay connection lost: %v (retrying in %s)", err, backoff)
		select {
		case <-s.stop:
			return
		case <-time.After(backoff):
		}
		if backoff < time.Minute {
			backoff *= 2
		}
	}
}

func relayRegisterLine(key *ident.Key, nonce string) string {
	sig := ed25519.Sign(key.Priv, []byte("mlry1-register "+key.ID()+" "+nonce))
	return fmt.Sprintf("MLRY1 REGISTER %s %s %s\n", key.ID(), ident.EncodeKey(key.Pub), ident.EncodeKey(sig))
}

func (s *Server) relaySession() error {
	addr := s.conf().RelayAddr
	c, err := net.DialTimeout("tcp", addr, 20*time.Second)
	if err != nil {
		return err
	}
	defer c.Close()
	go func() { <-s.stop; c.Close() }()
	c.SetDeadline(time.Now().Add(30 * time.Second))
	nonce, err := wire.ReadRelayHello(c)
	if err != nil {
		return err
	}
	if _, err := c.Write([]byte(relayRegisterLine(s.key, nonce))); err != nil {
		return err
	}
	c.SetDeadline(time.Time{})
	br := bufio.NewReader(c)
	line, err := br.ReadString('\n')
	if err != nil {
		return err
	}
	if strings.TrimSpace(line) != "OK" {
		return fmt.Errorf("relay refused registration: %s", strings.TrimSpace(line))
	}
	s.log.Infof("registered at relay %s", addr)
	for {
		c.SetReadDeadline(time.Now().Add(3 * time.Minute))
		line, err := br.ReadString('\n')
		if err != nil {
			return err
		}
		f := strings.Fields(line)
		switch {
		case len(f) == 1 && f[0] == "PING":
			c.SetWriteDeadline(time.Now().Add(30 * time.Second))
			c.Write([]byte("PONG\n"))
		case len(f) == 2 && f[0] == "CONNECT":
			go s.relayData(addr, f[1])
		}
	}
}

func (s *Server) relayData(addr, token string) {
	c, err := net.DialTimeout("tcp", addr, 20*time.Second)
	if err != nil {
		s.log.Warnf("relay data connection: %v", err)
		return
	}
	c.SetDeadline(time.Now().Add(30 * time.Second))
	if _, err := wire.ReadRelayHello(c); err != nil {
		c.Close()
		return
	}
	if _, err := fmt.Fprintf(c, "MLRY1 DATA %s\n", token); err != nil {
		c.Close()
		return
	}
	c.SetDeadline(time.Time{})
	s.handleConn(c, false) // relayed: the address is the relay's, never banned
}
