#!/usr/bin/env bash
# upgrade.sh <old-commit> [new-commit] - upgrade by hand from an older release: a server and
# 3 clients of <old-commit> compute a batch, stop; the binaries are replaced by
# [new-commit] (default: the working tree's HEAD) and they start on the SAME directories;
# a second batch runs. Before and after: verify and check clean, the settings unchanged,
# the first batch's results still there, the clients reconnect with their old keys.
# Everything on this computer, port 44310, ~/matriline/upgradetest; no system service.
set -uo pipefail
export MATRILINE_NO_REGISTRY=1 # lab projects stay off the person's list (common/projects)
REPO=$(cd "$(dirname "$(readlink -f "$0")")/../.." && pwd)
OLD=${1:?usage: upgrade.sh <old-commit> [new-commit]} NEW=${2:-HEAD}
W=$HOME/matriline/upgradetest
PORT=44310
log() { echo "$(date +%H:%M:%S) $*"; }
fail() { log "FAIL: $*"; stop; exit 1; }
ours() { [ -f "$1" ] && case $(readlink "/proc/$(cat "$1")/exe" 2>/dev/null) in "$W"/bin-*) return 0 ;; esac; return 1; }
stop() {
	local f
	for f in "$W"/c*/run.pid "$W"/srv/run.pid; do ours "$f" && kill "$(cat "$f")"; done
	sleep 3
	for f in "$W"/c*/run.pid "$W"/srv/run.pid; do ours "$f" && kill -9 "$(cat "$f")"; rm -f "$f"; done
	return 0
}
start() { (cd "$1" && exec nohup "$2" run </dev/null >>"$3" 2>&1) & echo $! >"$1/run.pid"; }
build() { # commit dir
	local t=$W/src-$1
	git -C "$REPO" worktree add -f --detach "$t" "$1" >/dev/null 2>&1 || fail "worktree $1"
	(cd "$t/src" && go build -ldflags "-X main.commit=$1" -o "$2/matriline-server" ./server && go build -ldflags "-X main.commit=$1" -o "$2/matriline-client" ./client) || fail "build $1"
	git -C "$REPO" worktree remove --force "$t"
}
srv() { (cd "$W/srv" && "$B/matriline-server" "$@"); }
batch() { # name n: add n inputs, wait until all are in output/
	local i d=$W/in-$1
	mkdir -p "$d"
	for i in $(seq 1 "$2"); do
		printf '! HF STO-3G\n* xyz 0 1\nO 0 0 0.1173\nH 0 0.7572 -0.4692\nH 0 -0.7572 -0.4692\n*\n' >"$d/$1-$i.inp"
	done
	srv add "$d" >/dev/null || fail "add $1"
	for i in $(seq 1 120); do
		[ "$(find "$W/srv/output/in-$1" -name '*.out' 2>/dev/null | wc -l)" -ge "$2" ] && return 0
		sleep 2
	done
	fail "batch $1 did not finish: $(srv status | sed -n 2,3p)"
}
clean() { # label
	srv check | grep -q "exactly one place" || fail "$1: check: $(srv check | tail -2)"
	srv verify | grep -q "RESULT: spool consistent" || fail "$1: verify: $(srv verify | tail -3)"
	log "$1: check and verify clean ($(srv verify | grep -o 'ledger: [0-9]* entries'))"
}
connected() { # n
	local i
	for i in $(seq 1 60); do
		[ "$(srv status | sed -n 's/^clients connected: \([0-9]*\).*/\1/p')" = "$1" ] && return 0
		sleep 1
	done
	fail "$(srv status | grep 'clients connected')"
}

[ -d "$W" ] && { stop; rm -rf "${W:?}"; }
mkdir -p "$W"
oldc=$(git -C "$REPO" rev-parse --short=12 "$OLD") newc=$(git -C "$REPO" rev-parse --short=12 "$NEW")
build "$oldc" "$W/bin-old"
build "$newc" "$W/bin-new"
B=$W/bin-old
log "old $("$B/matriline-server" version), new $("$W/bin-new/matriline-server" version)"
"$B/matriline-server" init "$W/srv" >/dev/null || fail init
sed -i "s/^listen =.*/listen = 127.0.0.1:$PORT/; s/^advertise =.*/advertise = 127.0.0.1:$PORT/; s/^duplicate_when_idle =.*/duplicate_when_idle = false/; s/^second_opinion_unavailable =.*/second_opinion_unavailable = accept/" "$W/srv/server.conf"
start "$W/srv" "$B/matriline-server" "$W/srv/server.out"
for i in $(seq 1 30); do srv status >/dev/null 2>&1 && break; sleep 1; done
for c in c1 c2 c3; do
	srv keys issue "$c" "$W/$c.cred" >/dev/null || fail "keys issue $c"
	"$B/matriline-client" init "$W/$c" --credential "$W/$c.cred" >/dev/null 2>&1 || fail "client init $c"
	sed -i 's/^cores = .*/cores = 2/; s/^updates = .*/updates = false/; s/^pause_on_battery = .*/pause_on_battery = false/' "$W/$c/client.conf"
	start "$W/$c" "$B/matriline-client" "$W/$c/client.out"
done
connected 3
batch one 30
clean "old version"
sum_conf=$(sha256sum "$W/srv/server.conf" "$W"/c*/client.conf | sha256sum)
keys=$(srv clients list | awk '/^ *c[123] /{print $1, $3}' | sort)
[ "$(echo "$keys" | grep -c ' ml1-')" -eq 3 ] || fail "clients list: $keys"
stop
log "stopped; installing $newc on the same directories"
B=$W/bin-new
start "$W/srv" "$B/matriline-server" "$W/srv/server.out"
for i in $(seq 1 30); do srv status >/dev/null 2>&1 && break; sleep 1; done
srv version | grep -q "$newc" || fail "the server does not run the new version: $(srv version)"
grep -E ' ERROR | WARN ' "$W/srv/server.out" | tail -n +1 | grep -v 'alert \[enrolled\]\|only [0-9] active client' | tail -5
clean "new version, before any work"
for c in c1 c2 c3; do start "$W/$c" "$B/matriline-client" "$W/$c/client.out"; done
connected 3
[ "$(srv clients list | awk '/^ *c[123] /{print $1, $3}' | sort)" = "$keys" ] || fail "clients changed: $(srv clients list)"
log "the 3 clients reconnected with their old keys"
batch two 30
clean "new version, after a batch"
[ "$(find "$W/srv/output/in-one" -name '*.out' | wc -l)" -eq 30 ] || fail "first batch results missing"
[ "$(sha256sum "$W/srv/server.conf" "$W"/c*/client.conf | sha256sum)" = "$sum_conf" ] || fail "a configuration file was changed by the upgrade"
log "settings unchanged, first batch kept, second batch done"
errs=$(grep -h ' ERROR ' "$W/srv/server.out" "$W"/c*/client.out | wc -l)
stop
[ "$errs" -eq 0 ] || { grep -h ' ERROR ' "$W/srv/server.out" "$W"/c*/client.out | head -5; fail "$errs ERROR lines"; }
log "PASS: $oldc -> $newc"
