package main

import (
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/agaloya/matriline/common/ident"
	"github.com/agaloya/matriline/common/logx"
	"github.com/agaloya/matriline/common/wire"
)

func banServer(t *testing.T) *Server {
	t.Helper()
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, dState), 0o750)
	lg, err := logx.New(filepath.Join(dir, dState, "server.log"), "info")
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{log: lg, cfg: &Config{Root: dir,
		BanGarbageAfter: 10, BanGarbageWindow: time.Hour, BanGarbageDuration: 7 * 24 * time.Hour,
		BanAuthAfter: 20, BanAuthWindow: 24 * time.Hour, BanAuthDuration: 24 * time.Hour, BanTrustKnown: true}}
	s.loadBans()
	return s
}

func TestBanKey(t *testing.T) {
	if k := banKey("203.0.113.7"); k != "203.0.113.7" {
		t.Fatalf("IPv4 key %q", k)
	}
	if a, b := banKey("2001:db8:1:2::1"), banKey("2001:db8:1:2:ffff::9"); a != b || a != "2001:db8:1:2::/64" {
		t.Fatalf("IPv6 keys %q %q: one /64 must share a key", a, b)
	}
}

func TestBanGarbageAfterTen(t *testing.T) {
	s := banServer(t)
	for i := 0; i < 9; i++ {
		s.connFailed("198.51.100.5", true, "bad magic")
	}
	if s.banned("198.51.100.5") {
		t.Fatal("banned after 9 garbage connections")
	}
	s.connFailed("198.51.100.5", true, "bad magic")
	if !s.banned("198.51.100.5") {
		t.Fatal("not banned after 10 garbage connections")
	}
	// survives a restart
	s2 := &Server{log: s.log, cfg: s.cfg}
	s2.loadBans()
	if !s2.banned("198.51.100.5") {
		t.Fatal("ban lost on restart")
	}
	if _, err := s2.cmdBans([]string{"lift", "198.51.100.5"}); err != nil || s2.banned("198.51.100.5") {
		t.Fatalf("lift failed: %v", err)
	}
}

func TestAuthFailuresAreForgivenLonger(t *testing.T) {
	s := banServer(t)
	for i := 0; i < 19; i++ {
		s.connFailed("198.51.100.6", false, "unknown client")
	}
	if s.banned("198.51.100.6") {
		t.Fatal("protocol-speaking peer banned before ban_auth_after")
	}
	s.connFailed("198.51.100.6", false, "unknown client")
	if !s.banned("198.51.100.6") {
		t.Fatal("not banned after ban_auth_after failed authentications")
	}
	s.bans.mu.Lock()
	until := s.bans.Bans["198.51.100.6"].Until
	s.bans.mu.Unlock()
	if d := time.Until(until); d > 25*time.Hour || d < 23*time.Hour {
		t.Fatalf("auth ban lasts %s, want ~24h", d)
	}
}

func TestSharedNATIsNotBanned(t *testing.T) {
	s := banServer(t)
	s.connOK("192.0.2.10") // a registered helper behind the same NAT
	for i := 0; i < 50; i++ {
		s.connFailed("192.0.2.10", true, "bad magic")
	}
	if s.banned("192.0.2.10") {
		t.Fatal("address of a recently admitted client banned (ban_trust_known)")
	}
	s.cfg.BanTrustKnown = false
	for i := 0; i < 10; i++ {
		s.connFailed("192.0.2.10", true, "bad magic")
	}
	if !s.banned("192.0.2.10") {
		t.Fatal("ban_trust_known = false must ban")
	}
}

func TestExpiredBanIsDropped(t *testing.T) {
	s := banServer(t)
	s.bans.Bans["198.51.100.7"] = Ban{Until: time.Now().Add(-time.Minute), Reason: "old"}
	if s.banned("198.51.100.7") {
		t.Fatal("expired ban still active")
	}
}

type addrConn struct {
	net.Conn
	addr net.Addr
}

func (c addrConn) RemoteAddr() net.Addr { return c.addr }

// dialPipe runs handleConn on one end of a pipe from host and returns the other end.
func dialPipe(s *Server, host string) net.Conn {
	a, b := net.Pipe()
	done := make(chan struct{})
	go func() {
		s.handleConn(addrConn{b, &net.TCPAddr{IP: net.ParseIP(host), Port: 40000}}, true)
		close(done)
	}()
	return closeWait{a, done}
}

type closeWait struct {
	net.Conn
	done chan struct{}
}

func (c closeWait) Close() error { err := c.Conn.Close(); <-c.done; return err }

func TestHandleConnClassifiesPeers(t *testing.T) {
	s := banServer(t)
	key, err := ident.Generate()
	if err != nil {
		t.Fatal(err)
	}
	s.key, s.cfg.Mode, s.cfg.MaxSessions, s.sessions = key, wire.ModeTLS, 10, map[string]*Session{}
	// a port scanner / other protocol: garbage, banned at the 10th
	for i := 0; i < 10; i++ {
		c := dialPipe(s, "198.51.100.20")
		c.Write([]byte("GET / HTTP/1.1\r\n\r\n"))
		c.Close()
	}
	if !s.banned("198.51.100.20") {
		t.Fatal("garbage peer not banned after 10 connections")
	}
	// speaks the protocol (valid magic), then drops: the lenient rule
	for i := 0; i < 10; i++ {
		c := dialPipe(s, "198.51.100.21")
		hello := append([]byte("MWP1"), 1)
		c.Write(append(hello, make([]byte, 32)...))
		buf := make([]byte, 512)
		c.Read(buf) // server hello
		c.Close()
	}
	if s.banned("198.51.100.21") {
		t.Fatal("protocol-speaking peer banned by the garbage rule")
	}
	s.bans.mu.Lock()
	n := len(s.bans.auth["198.51.100.21"])
	s.bans.mu.Unlock()
	if n != 10 {
		t.Fatalf("auth failures counted: %d, want 10", n)
	}
}

// TestLoopbackNeverBanned: connections from this computer are never banned.
func TestLoopbackNeverBanned(t *testing.T) {
	s := banServer(t)
	for _, h := range []string{"127.0.0.1", "::1", "127.0.1.1"} {
		for i := 0; i < 30; i++ {
			s.connFailed(h, true, "handshake: EOF")
			s.connFailed(h, false, "unknown client")
		}
		if s.banned(h) {
			t.Errorf("%s banned", h)
		}
	}
}
