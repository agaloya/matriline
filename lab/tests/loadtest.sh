#!/usr/bin/env bash
# loadtest.sh - many clients against one server, all on this computer: N client processes
# (1 core each) compute M tiny ORCA jobs (HF/STO-3G water, about a second each) from a
# separate lab server on 127.0.0.1. It measures whether every job ends in output/ exactly
# once, how long it takes, the server's CPU time and memory, and the errors in the logs.
# It never touches the real server (other directory, port and no system service).
#
#   lab/tests/loadtest.sh run [clients] [jobs] [verify]   (default 120 1200 off)
#   lab/tests/loadtest.sh stop
#   verify: off (verify.enabled = false) or on (checks on; with every client on one host
#           a second opinion is impossible, so second_opinion_unavailable = accept)
#
# Work directory: ~/matriline/loadtest (on disk: /tmp is RAM here), or LOADTEST_DIR (on a
# Mac ~/matriline is often the repository itself). Processes are stopped by their pid files
# (never by pattern, which could hit the real server or this shell). Runs on Linux and macOS.
set -uo pipefail
export MATRILINE_NO_REGISTRY=1 # lab projects stay off the person's list (common/projects)
REPO=$(cd "$(dirname "$(readlink -f "$0")")/../.." && pwd)
W=${LOADTEST_DIR:-$HOME/matriline/loadtest}
PORT=44300
B=$W/bin

# ours: a pid from a pid file that still runs one of this test's binaries
# (Linux: /proc/<pid>/exe; macOS has no /proc: ps prints the executable's path)
ours() {
	[ -f "$1" ] || return 1
	local exe
	if [ -d /proc ]; then exe=$(readlink "/proc/$(cat "$1")/exe" 2>/dev/null); else exe=$(ps -o comm= -p "$(cat "$1")" 2>/dev/null); fi
	case $exe in "$B"/*) return 0 ;; esac
	return 1
}

stop() {
	local f
	for f in "$W"/cli/*/run.pid "$W"/srv/run.pid; do
		ours "$f" && kill "$(cat "$f")"
	done
	sleep 3
	for f in "$W"/cli/*/run.pid "$W"/srv/run.pid; do
		ours "$f" && kill -9 "$(cat "$f")"
		rm -f "$f"
	done
	return 0
}

start() { # dir program: run it in dir, detached, its own pid in dir/run.pid
	(cd "$1" && exec nohup "$2" run </dev/null >"$3" 2>&1) &
	echo $! >"$1/run.pid"
}

srv() { (cd "$W/srv" && "$B/matriline-server" "$@"); }

setconf() { # file section key value
	python3 - "$@" <<'EOF'
import sys,re
f,sec,key,val=sys.argv[1:]
s=open(f).read().split("\n")
cur=None;done=False
for i,l in enumerate(s):
    m=re.match(r"\[(\w+)\]",l)
    if m: cur=m.group(1); continue
    if cur==sec and re.match(r"%s\s*="%re.escape(key),l):
        s[i]="%s = %s"%(key,val); done=True; break
if not done:
    s.append("[%s]\n%s = %s"%(sec,key,val))
open(f,"w").write("\n".join(s))
EOF
}

run() {
	local N=${1:-120} M=${2:-1200} V=${3:-off} i
	[ -d "$W" ] && { stop; rm -rf "${W:?}"; }
	mkdir -p "$B" "$W/cred" "$W/cli" "$W/inputs"
	(cd "$REPO/src" && go build -o "$B/matriline-server" ./server && go build -o "$B/matriline-client" ./client) || exit 1
	"$B/matriline-server" init "$W/srv" --language en >/dev/null || exit 1
	local C=$W/srv/server.conf
	setconf "$C" network listen "127.0.0.1:$PORT"
	setconf "$C" network advertise "127.0.0.1:$PORT"
	setconf "$C" network max_sessions 512
	setconf "$C" tasks duplicate_when_idle false
	if [ "$V" = on ]; then
		setconf "$C" verify second_opinion_unavailable accept
	else
		setconf "$C" verify enabled false
	fi
	start "$W/srv" "$B/matriline-server" server.out
	for i in $(seq 1 30); do srv status >/dev/null 2>&1 && break; sleep 1; done
	echo "server up (pid $(cat "$W/srv/run.pid")); issuing $N credentials"
	for i in $(seq -w 1 "$N"); do
		srv keys issue "lt$i" "$W/cred/lt$i.conf" >/dev/null || { echo "keys issue lt$i failed"; exit 1; }
		"$B/matriline-client" init "$W/cli/lt$i" --credential "$W/cred/lt$i.conf" --language en >/dev/null 2>&1 || { echo "client init lt$i failed"; exit 1; }
		setconf "$W/cli/lt$i/client.conf" resources cores 1
		setconf "$W/cli/lt$i/client.conf" security updates false
		setconf "$W/cli/lt$i/client.conf" schedule pause_on_battery false
	done
	# the first client fingerprints ORCA once; the others reuse its cache (same files)
	local first=$W/cli/lt$(printf "%0${#N}d" 1)
	(cd "$first" && "$B/matriline-client" doctor >"$W/doctor.out" 2>&1) || { echo "doctor failed:"; tail -5 "$W/doctor.out"; exit 1; }
	for d in "$W"/cli/*; do [ "$d" != "$first" ] && mkdir -p "$d/state" && cp "$first"/state/fpcache-* "$d/state/" 2>/dev/null; done
	echo "starting $N clients"
	for d in "$W"/cli/*; do
		start "$d" "$B/matriline-client" client.out
	done
	local t0=$(date +%s) up=0
	for i in $(seq 1 120); do
		up=$(srv status 2>/dev/null | sed -n 's/^clients connected: \([0-9]*\).*/\1/p')
		[ "${up:-0}" -ge "$N" ] && break
		sleep 1
	done
	echo "$up of $N clients connected after $(( $(date +%s) - t0 )) s"
	for i in $(seq -w 1 "$M"); do
		printf '! HF STO-3G\n* xyz 0 1\nO 0 0 0.1173\nH 0 0.7572 -0.4692\nH 0 -0.7572 -0.4692\n*\n' >"$W/inputs/w$i.inp"
	done
	t0=$(date +%s)
	srv add "$W/inputs" >/dev/null
	local q out last=-1 still=0
	while :; do
		out=$(find "$W/srv/output" -name '*.out' 2>/dev/null | wc -l)
		q=$(find "$W/srv/input" -name '*.inp' 2>/dev/null | wc -l)
		[ "$q" -eq 0 ] && [ "$out" -ge "$M" ] && break
		if [ "$out" -eq "$last" ]; then still=$((still + 1)); else still=0; fi
		[ $still -ge 120 ] && { echo "STUCK: no new result for 10 min ($out of $M)"; break; }
		last=$out
		sleep 5
	done
	local t=$(( $(date +%s) - t0 ))
	local pid=$(cat "$W/srv/run.pid")
	echo "results: $out of $M in output/ after $t s ($(python3 -c "print(round($M/max($t,1),1))") jobs/s)"
	echo "server: $(ps -o rss=,cputime= -p "$pid" | awk '{printf "%d MB RSS, CPU time %s", $1/1024, $2}')"
	srv status | sed -n '2,4p'
	srv check 2>&1 | tail -3
	srv verify 2>&1 | tail -3
	echo "weird: $(find "$W/srv/weird" -type f | wc -l)  errors: $(find "$W/srv/errors" -type f | wc -l)"
	echo "client disconnects during the run: $(grep -c ' disconnected: ' "$W/srv/server.out")"
	grep ' disconnected: ' "$W/srv/server.out" | sed 's/^.*disconnected: //' | cut -c1-80 | sort | uniq -c | sort -rn | head -5
	echo "server log ERROR/WARN lines: $(grep -c ' ERROR \| WARN ' "$W/srv/server.out")"
	grep ' ERROR \| WARN ' "$W/srv/server.out" | sed 's/^[0-9/ :.]*//' | cut -c1-90 | sort | uniq -c | sort -rn | head -8
	stop
	echo "client log ERROR lines: $(cat "$W"/cli/*/client.out | grep -c ' ERROR ')"
	cat "$W"/cli/*/client.out | grep ' ERROR ' | sed 's/^[0-9/ :.]*//' | cut -c1-90 | sort | uniq -c | sort -rn | head -5
	return 0
}

case ${1:-} in
run) shift; run "$@" ;;
stop) stop ;;
*) echo "usage: loadtest.sh run [clients] [jobs] [on|off] | stop" >&2; exit 2 ;;
esac
