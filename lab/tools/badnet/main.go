// badnet - a TCP proxy that makes a connection behave like a bad wireless link, for tests
// where kernel traffic shaping (tc netem) is not available (it needs root; the Windows VMs
// use QEMU user networking, outside the lab router). Each chunk is delayed by a base
// latency plus random jitter, the link stalls now and then (nothing passes for a while,
// like a signal dropout) and a connection is cut at random. Stdlib only.
//
//	badnet -listen 127.0.0.1:44397 -to 127.0.0.1:44390 -delay 300ms -jitter 200ms \
//	       -stall-every 60s -stall 15s -cut-every 3m
//
// Times are averages (exponential); 0 turns a behaviour off. Events are logged.
package main

import (
	"flag"
	"log"
	"math/rand/v2"
	"net"
	"sync"
	"time"
)

var (
	listen     = flag.String("listen", "127.0.0.1:44397", "address to listen on")
	to         = flag.String("to", "127.0.0.1:44390", "address to forward to")
	delay      = flag.Duration("delay", 200*time.Millisecond, "base one-way latency")
	jitter     = flag.Duration("jitter", 100*time.Millisecond, "random extra latency, 0..jitter")
	stallEvery = flag.Duration("stall-every", time.Minute, "average time between stalls (0 = none)")
	stallFor   = flag.Duration("stall", 10*time.Second, "how long a stall lasts")
	cutEvery   = flag.Duration("cut-every", 3*time.Minute, "average connection lifetime before a cut (0 = never)")
)

// link is shared by all connections: a stall freezes every direction at once.
var link struct {
	mu    sync.Mutex
	until time.Time
}

func stalled() time.Duration {
	link.mu.Lock()
	defer link.mu.Unlock()
	return time.Until(link.until)
}

func expo(mean time.Duration) time.Duration {
	return time.Duration(rand.ExpFloat64() * float64(mean))
}

// pipe copies src to dst, holding each chunk for the latency and any stall.
func pipe(dst, src net.Conn, done chan<- struct{}) {
	defer func() { done <- struct{}{} }()
	buf := make([]byte, 32<<10)
	for {
		n, err := src.Read(buf)
		if n > 0 {
			wait := *delay
			if *jitter > 0 {
				wait += time.Duration(rand.Int64N(int64(*jitter)))
			}
			if s := stalled(); s > 0 {
				wait += s
			}
			time.Sleep(wait)
			if _, werr := dst.Write(buf[:n]); werr != nil {
				return
			}
		}
		if err != nil {
			return
		}
	}
}

func serve(c net.Conn) {
	defer c.Close()
	s, err := net.DialTimeout("tcp", *to, 10*time.Second)
	if err != nil {
		log.Printf("dial %s: %v", *to, err)
		return
	}
	defer s.Close()
	done := make(chan struct{}, 2)
	go pipe(s, c, done)
	go pipe(c, s, done)
	var cut <-chan time.Time
	if *cutEvery > 0 {
		cut = time.After(expo(*cutEvery))
	}
	select {
	case <-done:
	case <-cut:
		log.Printf("cut %s", c.RemoteAddr())
	}
}

func main() {
	flag.Parse()
	ln, err := net.Listen("tcp", *listen)
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("badnet %s -> %s: delay %s + jitter %s, stall %s every ~%s, cut every ~%s",
		*listen, *to, *delay, *jitter, *stallFor, *stallEvery, *cutEvery)
	if *stallEvery > 0 {
		go func() {
			for {
				time.Sleep(expo(*stallEvery))
				link.mu.Lock()
				link.until = time.Now().Add(*stallFor)
				link.mu.Unlock()
				log.Printf("stall %s", *stallFor)
			}
		}()
	}
	for {
		c, err := ln.Accept()
		if err != nil {
			log.Fatal(err)
		}
		go serve(c)
	}
}
