//go:build linux

package wire

import (
	"net"
	"syscall"
	"time"
)

// SetUserTimeout makes the kernel drop a TCP connection whose sent data stays
// unacknowledged for d (TCP_USER_TIMEOUT, RFC 5482). After a network change (a laptop on
// the move, a NAT that forgot the connection) keepalives do not fire while data is
// pending, and Linux retransmits for ~15 minutes before giving up; seen in the lab: a
// client kept writing heartbeats into a dead connection for minutes.
func SetUserTimeout(c net.Conn, d time.Duration) {
	tc, ok := c.(*net.TCPConn)
	if !ok {
		return
	}
	if rc, err := tc.SyscallConn(); err == nil {
		rc.Control(func(fd uintptr) {
			syscall.SetsockoptInt(int(fd), syscall.IPPROTO_TCP, 0x12 /* TCP_USER_TIMEOUT */, int(d.Milliseconds()))
		})
	}
}
