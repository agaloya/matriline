#!/usr/bin/env python3
"""labnet.py - tiny reachability probe for the Matriline lab (stdlib only).

  labnet.py serve <port>[,<port>...] [seconds]
        TCP+UDP servers. TCP: sends "peer=<ip>:<port>" (address as seen), then
        echoes every line until the peer closes. UDP: answers "peer=..." to each datagram.
  labnet.py tcp <host> <port> [sport]       connect, print OK/FAIL + seen address
  labnet.py udp <host> <port> [sport]       one datagram, print OK/FAIL + seen address
  labnet.py tcpidle <host> <port> <idle_s>  connect, stay idle, then send a line;
                                            OK only if the echo still comes back
  labnet.py udpwait <host> <port> <sport> <seconds>
        bind <sport>, send one datagram to host:port (creates a NAT mapping),
        then answer "peer=..." to anything that arrives on <sport> (any sender)

Copied into the VMs by lab/tests/labtests.sh; Matriline itself never needs it.
"""
import socket
import sys
import threading
import time


def serve(ports, seconds):
    def conn(c, a):
        try:
            c.sendall(f"peer={a[0]}:{a[1]}\n".encode())
            f = c.makefile("rb")
            for line in f:
                c.sendall(line)
        except OSError:
            pass
        finally:
            c.close()

    def tcp(p):
        s = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
        s.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
        s.bind(("0.0.0.0", p))
        s.listen(64)
        while True:
            c, a = s.accept()
            threading.Thread(target=conn, args=(c, a), daemon=True).start()

    def udp(p):
        s = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
        s.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
        s.bind(("0.0.0.0", p))
        while True:
            _, a = s.recvfrom(2048)
            s.sendto(f"peer={a[0]}:{a[1]}\n".encode(), a)

    for p in ports:
        for f in (tcp, udp):
            threading.Thread(target=f, args=(p,), daemon=True).start()
    time.sleep(seconds)


def probe(kind, host, port, sport=0, timeout=4.0):
    st = socket.SOCK_STREAM if kind == "tcp" else socket.SOCK_DGRAM
    s = socket.socket(socket.AF_INET, st)
    s.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
    s.settimeout(timeout)
    try:
        if sport:
            s.bind(("0.0.0.0", sport))
        if kind == "tcp":
            s.connect((host, port))
            data = s.recv(200)
        else:
            s.sendto(b"ping", (host, port))
            data, _ = s.recvfrom(200)
        local = "%s:%d" % s.getsockname()
        print(f"OK {kind} {host}:{port} local={local} {data.decode().strip()}")
        return 0
    except Exception as e:  # timeout, refused, unreachable
        print(f"FAIL {kind} {host}:{port} {type(e).__name__}: {e}")
        return 1
    finally:
        s.close()


def tcpidle(host, port, idle):
    s = socket.create_connection((host, port), timeout=5)
    f = s.makefile("rb")
    first = f.readline().decode().strip()
    time.sleep(idle)
    try:
        s.sendall(b"still-there\n")
        s.settimeout(8)
        back = f.readline()
        ok = back.strip() == b"still-there"
    except Exception as e:
        ok, back = False, repr(e).encode()
    print(f"{'OK' if ok else 'FAIL'} tcpidle {host}:{port} idle={idle}s {first} reply={back!r}")
    return 0 if ok else 1


def udpwait(host, port, sport, seconds):
    s = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
    s.bind(("0.0.0.0", sport))
    s.sendto(b"open-mapping", (host, port))
    end = time.time() + seconds
    while time.time() < end:
        s.settimeout(max(0.1, end - time.time()))
        try:
            data, a = s.recvfrom(2048)
        except socket.timeout:
            break
        print(f"RECV from {a[0]}:{a[1]} {data[:60]!r}", flush=True)
        if not data.startswith(b"peer="):
            s.sendto(f"peer={a[0]}:{a[1]}\n".encode(), a)


if __name__ == "__main__":
    a = sys.argv[1:]
    if not a:
        print(__doc__)
        sys.exit(2)
    if a[0] == "serve":
        serve([int(x) for x in a[1].split(",")], float(a[2]) if len(a) > 2 else 3600)
    elif a[0] == "tcpidle":
        sys.exit(tcpidle(a[1], int(a[2]), float(a[3])))
    elif a[0] == "udpwait":
        udpwait(a[1], int(a[2]), int(a[3]), float(a[4]))
    else:
        sys.exit(probe(a[0], a[1], int(a[2]), int(a[3]) if len(a) > 3 else 0))
