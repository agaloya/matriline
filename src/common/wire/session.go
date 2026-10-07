// Package wire implements the Matriline Wire Protocol (MWP/1): the hello exchange that lets
// the server dictate the security mode without downgrade risk, the three security modes
// (tls, auth, none) and the length-prefixed frame layer. See docs/PROTOCOL.md sections 3-4.
package wire

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/agaloya/matriline/common/ident"
)

// Mode is the security mode chosen by the server.
type Mode uint8

const (
	ModeTLS  Mode = 1 // TLS 1.3, mutual authentication, encrypted (default)
	ModeAuth Mode = 2 // authenticated + integrity (HMAC per frame), plaintext payload
	ModeNone Mode = 3 // no protection (identity asserted once at connection start)
)

func (m Mode) String() string {
	switch m {
	case ModeTLS:
		return "tls"
	case ModeAuth:
		return "auth"
	case ModeNone:
		return "none"
	}
	return fmt.Sprintf("mode(%d)", uint8(m))
}

// ParseMode converts a config value into a Mode.
func ParseMode(s string) (Mode, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "tls", "":
		return ModeTLS, nil
	case "auth", "integrity":
		return ModeAuth, nil
	case "none", "off":
		return ModeNone, nil
	}
	return 0, fmt.Errorf("unknown security mode %q (tls, auth, none)", s)
}

const (
	magic          = "MWP1"
	version        = 1
	nonceLen       = 32
	tagLen         = 16
	MaxFrame       = 1<<20 + 64 // payload limit (1 MiB) + headroom
	helloTimeout   = 20 * time.Second
	connectTimeout = 10 * time.Second
)

var (
	ErrBadMagic    = errors.New("mwp: not a Matriline peer")
	ErrBadServer   = errors.New("mwp: server key does not match the pinned key")
	ErrBadMAC      = errors.New("mwp: frame authentication failed")
	ErrFrameTooBig = errors.New("mwp: frame too large")
)

// Conn is an established MWP session.
type Conn struct {
	raw     net.Conn
	stream  io.ReadWriter // raw, tls.Conn or rate limited wrapper
	br      *bufio.Reader
	Mode    Mode
	PeerPub ed25519.PublicKey
	wmu     sync.Mutex
	tx      sync.Mutex // held for the whole duration of multi-frame messages
	// auth mode state
	sendKey, recvKey []byte
	sendSeq, recvSeq uint64
	// counters for telemetry
	BytesIn, BytesOut uint64
	// ReadIdle, if > 0, is the maximum time ReadFrame waits for each frame. With 0,
	// ReadFrame leaves any deadline already set on the connection untouched.
	ReadIdle time.Duration
}

// NetConn exposes the underlying TCP connection (for TCP_INFO statistics).
func (c *Conn) NetConn() net.Conn { return c.raw }

// RemoteAddr returns the peer network address.
func (c *Conn) RemoteAddr() net.Addr { return c.raw.RemoteAddr() }

// SetReadDeadline forwards to the underlying connection.
func (c *Conn) SetReadDeadline(t time.Time) error { return c.raw.SetReadDeadline(t) }

// Close closes the connection.
func (c *Conn) Close() error { return c.raw.Close() }

// PeerID returns the textual identity of the peer.
func (c *Conn) PeerID() string { return ident.IDOf(c.PeerPub) }

// ---------------------------------------------------------------------------------------
// Hello exchange

func helloSigMsg(clientNonce []byte, mode Mode, serverNonce []byte) []byte {
	m := make([]byte, 0, 10+2*nonceLen+1)
	m = append(m, "mwp1-hello"...)
	m = append(m, clientNonce...)
	m = append(m, byte(mode))
	return append(m, serverNonce...)
}

// Dialer holds the client side parameters.
type Dialer struct {
	Key       *ident.Key
	ServerPub ed25519.PublicKey // pinned server key (required)
	Relay     string            // optional relay host:port; then Addr is ignored for TCP
	Timeout   time.Duration
	// Limiter, if set, throttles outgoing bytes (client bandwidth option).
	Limiter *RateLimiter
}

// DeadPeerTimeout: unacknowledged data older than this closes the connection (see
// SetUserTimeout); generous enough for a slow, lossy mobile link.
const DeadPeerTimeout = 90 * time.Second

// Dial connects to the server (directly or through the relay) and runs the handshake.
func (d *Dialer) Dial(ctx context.Context, addr string) (*Conn, error) {
	to := d.Timeout
	if to == 0 {
		to = helloTimeout
	}
	// the TCP connect itself: 10 s still allows three SYN retransmissions on a lossy
	// link; the handshake keeps helloTimeout. With 20 s, a client retried only every 84 s
	// during a long outage (real-network test, 600 s blackhole).
	nd := net.Dialer{Timeout: min(to, connectTimeout), KeepAlive: 30 * time.Second}
	target := addr
	if d.Relay != "" {
		target = d.Relay
	}
	raw, err := nd.DialContext(ctx, "tcp", resolveTarget(ctx, target))
	if err != nil {
		dialFailed(target)
		return nil, err
	}
	SetUserTimeout(raw, DeadPeerTimeout)
	_ = raw.SetDeadline(time.Now().Add(to))
	if d.Relay != "" {
		if err := relayRoute(raw, ident.IDOf(d.ServerPub)); err != nil {
			raw.Close()
			return nil, err
		}
	}
	c, err := clientHandshake(raw, d)
	if err != nil {
		raw.Close()
		return nil, err
	}
	_ = raw.SetDeadline(time.Time{})
	return c, nil
}

// relayRoute asks the relay to splice this connection to the given server.
func relayRoute(raw net.Conn, serverID string) error {
	hello, err := readLine(raw, 128)
	if err != nil || !strings.HasPrefix(hello, "MLRY1 HELLO ") {
		return fmt.Errorf("relay: unexpected greeting %q (%v)", hello, err)
	}
	if _, err := fmt.Fprintf(raw, "MLRY1 ROUTE %s\n", serverID); err != nil {
		return err
	}
	line, err := readLine(raw, 128)
	if err != nil {
		return fmt.Errorf("relay: %v", err)
	}
	if line != "OK" {
		return fmt.Errorf("relay refused: %s", line)
	}
	return nil
}

// ReadRelayHello reads the relay greeting and returns its nonce.
func ReadRelayHello(raw net.Conn) (string, error) {
	hello, err := readLine(raw, 128)
	if err != nil {
		return "", err
	}
	f := strings.Fields(hello)
	if len(f) != 3 || f[0] != "MLRY1" || f[1] != "HELLO" {
		return "", fmt.Errorf("relay: unexpected greeting %q", hello)
	}
	return f[2], nil
}

// readLine reads one '\n'-terminated line byte by byte (no read-ahead, so the rest of the
// stream stays untouched for the next protocol layer).
func readLine(r io.Reader, max int) (string, error) {
	var b [1]byte
	var out []byte
	for len(out) < max {
		if _, err := io.ReadFull(r, b[:]); err != nil {
			return "", err
		}
		if b[0] == '\n' {
			return strings.TrimSpace(string(out)), nil
		}
		out = append(out, b[0])
	}
	return "", errors.New("line too long")
}

func clientHandshake(raw net.Conn, d *Dialer) (*Conn, error) {
	cn := make([]byte, nonceLen)
	rand.Read(cn)
	ch := append([]byte(magic), version)
	ch = append(ch, cn...)
	if _, err := raw.Write(ch); err != nil {
		return nil, err
	}
	// ServerHello: magic(4) ver(1) mode(1) server_nonce(32) server_pub(32) sig(64)
	sh := make([]byte, 4+1+1+nonceLen+ed25519.PublicKeySize+ed25519.SignatureSize)
	if _, err := io.ReadFull(raw, sh); err != nil {
		return nil, err
	}
	if string(sh[:4]) != magic {
		return nil, ErrBadMagic
	}
	mode := Mode(sh[5])
	sn := sh[6 : 6+nonceLen]
	spub := ed25519.PublicKey(sh[6+nonceLen : 6+nonceLen+32])
	sig := sh[6+nonceLen+32:]
	if !bytes.Equal(spub, d.ServerPub) {
		return nil, ErrBadServer
	}
	if !ed25519.Verify(spub, helloSigMsg(cn, mode, sn), sig) {
		return nil, ErrBadServer
	}
	transcript := sha256.Sum256(append(append([]byte{}, ch...), sh...))
	var stream net.Conn = raw
	if d.Limiter != nil {
		stream = &limitedConn{Conn: raw, l: d.Limiter}
	}
	c := &Conn{raw: raw, Mode: mode, PeerPub: spub}
	switch mode {
	case ModeTLS:
		cert, err := d.Key.Certificate()
		if err != nil {
			return nil, err
		}
		tc := tls.Client(stream, &tls.Config{
			MinVersion:         tls.VersionTLS13,
			Certificates:       []tls.Certificate{cert},
			InsecureSkipVerify: true, // chain is not used: trust = pinned server key (below)
			VerifyPeerCertificate: func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
				if len(rawCerts) == 0 {
					return ErrBadServer
				}
				pub, err := ident.PeerKey(rawCerts[0])
				if err != nil || !bytes.Equal(pub, spub) {
					return ErrBadServer
				}
				return nil
			},
			ServerName: "matriline",
		})
		if err := tc.Handshake(); err != nil {
			return nil, err
		}
		c.stream = tc
	case ModeAuth, ModeNone:
		if err := proveIdentity(c, stream, d.Key, transcript[:], true); err != nil {
			return nil, err
		}
		c.stream = stream
	default:
		return nil, fmt.Errorf("mwp: server requested unknown mode %d", mode)
	}
	c.br = bufio.NewReaderSize(c.stream, 64<<10)
	return c, nil
}

// Server side --------------------------------------------------------------------------

// Acceptor holds server side parameters.
type Acceptor struct {
	Key      *ident.Key
	Mode     Mode
	SawHello bool // set by Accept once the peer sent a valid protocol magic
}

// Accept runs the server handshake on a fresh TCP connection. It authenticates key
// possession only; authorization (registry lookup) is the caller's job.
func (a *Acceptor) Accept(raw net.Conn) (*Conn, error) {
	_ = raw.SetDeadline(time.Now().Add(helloTimeout))
	ch := make([]byte, 4+1+nonceLen)
	if _, err := io.ReadFull(raw, ch); err != nil {
		return nil, err
	}
	if string(ch[:4]) != magic {
		return nil, ErrBadMagic
	}
	a.SawHello = true // from here on the peer speaks the protocol (for the ban rules)
	if ch[4] != version {
		return nil, fmt.Errorf("mwp: unsupported protocol version %d", ch[4])
	}
	sn := make([]byte, nonceLen)
	rand.Read(sn)
	sig := ed25519.Sign(a.Key.Priv, helloSigMsg(ch[5:], a.Mode, sn))
	sh := append([]byte(magic), version, byte(a.Mode))
	sh = append(sh, sn...)
	sh = append(sh, a.Key.Pub...)
	sh = append(sh, sig...)
	if _, err := raw.Write(sh); err != nil {
		return nil, err
	}
	transcript := sha256.Sum256(append(append([]byte{}, ch...), sh...))
	c := &Conn{raw: raw, Mode: a.Mode}
	switch a.Mode {
	case ModeTLS:
		cert, err := a.Key.Certificate()
		if err != nil {
			return nil, err
		}
		tc := tls.Server(raw, &tls.Config{
			MinVersion:   tls.VersionTLS13,
			Certificates: []tls.Certificate{cert},
			ClientAuth:   tls.RequireAnyClientCert, // trust = registry lookup by key
		})
		if err := tc.Handshake(); err != nil {
			return nil, err
		}
		st := tc.ConnectionState()
		if len(st.PeerCertificates) == 0 {
			return nil, errors.New("mwp: client sent no certificate")
		}
		pub, err := ident.PeerKey(st.PeerCertificates[0].Raw)
		if err != nil {
			return nil, err
		}
		c.PeerPub = pub
		c.stream = tc
	case ModeAuth, ModeNone:
		if err := proveIdentity(c, raw, a.Key, transcript[:], false); err != nil {
			return nil, err
		}
		c.stream = raw
	}
	_ = raw.SetDeadline(time.Time{})
	c.br = bufio.NewReaderSize(c.stream, 64<<10)
	return c, nil
}

// proveIdentity performs the signed X25519 exchange used by the auth and none modes.
// Both sides sign transcript || own ephemeral key || peer ephemeral key, so each side proves
// possession of its long-term key and the ephemeral keys are bound to this session.
// In auth mode the shared secret keys the per-frame HMACs; in none mode it is discarded.
func proveIdentity(c *Conn, rw io.ReadWriter, key *ident.Key, transcript []byte, isClient bool) error {
	eph, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	myEph := eph.PublicKey().Bytes()
	// 1) exchange ephemeral keys
	if _, err := rw.Write(myEph); err != nil {
		return err
	}
	peerEph := make([]byte, 32)
	if _, err := io.ReadFull(rw, peerEph); err != nil {
		return err
	}
	role := func(client bool) string {
		if client {
			return "client"
		}
		return "server"
	}
	sigMsg := func(r string, own, peer []byte) []byte {
		m := append([]byte("mwp1-id-"+r), transcript...)
		m = append(m, own...)
		return append(m, peer...)
	}
	// 2) exchange long-term public key + signature
	mySig := ed25519.Sign(key.Priv, sigMsg(role(isClient), myEph, peerEph))
	if _, err := rw.Write(append(append([]byte{}, key.Pub...), mySig...)); err != nil {
		return err
	}
	buf := make([]byte, 32+64)
	if _, err := io.ReadFull(rw, buf); err != nil {
		return err
	}
	peerPub := ed25519.PublicKey(buf[:32])
	if !ed25519.Verify(peerPub, sigMsg(role(!isClient), peerEph, myEph), buf[32:]) {
		return errors.New("mwp: peer identity signature invalid")
	}
	if isClient && !bytes.Equal(peerPub, c.PeerPub) {
		return ErrBadServer
	}
	c.PeerPub = peerPub
	if c.Mode == ModeAuth {
		pe, err := ecdh.X25519().NewPublicKey(peerEph)
		if err != nil {
			return err
		}
		shared, err := eph.ECDH(pe)
		if err != nil {
			return err
		}
		c2s, err := hkdf.Key(sha256.New, shared, transcript, "mwp1 c2s mac", 32)
		if err != nil {
			return err
		}
		s2c, err := hkdf.Key(sha256.New, shared, transcript, "mwp1 s2c mac", 32)
		if err != nil {
			return err
		}
		if isClient {
			c.sendKey, c.recvKey = c2s, s2c
		} else {
			c.sendKey, c.recvKey = s2c, c2s
		}
	}
	return nil
}

// ---------------------------------------------------------------------------------------
// Frames

func (c *Conn) tag(key []byte, seq uint64, hdr, payload []byte) []byte {
	m := hmac.New(sha256.New, key)
	var s [8]byte
	binary.BigEndian.PutUint64(s[:], seq)
	m.Write(s[:])
	m.Write(hdr)
	m.Write(payload)
	return m.Sum(nil)[:tagLen]
}

// Sender is implemented by *Conn (each call atomic) and Tx (inside Exclusive).
type Sender interface {
	WriteFrame(t MsgType, payload []byte) error
	Send(t MsgType, v any) error
}

// WriteFrame sends one frame. It is safe for concurrent use and never interleaves with a
// multi-frame message sent through Exclusive.
func (c *Conn) WriteFrame(t MsgType, payload []byte) error {
	c.tx.Lock()
	defer c.tx.Unlock()
	return c.writeFrame(t, payload)
}

// Tx sends frames while the caller holds the connection exclusively.
type Tx struct{ c *Conn }

// WriteFrame sends one frame inside an exclusive section.
func (x Tx) WriteFrame(t MsgType, payload []byte) error { return x.c.writeFrame(t, payload) }

// Send marshals and sends inside an exclusive section.
func (x Tx) Send(t MsgType, v any) error {
	b, err := marshal(v)
	if err != nil {
		return err
	}
	return x.c.writeFrame(t, b)
}

// Exclusive runs fn while no other goroutine can send on the connection, so a message made
// of several frames (TASK or RESULT followed by file chunks) is never interleaved.
func (c *Conn) Exclusive(fn func(Tx) error) error {
	c.tx.Lock()
	defer c.tx.Unlock()
	return fn(Tx{c})
}

func (c *Conn) writeFrame(t MsgType, payload []byte) error {
	if len(payload)+1 > MaxFrame {
		return ErrFrameTooBig
	}
	c.wmu.Lock()
	defer c.wmu.Unlock()
	hdr := make([]byte, 5)
	binary.BigEndian.PutUint32(hdr, uint32(len(payload)+1))
	hdr[4] = byte(t)
	buf := make([]byte, 0, 5+len(payload)+tagLen)
	buf = append(buf, hdr...)
	buf = append(buf, payload...)
	if c.Mode == ModeAuth {
		buf = append(buf, c.tag(c.sendKey, c.sendSeq, hdr, payload)...)
		c.sendSeq++
	}
	n, err := c.stream.Write(buf)
	c.BytesOut += uint64(n)
	return err
}

// ReadFrame reads one frame. Not safe for concurrent readers.
func (c *Conn) ReadFrame() (MsgType, []byte, error) {
	if c.ReadIdle > 0 {
		_ = c.raw.SetReadDeadline(time.Now().Add(c.ReadIdle))
	}
	hdr := make([]byte, 5)
	if _, err := io.ReadFull(c.br, hdr); err != nil {
		return 0, nil, err
	}
	n := binary.BigEndian.Uint32(hdr)
	if n == 0 || n > MaxFrame {
		return 0, nil, ErrFrameTooBig
	}
	payload := make([]byte, n-1)
	if _, err := io.ReadFull(c.br, payload); err != nil {
		return 0, nil, err
	}
	c.BytesIn += uint64(5 + len(payload))
	if c.Mode == ModeAuth {
		tag := make([]byte, tagLen)
		if _, err := io.ReadFull(c.br, tag); err != nil {
			return 0, nil, err
		}
		if subtle.ConstantTimeCompare(tag, c.tag(c.recvKey, c.recvSeq, hdr, payload)) != 1 {
			return 0, nil, ErrBadMAC
		}
		c.recvSeq++
		c.BytesIn += tagLen
	}
	return MsgType(hdr[4]), payload, nil
}
