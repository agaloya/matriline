package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"time"

	"github.com/agaloya/matriline/common/wire"
)

// watchLink closes the session's connection as soon as it is certainly dead, instead of
// waiting for the TCP timeout (~90 s, wire.DeadPeerTimeout):
//   - the computer was suspended: Go's monotonic clock (CLOCK_MONOTONIC on Linux) stops
//     during suspend while the wall clock keeps going, so a gap between them is a resume;
//     the NAT on the way has usually forgotten the connection by then;
//   - the connection's local address no longer exists on any interface (another network).
//
// Seen on the real-network test: after a 26 min suspend and a change from the home Wi-Fi to
// another network, the client noticed only 89 s after the network came back.
func (a *Agent) watchLink(conn *wire.Conn, stop <-chan struct{}) {
	const every = 5 * time.Second
	t := time.NewTicker(every)
	defer t.Stop()
	prev := time.Now()
	local := conn.NetConn().LocalAddr()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
		}
		now := time.Now()
		if gap := now.Round(0).Sub(prev.Round(0)) - now.Sub(prev); gap > 10*time.Second {
			a.log.Infof("resumed after ~%s asleep: reconnecting now", gap.Round(time.Second))
			conn.Close()
			return
		}
		prev = now
		if ip := addrIP(local); ip != nil && !ip.IsLoopback() && !hasLocalIP(ip) {
			a.log.Infof("local address %s is gone (network changed): reconnecting now", ip)
			conn.Close()
			return
		}
	}
}

func addrIP(a net.Addr) net.IP {
	if t, ok := a.(*net.TCPAddr); ok {
		return t.IP
	}
	return nil
}

// hasLocalIP reports whether ip is still assigned to an interface (true if unknown).
func hasLocalIP(ip net.IP) bool {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return true
	}
	for _, a := range addrs {
		if n, ok := a.(*net.IPNet); ok && n.IP.Equal(ip) {
			return true
		}
	}
	return false
}

// isDialError: the server could not be reached at all (no session was opened).
func isDialError(err error) bool {
	var op *net.OpError
	return errors.As(err, &op) && op.Op == "dial"
}

// unreachableHint explains, for someone who is not a network person, what to check when
// the server cannot be reached.
func unreachableHint(server, relay string, err error) string {
	addr := server
	if relay != "" {
		addr = relay + " (the relay)"
	}
	portal := ""
	if ne, ok := err.(interface{ Timeout() bool }); ok && ne.Timeout() {
		// silently dropped connections: typical of a public Wi-Fi waiting for its login page
		// (3.5 h in a real-network test)
		portal = " If this is a public Wi-Fi (hotel, café, airport), open a web page: it may ask you to log in or accept its terms first."
	}
	return fmt.Sprintf("cannot reach the Matriline server at %s (%v). Check that this computer is online; "+
		"that its network lets it connect to that address and port (some networks only allow web ports such as 443: "+
		"ask the admin for an address on port 443 or for the relay); and that the address in credential.conf is "+
		"right and the server is running (ask the server admin).", addr, err) + portal
}

// waitOrNetwork waits d before the next connection attempt, but returns at once when a new
// network address appears (the Wi-Fi came up after a resume or a move: no need to sit out
// the rest of the backoff; the network-change test lost ~3 s here). false if ctx
// ended.
func waitOrNetwork(ctx context.Context, d time.Duration) bool {
	before := localAddrs()
	t := time.NewTimer(d)
	defer t.Stop()
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return false
		case <-t.C:
			return true
		case <-tick.C:
			for a := range localAddrs() {
				if !before[a] {
					return true
				}
			}
		}
	}
}

// localAddrs is the set of this computer's non-loopback addresses.
func localAddrs() map[string]bool {
	out := map[string]bool{}
	addrs, _ := net.InterfaceAddrs()
	for _, a := range addrs {
		if n, ok := a.(*net.IPNet); ok && !n.IP.IsLoopback() && !n.IP.IsLinkLocalUnicast() {
			out[n.IP.String()] = true
		}
	}
	return out
}

// humanBytes: "512 B", "3.4 KB", "12.50 MB" (a short session's few KB of heartbeats used to
// show as "0.00 MB").
func humanBytes(n uint64) string {
	switch {
	case n < 1000:
		return fmt.Sprintf("%d B", n)
	case n < 1e6:
		return fmt.Sprintf("%.1f KB", float64(n)/1e3)
	}
	return fmt.Sprintf("%.2f MB", float64(n)/1e6)
}
