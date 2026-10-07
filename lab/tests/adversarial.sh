#!/usr/bin/env bash
# adversarial.sh - integrity attacks against a separate test server (D37, D44).
#
#   tests/adversarial.sh run <attack> [inputs-dir]   one round: fresh test server on :44200,
#                                                    honest clients (server VM, ubuntu) and a
#                                                    cheating client on artix
#   tests/adversarial.sh report                      verdicts of the last round
#   tests/adversarial.sh stop                        stop the test server and its clients
#
# Attacks (client/cheat_on.go, built with -tags cheat): energy loose copy forge hess, collude
# (two cheaters, artix and void, forging every energy consistently), or "none"
# (control round: the "cheater" is honest, so any detection is a false positive), or slander
# (honest results from the server VM; artix lies on the checks it gets, so ubuntu and void
# must settle the disputes: tests verify.rescue_weird), or dodge (artix lies on every task
# that does not look like the server's own: a first honest batch provides verified
# canary sources, then artix alone computes a second batch; the canaries must catch it).
# Every verification layer is enabled at 100 % so each one can be credited separately.
# The main lab deployment (port 44100) is not touched; each process uses one core that
# its VM has to spare.
set -uo pipefail
export MATRILINE_NO_REGISTRY=1 # lab projects stay off the person's list (common/projects)
LAB=$(cd "$(dirname "$(readlink -f "$0")")/.." && pwd)
L=$LAB/labctl
SHARE=/opt/matriline/bin # live host build output (src/bin), read-only
# Processes run from a private copy: a rebuild on the host replaced the shared binary
# under a starting client, which crashed with "invalid runtime symbol table".
# install(1) writes a new file, so copies never change under a running process.
# Its own binary directory: syncing into ~/ml/bin replaced the binaries of the main lab
# deployment (matriline.sh) under its running processes; a crash or reboot would then have
# brought them back on another build unnoticed (found in a user's review).
BIN='~/ml/advbin'
syncbin() { "$L" ssh "$1" "mkdir -p ~/ml/advbin && for f in $SHARE/*; do install -m 755 \$f ~/ml/advbin/; done"; }
PORT=44200
HONEST=(server ubuntu)
CHEATERS=(artix)      # "collude" adds a second cheater on void

s() { "$L" ssh "$@"; }
asrv() { s server "cd ~/ml/adv && $BIN/matriline-server $*"; }
# also stops a client whose directory was already deleted (its run.pid went with it): one
# such orphan kept retrying an old credential for hours (seen 2026-10-04)
stop_dir() { s "$1" "[ -f $2/run.pid ] && kill \$(cat $2/run.pid) 2>/dev/null; rm -f $2/run.pid; for p in \$(pgrep -f 'matriline-client'); do case \$(readlink /proc/\$p/cwd) in */ml/advcli*|*/ml/advcheat*) kill \$p;; esac; done" || true; }

small_inputs() { # dir: quick jobs covering SP, Opt, Opt+Freq, DFT/RIJCOSX and HF
	local d=$1
	mkdir -p "$d"
	mk() { printf '! %s\n* xyz 0 1\n%b*\n' "$2" "$3" >"$d/$1.inp"; }
	mk water_sp "B3LYP def2-SVP" "O 0 0 0\nH 0 0.757 0.587\nH 0 -0.757 0.587\n"
	mk water_opt "B3LYP def2-SVP Opt" "O 0 0 0.1\nH 0 0.78 0.58\nH 0 -0.78 0.58\n"
	mk nh3_optfreq "HF def2-SVP Opt Freq" "N 0 0 0.1\nH 0 0.94 -0.27\nH 0.81 -0.47 -0.27\nH -0.81 -0.47 -0.27\n"
	mk ch4_opt "PBE def2-SVP Opt" "C 0 0 0\nH 0.63 0.63 0.63\nH -0.63 -0.63 0.63\nH -0.63 0.63 -0.63\nH 0.63 -0.63 -0.63\n"
	mk hf_sp "HF def2-SVP" "F 0 0 0\nH 0 0 0.92\n"
	mk co_optfreq "B3LYP def2-SVP Opt Freq" "C 0 0 0\nO 0 0 1.13\n"
	mk n2_mp2 "MP2 def2-SVP" "N 0 0 0\nN 0 0 1.10\n"
	mk n2_mp2_opt "MP2 def2-SVP Opt" "N 0 0 0\nN 0 0 1.13\n"
	mk hcn_optfreq "PBE def2-SVP Opt Freq" "H 0 0 -1.07\nC 0 0 0\nN 0 0 1.16\n"
	mk ch2o_opt "B3LYP def2-SVP Opt" "C 0 0 0\nO 0 0 1.21\nH 0 0.94 -0.59\nH 0 -0.94 -0.59\n"
	mk h2o2_sp "B3LYP def2-SVP" "O 0 0.73 -0.06\nO 0 -0.73 -0.06\nH 0.8 0.9 0.5\nH -0.8 -0.9 0.5\n"
	mk c2h4_optfreq "HF def2-SVP Opt Freq" "C 0 0 0.67\nC 0 0 -0.67\nH 0 0.92 1.24\nH 0 -0.92 1.24\nH 0 0.92 -1.24\nH 0 -0.92 -1.24\n"
	mk lif_sp "B3LYP def2-SVP" "Li 0 0 0\nF 0 0 1.56\n"
}

t_stop() {
	local n
	for n in server ubuntu void; do stop_dir "$n" "~/ml/advcli"; done
	stop_dir artix "~/ml/advcheat"
	stop_dir void "~/ml/advcheat"
	asrv stop >/dev/null 2>&1 || stop_dir server "~/ml/adv"
}

t_run() {
	local attack=${1:?attack} inputs=${2:-} spub n
	t_stop
	"$L" forward server 44100,443,$PORT >/dev/null 2>&1
	spub=$("$L" addr server)
	syncbin server
	s server "rm -rf ~/ml/adv && cd ~/ml && $BIN/matriline-server init adv >/dev/null && cd adv && sed -i \
		-e 's/^listen = .*/listen = :$PORT/' -e 's/^advertise =.*/advertise = $spub:$PORT/' \
		-e 's/^scan_interval = .*/scan_interval = 1s/' -e 's/^settle_time = .*/settle_time = 0s/' \
		-e 's/^output_consistency = .*/output_consistency = true/' \
		-e 's/^timing_plausibility = .*/timing_plausibility = true/' \
		-e 's/^scf_recheck_fraction = .*/scf_recheck_fraction = 1/' \
		-e 's/^gradient_check_fraction = .*/gradient_check_fraction = 1/' \
		-e 's/^hessian_probe_fraction = .*/hessian_probe_fraction = 1/' \
		-e 's/^canary_rate = .*/canary_rate = 0.5/' \
		-e 's/^replication_rate = .*/replication_rate = 1/' \
		-e 's/^duplicate_when_idle = .*/duplicate_when_idle = false/' server.conf \
		&& (nohup $BIN/matriline-server run >> run.log 2>&1 & echo \$! > run.pid)"
	for _ in $(seq 120); do s server "grep -q ' ready, spool' ~/ml/adv/run.log" && break; sleep 1; done
	start_client() {
		local n=$1 dir=advcli bin=matriline-client env="" addr=$spub:$PORT
		if [[ " ${CHEATERS[*]} " == *" $n "* ]]; then
			# colluders both run the energy forgery on EVERYTHING they report, including
			# the checks they are given: two cheaters then agree with each other
			local mode=$attack; [[ $attack == collude ]] && mode=energy
			dir=advcheat bin=matriline-client-cheat env="MATRILINE_CHEAT=$mode"
		fi
		[[ $n == server ]] && addr=127.0.0.1:$PORT
		syncbin "$n"
		asrv "keys issue $n-adv /tmp/$n-adv.cred $addr" >/dev/null
		s server "cat /tmp/$n-adv.cred && rm /tmp/$n-adv.cred" >"$LAB/state/adv.cred"
		s "$n" "rm -rf ~/ml/$dir && mkdir -p ~/ml && cd ~/ml && $BIN/matriline-client init $dir >/dev/null && cat > $dir/credential.conf && cd $dir && sed -i -e 's/^disk_reserve = .*/disk_reserve = 1GiB/' -e 's/^cores = 1/' client.conf && (env $env nohup $BIN/$bin run >> run.log 2>&1 & echo \$! > run.pid)" <"$LAB/state/adv.cred"
		rm -f "$LAB/state/adv.cred"
	}
	# the cheater alone first, so it produces every real result; the honest hosts join
	# afterwards and run the verification sub-tasks (which never go to the producer)
	[[ $attack == collude ]] && CHEATERS=(artix void)
	local first=("${CHEATERS[@]}") later=("${HONEST[@]}")
	if [[ $attack == slander || $attack == dodge ]]; then
		# the honest producer first, then the slanderer and two honest hosts for the
		# checks, the confirmations and the tie-breaks
		HONEST=(ubuntu void) first=(server) later=(artix)
	fi
	for n in "${first[@]}"; do start_client "$n"; done
	local tmp; tmp=$(mktemp -d)
	if [[ -n $inputs ]]; then cp -r "$inputs"/. "$tmp"; else small_inputs "$tmp"; fi
	s server "rm -rf /tmp/advin && mkdir -p /tmp/advin"
	tar -C "$tmp" -cf - . | s server "tar -C /tmp/advin -xf -"
	rm -rf "$tmp"
	sleep 5
	asrv "add /tmp/advin input/$attack" >/dev/null
	echo "### round '$attack' started $(date +%H:%M:%S)"
	local total; total=$(s server "ls /tmp/advin/*.inp | wc -l")
	for _ in $(seq 360); do
		(($(asrv stats 2>/dev/null | awk -v re="^($(IFS='|'; echo "${first[*]}"))-adv\$" '$1 ~ re {n += $2+$3+$4} END {print n+0}') >= total)) && break
		sleep 5
	done
	echo "### first producers delivered $total results $(date +%H:%M:%S); verifiers join"
	# slander: the three verifiers start together (all must be registered, or a failed
	# check has no independent host to confirm it and is condemned unconfirmed)
	[[ $attack == slander ]] && later=(artix "${HONEST[@]}")
	[[ $attack == dodge ]] && later=("${HONEST[@]}")
	for n in "${later[@]}"; do start_client "$n"; done
	# done when no task (real or internal) is left
	for _ in $(seq 360); do
		# "still queued" counts internal verification tasks too
		# lingering duplicate attempts do not matter once every task and check is done
		[[ $(asrv stats 2>/dev/null | awk '/still queued/{print $NF}') == 0 ]] && break
		sleep 10
	done
	if [[ $attack == dodge ]]; then
		echo "### honest batch done $(date +%H:%M:%S); artix alone computes the second batch"
		for n in server ubuntu void; do stop_dir "$n" "~/ml/advcli"; done
		start_client artix
		sleep 5
		asrv "add /tmp/advin input/dodge2" >/dev/null
		for _ in $(seq 360); do
			(($(asrv stats 2>/dev/null | awk '$1 == "artix-adv" {n += $2+$3+$4} END {print n+0}') >= total)) && break
			sleep 5
		done
		# restart the existing honest clients (their one-time credentials are used up)
		for n in ubuntu void; do
			s "$n" "cd ~/ml/advcli && (nohup $BIN/matriline-client run >> run.log 2>&1 & echo \$! > run.pid)"
		done
		for _ in $(seq 360); do
			[[ $(asrv stats 2>/dev/null | awk '/still queued/{print $NF}') == 0 ]] && break
			sleep 10
		done
	fi
	echo "### round '$attack' finished $(date +%H:%M:%S)"
	t_report
}

t_report() {
	s server "cd ~/ml/adv && echo '--- results per client (accepted / weird / errors)' && $BIN/matriline-server stats | sed -n '/CLIENT/,\$p'
		echo '--- checks that failed (who produced the result, which layer)'
		grep -E 'FAILED verification|verification .* fail|canary .* fail|confirm' run.log | cut -c27-260
		echo '--- checks run (count by layer)'
		grep -oE 'assigned ~check/[0-9a-f]+/[a-z_]+' run.log | sed 's/.*\///' | sort | uniq -c
		echo '--- weird/'; find weird -name 'matriline.verdict' -exec sh -c 'echo \"\$1:\"; cat \"\$1\" | head -5' _ {} \;"
}

case ${1:-} in
run)
	# one round at a time: two overlapping rounds share ~/ml/adv and mix their results
	exec 9>"$LAB/run/adversarial.lock"
	flock -n 9 || { echo "another adversarial round is running"; exit 1; }
	t_run "${2:-}" "${3:-}" ;;
report) t_report ;;
stop) t_stop ;;
*) sed -n '2,14p' "$0"; exit 2 ;;
esac
