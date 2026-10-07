#!/usr/bin/env bash
# kit-win.sh [win10|win11] - the helper kit on Windows, end to end: a server on this host,
# 'matriline-server kit' makes the .bat, the VM runs it (real scheduled task), the client computes a
# job (keep_awake must show in 'powercfg /requests' meanwhile), the .bat run again carries
# on, 'status' works from another folder (project list), then everything is removed.
set -uo pipefail
export MATRILINE_NO_REGISTRY=1 # lab projects stay off the person's list (common/projects)
LAB=$(cd "$(dirname "$(readlink -f "$0")")/.." && pwd)
SRC=$LAB/../src
VM=${1:-win11}
W="$LAB/windows/winvm.sh"
D=$LAB/images/kit-$VM
PORT=$([[ $VM == win10 ]] && echo 44481 || echo 44482)
sshport=$([[ $VM == win10 ]] && echo 2241 || echo 2242)
log() { echo "$(date +%H:%M:%S) $*"; }
fail() { log "FAIL: $*"; exit 1; }
vm() { timeout 900 "$W" ssh "$VM" "$1" 2>&1 | tr -d '\r'; }
rm -rf "$D" && mkdir -p "$D/bin"
(cd "$SRC" && go build -o "$D/bin/matriline-server" ./server) || fail build
export PATH=$D/bin:$PATH
matriline-server init "$D/srv" --language en >/dev/null 2>&1 || fail init
sed -i -e "s/^listen = 44100/listen = 127.0.0.1:$PORT/" -e "s/^advertise =\$/advertise = 10.0.2.2:$PORT/" \
	-e 's/^scan_interval = .*/scan_interval = 1s/' -e 's/^settle_time = .*/settle_time = 0s/' -e 's/^enabled = true/enabled = false/' "$D/srv/server.conf"
(cd "$D/srv" && exec matriline-server run >"$D/srv.log" 2>&1) &
srv=$!
cleanup() {
	vm 'cd $HOME\Matriline\cli -ErrorAction SilentlyContinue; if (Test-Path ..\matriline-client.exe) { ..\matriline-client.exe service remove | Out-Null }; Start-Sleep 4; Get-Process matriline* -ErrorAction SilentlyContinue | Stop-Process -Force; Start-Sleep 2; cd C:\; Remove-Item -Recurse -Force C:\kittest,$HOME\Matriline -ErrorAction SilentlyContinue' >/dev/null
	kill "$srv" 2>/dev/null
}
trap cleanup EXIT
sleep 2
(cd "$SRC" && CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -trimpath -o "$D/dist/matriline-client-windows-amd64.exe" ./client) || fail "client build"
matriline-server -c "$D/srv/server.conf" kit kittest "$D/kits" --language en --only windows --clients "$D/dist" >"$D/kit.log" 2>&1 || fail "kit: $(tail -2 "$D/kit.log")"
# the VM's list of client folders also holds other lab tests' clients: start without it
vm 'Remove-Item -Recurse -Force C:\kittest,$HOME\Matriline,$env:APPDATA\matriline -ErrorAction SilentlyContinue; New-Item -ItemType Directory C:\kittest | Out-Null' >/dev/null
scp -q -i "$LAB/keys/lab_ed25519" -P "$sshport" -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null "$D/kits/kittest-setup.bat" matriline@127.0.0.1:C:/kittest/ || fail scp
out=$(vm '$env:MATRILINE_KIT_NO_PAUSE="1"; cmd /c C:\kittest\kittest-setup.bat')
echo "$out" >"$D/run1.log"
grep -q "installed and started the scheduled task" <<<"$out" || fail "first run: $(tail -5 <<<"$out")"
grep -q "keep_awake" <<<"$out" || fail "first run: no keep_awake notice"
log "first run: service installed"
for _ in $(seq 60); do matriline-server -c "$D/srv/server.conf" clients 2>/dev/null | grep -q "kittest.*active" && break; sleep 5; done
matriline-server -c "$D/srv/server.conf" clients | grep -q "kittest.*active" || fail "the client never enrolled"
log "client enrolled"
# a job of a few minutes on the VM (keep_awake is checked every 10 s): naphthalene,
# B3LYP-D4/def2-TZVP
mkdir -p "$D/in"
cp "$HOME/Desktop/matriline-test/heavy/naphthalene.inp" "$D/in/naphthalene.inp" 2>/dev/null ||
	printf '! B3LYP D4 def2-TZVP\n%%maxcore 500\n* xyz 0 1\nC 1.3915 0 0\nC 0.6958 1.2051 0\nC -0.6958 1.2051 0\nC -1.3915 0 0\nC -0.6958 -1.2051 0\nC 0.6958 -1.2051 0\nH 2.4715 0 0\nH 1.2358 2.1404 0\nH -1.2358 2.1404 0\nH -2.4715 0 0\nH -1.2358 -2.1404 0\nH 1.2358 -2.1404 0\n*\n' >"$D/in/naphthalene.inp"
sed -i 's/^! .*/! B3LYP D4 def2-TZVP/' "$D/in/naphthalene.inp" # a single point: a few minutes, not an hour
matriline-server -c "$D/srv/server.conf" add "$D/in" >/dev/null || fail add
awake=""
for _ in $(seq 240); do
	if [[ -z $awake ]] && vm 'powercfg /requests' | grep -qi "matriline-client"; then awake=yes; fi
	matriline-server -c "$D/srv/server.conf" status 2>/dev/null | grep -q "^results: 1 output" && break
	sleep 5
done
matriline-server -c "$D/srv/server.conf" status | grep -q "^results: 1 output" || fail "no result in 20 min"
log "result accepted"
[[ -n $awake ]] && log "keep_awake: matriline-client listed by powercfg /requests while computing" || fail "keep_awake: not seen in powercfg /requests"
sleep 25 # the client lets the computer sleep again within 10 s of the last job
vm 'powercfg /requests' | grep -qi "matriline-client" && fail "keep_awake: still listed after the job"
log "keep_awake released after the job"
out=$(vm '$env:MATRILINE_KIT_NO_PAUSE="1"; cmd /c C:\kittest\kittest-setup.bat')
echo "$out" >"$D/run2.log"
grep -q "already set up: carrying on" <<<"$out" || fail "second run did not carry on: $(tail -5 <<<"$out")"
log "second run carried on"
st=$(vm 'cd C:\; & $HOME\Matriline\matriline-client.exe status')
grep -qi "running" <<<"$st" || fail "status from another folder: $st"
log "status from another folder: $(head -1 <<<"$st")"
log "PASS: kit on $VM"
