#!/usr/bin/env bash
# matriline.sh - deploy Matriline in the lab VMs and run end-to-end scenarios (host side).
#
#   tests/matriline.sh deploy            server + its own client on "server", clients on
#                                        void artix devuan ubuntu, relay on "relay"
#   tests/matriline.sh campaign <dir>    copy inputs to the server and queue them
#   tests/matriline.sh wait [minutes]    wait until input/ is empty, print progress
#   tests/matriline.sh status            server status + per-client counters
#   tests/matriline.sh fetch <dest>      copy the server spool (output/ weird/ errors/) to the host
#   tests/matriline.sh logs <node>       last lines of the node's Matriline log
#   tests/matriline.sh powercut <node>   hard power cut of a client VM mid-job, then boot it again
#   tests/matriline.sh restart-server    stop and start the server process mid-campaign
#   tests/matriline.sh restart-clients   restart every client process (e.g. new binary); running
#                                        jobs must be re-adopted, not recomputed
#   tests/matriline.sh stop              stop every Matriline process
#
# Binaries come live from src/bin (shared read-only as /opt/matriline/bin), so rebuild on
# the host and "stop; deploy" to test a change. Network conditions are changed with
# "labctl profile/netem" while a campaign runs.
set -uo pipefail
export MATRILINE_NO_REGISTRY=1 # lab projects stay off the person's list (common/projects)
declare -A TEMP_LIMIT=([server]=80 [void]=79 [artix]=82 [devuan]=84 [ubuntu]=85) # degrees C
LAB=$(cd "$(dirname "$(readlink -f "$0")")/.." && pwd)
L=$LAB/labctl
SHARE=/opt/matriline/bin # live host build output (src/bin), read-only
# Processes run from a private copy: a rebuild on the host replaced the shared binary
# under a starting client, which crashed with "invalid runtime symbol table".
# install(1) writes a new file, so copies never change under a running process.
BIN='~/ml/bin'
syncbin() { "$L" ssh "$1" "mkdir -p ~/ml/bin && for f in $SHARE/*; do install -m 755 \$f ~/ml/bin/; done"; }
CLIENTS=(server void artix devuan ubuntu) # server also computes (D32)
PORT=44100

s() { "$L" ssh "$@"; }
pub() { "$L" addr "$1"; }
srv() { s server "cd ~/ml/srv && $BIN/matriline-server $*"; }

# start_bg node dir cmd... : run a long-lived process with nohup, pid in dir/run.pid
start_bg() {
	local n=$1 d=$2; shift 2
	syncbin "$n"
	s "$n" "cd $d && (nohup $* >> run.log 2>&1 & echo \$! > run.pid)"
}
stop_bg() { s "$1" "[ -f $2/run.pid ] && kill \$(cat $2/run.pid) 2>/dev/null; rm -f $2/run.pid" || true; }

t_deploy() {
	local spub; spub=$(pub server)
	# default profiles (lab/README.md); devuan (P5) can only leave through TCP/443, so the
	# server also listens on 443 as its config documents for such networks
	local p
	for p in relay:P1 server:P2 void:P3 artix:P4 devuan:P5 ubuntu:P3; do "$L" profile "${p%%:*}" "${p#*:}" >/dev/null 2>&1; done
	"$L" forward server $PORT,443 >/dev/null 2>&1
	spub=$(pub server)
	echo "### deploy (server public address $spub:$PORT and :443)"
	start_bg relay "~" "$BIN/matriline-relay -listen :$PORT"
	s server "rm -rf ~/ml/srv ~/ml/cli && mkdir -p ~/ml"; syncbin server
	s server "cd ~/ml && cd ~/ml && $BIN/matriline-server init srv >/dev/null"
	# Test policy (D44): distrustful but compute-frugal. Every layer on, sampled.
	s server "cd ~/ml/srv && sed -i \
		-e 's/^listen = .*/listen = :$PORT, :443/' \
		-e 's/^advertise =.*/advertise = $spub:$PORT/' \
		-e 's/^output_consistency = .*/output_consistency = true/' \
		-e 's/^timing_plausibility = .*/timing_plausibility = true/' \
		-e 's/^scf_recheck_fraction = .*/scf_recheck_fraction = 0.2/' \
		-e 's/^gradient_check_fraction = .*/gradient_check_fraction = 0.2/' \
		-e 's/^hessian_probe_fraction = .*/hessian_probe_fraction = 0.1/' \
		-e 's/^canary_rate = .*/canary_rate = 0.1/' \
		-e 's/^replication_rate = .*/replication_rate = 0.1/' \
		-e 's/^probation_results = .*/probation_results = 3/' server.conf"
	# persistent: a VM reboot lost the runtime setting and the server could not bind :443
	s server "echo 'net.ipv4.ip_unprivileged_port_start = 443' | sudo tee /etc/sysctl.d/90-matriline-lab.conf >/dev/null && sudo sysctl -q --system"
	local r; r=$(readies)
	start_bg server "~/ml/srv" "$BIN/matriline-server run"
	wait_ready "$r"
	local n addr
	for n in "${CLIENTS[@]}"; do
		addr=$spub:$PORT
		[[ $n == server ]] && addr=127.0.0.1:$PORT
		[[ $("$L" profile | awk -v n="$n" '$1==n{print $2}') == P5 ]] && addr=$spub:443
		srv "keys issue $n /tmp/$n.cred $addr" >/dev/null
		s server "cat /tmp/$n.cred && rm /tmp/$n.cred" >"$LAB/state/$n.cred"
		syncbin "$n"
		s "$n" "rm -rf ~/ml/cli && mkdir -p ~/ml && cd ~/ml && $BIN/matriline-client init cli >/dev/null && cat > cli/credential.conf && chmod 600 cli/credential.conf" <"$LAB/state/$n.cred"
		# the VMs have no sensor: the host's CPU temperature, published by the host into the
		# shared bin directory (lab/README.md), with a different limit per VM
		s "$n" "sed -i -e 's|^disk_reserve = .*|disk_reserve = 1GiB|' -e 's|^cpu_temperature_limit = .*|cpu_temperature_limit = ${TEMP_LIMIT[$n]:-80}|' -e 's|^cpu_temperature_file =.*|cpu_temperature_file = /opt/matriline/bin/host-temp|' ~/ml/cli/client.conf"
		rm -f "$LAB/state/$n.cred"
		echo "- $n doctor: $(s "$n" "cd ~/ml/cli && $BIN/matriline-client doctor 2>&1" | grep -Ei 'warn|fail|error|sandbox' | tr '\n' ';' | cut -c1-300)"
		start_bg "$n" "~/ml/cli" "$BIN/matriline-client run"
	done
	sleep 5
	srv status | sed -n 1,20p
}

# the server fingerprints the ORCA reference tree before listening (~15 s over virtiofs)
wait_ready() {
	local i want=$((${1:-0} + 1)) # run.log is appended: wait for one more "ready" line
	for i in $(seq 120); do
		(($(s server "grep -c ' ready, spool' ~/ml/srv/run.log 2>/dev/null || echo 0") >= want)) && return 0
		sleep 1
	done
	echo "server not ready after 120 s:"; s server "tail -5 ~/ml/srv/run.log"; return 1
}
readies() { s server "grep -c ' ready, spool' ~/ml/srv/run.log 2>/dev/null || echo 0"; }

t_campaign() {
	local dir=${1:?input directory} name
	name=$(basename "$dir")
	s server "rm -rf /tmp/$name && mkdir -p /tmp/$name"
	tar -C "$dir" -cf - . | s server "tar -C /tmp/$name -xf -"
	srv "add /tmp/$name"
}

t_wait() {
	local mins=${1:-120} end
	end=$(($(date +%s) + mins * 60))
	while (($(date +%s) < end)); do
		local st; st=$(srv status 2>&1)
		echo "$(date +%H:%M:%S) $(grep -E '^(tasks|results):' <<<"$st" | tr -s ' ' | tr '\n' ' ')"
		grep -q '^tasks: *0 in input/' <<<"$st" && return 0
		sleep 60
	done
	echo "timeout after $mins min"; return 1
}

t_fetch() {
	local dest=${1:?destination}
	# a fresh directory only: fetching over an older copy left a stale weird/ result next to
	# the output/ copy it was later rescued to (a user's review)
	if [[ -e $dest && -n $(ls -A "$dest" 2>/dev/null) ]]; then
		echo "fetch: $dest is not empty; use a new directory" >&2
		return 1
	fi
	mkdir -p "$dest"
	s server "cd ~/ml/srv && tar -cf - output weird errors state/ledger.log run.log 2>/dev/null" | tar -C "$dest" -xf -
	echo "fetched into $dest: $(find "$dest" -name '*.out' | wc -l) outputs"
}

t_logs() { s "${1:?node}" "tail -n ${2:-40} ~/ml/$([[ $1 == server && ${3:-} != cli ]] && echo srv || echo cli)/run.log"; }

t_powercut() {
	local n=${1:?node}
	echo "### power cut on $n at $(date +%H:%M:%S) (running: $(s "$n" "cd ~/ml/cli && $BIN/matriline-client status 2>&1 | head -5" | tr '\n' ' '))"
	"$L" kill "$n"
	sleep "${2:-90}"
	"$L" up "$n"
	start_bg "$n" "~/ml/cli" "$BIN/matriline-client run"
	echo "$n back at $(date +%H:%M:%S)"
}

t_restart_server() {
	echo "### server restart at $(date +%H:%M:%S)"
	srv stop || stop_bg server "~/ml/srv"
	sleep "${1:-20}"
	s server "cd ~/ml/srv && : > run.log.restart && cat run.log >> run.log.restart"
	local r; r=$(readies)
	start_bg server "~/ml/srv" "$BIN/matriline-server run"
	wait_ready "$r"
}

t_restart_clients() {
	local n
	for n in "${CLIENTS[@]}"; do
		stop_bg "$n" "~/ml/cli"
		start_bg "$n" "~/ml/cli" "$BIN/matriline-client run"
		echo "$n restarted at $(date +%H:%M:%S)"
	done
}

t_stop() {
	local n
	for n in "${CLIENTS[@]}"; do stop_bg "$n" "~/ml/cli"; done
	srv stop 2>/dev/null || stop_bg server "~/ml/srv"
	stop_bg relay "~"
}

case ${1:-} in
deploy) t_deploy ;;
campaign) t_campaign "${2:-}" ;;
wait) t_wait "${2:-}" ;;
status) srv status; srv stats ;;
fetch) t_fetch "${2:-}" ;;
logs) t_logs "${2:-}" "${3:-40}" "${4:-}" ;;
powercut) t_powercut "${2:-}" "${3:-90}" ;;
restart-server) t_restart_server "${2:-20}" ;;
restart-clients) t_restart_clients ;;
stop) t_stop ;;
*) sed -n '2,21p' "$0"; exit 2 ;;
esac
