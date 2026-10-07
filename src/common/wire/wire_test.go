package wire

import (
	"context"
	"net"
	"testing"

	"github.com/agaloya/matriline/common/ident"
)

func pair(t *testing.T, mode Mode, pinned func(srv *ident.Key) []byte) (*Conn, *Conn, error) {
	t.Helper()
	srvKey, _ := ident.Generate()
	cliKey, _ := ident.Generate()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	type res struct {
		c   *Conn
		err error
	}
	ch := make(chan res, 1)
	go func() {
		raw, err := ln.Accept()
		if err != nil {
			ch <- res{nil, err}
			return
		}
		c, err := (&Acceptor{Key: srvKey, Mode: mode}).Accept(raw)
		if err != nil {
			raw.Close()
		}
		ch <- res{c, err}
	}()
	d := &Dialer{Key: cliKey, ServerPub: pinned(srvKey)}
	cc, err := d.Dial(context.Background(), ln.Addr().String())
	r := <-ch
	if err != nil {
		return nil, nil, err
	}
	if r.err != nil {
		return nil, nil, r.err
	}
	if r.c.PeerID() != cliKey.ID() {
		t.Fatalf("server sees wrong client id")
	}
	return cc, r.c, nil
}

func TestModes(t *testing.T) {
	for _, m := range []Mode{ModeTLS, ModeAuth, ModeNone} {
		t.Run(m.String(), func(t *testing.T) {
			cc, sc, err := pair(t, m, func(k *ident.Key) []byte { return k.Pub })
			if err != nil {
				t.Fatal(err)
			}
			defer cc.Close()
			defer sc.Close()
			if cc.Mode != m || sc.Mode != m {
				t.Fatalf("mode mismatch")
			}
			for i := 0; i < 3; i++ {
				if err := cc.Send(TGetTask, GetTask{Free: i}); err != nil {
					t.Fatal(err)
				}
				var g GetTask
				if err := sc.Expect(TGetTask, &g); err != nil || g.Free != i {
					t.Fatalf("got %v %v", g, err)
				}
				big := make([]byte, 300000)
				big[12345] = 7
				if err := sc.WriteFrame(TChunk, big); err != nil {
					t.Fatal(err)
				}
				typ, p, err := cc.ReadFrame()
				if err != nil || typ != TChunk || len(p) != len(big) || p[12345] != 7 {
					t.Fatalf("chunk roundtrip failed: %v", err)
				}
			}
		})
	}
}

func TestWrongPinnedKeyRejected(t *testing.T) {
	other, _ := ident.Generate()
	for _, m := range []Mode{ModeTLS, ModeAuth, ModeNone} {
		_, _, err := pair(t, m, func(*ident.Key) []byte { return other.Pub })
		if err == nil {
			t.Fatalf("%v: connection to impostor server succeeded", m)
		}
	}
}

// A man in the middle flipping one payload bit must be detected in auth mode.
func TestAuthModeDetectsTampering(t *testing.T) {
	cc, sc, err := pair(t, ModeAuth, func(k *ident.Key) []byte { return k.Pub })
	if err != nil {
		t.Fatal(err)
	}
	defer cc.Close()
	defer sc.Close()
	cc.sendKey = append([]byte{}, cc.sendKey...)
	cc.sendKey[0] ^= 1 // equivalent to a modified tag/payload on the wire
	cc.Send(TGetTask, GetTask{Free: 1})
	if _, _, err := sc.ReadFrame(); err != ErrBadMAC {
		t.Fatalf("expected ErrBadMAC, got %v", err)
	}
}
