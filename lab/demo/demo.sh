#!/usr/bin/env bash
# demo.sh - a realistic Matriline project on this computer, for the README screenshots:
# ~1200 results done, ~10 errors, ~5 results held in weird/ (from a lab-only cheating
# client), then 12 computers running 30 jobs with ~1700 inputs still queued.
# Separate server (port 44450, ~/matriline/demo); the real server is never touched.
#
#   lab/demo/demo.sh fast      phase A: 60 helpers compute the quick part (GFN2-xTB)
#   lab/demo/demo.sh slow      phase B: 12 helpers, 30 slots, PBEh-3c Opt Freq (minutes each)
#   lab/demo/demo.sh stop      stop every demo process
#
# Molecules: well-known public ones (caffeine, aspirin, vanillin ...), geometries made with
# RDKit into $MOL (lab/demo/README.md); never anyone's research molecules.
set -uo pipefail
export MATRILINE_NO_REGISTRY=1 # lab projects stay off the person's list (common/projects)
REPO=$(cd "$(dirname "$(readlink -f "$0")")/../.." && pwd)
W=$HOME/matriline/demo
MOL=${MOL:-$HOME/matriline/demo-tools/mol}
PORT=44450
B=$W/bin

ours() { [ -f "$1" ] && case $(readlink "/proc/$(cat "$1")/exe" 2>/dev/null) in "$W"/*) return 0 ;; esac; return 1; }
stopdir() { local f; for f in "$@"; do ours "$f" && kill "$(cat "$f")"; done; sleep 3; for f in "$@"; do ours "$f" && kill -9 "$(cat "$f")"; rm -f "$f"; done; return 0; }
stop() { stopdir "$W"/cli/*/run.pid "$W"/srv/run.pid "$W"/web.pid; }
start() { (cd "$1" && exec nohup "$2" run </dev/null >>"$1/out.log" 2>&1) & echo $! >"$1/run.pid"; }
srv() { (cd "$W/srv" && "$B/matriline-server" "$@"); }
setconf() { python3 - "$@" <<'EOF'
import sys,re
f,sec,key,val=sys.argv[1:]
s=open(f).read().split("\n"); cur=None
for i,l in enumerate(s):
    m=re.match(r"\[(\w+)\]",l)
    if m: cur=m.group(1); continue
    if cur==sec and re.match(r"%s\s*="%re.escape(key),l): s[i]="%s = %s"%(key,val); break
else: s.append("[%s]\n%s = %s"%(sec,key,val))
open(f,"w").write("\n".join(s))
EOF
}

# the projects (sub-directories, one card and one pie each) and their molecules; the
# sizes and the share done differ (user: "more variation in size and progress")
declare -A MOLS=(
	[physical-organic-tests]="phenol benzoic_acid acetic_acid pyridine benzene naphthalene indole salicylic_acid"
	[chemoinformatics]="caffeine aspirin paracetamol ibuprofen nicotine vanillin menthol thymol carvone limonene coumarin camphor glucose urea"
	[reaction-mechanisms]="acetone ethanol cinnamaldehyde citral carvone anethole acetic_acid"
	[nmr-spectra-modeling]="vanillin eugenol thymol anethole coumarin menthol camphor limonene glycine alanine serine"
	[drug-screening]="caffeine aspirin paracetamol ibuprofen nicotine serotonin dopamine adrenaline melatonin theobromine histamine lidocaine benzocaine tryptophan"
)
# done (quick part) and still to do (slow part) per project
declare -A DONE=([physical-organic-tests]=310 [chemoinformatics]=520 [reaction-mechanisms]=90 [nmr-spectra-modeling]=180 [drug-screening]=95)
declare -A TODO=([physical-organic-tests]=40 [chemoinformatics]=430 [reaction-mechanisms]=330 [nmr-spectra-modeling]=420 [drug-screening]=510)

inputs() { # method kind(DONE|TODO) suffix: each project's inputs, cycling over its molecules
	local method=$1 sfx=$3 p n i m c f
	for p in "${!MOLS[@]}"; do
		if [ "$2" = DONE ]; then n=${DONE[$p]}; else n=${TODO[$p]}; fi
		read -ra ms <<<"${MOLS[$p]}"
		mkdir -p "$W/new/$p"
		for ((i = 0; i < n; i++)); do
			m=${ms[$((i % ${#ms[@]}))]}
			c=$((i / ${#ms[@]} % 3 + 1))
			f=$MOL/${m}_c$c.xyz
			{ printf '%%maxcore 500\n! %s\n* xyz 0 1\n' "$method"; cat "$f"; echo '*'; } >"$W/new/$p/${m}_$sfx$(printf %04d "$i").inp"
		done
	done
}

client() { # name cores [cheat-mode]
	local d=$W/cli/$1
	srv keys issue "$1" "$W/$1.cred" >/dev/null || return 1
	"$B/matriline-client" init "$d" --credential "$W/$1.cred" --language en >/dev/null 2>&1 || return 1
	setconf "$d/client.conf" resources cores "$2"
	setconf "$d/client.conf" security updates false
	setconf "$d/client.conf" schedule pause_on_battery false
	case $1 in laptop-*) setconf "$d/client.conf" resources bandwidth_limit 200kbit ;; esac # a slow uplink
	mkdir -p "$d/state" && cp "$W"/fpcache/fpcache-* "$d/state/" 2>/dev/null
	if [ -n "${3:-}" ]; then
		(cd "$d" && MATRILINE_CHEAT=$3 exec nohup "$W/cheat/matriline-client" run </dev/null >>"$d/out.log" 2>&1) &
		echo $! >"$d/run.pid"
	else
		start "$d" "$B/matriline-client"
	fi
}

count() { find "$W/srv/$1" -name "*.inp" 2>/dev/null | wc -l; }

fast() {
	[ -d "$W" ] && { stop; rm -rf "${W:?}"; }
	mkdir -p "$B" "$W/cheat" "$W/cli" "$W/fpcache"
	local v="-X main.commit=$(git -C "$REPO" log -1 --format=%h --abbrev=12 -- src)"
	(cd "$REPO/src" && go build -ldflags "$v" -o "$B/matriline-server" ./server && go build -ldflags "$v" -o "$B/matriline-client" ./client &&
		go build -tags cheat -o "$W/cheat/matriline-client" ./client) || exit 1
	"$B/matriline-server" init "$W/srv" --language en >/dev/null || exit 1
	local C=$W/srv/server.conf
	setconf "$C" network listen "127.0.0.1:$PORT"
	setconf "$C" network advertise "127.0.0.1:$PORT"
	setconf "$C" network max_sessions 512
	setconf "$C" verify retry_weird false # the 5 forged results stay in weird/ for the picture
	setconf "$C" tasks order random # a mix of molecules running, not one after another
	start "$W/srv" "$B/matriline-server"
	for i in $(seq 30); do srv status >/dev/null 2>&1 && break; sleep 1; done
	# one client fingerprints ORCA; the others reuse its cache
	client warmup 1 && sleep 1 && (cd "$W/cli/warmup" && "$B/matriline-client" doctor >/dev/null 2>&1)
	cp "$W"/cli/warmup/state/fpcache-* "$W/fpcache/"
	stopdir "$W/cli/warmup/run.pid"
	for i in $(seq -w 1 60); do client "node-$i" 1; done
	client "untrusted-pc" 1 copy # returns another job's output: caught, held in weird/
	echo "phase A: 61 helpers"
	inputs "XTB2 Opt" DONE x
	for i in $(seq 1 10); do # inputs with a typo in the method: they end in errors/
		mkdir -p "$W/new/reaction-mechanisms"
		{ printf '! r2SCAN-3c Optt\n* xyz 0 1\n'; cat "$MOL/acetone_c1.xyz"; echo '*'; } >"$W/new/reaction-mechanisms/acetone_ts_guess$i.inp"
	done
	for d in "$W"/new/*/; do srv add "$d" >/dev/null; done # input/<category>/
	rm -rf "${W:?}/new"
	local t0=$SECONDS
	while [ "$(count weird)" -lt 5 ] && [ $((SECONDS - t0)) -lt 600 ]; do sleep 2; done
	stopdir "$W/cli/untrusted-pc/run.pid"
	echo "weird: $(count weird) (the cheating client is stopped)"
	while [ "$(count input)" -gt 0 ] && [ $((SECONDS - t0)) -lt 1800 ]; do sleep 5; done
	stopdir "$W"/cli/node-*/run.pid
	echo "phase A done: output $(find "$W/srv/output" -name '*.out' | wc -l), errors $(count errors), weird $(count weird)"
}

slow() {
	# 12 computers with 30 slots in all: what a small group could gather; the laptops on a
	# slow uplink (their results take a while to send), started a few at a time
	local names=(workstation:5 gaming-pc:5 lab-pc-01:2 lab-pc-02:2 lab-pc-03:2 lab-pc-04:2 lab-pc-05:2
		lab-pc-06:2 office-pc:2 old-desktop:2 laptop-claudia:2 laptop-luis:2) n k=0
	inputs "PBEh-3c Opt Freq" TODO p
	for d in "$W"/new/*/; do srv add "$d" >/dev/null; done
	rm -rf "${W:?}/new"
	for n in "${names[@]}"; do
		client "${n%:*}" "${n#*:}"
		k=$((k + 1))
		[ $((k % 3)) = 0 ] && sleep 40 # staggered: the times on the live page differ
	done
	echo "phase B: 12 computers, 30 slots; screenshots in ~10 min (web: matriline-server -c $W/srv/server.conf web)"
}

case ${1:-} in
fast) fast ;;
slow) slow ;;
stop) stop ;;
*) echo "usage: demo.sh fast | slow | stop" >&2; exit 2 ;;
esac
