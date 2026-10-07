package wire

import (
	"context"
	"net"
	"sync"
	"time"
)

// Resolving the server's host name on every (re)connection can get a client rate-limited
// or blocked by its DNS resolver (a mobile client reconnecting often, or a long outage
// with many retries). Addresses are cached for dnsFresh; when a lookup fails, the last
// good address keeps being used for up to dnsStale instead of retrying the lookup.
const (
	dnsFresh = 10 * time.Minute
	dnsStale = 24 * time.Hour
)

type dnsEntry struct {
	addrs  []string
	at     time.Time
	failed bool // a connection to the cached address failed: the server may have moved
}

// dnsRetry: after a failed connection the name is looked up again, at most this often
// (the server's address can change, e.g. a dynamic home IP).
const dnsRetry = time.Minute

var dnsCache = struct {
	sync.Mutex
	m map[string]dnsEntry
}{m: map[string]dnsEntry{}}

// resolveTarget turns "host:port" into "ip:port" using the cache; IP literals pass through.
func resolveTarget(ctx context.Context, target string) string {
	host, port, err := net.SplitHostPort(target)
	if err != nil || net.ParseIP(host) != nil {
		return target
	}
	dnsCache.Lock()
	e, ok := dnsCache.m[host]
	dnsCache.Unlock()
	if ok && time.Since(e.at) < dnsFresh && !(e.failed && time.Since(e.at) >= dnsRetry) {
		return net.JoinHostPort(e.addrs[0], port)
	}
	lctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	addrs, err := net.DefaultResolver.LookupHost(lctx, host)
	if err != nil || len(addrs) == 0 {
		if ok && time.Since(e.at) < dnsStale {
			return net.JoinHostPort(e.addrs[0], port) // DNS down or refusing: keep the last answer
		}
		return target // nothing cached: let the dialer report the error
	}
	dnsCache.Lock()
	dnsCache.m[host] = dnsEntry{addrs: addrs, at: time.Now()}
	dnsCache.Unlock()
	return net.JoinHostPort(addrs[0], port)
}

// dialFailed marks the cached address of target as failing, so the next connection
// attempt after dnsRetry looks the name up again.
func dialFailed(target string) {
	host, _, err := net.SplitHostPort(target)
	if err != nil {
		return
	}
	dnsCache.Lock()
	if e, ok := dnsCache.m[host]; ok {
		e.failed = true
		dnsCache.m[host] = e
	}
	dnsCache.Unlock()
}
