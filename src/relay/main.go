// Command matriline-relay is the optional public rendezvous for Matriline (docs/PROTOCOL.md
// section 8). Servers and clients both connect OUT to it, so nobody needs port forwarding.
// The relay only splices TCP streams; the MWP handshake runs end to end through it, so with
// security = tls it can read nothing but lengths and timing.
//
// Relay line protocol (one line per connection, before splicing):
//
//	relay -> any : MLRY1 HELLO <nonce-hex>
//	server -> relay: MLRY1 REGISTER <server-id> <pubkey-b64> <sig-b64>   (sig over "mlry1-register <id> <nonce>")
//	client -> relay: MLRY1 ROUTE <server-id>
//	server -> relay: MLRY1 DATA <token>   (answer to "CONNECT <token>" on the control line)
package main

import (
	"bufio"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/agaloya/matriline/common/ident"
)

type relay struct {
	mu       sync.Mutex
	servers  map[string]*control // server id -> control connection
	pending  map[string]chan net.Conn
	perIP    map[string]int
	allowed  map[string]bool // empty = any server may register
	maxPerIP int
	idle     time.Duration
	log      *log.Logger
}

type control struct {
	c  net.Conn
	mu sync.Mutex
}

func (c *control) send(line string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.c.SetWriteDeadline(time.Now().Add(20 * time.Second))
	_, err := io.WriteString(c.c, line+"\n")
	return err
}

const agentVersion = "matriline-relay/0.1.0"

// commit is set by tools/release.sh (-ldflags -X main.commit=<git revision>).
var commit = "dev"

func main() {
	listen := flag.String("listen", ":44100", "address to listen on (443 helps clients behind very restrictive firewalls)")
	allow := flag.String("allow", "", "comma separated server ids allowed to register (empty = any)")
	maxPerIP := flag.Int("max-per-ip", 64, "maximum simultaneous connections per source IP")
	idle := flag.Duration("idle", 15*time.Minute, "close spliced connections idle for this long")
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "matriline-relay - public rendezvous for Matriline servers and clients\n\nUsage: matriline-relay [options] | matriline-relay version\n\n")
		flag.PrintDefaults()
	}
	flag.Parse()
	if flag.NArg() == 1 && flag.Arg(0) == "version" {
		fmt.Printf("%s (commit %s)\n", agentVersion, commit)
		return
	}
	// the relay takes options only (and "version"): any other word is a mistake
	if flag.NArg() > 0 {
		flag.Usage()
		os.Exit(2)
	}
	r := &relay{servers: map[string]*control{}, pending: map[string]chan net.Conn{}, perIP: map[string]int{},
		allowed: map[string]bool{}, maxPerIP: *maxPerIP, idle: *idle,
		log: log.New(os.Stderr, "", log.LstdFlags|log.Lmicroseconds)}
	for _, id := range strings.Split(*allow, ",") {
		if id = strings.TrimSpace(id); id != "" {
			r.allowed[id] = true
		}
	}
	ln, err := net.Listen("tcp", *listen)
	if err != nil {
		r.log.Fatal(err)
	}
	r.log.Printf("matriline-relay listening on %s (allowed servers: %s)", ln.Addr(), nzs(*allow, "any"))
	go func() {
		ch := make(chan os.Signal, 1)
		signal.Notify(ch, os.Interrupt, syscall.SIGTERM)
		<-ch
		ln.Close()
	}()
	for {
		c, err := ln.Accept()
		if err != nil {
			if strings.Contains(err.Error(), "use of closed") {
				return
			}
			time.Sleep(100 * time.Millisecond)
			continue
		}
		go r.handle(c)
	}
}

func nzs(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func (r *relay) handle(c net.Conn) {
	ip, _, _ := net.SplitHostPort(c.RemoteAddr().String())
	r.mu.Lock()
	if r.perIP[ip] >= r.maxPerIP {
		r.mu.Unlock()
		c.Close()
		return
	}
	r.perIP[ip]++
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		r.perIP[ip]--
		if r.perIP[ip] == 0 {
			delete(r.perIP, ip)
		}
		r.mu.Unlock()
	}()
	nonce := make([]byte, 16)
	rand.Read(nonce)
	nhex := hex.EncodeToString(nonce)
	c.SetDeadline(time.Now().Add(15 * time.Second))
	if _, err := fmt.Fprintf(c, "MLRY1 HELLO %s\n", nhex); err != nil {
		c.Close()
		return
	}
	line, err := readLine(c, 512)
	if err != nil {
		c.Close()
		return
	}
	f := strings.Fields(line)
	if len(f) < 3 || f[0] != "MLRY1" {
		c.Close()
		return
	}
	switch {
	case f[1] == "REGISTER" && len(f) == 5:
		r.register(c, f[2], f[3], f[4], nhex)
	case f[1] == "ROUTE":
		r.route(c, f[2])
	case f[1] == "DATA":
		r.data(c, f[2])
	default:
		c.Close()
	}
}

// readLine reads up to '\n' without buffering beyond it.
func readLine(c net.Conn, max int) (string, error) {
	var b [1]byte
	var out []byte
	for len(out) < max {
		if _, err := io.ReadFull(c, b[:]); err != nil {
			return "", err
		}
		if b[0] == '\n' {
			return strings.TrimSpace(string(out)), nil
		}
		out = append(out, b[0])
	}
	return "", fmt.Errorf("line too long")
}

func (r *relay) register(c net.Conn, id, pubB64, sigB64, nonce string) {
	pub, err1 := ident.DecodeKey(pubB64)
	sig, err2 := ident.DecodeKey(sigB64)
	if err1 != nil || err2 != nil || len(pub) != ed25519.PublicKeySize || ident.IDOf(pub) != id ||
		!ed25519.Verify(pub, []byte("mlry1-register "+id+" "+nonce), sig) {
		fmt.Fprintf(c, "ERR bad registration\n")
		c.Close()
		return
	}
	if len(r.allowed) > 0 && !r.allowed[id] {
		fmt.Fprintf(c, "ERR server not allowed on this relay\n")
		c.Close()
		return
	}
	ctl := &control{c: c}
	r.mu.Lock()
	if old := r.servers[id]; old != nil {
		old.c.Close()
	}
	r.servers[id] = ctl
	r.mu.Unlock()
	c.SetDeadline(time.Time{})
	ctl.send("OK")
	r.log.Printf("server %s registered from %s", id, c.RemoteAddr())
	// keepalive: PING every 60 s, the server answers PONG
	done := make(chan struct{})
	go func() {
		t := time.NewTicker(60 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				if ctl.send("PING") != nil {
					c.Close()
					return
				}
			}
		}
	}()
	br := bufio.NewReader(c)
	for {
		c.SetReadDeadline(time.Now().Add(3 * time.Minute))
		if _, err := br.ReadString('\n'); err != nil {
			break
		}
	}
	close(done)
	r.mu.Lock()
	if r.servers[id] == ctl {
		delete(r.servers, id)
	}
	r.mu.Unlock()
	c.Close()
	r.log.Printf("server %s unregistered", id)
}

func (r *relay) route(c net.Conn, id string) {
	r.mu.Lock()
	ctl := r.servers[id]
	r.mu.Unlock()
	if ctl == nil {
		fmt.Fprintf(c, "ERR server not connected to this relay\n")
		c.Close()
		return
	}
	tb := make([]byte, 16)
	rand.Read(tb)
	token := hex.EncodeToString(tb)
	ch := make(chan net.Conn, 1)
	r.mu.Lock()
	r.pending[token] = ch
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		delete(r.pending, token)
		r.mu.Unlock()
	}()
	if err := ctl.send("CONNECT " + token); err != nil {
		c.Close()
		return
	}
	select {
	case sc := <-ch:
		if _, err := io.WriteString(c, "OK\n"); err != nil {
			c.Close()
			sc.Close()
			return
		}
		c.SetDeadline(time.Time{})
		sc.SetDeadline(time.Time{})
		r.splice(c, sc)
	case <-time.After(20 * time.Second):
		fmt.Fprintf(c, "ERR server did not answer\n")
		c.Close()
	}
}

func (r *relay) data(c net.Conn, token string) {
	r.mu.Lock()
	ch := r.pending[token]
	delete(r.pending, token)
	r.mu.Unlock()
	if ch == nil {
		c.Close()
		return
	}
	ch <- c
}

// splice copies both directions until one side closes or the link is idle too long.
func (r *relay) splice(a, b net.Conn) {
	var wg sync.WaitGroup
	cp := func(dst, src net.Conn) {
		defer wg.Done()
		buf := make([]byte, 32<<10)
		for {
			src.SetReadDeadline(time.Now().Add(r.idle))
			n, err := src.Read(buf)
			if n > 0 {
				dst.SetWriteDeadline(time.Now().Add(r.idle))
				if _, werr := dst.Write(buf[:n]); werr != nil {
					break
				}
			}
			if err != nil {
				break
			}
		}
		a.Close()
		b.Close()
	}
	wg.Add(2)
	go cp(a, b)
	go cp(b, a)
	wg.Wait()
}
