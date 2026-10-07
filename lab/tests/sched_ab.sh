#!/usr/bin/env bash
# sched_ab.sh - A/B test of tasks.scheduler (order vs learned = longest predicted job first)
# on a separate test server, so that nothing else competes for the slots (the first A/B on
# the main lab deployment was confounded by its replicas and checks).
#
#   tests/sched_ab.sh run <test-inputs-dir> <training-inputs-dir>
#   tests/sched_ab.sh stop
#
# 1. training: the training inputs run with scheduler = order, so the model learns the job
#    times and every client's speed (from this server's own accepted results);
# 2. 'learned': the test inputs, scheduler = learned;
# 3. 'order': the same test inputs again, scheduler = order (after 'learned', so the order
#    run cannot profit from what the model learned about these very inputs, and the order
#    run does not use the model at all).
# Makespan = from 'add' to the last input of the batch leaving input/. Verification,
# replicas, canaries and idle duplicates are off. Clients: void 1 slot, artix 2, server 1,
# ubuntu 4 (different speeds, which is what the learned rule exploits). The main lab
# deployment must be idle (nothing queued) while this runs; it is not touched otherwise.
set -uo pipefail
export MATRILINE_NO_REGISTRY=1 # lab projects stay off the person's list (common/projects)
LAB=$(cd "$(dirname "$(readlink -f "$0")")/.." && pwd)
L=$LAB/labctl
SHARE=/opt/matriline/bin
BIN='~/ml/abbin'
PORT=44210
# devuan (P5, egress TCP/443 only) cannot reach the test port; the server VM lends 1 slot
declare -A SLOTS=([void]=1 [artix]=2 [server]=1 [ubuntu]=4)
s() { "$L" ssh "$@"; }
ab() { s server "cd ~/ml/ab && $BIN/matriline-server $*"; }
syncbin() { s "$1" "mkdir -p ~/ml/abbin && for f in $SHARE/*; do install -m 755 \$f ~/ml/abbin/; done"; }
stop_dir() { s "$1" "[ -f $2/run.pid ] && kill \$(cat $2/run.pid) 2>/dev/null; rm -f $2/run.pid; for p in \$(pgrep -f 'matriline-client'); do case \$(readlink /proc/\$p/cwd) in */ml/abcli*) kill \$p;; esac; done" || true; }

t_stop() {
	local n
	for n in "${!SLOTS[@]}"; do stop_dir "$n" "~/ml/abcli"; done
	ab stop >/dev/null 2>&1 || true
}

batch() { # name scheduler: run one batch to completion, print its makespan
	local name=$1 mode=$2 t0 t1
	s server "cd ~/ml/ab && sed -i 's/^scheduler = .*/scheduler = $mode/' server.conf"
	ab config reload >/dev/null
	t0=$(date +%s)
	ab "add /tmp/$name input/$name" >/dev/null
	sleep 20
	# done when nothing is queued (verification is off: no sub-tasks)
	until [[ $(ab stats 2>/dev/null | awk '/still queued/{print $NF}') == 0 ]]; do sleep 15; done
	t1=$(date +%s)
	echo "$(date +%T) batch $name ($mode): makespan $(( (t1 - t0) / 60 )) min $(( (t1 - t0) % 60 )) s"
}

t_run() {
	local test=${1:?test inputs dir} train=${2:?training inputs dir} spub n
	t_stop
	"$L" forward server 44100,443,$PORT >/dev/null 2>&1
	spub=$("$L" addr server)
	syncbin server
	s server "rm -rf ~/ml/ab && cd ~/ml && $BIN/matriline-server init ab >/dev/null && cd ab && sed -i \
		-e 's/^listen = .*/listen = :$PORT/' -e 's/^advertise =.*/advertise = $spub:$PORT/' \
		-e 's/^scan_interval = .*/scan_interval = 1s/' -e 's/^settle_time = .*/settle_time = 0s/' \
		-e 's/^scf_recheck_fraction = .*/scf_recheck_fraction = 0/' \
		-e 's/^gradient_check_fraction = .*/gradient_check_fraction = 0/' \
		-e 's/^hessian_probe_fraction = .*/hessian_probe_fraction = 0/' \
		-e 's/^canary_rate = .*/canary_rate = 0/' \
		-e 's/^replication_rate = .*/replication_rate = 0/' \
		-e 's/^probation_results = .*/probation_results = 0/' -e 's/^probation_replication = .*/probation_replication = 0/' \
		-e 's/^duplicate_when_idle = .*/duplicate_when_idle = false/' server.conf \
		&& (nohup $BIN/matriline-server run >> run.log 2>&1 & echo \$! > run.pid)"
	for _ in $(seq 120); do s server "grep -q ' ready, spool' ~/ml/ab/run.log" && break; sleep 1; done
	for n in "${!SLOTS[@]}"; do
		syncbin "$n"
		local addr=$spub:$PORT
		[[ $n == server ]] && addr=127.0.0.1:$PORT
		ab "keys issue $n-ab /tmp/$n-ab.cred $addr" >/dev/null
		s server "cat /tmp/$n-ab.cred && rm /tmp/$n-ab.cred" >"$LAB/state/ab.cred"
		s "$n" "rm -rf ~/ml/abcli && mkdir -p ~/ml && cd ~/ml && $BIN/matriline-client init abcli >/dev/null && cat > abcli/credential.conf && cd abcli && sed -i -e 's/^disk_reserve = .*/disk_reserve = 1GiB/' -e 's/^cores = ${SLOTS[$n]}/' client.conf && (nohup $BIN/matriline-client run >> run.log 2>&1 & echo \$! > run.pid)" <"$LAB/state/ab.cred"
		rm -f "$LAB/state/ab.cred"
	done
	sleep 10
	ab status | grep -E "^  [a-z]+-ab "
	(cd "$train" && tar -cf - ./*.inp) | { s server "rm -rf /tmp/abtrain && mkdir -p /tmp/abtrain && tar -C /tmp/abtrain -xf -"; }
	(cd "$test" && tar -cf - ./*.inp) | { s server "rm -rf /tmp/abtest && mkdir -p /tmp/abtest && tar -C /tmp/abtest -xf -"; }
	# fresh copies (a leftover /tmp/train from an interrupted run made cp/mv nest them)
	s server "rm -rf /tmp/train /tmp/learned /tmp/order && cp -r /tmp/abtest /tmp/learned && cp -r /tmp/abtest /tmp/order && mv /tmp/abtrain /tmp/train"
	echo "### $(date +%T) training: $(s server 'ls /tmp/train | wc -l') inputs; test: $(s server 'ls /tmp/learned | wc -l') inputs"
	batch train order
	ab model | head -20
	batch learned learned
	batch order order
	echo "### $(date +%T) done"
	ab stats | sed -n '/CLIENT/,$p'
	t_stop
}

case ${1:-} in
run)
	exec 9>"$LAB/run/sched_ab.lock"
	flock -n 9 || { echo "another A/B is running"; exit 1; }
	t_run "${2:-}" "${3:-}" ;;
stop) t_stop ;;
*) sed -n '2,20p' "$0"; exit 2 ;;
esac
