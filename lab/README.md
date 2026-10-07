# Matriline virtual lab

A QEMU/KVM lab that reproduces the networks Matriline has to survive: one central
server, one relay and four volunteer hosts on different Linux distributions, each
behind its own home router with one of five NAT/firewall profiles (D14, D26, D31-D35).
Everything runs as the normal user: no bridges, no TAP devices, no sudo on the host.

## Topology

```
                          host (QEMU, normal user)
  ssh 127.0.0.1:2220-2226  ──>  management NICs (slirp, ssh only; "restrict=on" on nodes)

  ┌──────────────────────────── router VM (Alpine) ─────────────────────────────┐
  │  root ns: mgmt NIC = real internet (packages), masquerade for the lab       │
  │                                                                             │
  │  "core" ns = the public internet (203.0.113.0/24, 198.51.100.0/24)          │
  │      │  pcapsampler: every inter-home packet -> lab/captures (max 6 GB)     │
  │      ├── h-relay  (CPE)  ── lan ──>  relay  VM  (Alpine)                    │
  │      ├── h-server (CPE)  ── lan ──>  server VM  (Debian 13)                 │
  │      ├── h-void   (CPE)  ── lan ──>  void   VM  (Void glibc, runit)         │
  │      ├── h-artix  (CPE)  ── lan ──>  artix  VM  (Artix, dinit)              │
  │      ├── h-ubuntu (CPE)  ── lan ──>  ubuntu VM  (Ubuntu Server 26.04)       │
  │      └── "isp" ns (CGNAT 100.64.0.0/10 -> 203.0.113.100)                    │
  │              └── h-<node> of every P5 home  ── lan ──>  e.g. devuan VM      │
  └─────────────────────────────────────────────────────────────────────────────┘
```

Each `h-<node>` namespace is that node's home router: its LAN NIC is a point-to-point
QEMU link (`-netdev dgram` over 127.0.0.1 UDP) to the node VM, it runs dnsmasq
(DHCP + DNS for the node), nftables (the NAT profile) and optional tc-netem.
Node VMs have two NICs: `mgmt` (slirp, ssh from the host only; it cannot reach the
host or the internet, so it never carries lab traffic) and `lan` (default route).

| VM | OS | vCPU | RAM | disk | role | host shares |
|---|---|---|---|---|---|---|
| router | Alpine 3.24 | 1 | 512 MB | 1 G | simulated internet, NAT, capture | 9p |
| relay | Alpine 3.24 | 1 | 256 MB | 1 G | Matriline relay | 9p |
| server | Debian 13 | 2 | 4 GB | 8 G | central server + its own client (D32) | virtiofs |
| void | Void Linux glibc | 1 | 1.5 GB | 6 G | client (1-core edge case, D35) | 9p |
| artix | Artix Linux (dinit, ConnMan) | 2 | 3 GB | 8 G | client | 9p |
| devuan | Devuan 6 excalibur (sysvinit, ifupdown + dhcpcd) | 3 | 6.5 GB | 10 G | client | virtiofs |
| ubuntu | Ubuntu Server 26.04 | 6 | 10 GB | 8 G | client (most resources) | 9p |

Compute VMs see the host's ORCA trees read-only at the same paths as on the host
(`/opt/orca-6.1.1`, `/opt/openmpi-4.1.8`; 6.1.0 was removed on 2026-10-05) plus `src/bin` as
`/opt/matriline/bin`; `guest/guest-setup.sh` writes the wrappers `orca`, `orca-611`,
`orca-610` (same as the host's). Nothing of ORCA is copied into the images.
The Debian "cloud" kernel has no 9p support, so server uses virtiofs (one unprivileged
`virtiofsd --sandbox namespace` per share); devuan uses virtiofs too, so that it is also
exercised on a non-systemd guest.

Init and network stacks of the compute nodes (all glibc x86-64, as ORCA 6.1.1 needs):
server Debian 13 and ubuntu systemd + netplan/systemd-networkd (written by cloud-init),
void runit + dhcpcd, artix dinit + ConnMan, devuan sysvinit + ifupdown (dhcpcd as its DHCP
client). Void, Artix and Devuan publish no cloud image: `labctl build` builds their disks
in a throw-away builder VM (`guest/build-void.sh`, `build-artix.sh`, `build-devuan.sh`),
so they have no cloud-init; the builder writes the matriline user, the NIC names
(udev rules by MAC) and the network configuration, then runs `guest/guest-setup.sh`.

Every vCPU is pinned to its own physical core by `tools/pin.sh` (called by
`labctl up` after each VM starts; the host has 16 cores / 32 threads and the lab has
exactly 16 vCPUs). All other QEMU threads go to the sibling threads 16-31. Changing a
VM's vCPU count requires changing the core map in `tools/pin.sh`.

## NAT profiles

| profile | what it models | node address | public address | inbound |
|---|---|---|---|---|
| P1 | public IP, no NAT | 198.51.100.(16i+2) | same | everything |
| P2 | permissive full-cone NAT (endpoint-independent mapping and filtering) + port forwards | 192.168.i.100 | 203.0.113.(10+i) | forwards, and any remote host to a port the node already used outbound (5 min) |
| P3 | typical home router: NAT + stateful firewall | 192.168.i.100 | 203.0.113.(10+i) | only one port forward (the first one), nothing else |
| P4 | symmetric NAT (random port per flow) + strict firewall, 60 s TCP idle timeout | 192.168.i.100 | 203.0.113.(10+i) | nothing (forwards ignored) |
| P5 | CGNAT shared by several homes + home NAT, egress TCP/443 only, 30 s idle timeout | 192.168.i.100 | 203.0.113.100 (shared) | nothing |

`i` is the node index (relay 1, server 2, void 3, artix 4, devuan 5, ubuntu 6).
Defaults: relay P1, server P2 (forward 44100), void P3, artix P4, devuan P5, ubuntu P3.
The settings live in `state/net/<node>.conf` on the host; the router reads them at
boot and `labctl` re-applies a home live and bounces the node's link so it renews
its DHCP lease.

## Using labctl

```sh
lab/labctl build                 # downloads, Void image, overlays, cloud-init seeds (idempotent)
lab/labctl up                    # router first, then all nodes; waits for ssh + cloud-init
lab/labctl status                # state, profile, addresses, host RSS
lab/labctl ssh ubuntu            # shell as user matriline (passwordless sudo)
lab/labctl ssh void 'orca-611 x.inp'
lab/labctl profile               # list profiles;  lab/labctl profile void P4  (live)
lab/labctl forward server 44100  # port forward on the home router (P2/P3)
lab/labctl netem artix delay 100ms 20ms | loss 5% | down | up | clear | show
lab/labctl addr server           # address other nodes must use to reach "server"
lab/labctl kill artix            # hard power cut;  lab/labctl up artix  boots it again
lab/labctl captures              # capture size and sampling stage
# capture is OFF since 2026-10-05 (user): the running router still has its sampler but
# lab/captures is read-only (chmod 0555), so nothing is stored; a reinstalled router only
# starts it with CAPTURE=1 in lab/state/router.env (then chmod 0755 lab/captures)
lab/labctl down                  # clean shutdown, router last
```

To reinstall every guest OS from scratch: `labctl down`, delete `images/vms/*.qcow2`
and `images/seed/*.iso`, then `labctl build && labctl up` (first boot provisions with
cloud-init; about 2.5 minutes for all seven VMs). `images/base/` keeps the downloaded
cloud images and the built Void, Artix and Devuan images (builders: Debian, Arch). Files under `images/`, `run/`, `state/`,
`keys/` and `captures/` are machine-local and not committed.

Notes from making the lab boot reliably:
- Alpine's OpenSSH is built without PAM and rejects key logins to accounts whose
  shadow password is `!` (what cloud-init's `lock_passwd` writes); the seed sets it
  to `*` instead (still no password login).
- `-vga none` makes the GRUB of the Debian and Void images reboot in a loop, so the
  VMs keep QEMU's default VGA device (with `-display none`).
- Ubuntu 26.04 brings its NICs up in the initramfs, so cloud-init cannot rename them
  on first boot; `guest-setup.sh` renames them to `mgmt`/`lan` by MAC.
- nftables reserves `fwd` and `in` as keywords, so the filter chains in
  `router/netctl.sh` are called `filt_fwd`/`filt_in`.
- `cmd | grep -q` under `set -o pipefail` fails at random (SIGPIPE), so netctl.sh tests
  `/run/netns/<name>` to see whether a namespace exists.
- Devuan: `debootstrap --variant=minbase` has no e2fsprogs; without `fsck.ext4` the
  sysvinit root fsck fails and the boot stops in maintenance mode. The keyring on
  files.devuan.org lacks the current archive key (apt: `NO_PUBKEY B3982868D104092C`), so
  the image installs the `devuan-keyring` package.
- Devuan: dhclient ignores carrier changes, so the link bounce after `labctl profile`
  did not change the address; ifplugd is gone from Debian 13 and netplug 1.2.9.2 never
  reaps its `ifup` child (stuck in WAIT_IN). ifupdown therefore runs dhcpcd.
- Artix: ConnMan made the gateway-less `mgmt` its default service (`default dev mgmt`),
  so `mgmt` is blacklisted in ConnMan and configured from `rc.local`; ConnMan's tmpfiles
  `L` line does not replace the stock `/etc/resolv.conf`, so the image links it to
  `/run/connman/resolv.conf`.
- `netctl.sh remove <node>` drops one home on the running router; use it before
  renaming a node (it renames the router NIC to `r-<node>` so two can wait in the root
  namespace).

## CPU temperature limit

The VMs have no temperature sensor. A host script (outside the repo) writes the host CPU's
Tctl every 30 s to `src/bin/host-temp`, which the VMs see read-only as
`/opt/matriline/bin/host-temp`. `tests/matriline.sh` points every client's
`limits.cpu_temperature_file` there and sets `limits.cpu_temperature_limit` per VM
(server 80, void 79, artix 82, devuan 84, ubuntu 85 °C), so the clients stop taking new
tasks one after another as the host heats up. If the host script is not running the file
goes stale; the clients then keep reading the last value.

## Tests

`tests/labtests.sh orca|isolation|matrix|profiles|pairs|capture|all` (host side,
VMs up). `tests/matriline.sh` deploys Matriline itself and runs campaign scenarios.

### Host replacement check (2026-10-04: arch -> artix, alma -> devuan)

Fresh images, run while the BDE campaign kept going on the other nodes.
`labtests.sh orca`: 15/15 PASS (artix and devuan: -74.963146775728 with orca-611,
orca-610 and orca). `labtests.sh isolation`: default route and lab route on `lan`, slirp
host blocked on all six nodes. Profile checks (relay serving TCP/UDP 45100 and TCP 443):
artix in P4: inbound timeout; outbound TCP/UDP OK with a random public port; idle 40 s
OK, 75 s dropped. devuan in P5: inbound timeout; outbound TCP 45100 rejected, UDP 45100
and 53 timeout, TCP 443 OK from the shared 203.0.113.100; idle 45 s on 443 dropped.
`labctl profile <node> P1` and back moved both nodes to the new address within seconds.
`matriline-client doctor`: sandbox landlock-abi10+userns+netns (artix) and
landlock-abi6+userns+netns (devuan); both clients connected. devuan finished campaign
jobs. artix's first campaign job (a DLPNO replica) ran 4.5 min in the sandbox and ended
with "machine: scratch disk full": its 8 G disk leaves 6.5 GB free, like the old arch,
and the remaining DLPNO jobs need about 6.3-6.8 GB. A water job through a private server
on artix itself finished OK (-74.963146775728).

### Acceptance run (2026-10-03, all VMs rebuilt from scratch, `labtests.sh all`, 10 min 36 s)

In the network tests "FAIL"/"no" is often the *expected* result (a NAT that must
block). The "expected" column says what each profile should do; every row matched.

**ORCA** (HF/STO-3G water through the `orca-611`, `orca-610` and `orca` wrappers on
the host's shared trees): 15/15 PASS. server, void, arch, alma and ubuntu all give
`FINAL SINGLE POINT ENERGY -74.963146775728`, bit-identical to the host. The
versions reported are 6.1.1 and 6.1.0.

**Management NIC isolation**: on all six nodes the default route and the route to the
lab internet use `lan`, and the slirp host (10.0.2.2) is `blocked`.

**Pairwise matrix** (default profiles, TCP 44100 to the destination's public address;
`OK (x)` = connected, x = source address as seen by the destination):

| src \ dst | relay (P1) | server (P2, fwd 44100) | void (P3) | arch (P4) | alma (P5) | ubuntu (P3) |
|---|---|---|---|---|---|---|
| relay | - | OK (198.51.100.18) | no | no | no | no |
| server | OK (203.0.113.12) | - | no | no | no | no |
| void | OK (203.0.113.13) | OK (203.0.113.13) | - | no | no | no |
| arch | OK (203.0.113.14, random port) | OK (203.0.113.14, random port) | no | - | no | no |
| alma | no (P5: egress 443 only) | no | no | no | - | no |
| ubuntu | OK (203.0.113.16) | OK (203.0.113.16) | no | no | no | - |

Inbound only reaches P1 and a forwarded P2 port; P4 rewrites the source port on
every flow (symmetric NAT); P5 cannot reach port 44100 at all.

**Profile behaviour** (node void in each profile; relay and server in P1):

| check | P1 | P2 | P3 | P4 | P5 | expected |
|---|---|---|---|---|---|---|
| inbound TCP, no forward | OK | refused | timeout | timeout | timeout | only P1 open |
| inbound TCP with forward 44100 | n/a | OK | OK | n/a | n/a | P2/P3 honour forwards |
| outbound TCP 44100, source port kept | OK, 40000 | OK, 40000 | OK, 40000 | OK, random | no route (rejected) | P4 randomises, P5 only 443 |
| outbound TCP 443 to relay / server | OK / OK | OK / OK | OK / OK | OK / OK | OK / OK | all |
| outbound UDP 44100 | OK | OK | OK | OK, random port | timeout | P5 only TCP/443 |
| third party UDP to an existing mapping | OK | OK | timeout | timeout | timeout | only P1/P2 (endpoint-independent filtering) |

Idle timeouts (TCP connection kept idle, then used): P3 75 s OK; P4 40 s OK and
75 s dropped (60 s timeout); P5 45 s on port 443 dropped (30 s timeout).
CGNAT: void and alma (both P5) reach relay:443 from the same public address
203.0.113.100; UDP 53 from P5 is blocked.

**Profile pairs** (client ubuntu in row profile -> server in column profile, server has
forward 44100):

| client \ server | P1 | P2 | P3 | P4 | P5 |
|---|---|---|---|---|---|
| P1 | OK | OK | OK | no | no |
| P2 | OK | OK | OK | no | no |
| P3 | OK | OK | OK | no | no |
| P4 | OK | OK | OK | no | no |
| P5 | no | no | no | no | no |

As designed: a server in P4/P5 cannot accept connections (that is what the relay is
for), and a P5 client can only use port 443.

**Capture**: the three TCP SYNs ubuntu (203.0.113.16) -> relay (198.51.100.18):44100
and the tagged UDP datagram were found in `captures/capture-000002-*.pcap`
(2047 packets eligible, 2047 kept, sampling 1 of 1, 180 KB).

Fault injection: `labctl netem arch delay 100ms` raised the arch -> 203.0.113.1 RTT
from 0.2 ms to about 200 ms. The delay is applied once on each side of the home
router, so the RTT goes up by twice the value. `netem clear` restored it.
