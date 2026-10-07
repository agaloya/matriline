#!/usr/bin/env bash
# winchaos.sh client|server [vm] - chaos tests with a Windows VM (lab/windows/winvm.sh), each
# event while a job runs, then a check that nothing got lost.
#   client  the VM's client (scheduled task; server: the host's test server in
#           lab/images/wintest, listening on 127.0.0.1:44390) suffers the events
#   server  the VM's server (C:\mls\spool, scheduled task, port 44391 forwarded; Linux
#           clients run on the host) suffers them; there is no "roam" for a server (its
#           clients would not find it at a new address either)
# Events:
#   dropout   network link down 60 s, then up
#   suspend   VM paused 90 s (laptop lid closed), then resumed
#   roam      a second network card with another address appears, the first goes down for
#             90 s (a laptop moving to another network), then back; the second card is removed
#   restart   the Matriline scheduled task restarted (update, crash)
#   powercut  VM killed, 30 s off, booted (autologon starts the scheduled tasks again)
# Inputs: an r2SCAN-3c optimization of ethanol (~40 s on the VM, ~8 s on the host), queued so
# that one is always running. Afterwards: queue drained, no new results in weird/ or errors/,
# no attempt lost.
set -uo pipefail
export MATRILINE_NO_REGISTRY=1 # lab projects stay off the person's list (common/projects)
LAB=$(cd "$(dirname "$(readlink -f "$0")")/.." && pwd)
mode=${1:?usage: winchaos.sh client|server [vm]} vm=${2:-win11}
W="$LAB/windows/winvm.sh"
MON=$LAB/images/windows/$vm.mon
B=$LAB/../src/bin
log() { echo "$(date +%H:%M:%S) $mode $*"; }
mon() { echo "$*" | socat - "UNIX-CONNECT:$MON" >/dev/null; }
vmssh() { timeout 60 "$W" ssh "$vm" "$@" 2>/dev/null; }

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

batch=0
if [[ $mode == client ]]; then
	events="dropout suspend roam restart powercut"
	task="matriline-client-*"
	status() { "$B/matriline-server" -c "$LAB/images/wintest/server.conf" status 2>/dev/null; }
	running() { status | awk -v n="${CLIENT:-win11ac}" '$1==n{print $4}'; }
	add() { # n inputs into the host server
		local d; d=$(mktemp -d); batch=$((batch + 1))
		for i in $(seq "$1"); do printf '%s\n' "$INPUT" >"$d/etoh_${batch}_$i.inp"; done
		"$B/matriline-server" -c "$LAB/images/wintest/server.conf" add "$d" "input/chaos-$(date +%H%M%S)" >/dev/null
		rm -rf "$d"
	}
elif [[ $mode == server ]]; then
	events="dropout suspend restart powercut"
	task="matriline-server-*"
	status() { vmssh 'C:\mls\matriline-server.exe -c C:\mls\spool\server.conf status'; }
	running() { status | sed -n 's/.*> running \([0-9]*\).*/\1/p'; }
	add() { # n inputs, written in the VM and added there
		batch=$((batch + 1))
		printf '%s\n' "$INPUT" | vmssh "\$t=[Console]::In.ReadToEnd(); \$d=\"C:\\mls\\chaos$$-$batch\"; New-Item -ItemType Directory -Force \$d | Out-Null; 1..$1 | ForEach-Object { Set-Content -Encoding ascii \"\$d\\etoh_\$_.inp\" \$t }; C:\\mls\\matriline-server.exe -c C:\\mls\\spool\\server.conf add \$d input/chaos$$-$batch | Out-Null"
	}
else
	echo "usage: winchaos.sh client|server [vm]" >&2; exit 2
fi
queued() { status | sed -n 's/^ \. queued \([0-9]*\).*/\1/p'; }
count() { status | sed -n "s/.*$1 \([0-9]*\).*/\1/p" | head -1; } # count weird / errors
wait_for() { # wait_for <seconds> <command...>: until the command succeeds
	local end=$(($(date +%s) + $1)); shift
	until "$@"; do (($(date +%s) > end)) && return 1; sleep 3; done
}
job_running() { [[ $(running) -gt 0 ]] 2>/dev/null; }
vm_back() { vmssh "Get-ScheduledTask -TaskName '$task' | Where-Object State -eq Running" | grep -q Running; }

weird0=$(count '? weird') errors0=$(count '! errors')
log "start: weird $weird0, errors $errors0"
for ev in $events; do
	[[ $(queued) -lt 4 ]] 2>/dev/null && add 8
	wait_for 600 job_running || { log "no job started in 10 min"; exit 1; }
	sleep 10 # into the job
	case $ev in
	dropout) log "dropout 60 s"; mon "set_link n0 off"; sleep 60; mon "set_link n0 on" ;;
	suspend) log "suspend 90 s"; mon stop; sleep 90; mon cont ;;
	roam)
		log "roam: second card (10.0.2.60+), first down 90 s"
		mon "netdev_add user,id=n1,dhcpstart=10.0.2.60"; mon "device_add e1000e,netdev=n1,id=nic1,bus=hp1" # a free hot-plug port (winvm.sh); q35's own bus has none
		sleep 20; mon "set_link n0 off"; sleep 90; mon "set_link n0 on"
		mon "device_del nic1"; sleep 5; mon "netdev_del n1" ;;
	restart)
		log "restart the scheduled task"
		vmssh "Get-ScheduledTask -TaskName '$task' | Stop-ScheduledTask; Start-Sleep 2; Get-ScheduledTask -TaskName '$task' | Start-ScheduledTask" ;;
	powercut)
		log "power cut 30 s"
		kill -9 "$(cat "$LAB/images/windows/$vm.pid")"; rm -f "$LAB/images/windows/$vm.pid"; sleep 30
		FWD=$([[ $mode == server ]] && echo 44391) "$W" start "$vm" >/dev/null ;;
	esac
	wait_for 600 vm_back || { log "the VM or its task did not come back in 10 min"; exit 1; }
	log "$ev done; queued $(queued), running $(running)"
done
log "waiting for the queue to drain"
drained() { local s; s=$(status); grep -q "queued 0 > running 0" <<<"$s" && grep -q " 0 verification" <<<"$s"; }
wait_for 3600 drained || log "not drained after 1 h"
s=$(status); echo "$s" | head -8
weird=$(count '? weird') errors=$(count '! errors') lost=$(echo "$s" | sed -n 's/.*running, \([0-9]*\) lost.*/\1/p')
log "end: weird $weird0 -> $weird, errors $errors0 -> $errors, lost ${lost:-?}"
[[ $weird == "$weird0" && $errors == "$errors0" && ${lost:-1} == 0 ]] && { log PASS; exit 0; }
log FAIL; exit 1
