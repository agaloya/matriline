#!/bin/bash
# cmdmatrix-mac.sh - every matriline-client command on macOS with its exit code (the macOS
# counterpart of cmdmatrix.sh, which covers Linux and Windows). A lab server on 127.0.0.1
# (enrollment = issued) and a second one (enrollment = register, for join) talk to clients of
# this build; inputs: a benzene optimisation long enough for 'pause --now'. Server-side:
# clients disable / enable and disable --wipe against a running client. Not covered: web.
#
#   lab/tests/cmdmatrix-mac.sh            (on the Mac, from the repository; bash 3.2 is enough)
#   GOARCH=amd64 ORCA=<Intel ORCA dir> lab/tests/cmdmatrix-mac.sh   (the Intel client under Rosetta 2)
#
# ORCA, when set, is written into every client.conf (orca.paths) instead of what init finds.
#
# Needs: Go, an unpacked ORCA for macOS without the quarantine attribute where 'init' finds it
# (~/orca*, ~/Applications, /Applications, /opt, one level down too), ports 44410/44411 free.
# Work directory: ~/matriline-cmdmatrix (an earlier one is moved aside, never deleted). It installs and removes a launchd agent.
# Prints a markdown table: | # | command | expected | got | exit PASS/FAIL |, then the totals.
set -u
LAB=$(cd "$(dirname "$0")/.." && pwd)
D=${CMDMATRIX_DIR:-$HOME/matriline-cmdmatrix}
B=$D/bin
PORT=44410
PREG=44411
[ -d "$D" ] && mv "$D" "$D.old.$(date +%s)"
mkdir -p "$D/inputs" "$B"
(cd "$LAB/../src" && go build -o "$B/matriline-server" ./server && go build -o "$B/matriline-client" ./client) || exit 1
n=0 pass=0 fail=0
row() { # row <expected text> <ok|fail> <exit> <got text> <command text>
	n=$((n + 1))
	local verdict
	if { [ "$2" = ok ] && [ "$3" -eq 0 ]; } || { [ "$2" = fail ] && [ "$3" -ne 0 ]; }; then verdict=PASS; pass=$((pass + 1)); else verdict=FAIL; fail=$((fail + 1)); fi
	printf '| %d | `%s` | %s | %s | %d %s |\n' "$n" "$5" "$1" "$(printf '%s' "$4" | tr '\n|' ' /' | cut -c1-150)" "$3" "$verdict"
}
# c <expect ok|fail> <expected text> <dir> args... : runs matriline-client -c <dir>/client.conf args
c() {
	local want=$1 exp=$2 dir=$3 out code
	shift 3
	out=$("$B/matriline-client" -c "$dir/client.conf" "$@" 2>&1 </dev/null)
	code=$?
	row "$exp" "$want" "$code" "$out" "client $*"
	LAST=$out
}
s() { # s <srvdir> args... : server admin command, output only (setup, not counted)
	local dir=$1
	shift
	"$B/matriline-server" -c "$dir/server.conf" "$@" 2>&1
}
waitlog() { # waitlog <file> <pattern> <seconds>
	local end=$(($(date +%s) + $3))
	until grep -q "$2" "$1" 2>/dev/null; do [ "$(date +%s)" -gt "$end" ] && return 1; sleep 1; done
}
conf() { # conf <file> <section> <key> <value>
	python3 - "$@" <<'EOF'
import sys,re
f,sec,key,val=sys.argv[1:]
L=open(f).read().split('\n'); cur=None; done=False
for i,l in enumerate(L):
    m=re.match(r'^\[([a-z_]+)\]',l)
    if m: cur=m.group(1); continue
    if cur==sec and re.match(rf'^{key} *=',l) and not done: L[i]=f'{key} = {val}'; done=True
if not done: sys.exit(f'{key} not found in [{sec}] of {f}')
open(f,'w').write('\n'.join(L))
EOF
}

setorca() { [ -n "${ORCA:-}" ] && conf "$1/client.conf" orca paths "$ORCA"; return 0; } # setorca <client dir>

# --- servers: one issued (main), one with enrollment = register (join)
"$B/matriline-server" init "$D/srv" >/dev/null 2>&1
conf "$D/srv/server.conf" network listen 127.0.0.1:$PORT
conf "$D/srv/server.conf" network advertise 127.0.0.1:$PORT
"$B/matriline-server" init "$D/srvreg" >"$D/srvreg.init" 2>&1
conf "$D/srvreg/server.conf" network listen 127.0.0.1:$PREG
conf "$D/srvreg/server.conf" network advertise 127.0.0.1:$PREG
conf "$D/srvreg/server.conf" network enrollment register
(cd "$D/srv" && exec "$B/matriline-server" run >"$D/srv.log" 2>&1) &
SRVPID=$!
(cd "$D/srvreg" && exec "$B/matriline-server" run >"$D/srvreg.log" 2>&1) &
REGPID=$!
trap 'kill $SRVPID $REGPID 2>/dev/null' EXIT # also when a check above exits early
waitlog "$D/srv.log" " ready, spool" 300 || { echo "lab server did not start:"; tail -3 "$D/srv.log"; exit 1; }
waitlog "$D/srvreg.log" " ready, spool" 300 || { echo "register-mode server did not start:"; tail -3 "$D/srvreg.log"; exit 1; }
s "$D/srv" keys issue mac "$D/mac.cred" >/dev/null
s "$D/srv" keys issue mac2 "$D/mac2.cred" >/dev/null

echo "client: $(file -b "$B/matriline-client" | cut -c1-40); ORCA: ${ORCA:-found by init}"
echo
echo "| # | command | expected | got (first 150 chars) | exit |"
echo "|---|---|---|---|---|"
# --- help, version
c ok "usage text" "$D/none" help
for cmd in init run join status pause resume enable service doctor fingerprint console version; do
	c ok "help for $cmd" "$D/none" help "$cmd"
done
c ok "version line" "$D/none" version

# --- init, init --credential
out=$("$B/matriline-client" init "$D/cli0" 2>&1); code=$?
setorca "$D/cli0"
row "client.conf created, ORCA found" ok $code "$out" "client init $D/cli0"
out=$("$B/matriline-client" init "$D/cli" --credential "$D/mac.cred" 2>&1); code=$?
setorca "$D/cli"
row "client.conf + credential installed" ok $code "$out" "client init $D/cli --credential mac.cred"
ls "$D/cli/credential.conf" >/dev/null 2>&1 && echo "<!-- credential installed -->"
c ok "ORCA, sandbox, credential, server answers" "$D/cli" doctor
c ok "fingerprint JSON" "$D/cli" fingerprint
LAST_FP=$LAST
c ok "not running" "$D/cli" status

# --- run (background), status
("$B/matriline-client" -c "$D/cli/client.conf" run >"$D/cli.log" 2>&1) &
CLIPID=$!
waitlog "$D/cli.log" "connected to" 60
row "connects, enrols its own key" ok $? "$(grep -m1 -E 'enrolled|connected to' "$D/cli.log")" "client run (background)"
c ok "running, connected" "$D/cli" status
c ok "paused until resume" "$D/cli" pause
c ok "shows paused" "$D/cli" status
c ok "lending again" "$D/cli" resume
c ok "paused for 2 min" "$D/cli" pause 2m
c ok "shows paused until <time>" "$D/cli" status
c ok "lending again" "$D/cli" resume
c fail "refused: bad duration" "$D/cli" pause xyz

# --- pause --now while a job runs (benzene B3LYP/def2-SVP Opt: a few minutes here)
mkdir -p "$D/inputs/long"
cat >"$D/inputs/long/benzene.inp" <<'EOF'
! B3LYP def2-SVP Opt
* xyz 0 1
C 1.40 0.00 0.00
C 0.70 1.21 0.00
C -0.70 1.21 0.00
C -1.40 0.00 0.00
C -0.70 -1.21 0.00
C 0.70 -1.21 0.00
H 2.49 0.00 0.00
H 1.25 2.16 0.00
H -1.25 2.16 0.00
H -2.49 0.00 0.00
H -1.25 -2.16 0.00
H 1.25 -2.16 0.00
*
EOF
s "$D/srv" add "$D/inputs/long" >/dev/null
waitlog "$D/cli.log" "started, pid" 60
c ok "running job stopped and given back" "$D/cli" pause --now
waitlog "$D/srv.log" "returned by mac" 30 # the client notices the pause within seconds
row "task back in the queue (server)" ok $? "$(s "$D/srv" status | sed -n 2p)" "server status after pause --now"
c ok "lending again" "$D/cli" resume
waitlog "$D/cli.log" "finished OK" 900
row "benzene finished after resume" ok $? "$(grep -E 'benzene|finished|uploading' "$D/cli.log" | tail -1)" "(job after resume)"

# --- console, driven through its menu: 1 status, 2 pause (empty = until resume), 3 resume, q
out=$(printf '1\n2\n\nn\n3\nq\n' | "$B/matriline-client" -c "$D/cli/client.conf" console 2>&1); code=$?
row "menu: status, pause, resume, quit" ok $code "$(printf '%s' "$out" | grep -vE '^ *[0-9q] ' | tr -s '\n' | tail -6)" "client console (1, 2, <enter>, n, 3, q)"
echo "$out" >"$D/console.out"

# --- clients disable / enable / client enable
s "$D/srv" clients disable mac "cmdmatrix test" >"$D/disable.out"
waitlog "$D/cli.log" "isabled" 60
sleep 3
kill -0 $CLIPID 2>/dev/null && st="client still running" || st="client exited"
row "client stops and stays off" ok $([ "$st" = "client exited" ] && grep -q "stays off" "$D/cli.log"; echo $?) "$st; $(grep -iE 'disabled' "$D/cli.log" | tail -1)" "server clients disable mac"
wait $CLIPID 2>/dev/null; echo "<!-- run exit after disable: $? -->"
c ok "status says disabled" "$D/cli" status
s "$D/srv" clients enable mac >"$D/enable.out"
c ok "enabled: connects at next run" "$D/cli" enable
c ok "nothing to undo now (rc 0, as on Linux)" "$D/cli" enable
("$B/matriline-client" -c "$D/cli/client.conf" run >>"$D/cli.log" 2>&1) &
CLIPID=$!
sleep 8
row "connects again after enable" ok $([ "$(grep -c "connected to" "$D/cli.log")" -ge 2 ]; echo $?) "$(grep 'connected to' "$D/cli.log" | tail -1)" "client run (after enable)"

# --- service install / remove (the run above holds the lock: install must refuse first)
c fail "refused: a client is running" "$D/cli" service install
kill $CLIPID; wait $CLIPID 2>/dev/null
c ok "launchd agent installed and started" "$D/cli" service install
sleep 6
c ok "running under launchd, connected" "$D/cli" status
c ok "agent removed, client stopped" "$D/cli" service remove
sleep 2
row "nothing left" ok $([ "$(ls ~/Library/LaunchAgents 2>/dev/null | grep -c matriline)" = 0 ] && ! pgrep -f "$D/cli/client.conf" >/dev/null; echo $?) "$(ls ~/Library/LaunchAgents 2>/dev/null | grep -c matriline) plists; $(pgrep -f "$D/cli/client.conf" | wc -l | tr -d ' ') processes" "(after remove)"

# --- join against the enrollment = register server
KEY=$(grep -m1 'server key:' "$D/srvreg.init" | awk '{print $3}')
TOK=$(s "$D/srvreg" keys token 1 | grep -oE 'mlj-[0-9a-f]+' | head -1)
"$B/matriline-client" init "$D/clij" >/dev/null 2>&1
setorca "$D/clij"
c ok "key + credential from the token" "$D/clij" join --server 127.0.0.1:$PREG --server-key "$KEY" --token "$TOK" --name macjoin
c fail "refused: this client already has a credential" "$D/clij" join --server 127.0.0.1:$PREG --server-key "$KEY" --token "$TOK" --name macjoin2
("$B/matriline-client" -c "$D/clij/client.conf" run >"$D/clij.log" 2>&1) &
JPID=$!
sleep 8
row "waits for approval" ok $(grep -q "waiting for the server admin to approve" "$D/clij.log"; echo $?) "$(grep -iE 'approv|pending|connected' "$D/clij.log" | tail -1)" "client run (joined, before approve)"
APPROVE=$(s "$D/srvreg" clients approve macjoin)
sleep 12
row "admitted after approve" ok $(grep -q "status active" "$D/clij.log"; echo $?) "$(grep -iE 'connected to|status active' "$D/clij.log" | tail -1) [$APPROVE]" "server clients approve macjoin"
kill $JPID; wait $JPID 2>/dev/null

# --- clients disable --wipe against a running client
out=$("$B/matriline-client" init "$D/cli2" --credential "$D/mac2.cred" 2>&1)
setorca "$D/cli2"
("$B/matriline-client" -c "$D/cli2/client.conf" run >"$D/cli2.log" 2>&1) &
C2=$!
waitlog "$D/cli2.log" "connected to" 60
s "$D/srv" clients disable mac2 --wipe "cmdmatrix wipe test" >"$D/wipe.out"
sleep 10
kill -0 $C2 2>/dev/null && st="still running" || st="exited"
row "client stops, deletes key and credential" ok $([ "$st" = exited ] && [ ! -e "$D/cli2/state/client.key" ] && [ ! -e "$D/cli2/credential.conf" ]; echo $?) "$st; key: $(ls "$D/cli2/state/client.key" 2>/dev/null || echo gone); cred: $(ls "$D/cli2/credential.conf" 2>/dev/null || echo gone); $(grep -iE 'wipe|disabled|deleted' "$D/cli2.log" | tail -1)" "server clients disable mac2 --wipe"
c fail "cannot connect any more" "$D/cli2" doctor
c fail "enable cannot undo a wipe" "$D/cli2" enable

echo
echo "PASS $pass  FAIL $fail  (of $n)"
kill $SRVPID $REGPID 2>/dev/null
