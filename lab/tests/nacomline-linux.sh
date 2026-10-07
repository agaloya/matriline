#!/usr/bin/env bash
# nacomline-linux.sh [vm] - Nacomline (../nacomline, next to this repo) on Linux, in a lab VM
# with a systemd user manager (default: server): init (finds ORCA), service install (a
# systemd user unit), 2 good inputs + 1 ORCA error, pause/resume, a settings change applied
# while running (client restarts), service remove with no Matriline process left behind.
# The same steps as nacomline-win.sh. It runs in a VM, not on this desktop, whose systemd
# user manager also runs the real server.
set -uo pipefail
export MATRILINE_NO_REGISTRY=1 # lab projects stay off the person's list (common/projects)
LAB=$(cd "$(dirname "$(readlink -f "$0")")/.." && pwd)
SRC=$LAB/../src NACO=$LAB/../../nacomline
VM=${1:-server}
D=$LAB/images/nacomline-$VM
log() { echo "$(date +%H:%M:%S) $*"; }
fail() { log "FAIL: $*"; exit 1; }
vm() { timeout 300 "$LAB/labctl" ssh "$VM" "$1" 2>&1; }
N='~/nc/bin/nacomline -c ~/nc/proj/nacomline.conf'
rm -rf "$D" && mkdir -p "$D"
(cd "$SRC" && go build -o "$D/matriline-server" ./server && go build -o "$D/matriline-client" ./client) || fail build
(cd "$NACO" && go build -o "$D/nacomline" .) || fail "build nacomline"
# a previous run: its service, then any process still running from ~/nc
vm '[ -x ~/nc/bin/nacomline ] && ~/nc/bin/nacomline -c ~/nc/proj/nacomline.conf service remove >/dev/null 2>&1; for p in $(ls /proc | grep -E "^[0-9]+$"); do case $(readlink /proc/$p/exe 2>/dev/null) in "$HOME"/nc/*) kill $p;; esac; done; sleep 1; rm -rf ~/nc; mkdir -p ~/nc/bin' >/dev/null
for f in nacomline matriline-server matriline-client; do
	"$LAB/labctl" ssh "$VM" "cat > ~/nc/bin/$f && chmod 755 ~/nc/bin/$f" <"$D/$f" || fail "copy $f"
done
log "init: $(vm '~/nc/bin/nacomline init ~/nc/proj' | head -2 | tr '\n' ' ')"
vm "grep -E '^path = .+' ~/nc/proj/nacomline.conf" | grep -i orca >/dev/null || fail "ORCA not found by init"
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
printf '%s\n' "$E" | vm 't=$(cat); printf "%s\n" "$t" > ~/nc/proj/input/etoh_1.inp; printf "%s\n" "$t" > ~/nc/proj/input/etoh_2.inp; printf "! NOTAKEYWORD def2-SVP\n* xyz 0 1\nH 0 0 0\nH 0 0 0.74\n*\n" > ~/nc/proj/input/typo.inp' >/dev/null
log "service: $(vm "$N service install" | tail -2 | tr '\n' ' ')"
vm 'systemctl --user list-units "nacomline-*" --no-legend' | grep -q running || fail "no running nacomline unit"
for _ in $(seq 120); do vm "$N status" | grep "output 2 .*errors 1" >/dev/null && break; sleep 5; done
vm "$N status" | grep -E "^results|this-computer"
vm "$N status" | grep "output 2 .*errors 1" >/dev/null || fail "results not filed: $(vm "tail -5 ~/nc/proj/.nacomline/*.out")"
log "pause: $(vm "$N pause 10m" | head -1)"
vm "$N status" | grep "this computer: paused" >/dev/null || fail "pause not shown"
log "resume: $(vm "$N resume")"
# a change the status shows: a slot count other than the current one
now=$(vm "$N status" | awk '$1 == "this-computer" {print $3}')
want=$([[ $now == 1 ]] && echo 2 || echo 1)
vm "sed -i 's/^cores = .*/cores = $want/' ~/nc/proj/nacomline.conf && $N config apply" | tail -1
for _ in $(seq 30); do vm "$N status" | grep -E "this-computer +ml1-[a-z0-9]+ +$want " >/dev/null && break; sleep 3; done
vm "$N status" | grep -E "this-computer +ml1-[a-z0-9]+ +$want " >/dev/null || fail "the settings change did not reach the client: $(vm "$N status" | grep this-computer)"
log "client restarted with $want slot(s) (was $now)"
vm "$N events 8" | cut -c1-130
log "remove: $(vm "$N service remove")"
sleep 10
left=$(vm 'for p in $(ls /proc | grep -E "^[0-9]+$"); do readlink /proc/$p/exe 2>/dev/null; done | grep "^$HOME/nc/" || true')
[[ -z $left ]] || fail "processes left after service remove: $left"
vm 'systemctl --user list-unit-files "nacomline-*" --no-legend' | grep -q nacomline && fail "unit file left after service remove"
log PASS
