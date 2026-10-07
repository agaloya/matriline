#!/usr/bin/env bash
# winnet.sh badnet|relay443 [minutes] [vm] - network tests for a Windows client (the
# Windows VMs use QEMU user networking, outside the lab router, so no tc netem):
#   badnet  the VM's client (C:\matriline-test, server: lab/images/wintest on 127.0.0.1:44390)
#           reaches its server through lab/tools/badnet: 300 ms + up to 400 ms jitter, a 20 s
#           stall about every minute, the connection cut about every 2 minutes. Inputs keep
#           a job running for the given time (default 15 min); then the link is healed and
#           the queue must drain with nothing in weird/ or errors/ and no attempt lost.
#   relay443 a fresh server in relay mode (connection = relay: it accepts no connection) and
#           matriline-relay on this host; a new Windows client (C:\mlrelay) whose network is
#           cut down to ONE destination, 10.0.2.100:443 (a second NIC with QEMU's
#           restrict=on and a guestfwd to the relay (cmd:socat: one connection per guest
#           connection; tcp: would open a single one); the first NIC down meanwhile; SSH on
#           host port 2252): no DNS, no time server, nothing else. Its jobs must come back.
set -uo pipefail
export MATRILINE_NO_REGISTRY=1 # lab projects stay off the person's list (common/projects)
LAB=$(cd "$(dirname "$(readlink -f "$0")")/.." && pwd)
mode=${1:?usage: winnet.sh badnet [minutes] [vm]} mins=${2:-15} vm=${3:-win11}
sshport=$([[ $vm == win10 ]] && echo 2241 || echo 2242) # winvm.sh's ports (relay443 copied to win11's before)
W="$LAB/windows/winvm.sh"
B=$LAB/../src/bin
SRV=$LAB/images/wintest
PROXY=44397
log() { echo "$(date +%H:%M:%S) $mode $*"; }
vmssh() { timeout 120 "$W" ssh "$vm" "$@"; }
srv() { "$B/matriline-server" -c "$SRV/server.conf" "$@" 2>/dev/null; }
count() { srv status | sed -n "s/.*$1 \([0-9]*\).*/\1/p" | head -1; }
INPUT='%maxcore 768
%pal nprocs 1 end
! r2SCAN-3c Opt
* xyz 0 1
C  -0.0447  -0.4201  0.0000
C   1.2470   0.3910  0.0000
O  -1.1480   0.4770  0.0000
H  -0.0980  -1.0610  0.8850
H  -0.0980  -1.0610 -0.8850
H   1.3010   1.0300  0.8850
H   1.3010   1.0300 -0.8850
H   2.1000  -0.2900  0.0000
H  -1.9500  -0.0500  0.0000
*'
# point the client at another server port: the address is in its credential (credential.conf)
client_port() {
	vmssh "\$f='C:\\matriline-test\\cli\\credential.conf'; (Get-Content \$f) -replace '^server_address = 10.0.2.2:[0-9]+', 'server_address = 10.0.2.2:$1' | Set-Content -Encoding ascii \$f; Get-ScheduledTask -TaskName 'matriline-client-*' | Stop-ScheduledTask; Start-Sleep 3; Get-ScheduledTask -TaskName 'matriline-client-*' | Start-ScheduledTask" >/dev/null
}

case $mode in
badnet)
	(cd "$LAB/tools/badnet" && go build -o "$LAB/images/badnet" main.go) || exit 1
	"$LAB/images/badnet" -listen 127.0.0.1:$PROXY -to 127.0.0.1:44390 -delay 300ms -jitter 400ms \
		-stall-every 60s -stall 20s -cut-every 2m >"$LAB/images/badnet.log" 2>&1 &
	proxy=$!
	trap 'kill $proxy 2>/dev/null' EXIT
	weird0=$(count '? weird') errors0=$(count '! errors')
	log "start: weird $weird0, errors $errors0; client through badnet"
	client_port $PROXY
	n=$((mins * 60 / 40 + 2)) # ~40 s per job on the VM
	d=$(mktemp -d)
	for i in $(seq "$n"); do printf '%s\n' "$INPUT" >"$d/etoh_$i.inp"; done
	srv add "$d" "input/badnet-$(date +%H%M%S)" >/dev/null
	rm -rf "$d"
	sleep $((mins * 60))
	log "healing the link; $(grep -c ' cut ' "$LAB/images/badnet.log") cuts, $(grep -c ' stall ' "$LAB/images/badnet.log") stalls"
	client_port 44390
	kill $proxy 2>/dev/null
	# two Linux clients join for the checks (a second opinion needs a third host)
	pids=()
	for c in lnx3 lnx4; do
		if [[ ! -f $SRV/$c/client.conf ]]; then
			srv keys issue "$c" "$SRV/$c.cred" 127.0.0.1:44390 >/dev/null
			"$B/matriline-client" init "$SRV/$c" --credential "$SRV/$c.cred" >/dev/null
		fi
		sed -i 's/^cores = 0$/cores = 1/' "$SRV/$c/client.conf"
		(cd "$SRV/$c" && exec "$B/matriline-client" -c client.conf run >>run.log 2>&1) &
		pids+=($!)
	done
	trap 'kill $proxy ${pids[*]} 2>/dev/null' EXIT
	end=$(($(date +%s) + 3600))
	until s=$(srv status) && grep -q "queued 0 > running 0" <<<"$s" && grep -q " 0 verification" <<<"$s"; do
		(($(date +%s) > end)) && { log "not drained after 1 h"; break; }
		sleep 10
	done
	srv status | head -6
	weird=$(count '? weird') errors=$(count '! errors') lost=$(srv status | sed -n 's/.*running, \([0-9]*\) lost.*/\1/p')
	log "end: weird $weird0 -> $weird, errors $errors0 -> $errors, lost ${lost:-?}"
	[[ $weird == "$weird0" && $errors == "$errors0" && ${lost:-1} == 0 ]] && { log PASS; exit 0; }
	log FAIL; exit 1 ;;
relay443)
	D=$LAB/images/winrelay
	RPORT=44398
	MON=$LAB/images/windows/$vm.mon
	mon() { echo "$*" | socat - "UNIX-CONNECT:$MON" >/dev/null; }
	rm -rf "$D" && mkdir -p "$D"
	"$B/matriline-relay" -listen 127.0.0.1:$RPORT >"$D/relay.log" 2>&1 &
	relay=$!
	"$B/matriline-server" init "$D/srv" >/dev/null
	sed -i -e "s/^connection = .*/connection = relay/" -e "s/^relay_address =.*/relay_address = 127.0.0.1:$RPORT/" \
		-e "s/^advertise =.*/advertise = relay/" -e 's/^scan_interval = .*/scan_interval = 1s/' -e 's/^settle_time = .*/settle_time = 0s/' \
		-e 's/^enabled = true/enabled = false/' \
		-e "s|^accepted_fingerprints =.*|accepted_fingerprints = builtin, $SRV/orca-win.json|" "$D/srv/server.conf"
	(cd "$D/srv" && exec "$B/matriline-server" -c server.conf run >run.log 2>&1) &
	server=$!
	trap 'kill $relay $server 2>/dev/null; mon "set_link n0 on"; mon "device_del nicr" 2>/dev/null; sleep 3; mon "netdev_del nr"' EXIT
	for _ in $(seq 60); do grep -q ' ready, spool' "$D/srv/run.log" 2>/dev/null && break; sleep 1; done
	"$B/matriline-server" -c "$D/srv/server.conf" keys issue winrelay "$D/winrelay.cred" >/dev/null
	# the client reaches the relay only as 10.0.2.100:443 (the guestfwd below)
	sed -i "s/^relay_address = .*/relay_address = 10.0.2.100:443/" "$D/winrelay.cred"
	grep -q "^relay_address = 10.0.2.100:443" "$D/winrelay.cred" || { log "credential has no relay_address"; exit 1; }
	# (single quotes: no shell escaping; and no backslash before a closing quote, which
	# Windows' command line would take as an escaped quote)
	vmssh 'Remove-Item -Recurse -Force C:\mlrelay -ErrorAction SilentlyContinue; New-Item -ItemType Directory -Force C:\mlrelay | Out-Null; Copy-Item C:\matriline-test\matriline-client.exe C:\mlrelay\matriline-client.exe' >/dev/null
	scp -q -i "$LAB/keys/lab_ed25519" -P "$sshport" -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null "$D/winrelay.cred" matriline@127.0.0.1:C:/mlrelay/winrelay.cred
	vmssh 'C:\mlrelay\matriline-client.exe init C:\mlrelay\cli --credential C:\mlrelay\winrelay.cred | Out-Null; C:\mlrelay\matriline-client.exe -c C:\mlrelay\cli\client.conf service install | Select -First 1' 
	# only 443 to the relay: a restricted second NIC, then the first one down
	# the monitor splits its arguments at spaces: the per-connection bridge is a script
	printf '#!/bin/sh\nexec socat - TCP:127.0.0.1:%s\n' "$RPORT" >"$D/fwd.sh"
	chmod +x "$D/fwd.sh"
	mon "netdev_add user,id=nr,restrict=on,dhcpstart=10.0.2.70,guestfwd=tcp:10.0.2.100:443-cmd:$D/fwd.sh,hostfwd=tcp:127.0.0.1:2252-10.0.2.70:22"
	mon "device_add e1000e,netdev=nr,id=nicr,bus=hp2" # a free hot-plug port (winvm.sh)
	sleep 25
	mon "set_link n0 off"
	log "first NIC down; the VM reaches only 10.0.2.100:443 (the relay)"
	n=$((mins * 60 / 40 + 2))
	d=$(mktemp -d)
	for i in $(seq "$n"); do printf '%s\n' "$INPUT" >"$d/etoh_$i.inp"; done
	"$B/matriline-server" -c "$D/srv/server.conf" add "$d" input/relay >/dev/null
	rm -rf "$d"
	end=$(($(date +%s) + mins * 60 + 1800))
	until s=$("$B/matriline-server" -c "$D/srv/server.conf" status 2>/dev/null) && grep -q "queued 0 > running 0" <<<"$s"; do
		(($(date +%s) > end)) && { log "not drained in time"; break; }
		sleep 15
	done
	"$B/matriline-server" -c "$D/srv/server.conf" status | head -9
	ok=$(grep -c "accepted ->" "$D/srv/run.log")
	log "results through the relay over 443 only: $ok of $n accepted; relay log: $(grep -c . "$D/relay.log") lines"
	mon "set_link n0 on"
	vmssh 'C:\mlrelay\matriline-client.exe -c C:\mlrelay\cli\client.conf service remove' >/dev/null 2>&1
	[[ $ok == "$n" ]] && { log PASS; exit 0; }
	log FAIL; exit 1 ;;
*) sed -n '2,13p' "$0"; exit 2 ;;
esac
