#!/bin/bash
# netctl.sh - builds the simulated internet inside the router VM (run as root).
#
#   netctl.sh boot            core + ISP namespaces, then every home from its state file
#   netctl.sh apply <node>    rebuild one home (profile / forwards / netem changed)
#   netctl.sh remove <node>   remove one home (before a node is renamed or dropped)
#   netctl.sh teardown        remove all lab namespaces
#   netctl.sh show [node]     print the current configuration
#
# Topology (all inside this VM; see lab/README.md for the diagram):
#   root ns  : mgmt NIC (QEMU slirp, real internet for package downloads) + NAT
#   "core"   : the public internet. Point-to-point veths to every home and to
#              the ISP; owns 203.0.113.1 on each of them. pcapsampler runs here.
#   "isp"    : carrier-grade NAT used by P5 homes (100.64.0.0/10 inside,
#              shared public address 203.0.113.100 outside).
#   "h-<n>"  : the CPE (home router) of node <n>: its LAN NIC (point-to-point
#              QEMU link to the node VM), a WAN veth, nftables, dnsmasq (DHCP+DNS).
#
# Node settings come from /mnt/state/net/<node>.conf (written by labctl on the host).
#
# References (block level):
#   - nftables NAT, sets with dynamic updates (full-cone emulation):
#     https://wiki.nftables.org/wiki-nftables/index.php/Performing_Network_Address_Translation_(NAT)
#     https://wiki.nftables.org/wiki-nftables/index.php/Sets (dynamic sets, "update @set")
#   - RFC 4787 terminology (endpoint-independent mapping/filtering, symmetric NAT).
#   - tc-netem(8) man page.
set -euo pipefail

NODES=(relay server void artix devuan ubuntu)
declare -A IDX=([relay]=1 [server]=2 [void]=3 [artix]=4 [devuan]=5 [ubuntu]=6)
STATEDIR=/mnt/state/net
RUNDIR=/run/lab
UPSTREAM_DNS=10.0.2.3 # QEMU slirp DNS on the router's mgmt NIC
CORE_IP=203.0.113.1
ISP_PUB=203.0.113.100
LAB_NETS="203.0.113.0/24 198.51.100.0/24"
mkdir -p "$RUNDIR"

nsx() { local ns=$1; shift; ip netns exec "$ns" "$@"; }
# Named namespaces are files in /run/netns (ip-netns(8)). Test the file instead
# of "ip netns list | grep -q": with pipefail, grep -q exiting at the first
# match gives "ip" a SIGPIPE and the pipeline fails at random.
has_ns() { [[ -e /run/netns/$1 ]]; }
log() { echo "netctl: $*"; }

mac_rlan() { printf '52:54:00:4c:02:%02x' "${IDX[$1]}"; }
mac_node() { printf '52:54:00:4c:01:%02x' "${IDX[$1]}"; }

# Find the router NIC wired to node $1 by its MAC, in whichever namespace it is.
find_nic() {
	local mac ns dev
	mac=$(mac_rlan "$1")
	for ns in "" $(ip netns list | awk '{print $1}'); do
		if [[ -z $ns ]]; then
			dev=$(ip -o link | awk -v m="$mac" '$0 ~ m {sub(":","",$2); sub("@.*","",$2); print $2; exit}')
		else
			dev=$(ip -n "$ns" -o link | awk -v m="$mac" '$0 ~ m {sub(":","",$2); sub("@.*","",$2); print $2; exit}')
		fi
		[[ -n $dev ]] && { echo "$ns $dev"; return 0; }
	done
	return 1
}

no_offload() { # ns dev : make every captured frame a real wire-size packet
	nsx "$1" ethtool -K "$2" gro off gso off tso off lro off >/dev/null 2>&1 || true
}

ns_sysctls() { # ns
	nsx "$1" sysctl -q -w net.ipv4.ip_forward=1 net.ipv4.conf.all.rp_filter=0 \
		net.ipv4.conf.default.rp_filter=0 >/dev/null
	nsx "$1" ip link set lo up
}

ct_timeouts() { # ns tcp_established_s udp_s loose(0|1)
	# Conntrack sysctls are per network namespace. They exist once the
	# namespace has conntrack state, which the nftables NAT table creates.
	nsx "$1" sysctl -q -w net.netfilter.nf_conntrack_tcp_timeout_established="$2" \
		net.netfilter.nf_conntrack_udp_timeout="$3" net.netfilter.nf_conntrack_udp_timeout_stream="$3" \
		net.netfilter.nf_conntrack_tcp_loose="$4" >/dev/null || log "warning: conntrack sysctls in $1"
}

# ------------------------------------------------------------------ core / isp
setup_root_and_core() {
	sysctl -q -w net.ipv4.ip_forward=1 net.ipv4.conf.all.rp_filter=0 net.ipv4.conf.default.rp_filter=0 >/dev/null
	ip netns add core 2>/dev/null || true
	ns_sysctls core
	if ! ip link show up-root >/dev/null 2>&1; then
		ip link add up-root type veth peer name up-core netns core
	fi
	ip addr replace 10.255.255.1/30 dev up-root
	ip link set up-root up
	nsx core ip addr replace 10.255.255.2/30 dev up-core
	nsx core ip link set up-core up
	# Lab public space lives in core; everything else (real internet) goes up to root.
	nsx core ip route replace default via 10.255.255.1
	local n
	for n in $LAB_NETS; do ip route replace "$n" via 10.255.255.2; done
	# Root namespace: masquerade lab traffic leaving through the mgmt NIC (slirp).
	nft -f - <<EOF
table ip labroot
delete table ip labroot
table ip labroot {
	chain post { type nat hook postrouting priority srcnat; policy accept;
		oifname "mgmt" ip saddr { 203.0.113.0/24, 198.51.100.0/24, 10.255.255.0/30 } masquerade
	}
}
EOF
	# Core: a plain router, no filtering (the internet).
	no_offload core up-core
}

setup_isp() {
	ip netns add isp 2>/dev/null || true
	ns_sysctls isp
	if ! nsx core ip link show c-isp >/dev/null 2>&1; then
		nsx core ip link add c-isp type veth peer name wan netns isp
	fi
	nsx core ip addr replace $CORE_IP/32 dev c-isp
	nsx core ip link set c-isp up
	nsx core ip route replace $ISP_PUB/32 dev c-isp
	nsx isp ip addr replace $ISP_PUB/32 dev wan
	nsx isp ip link set wan up
	nsx isp ip route replace default via $CORE_IP dev wan onlink
	no_offload core c-isp
	no_offload isp wan
	# Carrier-grade NAT: many homes share 203.0.113.100; nothing is accepted
	# inbound; short conntrack idle timeouts.
	nsx isp nft -f - <<EOF
table ip cgn
delete table ip cgn
table ip cgn {
	chain post { type nat hook postrouting priority srcnat; policy accept;
		oifname "wan" ip saddr 100.64.0.0/10 masquerade
	}
	chain filt_fwd { type filter hook forward priority filter; policy drop;
		ct state established,related accept
		ct state invalid drop
		iifname "i-*" oifname "wan" accept
	}
	chain filt_in { type filter hook input priority filter; policy drop;
		ct state established,related accept
		iifname "lo" accept
	}
}
EOF
	ct_timeouts isp 30 30 0
}

# ------------------------------------------------------------------ homes
load_conf() { # node -> PROFILE FORWARDS NETEM LINK
	PROFILE=P3 FORWARDS="" NETEM="" LINK=up
	local f=$STATEDIR/$1.conf
	# shellcheck disable=SC1090
	[[ -f $f ]] && source "$f"
	[[ $PROFILE =~ ^P[1-5]$ ]] || { log "$1: bad profile '$PROFILE', using P3"; PROFILE=P3; }
}

teardown_home() { # node
	local n=$1 ns=h-$1
	if [[ -f $RUNDIR/dnsmasq-$n.pid ]]; then
		kill "$(cat "$RUNDIR/dnsmasq-$n.pid")" 2>/dev/null || true
		rm -f "$RUNDIR/dnsmasq-$n.pid"
	fi
	if has_ns "$ns"; then
		# Return the physical NIC to the root namespace before deleting the ns.
		local loc
		if loc=$(find_nic "$n") && [[ ${loc%% *} == "$ns" ]]; then
			nsx "$ns" ip link set "${loc#* }" down || true
			# unique name: a second "lan" in the root namespace would block the move
			nsx "$ns" ip link set "${loc#* }" name "r-$n" || true
			nsx "$ns" ip link set "r-$n" netns 1 || true
		fi
		ip netns del "$ns"
	fi
	nsx core ip link del "c-$n" 2>/dev/null || true
	nsx isp ip link del "i-$n" 2>/dev/null || true
	# Clean stale ISP routes for this home.
	nsx isp ip route del "100.64.${IDX[$n]}.2/32" 2>/dev/null || true
}

apply_home() { # node
	local n=$1 i=${IDX[$1]} ns=h-$1
	load_conf "$n"
	teardown_home "$n"
	local loc dev
	loc=$(find_nic "$n") || { log "$n: router NIC $(mac_rlan "$n") not found"; return 1; }
	dev=${loc#* }
	ip netns add "$ns"
	ns_sysctls "$ns"
	ip link set "$dev" down
	ip link set "$dev" netns "$ns"
	nsx "$ns" ip link set "$dev" name lan
	no_offload "$ns" lan

	# WAN side: P1-P4 attach to core directly, P5 to the ISP's CGNAT.
	local wanip gw lanip lannet nodeip mask
	if [[ $PROFILE == P5 ]]; then
		nsx isp ip link add "i-$n" type veth peer name wan netns "$ns"
		wanip=100.64.$i.2 gw=100.64.0.1
		nsx isp ip addr replace 100.64.0.1/32 dev "i-$n"
		nsx isp ip link set "i-$n" up
		nsx isp ip route replace "$wanip/32" dev "i-$n"
		no_offload isp "i-$n"
	else
		nsx core ip link add "c-$n" type veth peer name wan netns "$ns"
		wanip=203.0.113.$((10 + i)) gw=$CORE_IP
		nsx core ip addr replace $CORE_IP/32 dev "c-$n"
		nsx core ip link set "c-$n" up
		nsx core ip route replace "$wanip/32" dev "c-$n"
		no_offload core "c-$n"
	fi
	nsx "$ns" ip addr add "$wanip/32" dev wan
	nsx "$ns" ip link set wan up
	nsx "$ns" ip route add default via "$gw" dev wan onlink
	no_offload "$ns" wan

	# LAN side.
	if [[ $PROFILE == P1 ]]; then
		# Public /28 routed to the home: the node owns a public address, no NAT.
		lannet=198.51.100.$((16 * i)) lanip=198.51.100.$((16 * i + 1)) nodeip=198.51.100.$((16 * i + 2)) mask=28
		nsx core ip route replace "$lannet/28" via "$wanip" dev "c-$n" onlink
	else
		lannet=192.168.$i.0 lanip=192.168.$i.1 nodeip=192.168.$i.100 mask=24
	fi
	nsx "$ns" ip addr add "$lanip/$mask" dev lan
	[[ $LINK == up ]] && nsx "$ns" ip link set lan up

	# Firewall / NAT per profile.
	local fw fwd_tcp="" first
	read -r -a fwds <<<"${FORWARDS:-}"
	case $PROFILE in
	P2) [[ ${#fwds[@]} -gt 0 ]] && fwd_tcp=$(IFS=,; echo "${fwds[*]}") ;;
	P3) [[ ${#fwds[@]} -gt 0 ]] && fwd_tcp=${fwds[0]} ;; # a single forward
	esac
	case $PROFILE in
	P1) fw=$(rules_p1) ;;
	P2) fw=$(rules_p2 "$nodeip" "$fwd_tcp") ;;
	P3) fw=$(rules_p3 "$nodeip" "$fwd_tcp") ;;
	P4) fw=$(rules_p4) ;;
	P5) fw=$(rules_p5) ;;
	esac
	echo "$fw" | nsx "$ns" nft -f -
	case $PROFILE in
	P4) ct_timeouts "$ns" 60 30 0 ;;  # strict: 60 s TCP idle timeout, no mid-stream pickup
	P5) ct_timeouts "$ns" 30 30 0 ;;  # CGNAT homes: short idle timeout
	*) ct_timeouts "$ns" 7440 30 1 ;;  # Linux defaults (2 h 4 min)
	esac

	# DHCP + DNS for the node (dnsmasq bound to the home's LAN only).
	local range_mask=255.255.255.0
	[[ $mask == 28 ]] && range_mask=255.255.255.240
	nsx "$ns" dnsmasq --conf-file=/dev/null --no-hosts --no-resolv --server=$UPSTREAM_DNS \
		--interface=lan --bind-interfaces --except-interface=lo \
		--dhcp-authoritative --dhcp-range="$nodeip,$nodeip,$range_mask,1h" \
		--dhcp-host="$(mac_node "$n"),$nodeip,$n" --dhcp-option=3,"$lanip" --dhcp-option=6,"$lanip" \
		--dhcp-leasefile="$RUNDIR/dnsmasq-$n.leases" --pid-file="$RUNDIR/dnsmasq-$n.pid" \
		--log-facility="$RUNDIR/dnsmasq-$n.log" --user=root

	# Fault injection (both directions: towards the node and towards the internet).
	if [[ -n ${NETEM:-} ]]; then
		# shellcheck disable=SC2086
		nsx "$ns" tc qdisc replace dev lan root netem $NETEM
		# shellcheck disable=SC2086
		nsx "$ns" tc qdisc replace dev wan root netem $NETEM
	fi
	if [[ $LINK == down ]]; then
		nsx "$ns" ip link set lan down
	fi
	cat >"$RUNDIR/$n.info" <<EOF
node=$n profile=$PROFILE link=$LINK netem='${NETEM:-}' forwards='${fwd_tcp}'
lan=$lanip/$mask node_ip=$nodeip wan=$wanip public=$(public_of "$n" "$wanip" "$nodeip")
EOF
	log "$n: $PROFILE node=$nodeip wan=$wanip forwards='${fwd_tcp}' netem='${NETEM:-}' link=$LINK"
}

public_of() { # node wanip nodeip
	case $PROFILE in
	P1) echo "$3" ;;
	P5) echo "$ISP_PUB (shared, CGNAT)" ;;
	*) echo "$2" ;;
	esac
}

# Every ruleset replaces table "ip lab" of the home namespace atomically.
hdr() { printf 'table ip lab\ndelete table ip lab\n'; }

rules_p1() { # public address, no NAT, permissive
	hdr
	cat <<'EOF'
table ip lab {
	chain filt_fwd { type filter hook forward priority filter; policy accept; }
}
EOF
}

rules_p2() { # nodeip forwards: full-cone NAT (EIM + EIF), optional forwards
	hdr
	local fw=""
	[[ -n $2 ]] && fw="iifname \"wan\" meta l4proto { tcp, udp } th dport { $2 } dnat to $1"
	cat <<EOF
table ip lab {
	# Endpoint-independent filtering: every (proto, port) the node used outbound
	# is reachable from ANY remote host while the mapping lives (5 min idle).
	set cone { type inet_proto . inet_service; flags dynamic,timeout; timeout 5m; }
	chain pre { type nat hook prerouting priority dstnat; policy accept;
		$fw
		iifname "wan" meta l4proto . th dport @cone dnat to $1
	}
	chain filt_fwd { type filter hook forward priority filter; policy accept;
		iifname "lan" oifname "wan" meta l4proto { tcp, udp } update @cone { meta l4proto . th sport }
	}
	# Endpoint-independent mapping: Linux keeps the source port when it is free
	# (one host per home -> internal port == external port).
	chain post { type nat hook postrouting priority srcnat; policy accept;
		oifname "wan" masquerade
	}
}
EOF
}

rules_p3() { # nodeip forward: typical home router
	hdr
	local fw=""
	[[ -n $2 ]] && fw="iifname \"wan\" meta l4proto { tcp, udp } th dport $2 dnat to $1"
	cat <<EOF
table ip lab {
	chain pre { type nat hook prerouting priority dstnat; policy accept;
		$fw
	}
	chain filt_fwd { type filter hook forward priority filter; policy drop;
		ct state established,related accept
		ct state invalid drop
		iifname "lan" oifname "wan" accept
		iifname "wan" ct status dnat accept
	}
	chain filt_in { type filter hook input priority filter; policy accept;
		iifname "wan" ct state established,related accept
		iifname "wan" drop
	}
	chain post { type nat hook postrouting priority srcnat; policy accept;
		oifname "wan" masquerade
	}
}
EOF
}

rules_p4() { # symmetric NAT + strict firewall
	hdr
	cat <<'EOF'
table ip lab {
	chain filt_fwd { type filter hook forward priority filter; policy drop;
		ct state established,related accept
		ct state invalid drop
		iifname "lan" oifname "wan" accept
	}
	chain filt_in { type filter hook input priority filter; policy accept;
		iifname "wan" ct state established,related accept
		iifname "wan" drop
	}
	# fully-random: a new random external port for every new flow, so the
	# mapping depends on the destination (RFC 4787 "address and port dependent").
	chain post { type nat hook postrouting priority srcnat; policy accept;
		oifname "wan" masquerade fully-random
	}
}
EOF
}

rules_p5() { # home NAT behind CGNAT, egress TCP/443 only
	hdr
	cat <<'EOF'
table ip lab {
	chain filt_fwd { type filter hook forward priority filter; policy drop;
		ct state established,related accept
		ct state invalid drop
		iifname "lan" oifname "wan" tcp dport 443 accept
		iifname "lan" oifname "wan" reject with icmp type admin-prohibited
	}
	chain filt_in { type filter hook input priority filter; policy accept;
		iifname "wan" ct state established,related accept
		iifname "wan" drop
	}
	chain post { type nat hook postrouting priority srcnat; policy accept;
		oifname "wan" masquerade
	}
}
EOF
}

# ------------------------------------------------------------------ main
case ${1:-} in
boot)
	modprobe nf_conntrack 2>/dev/null || true
	setup_root_and_core
	setup_isp
	for n in "${NODES[@]}"; do apply_home "$n" || log "$n: FAILED"; done
	;;
apply)
	n=${2:?node}
	[[ -n ${IDX[$n]:-} ]] || { echo "unknown node $n" >&2; exit 2; }
	has_ns core || { setup_root_and_core; setup_isp; }
	apply_home "$n"
	;;
remove)
	n=${2:?node}
	[[ -n ${IDX[$n]:-} ]] || { echo "unknown node $n" >&2; exit 2; }
	teardown_home "$n"
	rm -f "$RUNDIR/$n.info" "$RUNDIR/dnsmasq-$n".*
	;;
teardown)
	for n in "${NODES[@]}"; do teardown_home "$n"; done
	ip netns del isp 2>/dev/null || true
	ip netns del core 2>/dev/null || true
	ip link del up-root 2>/dev/null || true
	;;
show)
	if [[ -n ${2:-} ]]; then cat "$RUNDIR/$2.info"; nsx "h-$2" nft list ruleset; else cat "$RUNDIR"/*.info; fi
	;;
*)
	sed -n '2,9p' "$0"
	exit 2
	;;
esac
