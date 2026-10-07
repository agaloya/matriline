#!/usr/bin/env bash
# chaos.sh <node> <minutes> - a laptop on the move: random network and power events on one
# client while the main campaign runs, then a check that nothing got stuck.
#
# Events (one every 45-120 s, logged with timestamps):
#   wifi      netem delay 50-800 ms + jitter, loss 1-30 % (varying signal)
#   dropout   link down 10-60 s (signal lost), then up
#   roam      NAT profile change (new network, new public address)
#   suspend   VM paused 30-180 s (laptop lid closed), then resumed
#   powercut  hard power off, 20-60 s, boot, client restarted
#   restart   client process restarted (update, crash)
# Afterwards: network restored, then "matriline.sh wait"-style check that attempts do not
# stay lost and the node's client is connected again.
set -uo pipefail
export MATRILINE_NO_REGISTRY=1 # lab projects stay off the person's list (common/projects)
LAB=$(cd "$(dirname "$(readlink -f "$0")")/.." && pwd)
L=$LAB/labctl
n=${1:?node} mins=${2:-30}
home=$("$L" profile | awk -v n="$n" '$1==n{print $2}')
end=$(($(date +%s) + mins * 60))
log() { echo "$(date +%H:%M:%S) $n $*"; }
client_up() { "$L" ssh "$n" "for f in /opt/matriline/bin/*; do install -m 755 \$f ~/ml/bin/; done; cd ~/ml/cli && (nohup ~/ml/bin/matriline-client run >> run.log 2>&1 & echo \$! > run.pid)" >/dev/null 2>&1; }

while (($(date +%s) < end)); do
	ev=$(shuf -n1 -e wifi wifi dropout roam suspend powercut restart)
	case $ev in
	wifi)
		d=$(shuf -i 50-800 -n1) j=$(shuf -i 5-100 -n1) l=$(shuf -i 1-30 -n1)
		"$L" netem "$n" raw "delay ${d}ms ${j}ms loss ${l}%" >/dev/null 2>&1
		log "wifi: delay ${d}ms jitter ${j}ms loss ${l}%" ;;
	dropout)
		t=$(shuf -i 10-60 -n1); log "dropout ${t}s"
		"$L" netem "$n" down >/dev/null 2>&1; sleep "$t"; "$L" netem "$n" up >/dev/null 2>&1 ;;
	roam)
		p=$(shuf -n1 -e P1 P2 P3 P4); log "roam to $p ($(timeout 5 "$L" addr "$n" 2>/dev/null) -> new address)"
		"$L" profile "$n" "$p" >/dev/null 2>&1 ;;
	suspend)
		t=$(shuf -i 30-180 -n1); log "suspend ${t}s"
		"$L" monitor "$n" stop; sleep "$t"; "$L" monitor "$n" cont ;;
	powercut)
		t=$(shuf -i 20-60 -n1); log "power cut ${t}s"
		"$L" kill "$n" >/dev/null 2>&1; sleep "$t"; "$L" up "$n" >/dev/null 2>&1; client_up; log "booted, client started" ;;
	restart)
		log "client restart"
		"$L" ssh "$n" "kill \$(cat ~/ml/cli/run.pid) 2>/dev/null" >/dev/null 2>&1; sleep 3; client_up ;;
	esac
	sleep "$(shuf -i 45-120 -n1)"
done

log "chaos over: restoring the network"
"$L" netem "$n" clear >/dev/null 2>&1; "$L" netem "$n" up >/dev/null 2>&1
"$L" profile "$n" "$home" >/dev/null 2>&1
"$L" ssh "$n" "pgrep -c matriline-clien" >/dev/null 2>&1 || client_up
sleep 120
log "after 2 min: $("$L" ssh "$n" "pgrep -c matriline-clien; grep -E 'connected to|connection ended' ~/ml/cli/run.log | tail -1 | cut -c12-120" | tr '\n' ' ')"
"$L" ssh server "cd ~/ml/srv && ~/ml/bin/matriline-server status" | sed -n 2,3p
