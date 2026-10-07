#!/usr/bin/env bash
# labtests.sh - acceptance tests for the Matriline lab (run on the host, VMs up).
#
#   tests/labtests.sh orca        ORCA 6.1.1 via the wrappers in every compute VM
#   tests/labtests.sh isolation   management NIC cannot carry node traffic
#   tests/labtests.sh matrix      pairwise TCP reachability with the current profiles
#   tests/labtests.sh pairs       profile x profile matrix (client ubuntu -> server)
#   tests/labtests.sh profiles    behaviour checks for P1..P5
#   tests/labtests.sh capture     inter-home traffic is present in the router capture
#   tests/labtests.sh all
#
# Results are printed as markdown-friendly lines; lab/README.md records a run.
set -uo pipefail
export MATRILINE_NO_REGISTRY=1 # lab projects stay off the person's list (common/projects)
LAB=$(cd "$(dirname "$(readlink -f "$0")")/.." && pwd)
L=$LAB/labctl
NODES=(relay server void artix devuan ubuntu)
COMPUTE=(server void artix devuan ubuntu)
PORT=44100

s() { "$L" ssh "$@"; }
pub() { "$L" addr "$1"; }
push_labnet() { local n; for n in "$@"; do s "$n" "cat > /tmp/labnet.py" <"$LAB/guest/labnet.py"; done; }
# The probe server is stopped by PID (never "pkill -f <pattern>": the pattern
# also matches the remote "sh -c" that runs it, and busybox pkill on Alpine
# ends the ssh session itself).
serve_on() { # node seconds ports
	# sudo: port 443 is privileged (the P5 homes can only reach TCP/443).
	s "$1" "sudo kill \$(cat /tmp/labnet.pid 2>/dev/null) 2>/dev/null; sudo nohup python3 /tmp/labnet.py serve ${3:-$PORT,443} $2 >/dev/null 2>&1 & echo \$! > /tmp/labnet.pid"
	sleep 0.5
}
stop_serve() { local n; for n in "$@"; do s "$n" "sudo kill \$(cat /tmp/labnet.pid 2>/dev/null) 2>/dev/null; rm -f /tmp/labnet.pid" 2>/dev/null || true; done; }

t_orca() {
	local inp='! HF STO-3G
* xyz 0 1
O   0.000000   0.000000   0.117790
H   0.000000   0.755453  -0.471161
H   0.000000  -0.755453  -0.471161
*'
	# Reference: the same input run on the host with /usr/local/bin/orca-611
	# (ORCA 6.1.1) and orca-610 gives -74.963146775728 (2026-10-03, see D45).
	local n w out e ref=-74.963146775728
	echo "### ORCA (expected FINAL SINGLE POINT ENERGY $ref, as on the host)"
	for n in "${COMPUTE[@]}"; do
		for w in orca-611 orca; do
			out=$(s "$n" "d=\$(mktemp -d) && cd \$d && printf '%s\n' \"$inp\" > water.inp && $w water.inp > water.out 2>&1; grep -m1 'Program Version' water.out; grep 'FINAL SINGLE POINT ENERGY' water.out; cd / && rm -rf \$d")
			e=$(awk '/FINAL SINGLE/{print $NF}' <<<"$out")
			v=$(grep -o 'Version [0-9.]*' <<<"$out")
			if [[ $e == "$ref" ]]; then r=PASS; else r=FAIL; fi
			echo "$r $n $w ${v:-?} E=${e:-none}"
		done
	done
}

t_isolation() {
	echo "### Management NIC isolation"
	local n
	for n in "${NODES[@]}"; do
		local def via host
		def=$(s "$n" "ip -4 route show default" | head -1)
		via=$(s "$n" "ip -4 route get 203.0.113.1" | grep -o 'dev [a-z0-9]*')
		# 10.0.2.2 is the slirp gateway (= host); restrict=on must block it.
		host=$(s "$n" "timeout 3 bash -c 'echo > /dev/tcp/10.0.2.2/22' 2>/dev/null && echo REACHABLE || echo blocked" 2>/dev/null)
		echo "$n: default='$def' lab-route='$via' slirp-host=$host"
	done
}

t_matrix() {
	echo "### Pairwise matrix (TCP $PORT to the destination's public address; current profiles)"
	"$L" profile
	push_labnet "${NODES[@]}"
	local d src res row
	for d in "${NODES[@]}"; do serve_on "$d" 300; done
	printf '| src \\ dst |'; for d in "${NODES[@]}"; do printf ' %s (%s) |' "$d" "$(grep -o 'P[1-5]' <<<"$("$L" profile | grep "^$d ")")"; done; echo
	printf '|---|'; for d in "${NODES[@]}"; do printf -- '---|'; done; echo
	for src in "${NODES[@]}"; do
		row="| $src |"
		for d in "${NODES[@]}"; do
			if [[ $src == "$d" ]]; then row+=" - |"; continue; fi
			res=$(s "$src" "python3 /tmp/labnet.py tcp $(pub "$d") $PORT" 2>&1)
			if [[ $res == OK* ]]; then row+=" OK (${res##*peer=}) |"; else row+=" no |"; fi
		done
		echo "$row"
	done
	stop_serve "${NODES[@]}"
}

t_pairs() {
	echo "### Profile pairs: client ubuntu (row) -> server:$PORT with forward (column)"
	push_labnet ubuntu server
	local ps pd res saved_s saved_u row
	saved_s=$("$L" profile | awk '$1=="server"{print $2}')
	saved_u=$("$L" profile | awk '$1=="ubuntu"{print $2}')
	"$L" forward server $PORT >/dev/null
	printf '| client \\ server |'; for pd in P1 P2 P3 P4 P5; do printf ' %s |' $pd; done; echo
	echo '|---|---|---|---|---|---|'
	for ps in P1 P2 P3 P4 P5; do
		"$L" profile ubuntu $ps >/dev/null 2>&1
		row="| $ps |"
		for pd in P1 P2 P3 P4 P5; do
			"$L" profile server $pd >/dev/null 2>&1
			serve_on server 60
			res=$(s ubuntu "python3 /tmp/labnet.py tcp $(pub server) $PORT" 2>&1)
			if [[ $res == OK* ]]; then row+=" OK |"; else row+=" no |"; fi
		done
		echo "$row"
	done
	stop_serve server
	"$L" profile server "$saved_s" >/dev/null 2>&1
	"$L" profile ubuntu "$saved_u" >/dev/null 2>&1
}

t_profiles() {
	echo "### Profile behaviour (test node: void; peers: relay P1, server P1)"
	push_labnet void relay server
	local saved_v saved_s
	saved_v=$("$L" profile | awk '$1=="void"{print $2}')
	saved_s=$("$L" profile | awk '$1=="server"{print $2}')
	"$L" profile relay P1 >/dev/null 2>&1
	"$L" profile server P1 >/dev/null 2>&1
	serve_on relay 900; serve_on server 900
	local p R S V
	R=$(pub relay) S=$(pub server)
	for p in P1 P2 P3 P4 P5; do
		"$L" forward void none >/dev/null
		"$L" profile void $p >/dev/null 2>&1
		V=$(pub void)
		echo "#### void in $p (public address $V)"
		serve_on void 120
		echo "- inbound TCP from relay to $V:$PORT (no forward): $(s relay "python3 /tmp/labnet.py tcp $V $PORT")"
		if [[ $p == P2 || $p == P3 ]]; then
			"$L" forward void $PORT >/dev/null
			echo "- inbound TCP with forward $PORT: $(s relay "python3 /tmp/labnet.py tcp $V $PORT")"
			"$L" forward void none >/dev/null
			serve_on void 120
		fi
		echo "- outbound TCP sport 40000 -> relay:$PORT: $(s void "python3 /tmp/labnet.py tcp $R $PORT 40000")"
		sleep 1
		echo "- outbound TCP sport 40001 -> relay:443:  $(s void "python3 /tmp/labnet.py tcp $R 443 40001")"
		echo "- outbound TCP sport 40002 -> server:443: $(s void "python3 /tmp/labnet.py tcp $S 443 40002")"
		echo "- outbound UDP sport 40003 -> relay:$PORT: $(s void "python3 /tmp/labnet.py udp $R $PORT 40003")"
		# Endpoint-independent filtering: open a UDP mapping towards relay, then
		# a third party (server) sends to the mapped public port.
		s void "nohup timeout 12 python3 /tmp/labnet.py udpwait $R $PORT 45000 10 > /tmp/udpwait.log 2>&1 &"
		sleep 2
		echo "- third-party UDP server -> $V:45000 after void opened a mapping to relay: $(s server "python3 /tmp/labnet.py udp $V 45000")"
	done
	echo "#### idle timeouts (TCP connection to relay:$PORT left idle)"
	for p in P3 P4 P5; do
		"$L" profile void $p >/dev/null 2>&1
		local idle=75; [[ $p == P5 ]] && idle=45
		local port=$PORT; [[ $p == P5 ]] && port=443
		echo "- $p idle ${idle}s: $(s void "python3 /tmp/labnet.py tcpidle $R $port $idle")"
		[[ $p == P4 ]] && echo "- $p idle 40s: $(s void "python3 /tmp/labnet.py tcpidle $R $port 40")"
	done
	echo "#### P5 shares one public address between homes (void + devuan in P5)"
	local saved_a; saved_a=$("$L" profile | awk '$1=="devuan"{print $2}')
	"$L" profile void P5 >/dev/null 2>&1; "$L" profile devuan P5 >/dev/null 2>&1
	push_labnet devuan
	echo "- void -> relay:443: $(s void "python3 /tmp/labnet.py tcp $R 443")"
	echo "- devuan -> relay:443: $(s devuan "python3 /tmp/labnet.py tcp $R 443")"
	echo "- void -> relay UDP 53: $(s void "python3 /tmp/labnet.py udp $R 53")"
	stop_serve void relay server
	"$L" profile devuan "$saved_a" >/dev/null 2>&1
	"$L" profile void "$saved_v" >/dev/null 2>&1
	"$L" profile server "$saved_s" >/dev/null 2>&1
	"$L" forward server $PORT >/dev/null
}

t_capture() {
	echo "### Capture (router core namespace -> lab/captures)"
	push_labnet relay ubuntu
	serve_on relay 60
	local tag
	tag=capture-proof-$(date +%s)
	s ubuntu "for i in 1 2 3; do python3 /tmp/labnet.py tcp $(pub relay) $PORT; done" >/dev/null
	s ubuntu "python3 -c 'import socket; s=socket.socket(socket.AF_INET, socket.SOCK_DGRAM); s.sendto(b\"$tag\", (\"$(pub relay)\", $PORT))'"
	sleep 3 # pcapsampler flushes every second
	stop_serve relay
	local f; f=$(ls -t "$LAB"/captures/capture-*.pcap | head -1)
	echo "file: $(basename "$f")"
	echo "TCP SYNs ubuntu($(pub ubuntu)) -> relay($(pub relay)):$PORT seen in capture:"
	tshark -r "$f" -Y "ip.src==$(pub ubuntu) && ip.dst==$(pub relay) && tcp.dstport==$PORT && tcp.flags.syn==1 && tcp.flags.ack==0" 2>/dev/null | tail -3
	echo "UDP payload '$tag' found: $(tshark -r "$f" -Y "udp contains \"$tag\"" 2>/dev/null | wc -l) packet(s)"
	"$L" captures
}

case ${1:-all} in
orca) t_orca ;;
isolation) t_isolation ;;
matrix) t_matrix ;;
pairs) t_pairs ;;
profiles) t_profiles ;;
capture) t_capture ;;
all) t_orca; t_isolation; t_matrix; t_profiles; t_pairs; t_capture ;;
*) sed -n '2,12p' "$0"; exit 2 ;;
esac
