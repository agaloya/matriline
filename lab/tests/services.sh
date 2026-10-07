#!/usr/bin/env bash
# services.sh - several instances on one Linux computer, with their services handled
# carelessly (user's request, docs/RELEASE_CHECKLIST.md section 3):
#   two servers (A, B), three clients (A1, A2 -> A; B1 -> B), two Nacomline projects
#   (N1, N2), all as systemd user services. Each service is installed twice, removed twice,
#   stopped and reinstalled, and run by hand while its service runs (must be refused).
#   Then every instance must answer for itself (status, console, web page), jobs queued on
#   each server and project end in their own folders, and after removing everything no
#   process is left. Folder: lab/images/services.
set -uo pipefail
export MATRILINE_NO_REGISTRY=1 # lab projects stay off the person's list (common/projects)
LAB=$(cd "$(dirname "$(readlink -f "$0")")/.." && pwd)
SRC=$LAB/../src NACO=$LAB/../../nacomline
D=$LAB/images/services
pass=0 fail=0
ok() { pass=$((pass + 1)); echo "PASS  $*"; }
bad() { fail=$((fail + 1)); echo "FAIL  $*"; }
check() { local what=$1; shift; if "$@" >/dev/null 2>&1; then ok "$what"; else bad "$what"; fi; }
mine() { for p in /proc/[0-9]*; do e=$(readlink "$p/exe" 2>/dev/null); [[ $e == "$D/bin/"* ]] && echo "${p#/proc/} $(tr '\0' ' ' <"$p/cmdline")"; done; }

# --- clean start
for u in $(ls ~/.config/systemd/user 2>/dev/null | grep -E '^(matriline-(server|client)|nacomline)-' | sed 's/\.service$//'); do
	grep -q "$D" ~/.config/systemd/user/$u.service && { systemctl --user disable --now "$u" >/dev/null 2>&1; rm -f ~/.config/systemd/user/$u.service; }
done
systemctl --user daemon-reload
mine | awk '{print $1}' | xargs -r kill
sleep 2
rm -rf "$D" && mkdir -p "$D/bin" "$D/in"
(cd "$SRC" && go build -o "$D/bin/matriline-server" ./server && go build -o "$D/bin/matriline-client" ./client) || exit 1
(cd "$NACO" && go build -o "$D/bin/nacomline" .) || exit 1
S() { "$D/bin/matriline-server" -c "$D/$1/server.conf" "${@:2}"; }
C() { "$D/bin/matriline-client" -c "$D/$1/client.conf" "${@:2}"; }
N() { "$D/bin/nacomline" -c "$D/$1/nacomline.conf" "${@:2}"; }
printf '! HF def2-SVP\n* xyz 0 1\nO 0 0 0\nH 0 0.757 0.587\nH 0 -0.757 0.587\n*\n' >"$D/in/water.inp"

# --- two servers, three clients, two Nacomline projects
port=44430
for s in A B; do
	port=$((port + 1))
	"$D/bin/matriline-server" init "$D/$s" >/dev/null 2>&1
	sed -i -e "s/^listen = .*/listen = 127.0.0.1:$port/" -e "s/^advertise =.*/advertise = 127.0.0.1:$port/" \
		-e 's/^scan_interval = .*/scan_interval = 2s/' -e 's/^settle_time = .*/settle_time = 0s/' -e 's/^enabled = true/enabled = false/' "$D/$s/server.conf"
done
for n in N1 N2; do "$D/bin/nacomline" init "$D/$n" >/dev/null || bad "nacomline init $n"; done

# --- servers: careless service handling
for s in A B; do
	check "server $s service install" S "$s" service install
	check "server $s service install again" S "$s" service install
	check "server $s service remove" S "$s" service remove
	S "$s" service remove >/dev/null 2>&1; echo "info  server $s remove again -> rc $? (should not crash)"
	check "server $s service install after remove" S "$s" service install
done
for s in A B; do
	for _ in $(seq 60); do S "$s" status >/dev/null 2>&1 && break; sleep 2; done
	check "server $s answers" S "$s" status
	out=$(cd "$D/$s" && timeout 20 "$D/bin/matriline-server" -c server.conf run 2>&1)
	[[ $out == *"already running"* || $out == *"lock"* ]] && ok "server $s: a second 'run' by hand is refused" || bad "server $s: second run: $out"
done
unitA=$(ls ~/.config/systemd/user | grep '^matriline-server-' | while read -r u; do grep -q "$D/A/" ~/.config/systemd/user/$u && echo "${u%.service}"; done)
systemctl --user stop "$unitA"; sleep 2
S A status >/dev/null 2>&1 && bad "server A still answers after systemctl stop" || ok "server A stopped by systemctl stop"
check "server A service install while stopped (starts it)" S A service install
for _ in $(seq 60); do S A status >/dev/null 2>&1 && break; sleep 2; done
check "server A answers again" S A status

# --- clients: A1, A2 on A; B1 on B
for c in A1 A2 B1; do
	s=${c:0:1}
	S "$s" keys issue "$c" "$D/$c.cred" >/dev/null || bad "keys issue $c"
	"$D/bin/matriline-client" init "$D/$c" --credential "$D/$c.cred" >/dev/null 2>&1 || bad "client init $c"
	sed -i 's/^cores = 0$/cores = 1/' "$D/$c/client.conf"
	check "client $c service install" C "$c" service install
	check "client $c service install again" C "$c" service install
	check "client $c service remove" C "$c" service remove
	check "client $c service install after remove" C "$c" service install
done
for c in A1 A2 B1; do
	for _ in $(seq 60); do C "$c" status | head -1 | grep -q "connected to" && break; sleep 5; done
	st=$(C "$c" status | head -1)
	want=$([[ $c == B1 ]] && echo 44432 || echo 44431)
	[[ $st == *"connected to 127.0.0.1:$want"* ]] && ok "client $c connected to its own server" || bad "client $c: $st"
done
nA=$(S A clients | grep -cE '^(A1|A2) ') nB=$(S B clients | grep -cE '^B1 ')
[[ $nA == 2 && $nB == 1 && $(S A clients | grep -c '^B1 ') == 0 ]] && ok "each server lists only its own clients" || bad "clients: A has $nA, B has $nB"

# --- Nacomline: two projects at once
for n in N1 N2; do
	check "nacomline $n service install" N "$n" service install
	check "nacomline $n service install again" N "$n" service install
done
for n in N1 N2; do for _ in $(seq 90); do N "$n" status 2>/dev/null | grep -q "this-computer" && break; sleep 2; done; done
for n in N1 N2; do N "$n" status 2>/dev/null | grep -q "this-computer" && ok "nacomline $n running" || bad "nacomline $n: $(N "$n" status 2>&1 | head -2)"; done

# --- jobs on every server and project end in their own folders
S A add "$D/in" input/a >/dev/null; S B add "$D/in" input/b >/dev/null
cp "$D/in/water.inp" "$D/N1/input/n1.inp"; cp "$D/in/water.inp" "$D/N2/input/n2.inp"
for _ in $(seq 120); do
	[[ -d $D/A/output/a/in/water && -d $D/B/output/b/in/water && -d $D/N1/output/n1 && -d $D/N2/output/n2 ]] && break
	sleep 3
done
for d in A/output/a/in/water B/output/b/in/water N1/output/n1 N2/output/n2; do [[ -d $D/$d ]] && ok "result in $d" || bad "no result in $d"; done
[[ ! -e $D/A/output/b && ! -e $D/B/output/a && ! -e $D/N1/output/n2 && ! -e $D/N2/output/n1 ]] && ok "no result in another instance's folder" || bad "results crossed instances"

# --- each console and web page talks to its own server
for s in A B; do
	out=$(printf 'status\nquit\n' | S "$s" console 2>&1)
	[[ $out == *"spool $D/$s"* ]] && ok "server $s console shows its own spool" || bad "server $s console: $(grep spool <<<"$out")"
done
for s in A B; do (cd "$D/$s" && exec "$D/bin/matriline-server" -c server.conf web >"$D/web-$s.log" 2>&1) & done
sleep 3
for s in A B; do
	l=$(grep -o 'http://[^ ]*login[^ ]*' "$D/web-$s.log")
	page=$(curl -s -c "$D/jar-$s" -b "$D/jar-$s" -L "$l")
	[[ $page == *"spool $D/$s"* ]] && ok "web page of server $s shows its own spool" || bad "web $s: $(head -c 300 "$D/web-$s.log")"
done

# --- remove everything; nothing may be left
for n in N1 N2; do check "nacomline $n service remove" N "$n" service remove; done
for c in A1 A2 B1; do check "client $c service remove" C "$c" service remove; done
for s in A B; do check "server $s service remove" S "$s" service remove; done
mine | grep ' web' | awk '{print $1}' | xargs -r kill
sleep 5
left=$(mine)
[[ -z $left ]] && ok "no process left" || bad "left: $left"
echo "### services (linux): $pass passed, $fail failed"
((fail == 0))
