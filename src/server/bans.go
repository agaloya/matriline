package main

// Automatic IP bans ([network] ban_* options). Two kinds of failed connections:
//
//   - garbage: the peer never sent the protocol's magic bytes (port scanners, other
//     protocols, random data, empty connections). ban_garbage_after of them within
//     ban_garbage_window ban the address for ban_garbage_duration;
//   - auth: the peer speaks the protocol but is not admitted (unknown, wrong or revoked key,
//     bad join token, broken handshake after the magic). Honest clients can hit this now
//     and then (an old credential, a flaky link mid-handshake), so the allowance is larger:
//     ban_auth_after within ban_auth_window, then a temporary ban of ban_auth_duration.
//
// Authenticated connections never count. With ban_trust_known an address from which a
// registered client authenticated within the last 24 h is never banned automatically: on a
// shared NAT (an office, a hotel) an attacker must not lock out a legitimate helper. Its
// failed attempts are still refused and logged. Relayed connections all come from the
// relay's address and are never banned. IPv6 addresses are grouped by /64 (one host
// usually owns a whole /64 and could rotate within it).

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const trustWindow = 24 * time.Hour

type banState struct {
	mu      sync.Mutex
	Bans    map[string]Ban `json:"bans"` // address key -> ban
	garbage map[string][]time.Time
	auth    map[string][]time.Time
	trusted map[string]time.Time // last authenticated connection
}

// Ban is one automatic ban (persisted in state/bans.json).
type Ban struct {
	Until  time.Time `json:"until"`
	Reason string    `json:"reason"`
	Since  time.Time `json:"since"`
}

// banKey groups an address: the IPv4 address itself, or the /64 of an IPv6 address.
func banKey(host string) string {
	ip := net.ParseIP(host)
	if ip == nil {
		return host
	}
	if ip.To4() != nil {
		return ip.To4().String()
	}
	return (&net.IPNet{IP: ip.Mask(net.CIDRMask(64, 128)), Mask: net.CIDRMask(64, 128)}).String()
}

func (s *Server) bansPath() string { return filepath.Join(s.conf().Root, dState, "bans.json") }

func (s *Server) loadBans() {
	b := &s.bans
	b.mu.Lock()
	defer b.mu.Unlock()
	b.Bans, b.garbage, b.auth, b.trusted = map[string]Ban{}, map[string][]time.Time{}, map[string][]time.Time{}, map[string]time.Time{}
	if raw, err := os.ReadFile(s.bansPath()); err == nil {
		_ = json.Unmarshal(raw, &b.Bans)
	}
	if b.Bans == nil {
		b.Bans = map[string]Ban{}
	}
}

// saveBans writes state/bans.json; caller holds b.mu.
func (s *Server) saveBans() {
	raw, err := json.MarshalIndent(s.bans.Bans, "", "  ")
	if err != nil {
		return
	}
	tmp := s.bansPath() + ".tmp"
	if os.WriteFile(tmp, raw, 0o600) == nil {
		_ = os.Rename(tmp, s.bansPath())
	}
}

// banned reports whether host is under an automatic ban (expired bans are dropped).
func (s *Server) banned(host string) bool {
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		return false // (an older version's ban of 127.0.0.1 is ignored too)
	}
	b := &s.bans
	k := banKey(host)
	b.mu.Lock()
	defer b.mu.Unlock()
	ban, ok := b.Bans[k]
	if !ok {
		return false
	}
	if time.Now().After(ban.Until) {
		delete(b.Bans, k)
		s.saveBans()
		return false
	}
	return true
}

// connOK records an authenticated connection from host.
func (s *Server) connOK(host string) {
	b := &s.bans
	b.mu.Lock()
	b.trusted[banKey(host)] = time.Now()
	b.mu.Unlock()
}

// connFailed counts a failed connection (garbage = it never spoke the protocol) and bans
// the address when the configured threshold is reached.
func (s *Server) connFailed(host string, garbage bool, why string) {
	// never this computer: its own tools ('doctor' tests the port with a bare connection)
	// banned 127.0.0.1 for 7 days on a Mac before any client had enrolled (a macOS
	// test), and whoever can connect from here already controls the server
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		return
	}
	cfg := s.conf()
	after, window, dur, kind := cfg.BanAuthAfter, cfg.BanAuthWindow, cfg.BanAuthDuration, "failed authentications"
	if garbage {
		after, window, dur, kind = cfg.BanGarbageAfter, cfg.BanGarbageWindow, cfg.BanGarbageDuration, "connections that do not speak the protocol"
	}
	if after <= 0 {
		return
	}
	k := banKey(host)
	now := time.Now()
	b := &s.bans
	b.mu.Lock()
	m := b.auth
	if garbage {
		m = b.garbage
	}
	var recent []time.Time
	for _, t := range m[k] {
		if now.Sub(t) < window {
			recent = append(recent, t)
		}
	}
	recent = append(recent, now)
	m[k] = recent
	if len(m) > 50000 { // many addresses (a botnet): forget the ones outside the window
		for a, ts := range m {
			if now.Sub(ts[len(ts)-1]) >= window {
				delete(m, a)
			}
		}
	}
	if len(recent) < after {
		b.mu.Unlock()
		return
	}
	if cfg.BanTrustKnown && now.Sub(b.trusted[k]) < trustWindow {
		b.mu.Unlock()
		if len(recent) == after { // say it once per window, not on every attempt
			s.alertf("ban", "%s: %d %s within %s, NOT banned because a registered client connected from there within 24 h (shared NAT?); last: %s", k, len(recent), kind, window, why)
		}
		return
	}
	reason := fmt.Sprintf("%d %s within %s (last: %s)", len(recent), kind, window, why)
	b.Bans[k] = Ban{Until: now.Add(dur), Reason: reason, Since: now}
	delete(m, k)
	s.saveBans()
	b.mu.Unlock()
	s.alertf("ban", "%s banned until %s: %s", k, now.Add(dur).Format("2006-01-02 15:04"), reason)
}

// cmdBans lists the automatic bans, or lifts one ("bans lift <address>").
func (s *Server) cmdBans(args []string) (string, error) {
	b := &s.bans
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(args) == 2 && args[0] == "lift" {
		k := banKey(args[1])
		if _, ok := b.Bans[k]; !ok {
			return "", fmt.Errorf("%s is not banned", k)
		}
		delete(b.Bans, k)
		delete(b.garbage, k)
		delete(b.auth, k)
		s.saveBans()
		return "ban lifted: " + k, nil
	}
	if len(args) > 0 {
		return "", fmt.Errorf("usage: bans | bans lift <address>")
	}
	if len(b.Bans) == 0 {
		return "no automatic bans (manual blocks: network.blocked_ips)", nil
	}
	keys := make([]string, 0, len(b.Bans))
	for k := range b.Bans {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var out strings.Builder
	for _, k := range keys {
		ban := b.Bans[k]
		fmt.Fprintf(&out, "%-24s until %s  %s\n", k, ban.Until.Format("2006-01-02 15:04"), ban.Reason)
	}
	return out.String(), nil
}
