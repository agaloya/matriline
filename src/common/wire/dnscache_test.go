package wire

import (
	"context"
	"testing"
	"time"
)

func TestDNSCacheUsesLastGoodAddress(t *testing.T) {
	if got := resolveTarget(context.Background(), "192.0.2.7:44100"); got != "192.0.2.7:44100" {
		t.Fatalf("IP literal changed: %s", got)
	}
	// a cached but stale-for-freshness entry is used when the lookup fails
	dnsCache.Lock()
	dnsCache.m["matriline.invalid"] = dnsEntry{[]string{"192.0.2.9"}, time.Now().Add(-time.Hour), false}
	dnsCache.Unlock()
	if got := resolveTarget(context.Background(), "matriline.invalid:44100"); got != "192.0.2.9:44100" {
		t.Fatalf("expected the last good address, got %s", got)
	}
	// a fresh entry is used without any lookup
	dnsCache.Lock()
	dnsCache.m["fresh.invalid"] = dnsEntry{[]string{"192.0.2.10"}, time.Now(), false}
	dnsCache.Unlock()
	if got := resolveTarget(context.Background(), "fresh.invalid:44100"); got != "192.0.2.10:44100" {
		t.Fatalf("fresh entry not used: %s", got)
	}
}

func TestDNSCacheRetriesAfterFailure(t *testing.T) {
	dnsCache.Lock()
	dnsCache.m["moved.invalid"] = dnsEntry{[]string{"192.0.2.11"}, time.Now().Add(-2 * time.Minute), false}
	dnsCache.Unlock()
	dialFailed("moved.invalid:44100")
	dnsCache.Lock()
	e := dnsCache.m["moved.invalid"]
	dnsCache.Unlock()
	if !e.failed {
		t.Fatal("failure not recorded")
	}
	// the lookup of an .invalid name fails, so the last good address is kept (no hammering)
	if got := resolveTarget(context.Background(), "moved.invalid:44100"); got != "192.0.2.11:44100" {
		t.Fatalf("got %s", got)
	}
}
