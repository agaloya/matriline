#!/usr/bin/env bash
# pin.sh [vm...] - pin every vCPU thread of the running lab VMs to its own PHYSICAL core.
#
# Host: 16 cores / 32 threads (CPU n and n+16 are the two threads of core n, see
# "lscpu -e"). The lab has exactly 16 vCPUs, so vCPU threads get CPUs 0-15 (one per core)
# and every other QEMU thread (I/O, emulator) plus the host get the siblings 16-31.
# Idempotent: run it again after a VM (re)starts; labctl up calls it for each VM.
#
# vCPU thread ids come from the QEMU monitor ("info cpus" prints thread_id=...),
# https://www.qemu.org/docs/master/system/monitor.html ; affinity via taskset(1).
set -euo pipefail
LAB=$(cd "$(dirname "$(readlink -f "$0")")/.." && pwd)
RUN=$LAB/run

# host CPUs per VM, in vCPU order. void gets both threads of core 4 (the lab's SMT test
# machine); ubuntu (6 vCPUs) and devuan (3) share no core with anything else; the light
# router/relay take cores 0-1. The temporary image builder (labctl build) gets no core:
# all its threads stay on the host siblings.
declare -A CORES=(
	[router]="0" [relay]="1" [server]="2 3" [void]="4 20"
	[devuan]="5 6 7" [ubuntu]="8 9 10 11 12 13" [artix]="14 15" [builder]=""
)
HOST_SET=16-31

pin_vm() {
	local vm=$1 pid mon tids cores i=0 t
	pid=$(cat "$RUN/$vm.pid" 2>/dev/null) || return 0
	kill -0 "$pid" 2>/dev/null || return 0
	mon=$RUN/$vm.mon
	# every thread of the process -> sibling threads first, then vCPUs to their core
	for t in /proc/"$pid"/task/*; do taskset -pc "$HOST_SET" "${t##*/}" >/dev/null 2>&1 || true; done
	tids=$(printf 'info cpus\n' | socat -t2 - "UNIX-CONNECT:$mon" 2>/dev/null | tr -d '\r' | grep -o 'thread_id=[0-9]*' | cut -d= -f2)
	read -ra cores <<<"${CORES[$vm]}"
	for t in $tids; do
		if ((i < ${#cores[@]})); then
			taskset -pc "${cores[$i]}" "$t" >/dev/null
		fi
		i=$((i + 1))
	done
	echo "$vm: $i vCPU(s) -> cores ${CORES[$vm]}; other threads -> $HOST_SET"
}

vms=("$@")
((${#vms[@]})) || vms=(router relay server void artix devuan ubuntu)
for vm in "${vms[@]}"; do pin_vm "$vm"; done
