#!/usr/bin/env bash
# winadversarial.sh <attack> [vm] - the integrity attacks of adversarial.sh with the cheater(s)
# on a Windows VM (lab/windows/winvm.sh): a fresh test server on this host (127.0.0.1:44395,
# every verification layer at 100 % as in adversarial.sh), the cheating Windows client
# (client built with -tags cheat, MATRILINE_CHEAT=<attack>) computes every input first,
# then three honest Linux clients on this host join and run the checks. Attacks: energy loose
# copy forge hess, collude (two cheaters, on win11 and win10, forging every energy the
# same way), slander (an honest Linux producer; the Windows client lies on the checks it
# gets), dodge (honest only on tasks that look like the server's checks: an honest first
# batch gives verified canary sources, then the cheater alone computes a second batch and
# the canaries must catch it), or none (control: an honest "cheater", any detection is a false positive). The verdicts are printed at the end (review, clients, stats).
set -uo pipefail
export MATRILINE_NO_REGISTRY=1 # lab projects stay off the person's list (common/projects)
LAB=$(cd "$(dirname "$(readlink -f "$0")")/.." && pwd)
attack=${1:?usage: winadversarial.sh <attack> [vm]} vm=${2:-win11}
W="$LAB/windows/winvm.sh"
SRC=$LAB/../src
D=$LAB/images/winadv
PORT=44395
log() { echo "$(date +%H:%M:%S) $attack $*"; }
vmssh() { timeout 120 "$W" ssh "$vm" "$@"; }
srv() { "$D/bin/matriline-server" -c "$D/srv/server.conf" "$@"; }

# previous round: stop its server, host clients and the Windows cheater
srv stop >/dev/null 2>&1
# any client still running in this test's folders (found by working directory: a pid file
# once held the pid of a subshell, and 17 old clients kept retrying until the server banned
# 127.0.0.1, the address every lab client comes from)
for p in $(ps -eo pid,args | awk '$2 ~ /matriline-client$/ {print $1}'); do # (pgrep -x cannot: names are cut at 15 characters)
	case $(readlink "/proc/$p/cwd") in "$D"/*) kill "$p" ;; esac
done
for v in win10 win11; do
	timeout 60 "$W" ssh "$v" 'Get-CimInstance Win32_Process | Where-Object { $_.CommandLine -like "*mladv*" } | ForEach-Object { Stop-Process -Id $_.ProcessId -Force }' >/dev/null 2>&1
done
sleep 3
# the previous round's folder is kept as winadv-<attack> (results, logs) for inspection
if [[ -f $D/attack ]]; then rm -rf "$D-$(cat "$D/attack")"; mv "$D" "$D-$(cat "$D/attack")"; fi
rm -rf "$D" && mkdir -p "$D/bin" "$D/in" && echo "$attack" >"$D/attack"
# private copies of the binaries: a rebuild must not replace them under a running process
(cd "$SRC" && go build -o "$D/bin/matriline-server" ./server && go build -o "$D/bin/matriline-client" ./client &&
	GOOS=windows go build -tags cheat -o "$D/bin/cheat.exe" ./client) || exit 1

"$D/bin/matriline-server" init "$D/srv" >/dev/null
sed -i -e "s/^listen = .*/listen = 127.0.0.1:$PORT/" -e "s/^advertise =.*/advertise = 10.0.2.2:$PORT/" \
	-e 's/^scan_interval = .*/scan_interval = 1s/' -e 's/^settle_time = .*/settle_time = 0s/' \
	-e 's/^output_consistency = .*/output_consistency = true/' -e 's/^timing_plausibility = .*/timing_plausibility = true/' \
	-e 's/^scf_recheck_fraction = .*/scf_recheck_fraction = 1/' -e 's/^gradient_check_fraction = .*/gradient_check_fraction = 1/' \
	-e 's/^hessian_probe_fraction = .*/hessian_probe_fraction = 1/' -e 's/^canary_rate = .*/canary_rate = 0.5/' \
	-e 's/^replication_rate = .*/replication_rate = 1/' -e 's/^duplicate_when_idle = .*/duplicate_when_idle = false/' \
	-e "s|^accepted_fingerprints =.*|accepted_fingerprints = builtin, $LAB/images/wintest/orca-win.json|" \
	-e 's/^ban_auth_after = .*/ban_auth_after = 0/' -e 's/^ban_garbage_after = .*/ban_garbage_after = 0/' "$D/srv/server.conf"
# (no bans: every lab client reaches this server from 127.0.0.1)
(cd "$D/srv" && nohup "$D/bin/matriline-server" -c server.conf run >run.log 2>&1 & echo $! >"$D/srv/run.pid")
for _ in $(seq 120); do grep -q ' ready, spool' "$D/srv/run.log" 2>/dev/null && break; sleep 1; done

# a cheater on a Windows VM: its own folder, started through WMI so that it outlives the
# SSH session (an SSH session ends the processes it started on Windows)
start_cheater() { # vm name mode
	local cvm=$1 name=$2 mode=$3 port
	port=$([[ $cvm == win10 ]] && echo 2241 || echo 2242)
	srv keys issue "$name" "$D/$name.cred" >/dev/null
	timeout 120 "$W" ssh "$cvm" 'Remove-Item -Recurse -Force C:\mladv -ErrorAction SilentlyContinue; New-Item -ItemType Directory -Force C:\mladv | Out-Null' >/dev/null
	scp -q -i "$LAB/keys/lab_ed25519" -P "$port" -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null \
		"$D/bin/cheat.exe" "$D/$name.cred" matriline@127.0.0.1:C:/mladv/ || { log "copying the cheater to $cvm failed"; exit 1; }
	timeout 120 "$W" ssh "$cvm" "C:\\mladv\\cheat.exe init C:\\mladv\\cli --credential C:\\mladv\\$name.cred | Out-Null; Invoke-CimMethod -ClassName Win32_Process -MethodName Create -Arguments @{CommandLine='cmd.exe /c set MATRILINE_CHEAT=$mode&& C:\\mladv\\cheat.exe -c C:\\mladv\\cli\\client.conf run'} | Select -ExpandProperty ReturnValue" | grep -q '^0' ||
		{ log "the cheater on $cvm did not start"; exit 1; }
	log "cheater $name ($mode) started on $cvm"
}
# an honest Linux client on this host
start_honest() { # i
	srv keys issue "honest$1" "$D/h$1.cred" 127.0.0.1:$PORT >/dev/null
	"$D/bin/matriline-client" init "$D/cli$1" --credential "$D/h$1.cred" >/dev/null
	sed -i 's/^cores = 0$/cores = 1/' "$D/cli$1/client.conf"
	(cd "$D/cli$1" && exec "$D/bin/matriline-client" -c client.conf run >run.log 2>&1) &
	echo $! >"$D/cli$1/run.pid"
	log "honest$1 started"
}
# an honest client that enrolls now and goes offline: registered, as real helpers are,
# but not connected while the cheaters compute (restart_honest brings it back)
enroll_honest() { # i
	start_honest "$1"
	for _ in $(seq 60); do grep -q "client honest$1 (.*) connected" "$D/srv/run.log" && break; sleep 2; done
	kill "$(cat "$D/cli$1/run.pid")" 2>/dev/null
	log "honest$1 enrolled and stopped"
}
restart_honest() { # i
	(cd "$D/cli$1" && exec "$D/bin/matriline-client" -c client.conf run >>run.log 2>&1) &
	echo $! >"$D/cli$1/run.pid"
	log "honest$1 back"
}
# who computes first: the cheater(s), or for slander the honest producer (the slanderer
# then lies on the checks of honest results); the others join when the first are done
case $attack in
collude) # two cheaters forging every energy the same way, approving each other, while
	# the honest helpers are registered but offline
	enroll_honest 1; enroll_honest 2; enroll_honest 3
	start_cheater win11 cheat1 energy
	start_cheater win10 cheat2 energy
	later="r1 r2 r3" ;;
slander) # the slanderer connects first (its first start fingerprints ORCA for a minute;
	# it arrived after the checks were done) and waits paused while honest1 produces
	start_cheater "$vm" win-slander slander
	for _ in $(seq 120); do grep -q "client win-slander (.*) connected" "$D/srv/run.log" && break; sleep 2; done
	timeout 60 "$W" ssh "$vm" 'C:\mladv\cheat.exe -c C:\mladv\cli\client.conf pause' >/dev/null
	start_honest 1; later="resume 2 3" ;;
dodge) # an honest first batch makes verified canary sources; the cheater then computes a
	# second batch alone, honest only on what looks like the server's own checks
	start_honest 1; start_honest 2; start_honest 3; later="" ;;
*) start_cheater "$vm" win-cheat "$attack"; later="1 2 3" ;;
esac

# the inputs of adversarial.sh (quick jobs: SP, Opt, Opt+Freq, DFT, HF, MP2)
mk() { printf '! %s\n* xyz 0 1\n%b*\n' "$2" "$3" >"$D/in/$1.inp"; }
mk water_sp "B3LYP def2-SVP" "O 0 0 0\nH 0 0.757 0.587\nH 0 -0.757 0.587\n"
mk water_opt "B3LYP def2-SVP Opt" "O 0 0 0.1\nH 0 0.78 0.58\nH 0 -0.78 0.58\n"
mk nh3_optfreq "HF def2-SVP Opt Freq" "N 0 0 0.1\nH 0 0.94 -0.27\nH 0.81 -0.47 -0.27\nH -0.81 -0.47 -0.27\n"
mk hf_sp "HF def2-SVP" "F 0 0 0\nH 0 0 0.92\n"
mk co_optfreq "B3LYP def2-SVP Opt Freq" "C 0 0 0\nO 0 0 1.13\n"
mk n2_mp2 "MP2 def2-SVP" "N 0 0 0\nN 0 0 1.10\n"
mk ch2o_opt "B3LYP def2-SVP Opt" "C 0 0 0\nO 0 0 1.21\nH 0 0.94 -0.59\nH 0 -0.94 -0.59\n"
mk lif_sp "B3LYP def2-SVP" "Li 0 0 0\nF 0 0 1.56\n"
sleep 20 # the first clients connect (fingerprinting ORCA is cached from earlier runs)
srv add "$D/in" "input/$attack" >/dev/null
log "round started; the first clients compute everything"
status() { srv status 2>/dev/null; }
wait_for() { local end=$(($(date +%s) + $1)); shift; until "$@"; do (($(date +%s) > end)) && return 1; sleep 5; done; }
# the cheater has produced all it can when nothing runs for 30 s (what stays queued waits for
# other hosts: verification sub-tasks and canaries never go to the producer)
idle() { local s; s=$(status); grep -q "> running 0 " <<<"$s" && grep -qE "# output [1-9]|weird [1-9]" <<<"$s"; }
produced() { idle && sleep 30 && idle; }
wait_for 1800 produced || log "the cheater did not finish in 30 min"

# the others join: three honest Linux clients (a second opinion needs a host other than the
# producer and the first verifier), and for slander the slandering Windows verifier
for c in $later; do
	case $c in
	resume) timeout 60 "$W" ssh "$vm" 'C:\mladv\cheat.exe -c C:\mladv\cli\client.conf resume' >/dev/null; log "slanderer resumed" ;;
	r*) restart_honest "${c#r}" ;;
	*) start_honest "$c" ;;
	esac
done
drained() { local s; s=$(status); grep -q "queued 0 > running 0" <<<"$s" && grep -q " 0 verification" <<<"$s"; }
wait_for 3600 drained || log "not drained after 1 h"
if [[ $attack == dodge ]]; then
	log "honest batch done; the Windows cheater alone computes the second batch"
	for i in 1 2 3; do kill "$(cat "$D/cli$i/run.pid")" 2>/dev/null; done
	start_cheater "$vm" win-dodge dodge
	sleep 20
	srv add "$D/in" input/dodge2 >/dev/null
	wait_for 1800 produced || log "the cheater did not finish in 30 min"
	for i in 1 2 3; do # back for the checks (their credentials are used up: just restart)
		(cd "$D/cli$i" && exec "$D/bin/matriline-client" -c client.conf run >>run.log 2>&1) &
		echo $! >"$D/cli$i/run.pid"
	done
	wait_for 3600 drained || log "not drained after 1 h"
fi
echo "### round '$attack' (cheaters on Windows) $(date +%H:%M:%S)"
status | head -6
srv clients list
srv review | grep -E '^==|^reason' | head -40
