#!/usr/bin/env bash
# selfupdate-win.sh [win10|win11] - signed self-update on Windows (D66): a server and a
# client of version "old" run in the VM as scheduled tasks ('service install', as users
# install them) and update to 0.1.99, served from the host as
# a GitHub release would be (through an ssh tunnel: the VM sees it at 127.0.0.1). Checks that each restarts into the new version (Windows: a new
# process on the same console, the scheduled task's) and keeps running. Builds like
# selfupdate.sh, in lab/images/selfupdate-win.
set -uo pipefail
export MATRILINE_NO_REGISTRY=1 # lab projects stay off the person's list (common/projects)
LAB=$(cd "$(dirname "$(readlink -f "$0")")/.." && pwd)
SRC=$LAB/../src
VM=${1:-win11}
W="$LAB/windows/winvm.sh"
D=$LAB/images/selfupdate-win
WEB=44412 NEW=0.1.99
sshport=$([[ $VM == win10 ]] && echo 2241 || echo 2242)
log() { echo "$(date +%H:%M:%S) $*"; }
fail() { log "FAIL: $*"; exit 1; }
vm() { timeout 300 "$W" ssh "$VM" "$1" 2>&1 | tr -d '\r'; }
S='C:\su\srv\matriline-server.exe -c C:\su\srv\server.conf'
C='C:\su\cli\matriline-client.exe -c C:\su\cli\client.conf'
pkill -f "[h]ttp.server $WEB"
rm -rf "$D" && mkdir -p "$D"/{www,old}
for v in old new; do rsync -a --exclude bin "$SRC/" "$D/src-$v/"; done
(cd "$D/src-old" && go run ./relsign keygen "$D/test.key") >"$D/keygen.txt" || fail keygen
key=$(sed -n 's/^\t"\(.*\)",$/\1/p' "$D/keygen.txt")
# the test key alone, whatever the source trusts (the maintainer's real key since 2026-10-06)
for v in old new; do perl -0pi -e "s|var TrustedKeys = \\[\\]string\\{.*?\\n?\\}|var TrustedKeys = []string{\"$key\"}|s" "$D/src-$v/common/release/keys.go"; done
sed -i "s|matriline-server/[0-9.]*\"|matriline-server/$NEW\"|" "$D/src-new/server/server.go"
sed -i "s|matriline-client/[0-9.]*\"|matriline-client/$NEW\"|" "$D/src-new/client/agent.go"
export GOOS=windows GOARCH=amd64
(cd "$D/src-old" && go build -o "$D/old/matriline-server.exe" ./server && go build -o "$D/old/matriline-client.exe" ./client) || fail "build old"
(cd "$D/src-new" && go build -o "$D/www/matriline-server-windows-amd64.exe" ./server && go build -o "$D/www/matriline-client-windows-amd64.exe" ./client) || fail "build new"
unset GOOS GOARCH
(cd "$D/www" && sha256sum matriline-* >SHA256SUMS && printf 'version %s\ncommit test\n' "$NEW" >VERSION)
(cd "$D/src-new" && go run ./relsign sign "$D/test.key" "$D/www") >/dev/null || fail sign
# GitHub's layout: latest/download/release.json(.sig), download/v<version>/<program>
layout() { mkdir -p "$1/latest/download" "$1/download/v$NEW" && mv "$1"/release.json* "$1/latest/download/" && mv "$1"/matriline-* "$1/download/v$NEW/"; }
layout "$D/www"
(cd "$D" && exec python3 -m http.server "$WEB" --bind 127.0.0.1 >web.log 2>&1) &
WEBPID=$!
# the VM reaches the release at its own 127.0.0.1 (only https or this computer is allowed)
ssh -N -i "$LAB/keys/lab_ed25519" -p "$sshport" -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null \
	-o ExitOnForwardFailure=yes -R "$WEB:127.0.0.1:$WEB" matriline@127.0.0.1 &
TUNPID=$!
trap 'kill $TUNPID $WEBPID 2>/dev/null' EXIT

vm "Get-ScheduledTask -TaskName 'matriline-*' | Where-Object { \$_.Actions.Execute -like 'C:\\su\\*' -or \$_.Actions.Arguments -like '*C:\\su\\*' } | ForEach-Object { Stop-ScheduledTask -TaskName \$_.TaskName; Unregister-ScheduledTask -TaskName \$_.TaskName -Confirm:\$false }; Get-CimInstance Win32_Process | Where-Object { \$_.ExecutablePath -like 'C:\\su\\*' } | ForEach-Object { Stop-Process -Id \$_.ProcessId -Force }; Start-Sleep 1; Remove-Item -Recurse -Force C:\\su -ErrorAction SilentlyContinue; New-Item -ItemType Directory -Force C:\\su\\srv, C:\\su\\cli | Out-Null" >/dev/null
for p in server client; do
	scp -q -i "$LAB/keys/lab_ed25519" -P "$sshport" -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null \
		"$D/old/matriline-$p.exe" "matriline@127.0.0.1:C:/su/$([[ $p == server ]] && echo srv || echo cli)/" || fail "scp $p"
done
vm "C:\\su\\srv\\matriline-server.exe init C:\\su\\srv | Out-Null; \$f='C:\\su\\srv\\server.conf'; (Get-Content \$f) -replace '^listen = .*','listen = 127.0.0.1:44413' -replace '^advertise =.*','advertise = 127.0.0.1:44413' -replace '^enabled = true','enabled = false' -replace '^mode = off','mode = alert' -replace '^url = .*','url = http://127.0.0.1:$WEB/www' | Set-Content -Encoding ascii \$f; $S service install" | tail -2
for _ in $(seq 60); do vm "$S keys issue c1 C:\\su\\c1.cred" | grep "^issued" >/dev/null && break; sleep 3; done # once the server answers
vm "Test-Path C:\\su\\c1.cred" | grep True || fail "no credential (server not running?): $(vm "$S status" | head -3)"
vm "C:\\su\\cli\\matriline-client.exe init C:\\su\\cli --credential C:\\su\\c1.cred | Out-Null; \$f='C:\\su\\cli\\client.conf'; (Get-Content \$f) -replace '^update_url = .*','update_url = http://127.0.0.1:$WEB/www' | Set-Content -Encoding ascii \$f; $C service install" | tail -1
vm "$C status" | grep "^service" >/dev/null || fail "client not set up: $(vm "$C status")"
for _ in $(seq 60); do vm "$C status" | grep "connected to" >/dev/null && break; sleep 3; done
vm "$C status" | head -1
vm "$C status" | head -1 | grep "connected to" >/dev/null || fail "the client did not connect"
log "old: $(vm "$S version" | head -1)"
log "check: $(vm "$S update check")"
log "apply: $(vm "$S update apply")"
for _ in $(seq 60); do vm "Get-Content C:\\su\\cli\\state\\client.log -ErrorAction SilentlyContinue; Get-Content C:\\su\\cli\\*.log -ErrorAction SilentlyContinue" | grep "matriline-client/$NEW starting" >/dev/null && break; sleep 5; done
vm "Get-ChildItem -Recurse C:\\su\\cli -Include *.log | Get-Content" | grep -E "offered|installed|starting|restart|console host|stopp" | cut -c1-170
vm "Get-ChildItem -Recurse C:\\su\\cli -Include *.log | Get-Content" | grep "matriline-client/$NEW starting" >/dev/null || fail "the client did not restart into $NEW"
sleep 20
vm "Get-CimInstance Win32_Process | Where-Object { \$_.ExecutablePath -like 'C:\\su\\*' } | Select ProcessId, ParentProcessId, ExecutablePath | Format-Table -AutoSize | Out-String -Width 200"
vm "$C status" | head -1 | grep "running, connected" >/dev/null || fail "the updated client is not running and connected: $(vm "$C status" | head -1)"
for _ in $(seq 60); do vm "$S version" | grep "$NEW" >/dev/null && break; sleep 5; done
v=$(vm "$S version" | tail -1)
[[ $v == *"$NEW"* ]] || fail "the server did not restart into $NEW: $v"
log "server: $v"
sleep 20
vm "$S status" | grep -E "^server|clients connected" 
vm "$S verify" | tail -1
vm "Get-CimInstance Win32_Process | Where-Object { \$_.ExecutablePath -like 'C:\\su\\*' } | Select ProcessId, ParentProcessId, ExecutablePath | Format-Table -AutoSize | Out-String -Width 200"
kill "$WEBPID"
log PASS
