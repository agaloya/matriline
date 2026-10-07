#!/usr/bin/env bash
# winrelay-server.sh [win10|win11] [jobs] - a WINDOWS server behind a relay: the server in
# the VM (connection = relay, as a scheduled task) and three Linux clients on this host
# meet only at a matriline-relay on this host; neither side accepts a connection. The
# jobs must come back, check and verify clean. (relay443 in winnet.sh is the other way
# round: a Windows client through a relay.)
# The VM reaches this host's 127.0.0.1 as 10.0.2.2 (QEMU user networking). The server
# has no ORCA reference (the built-in fingerprints accept the clients' ORCA): its first
# start would fingerprint C:\ORCA_6.1.1 for many minutes on the VM's disk.
set -uo pipefail
export MATRILINE_NO_REGISTRY=1 # lab projects stay off the person's list (common/projects)
LAB=$(cd "$(dirname "$(readlink -f "$0")")/.." && pwd)
SRC=$LAB/../src
VM=${1:-win11} N=${2:-6}
W="$LAB/windows/winvm.sh"
D=$LAB/images/winrelay-server
RPORT=44397
sshport=$([[ $VM == win10 ]] && echo 2241 || echo 2242)
log() { echo "$(date +%H:%M:%S) $*"; }
fail() { log "FAIL: $*"; exit 1; }
vm() { timeout 300 "$W" ssh "$VM" "$1" 2>&1 | tr -d '\r'; }
S='C:\mlwsrv\matriline-server.exe -c C:\mlwsrv\srv\server.conf'
rm -rf "$D" && mkdir -p "$D/bin"
(cd "$SRC" && GOOS=windows go build -o "$D/matriline-server.exe" ./server && go build -o "$D/bin/matriline-relay" ./relay &&
	go build -o "$D/bin/matriline-client" ./client) || fail build
"$D/bin/matriline-relay" -listen 127.0.0.1:$RPORT >"$D/relay.log" 2>&1 &
relay=$!
pids=()
cleanup() {
	kill "${pids[@]}" "$relay" 2>/dev/null
	vm "$S service remove" >/dev/null 2>&1
}
trap cleanup EXIT
vm 'Remove-Item -Recurse -Force C:\mlwsrv -ErrorAction SilentlyContinue; New-Item -ItemType Directory -Force C:\mlwsrv | Out-Null' >/dev/null
scp -q -i "$LAB/keys/lab_ed25519" -P "$sshport" -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null "$D/matriline-server.exe" matriline@127.0.0.1:C:/mlwsrv/ || fail scp
vm "C:\\mlwsrv\\matriline-server.exe init C:\\mlwsrv\\srv --language en" | head -2
vm "\$c = Get-Content C:\\mlwsrv\\srv\\server.conf; \$c = \$c -replace '^connection = .*','connection = relay' -replace '^relay_address =.*','relay_address = 10.0.2.2:$RPORT' -replace '^advertise =.*','advertise = relay' -replace '^scan_interval = .*','scan_interval = 1s' -replace '^settle_time = .*','settle_time = 0s' -replace '^enabled = true','enabled = false' -replace '^reference_paths =.*','reference_paths ='; Set-Content -Encoding ascii C:\\mlwsrv\\srv\\server.conf \$c" >/dev/null
log "server service: $(vm "$S service install" | head -1)"
for _ in $(seq 300); do grep -q "registered" "$D/relay.log" && break; sleep 2; done # its first start fingerprints ORCA: minutes on Windows
grep -q "registered" "$D/relay.log" || fail "the Windows server never registered at the relay: $(vm "Get-Content C:\\mlwsrv\\srv\\state\\server.log -Tail 5")"
log "relay: $(grep registered "$D/relay.log" | tail -1 | cut -c28-)"
for c in lc1 lc2 lc3; do
	vm "$S keys issue $c C:\\mlwsrv\\$c.cred" >/dev/null
	scp -q -i "$LAB/keys/lab_ed25519" -P "$sshport" -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null "matriline@127.0.0.1:C:/mlwsrv/$c.cred" "$D/$c.cred" || fail "credential $c"
	sed -i -e "s/^relay_address = .*/relay_address = 127.0.0.1:$RPORT/" "$D/$c.cred"
	grep -q "^relay_address = 127.0.0.1:$RPORT" "$D/$c.cred" || fail "credential $c has no relay_address"
	"$D/bin/matriline-client" init "$D/$c" --credential "$D/$c.cred" --language en >/dev/null 2>&1 || fail "client init $c"
	sed -i 's/^cores = .*/cores = 1/; s/^updates = .*/updates = false/; s/^pause_on_battery = .*/pause_on_battery = false/' "$D/$c/client.conf"
	(cd "$D/$c" && exec "$D/bin/matriline-client" run </dev/null >"$D/$c.log" 2>&1) &
	pids+=($!)
done
E='%maxcore 768
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
printf '%s\n' "$E" | vm "\$t=[Console]::In.ReadToEnd(); New-Item -ItemType Directory -Force C:\\mlwsrv\\in | Out-Null; 1..$N | ForEach-Object { Set-Content -Encoding ascii (\"C:\\mlwsrv\\in\\etoh_\" + \$_ + \".inp\") \$t }" >/dev/null
vm "$S add C:\\mlwsrv\\in" | tail -1
end=$((SECONDS + 1800))
until vm "$S status" | grep -q "^results: $N output"; do
	((SECONDS > end)) && { vm "$S status" | sed -n 2,4p; fail "not all $N results in 30 min"; }
	sleep 15
done
vm "$S status" | sed -n 2,4p
vm "$S check" | tail -1
vm "$S verify" | tail -1
vm "$S verify" | grep -q "RESULT: spool consistent" || fail "verify"
log "clients: $(cat "$D"/lc?.log | grep -c 'connected to') connections through the relay; ERROR lines: $(cat "$D"/lc?.log | grep -c ' ERROR ')"
log "PASS: a Windows server ($VM) behind a relay, $N jobs from Linux clients"
