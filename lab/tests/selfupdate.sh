#!/usr/bin/env bash
# selfupdate.sh - signed self-update end to end on Linux (D66), with real processes: a
# server and a client of version "old" (the source as it is, plus a throwaway test key)
# update to "0.1.99" (the same source with that version) served by a local web server as a
# GitHub release would be. Checks: 'update check' sees it, 'update apply' moves the client
# first (it restarts into 0.1.99 with the same process id), then the server; a release
# signed by another key is refused. Leaves everything in lab/images/selfupdate.
set -uo pipefail
export MATRILINE_NO_REGISTRY=1 # lab projects stay off the person's list (common/projects)
LAB=$(cd "$(dirname "$(readlink -f "$0")")/.." && pwd)
SRC=$LAB/../src
D=$LAB/images/selfupdate
PORT=44411 WEB=44412
NEW=0.1.99
log() { echo "$(date +%H:%M:%S) $*"; }
fail() { log "FAIL: $*"; exit 1; }
for p in /proc/[0-9]*; do [[ $(readlink "$p/exe" 2>/dev/null) == "$D/"* ]] && kill "${p#/proc/}"; done
pkill -f "[h]ttp.server $WEB" ; sleep 1
rm -rf "$D" && mkdir -p "$D"/{www,run/server,run/client,bad}

# two copies of the source with a test key built in; the new one reports $NEW
for v in old new; do
	rsync -a --exclude bin "$SRC/" "$D/src-$v/"
done
(cd "$D/src-old" && go run ./relsign keygen "$D/test.key") >"$D/keygen.txt" || fail keygen
key=$(sed -n 's/^\t"\(.*\)",$/\1/p' "$D/keygen.txt")
[[ -n $key ]] || fail "no key in keygen output"
for v in old new; do
	# the test key alone, whatever the source trusts (the maintainer's real key since 2026-10-06)
	perl -0pi -e "s|var TrustedKeys = \\[\\]string\\{.*?\\n?\\}|var TrustedKeys = []string{\"$key\"}|s" "$D/src-$v/common/release/keys.go"
done
sed -i "s|matriline-server/[0-9.]*\"|matriline-server/$NEW\"|" "$D/src-new/server/server.go"
sed -i "s|matriline-client/[0-9.]*\"|matriline-client/$NEW\"|" "$D/src-new/client/agent.go"
(cd "$D/src-old" && go build -o "$D/run/server/matriline-server" ./server && go build -o "$D/run/client/matriline-client" ./client) || fail "build old"
(cd "$D/src-new" && go build -o "$D/www/matriline-server-linux-amd64" ./server && go build -o "$D/www/matriline-client-linux-amd64" ./client) || fail "build new"
(cd "$D/www" && sha256sum matriline-* >SHA256SUMS && printf 'version %s\ncommit test\n' "$NEW" >VERSION)
(cd "$D/src-new" && go run ./relsign sign "$D/test.key" "$D/www") || fail sign
(cd "$D/src-new" && go run ./relsign verify "$D/www") || fail verify
# GitHub's layout: latest/download/release.json(.sig), download/v<version>/<program>
layout() { mkdir -p "$1/latest/download" "$1/download/v$NEW" && mv "$1"/release.json* "$1/latest/download/" && mv "$1"/matriline-* "$1/download/v$NEW/"; }

# the same release signed by a key the programs do not trust
cp "$D"/www/* "$D/bad/"
(cd "$D/src-new" && sed -i "s|{\"$key\"}|{\"$(sed -n 's/^\t"\(.*\)",$/\1/p' <(cd "$D/src-new" && go run ./relsign keygen "$D/other2.key"))\"}|" common/release/keys.go && go run ./relsign sign "$D/other2.key" "$D/bad" >/dev/null) || fail "sign bad"
layout "$D/www" && layout "$D/bad"
(cd "$D" && exec python3 -m http.server "$WEB" --bind 127.0.0.1 >web.log 2>&1) &
WEBPID=$!

S=("$D/run/server/matriline-server" -c "$D/run/server/server.conf")
"${S[@]}" init "$D/run/server" >/dev/null 2>&1 || fail "server init"
sed -i -e "s/^listen = .*/listen = 127.0.0.1:$PORT/" -e "s/^advertise =.*/advertise = 127.0.0.1:$PORT/" \
	-e 's/^enabled = true/enabled = false/' -e "s|^url = .*|url = http://127.0.0.1:$WEB/bad|" -e 's/^mode = off/mode = alert/' "$D/run/server/server.conf"
(cd "$D/run/server" && exec "$D/run/server/matriline-server" -c server.conf run >run.log 2>&1) &
SPID=$!
for _ in $(seq 30); do "${S[@]}" status >/dev/null 2>&1 && break; sleep 1; done
"${S[@]}" keys issue c1 "$D/c1.cred" >/dev/null || fail "keys issue"
C=("$D/run/client/matriline-client" -c "$D/run/client/client.conf")
"$D/run/client/matriline-client" init "$D/run/client" --credential "$D/c1.cred" >/dev/null 2>&1 || fail "client init"
sed -i "s|^update_url = .*|update_url = http://127.0.0.1:$WEB/www|" "$D/run/client/client.conf" # its own address, not the server's
(cd "$D/run/client" && exec "$D/run/client/matriline-client" -c client.conf run >run.log 2>&1) &
CPID=$!
for _ in $(seq 60); do grep -q "connected" "$D/run/client/state/connection.json" 2>/dev/null && grep -q '"connected":true' "$D/run/client/state/connection.json" && break; sleep 1; done
log "server $SPID and client $CPID running $("${S[@]}" version | head -1)"

out=$("${S[@]}" update check 2>&1) && fail "a release signed by another key was accepted: $out"
log "foreign signature refused: $out"
sed -i "s|^url = .*|url = http://127.0.0.1:$WEB/www|" "$D/run/server/server.conf"
"${S[@]}" config reload >/dev/null || fail "reload"
out=$("${S[@]}" update check 2>&1) || fail "update check: $out"
log "check: $out"
"${S[@]}" update status
out=$("${S[@]}" update apply 2>&1) || fail "update apply: $out"
log "apply: $out"
for _ in $(seq 120); do grep -q "matriline-client/$NEW starting" "$D/run/client/run.log" 2>/dev/null && break; sleep 1; done
grep -q "matriline-client/$NEW starting" "$D/run/client/run.log" || fail "the client did not restart into $NEW"
kill -0 "$CPID" || fail "client process $CPID is gone"
[[ $(readlink "/proc/$CPID/exe") == "$D/run/client/matriline-client" ]] || fail "client $CPID runs $(readlink /proc/$CPID/exe)"
log "client: $(grep -E "offered|installed|$NEW starting" "$D/run/client/run.log" | cut -c1-160 | tr '\n' '|')"
[[ -f $D/run/client/matriline-client.previous ]] || fail "no matriline-client.previous"
# the server installs its own after the clients (2 min grace, checked every minute)
for _ in $(seq 240); do "${S[@]}" version 2>/dev/null | grep -q "$NEW" && break; sleep 1; done
v=$("${S[@]}" version 2>&1 | tail -1)
[[ $v == *"$NEW"* ]] || fail "the server did not restart into $NEW: $v"
kill -0 "$SPID" || fail "server process $SPID is gone"
log "server: $v (same process $SPID)"
for _ in $(seq 60); do grep -q '"connected":true' "$D/run/client/state/connection.json" && break; sleep 1; done
"${S[@]}" update status
"${S[@]}" verify | tail -1
kill "$CPID" "$SPID" "$WEBPID" 2>/dev/null
log PASS
