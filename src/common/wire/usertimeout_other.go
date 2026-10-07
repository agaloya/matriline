//go:build !linux

package wire

import (
	"net"
	"time"
)

// SetUserTimeout is Linux-only for now; elsewhere TCP keepalive and heartbeats apply.
func SetUserTimeout(c net.Conn, d time.Duration) {}
