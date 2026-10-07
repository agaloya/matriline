package main

import (
	"net"
	"strings"
	"testing"
	"time"
)

func TestHasLocalIP(t *testing.T) {
	if !hasLocalIP(net.ParseIP("127.0.0.1")) {
		t.Fatal("loopback address reported as gone")
	}
	if hasLocalIP(net.ParseIP("203.0.113.99")) { // TEST-NET-3, never assigned locally
		t.Fatal("an address this host does not have reported as present")
	}
}

func TestUnreachableHint(t *testing.T) {
	_, err := net.DialTimeout("tcp", "127.0.0.1:1", time.Second) // nothing listens on port 1
	if err == nil {
		t.Skip("port 1 open")
	}
	if !isDialError(err) {
		t.Fatalf("not a dial error: %v", err)
	}
	h := unreachableHint("example.org:44100", "", err)
	if !strings.Contains(h, "example.org:44100") || !strings.Contains(h, "443") || !strings.Contains(h, "refused") {
		t.Fatal(h)
	}
}

func TestHumanBytes(t *testing.T) {
	for n, want := range map[uint64]string{512: "512 B", 3400: "3.4 KB", 12_500_000: "12.50 MB"} {
		if got := humanBytes(n); got != want {
			t.Errorf("%d: %s", n, got)
		}
	}
}
