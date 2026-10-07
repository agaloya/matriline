#!/usr/bin/env bash
# cmdmatrix.sh linux|win10|win11 - every server and client command on one platform, with its
# exit code checked (and some that must fail). A server and a client of the platform talk
# to each other on 127.0.0.1 of that platform; inputs: one that works, one ORCA rejects.
# Not covered here: console, edit, config edit (interactive), web (checked separately),
# clients approve and join (enrollment = register), bans lift (needs a real ban), accept
# and reject (need a result that failed a check; unit tests cover them).
# Prints one line per command: PASS/FAIL, exit code, what was run, first line of output.
set -uo pipefail
export MATRILINE_NO_REGISTRY=1 # lab projects stay off the person's list (common/projects)
LAB=$(cd "$(dirname "$(readlink -f "$0")")/.." && pwd)
SRC=$LAB/../src
P=${1:?usage: cmdmatrix.sh linux|win10|win11}
W="$LAB/windows/winvm.sh"
PORT=44404
pass=0 fail=0
case $P in
linux)
	D=$LAB/images/cmdmatrix
	(cd "$SRC" && go build -o "$D/bin/matriline-server" ./server && go build -o "$D/bin/matriline-client" ./client) || exit 1
	SRV=("$D/bin/matriline-server" -c "$D/srv/server.conf")
	CLI=("$D/bin/matriline-client" -c "$D/cli/client.conf")
	BASE=$D ;;
win10 | win11)
	D=$LAB/images/cmdmatrix-$P
	mkdir -p "$D"
	(cd "$SRC" && GOOS=windows go build -o "$D/matriline-server.exe" ./server && GOOS=windows go build -o "$D/matriline-client.exe" ./client) || exit 1
	sshport=$([[ $P == win10 ]] && echo 2241 || echo 2242)
	BASE='C:\cm' ;;
*) echo "usage: cmdmatrix.sh linux|win10|win11" >&2; exit 2 ;;
esac

# run <expect: ok|fail> <program: server|client> args... ; arguments are passed as they are
run() {
	local want=$1 prog=$2 out code
	shift 2
	if [[ $P == linux ]]; then
		if [[ $prog == server ]]; then out=$("${SRV[@]}" "$@" 2>&1); else out=$("${CLI[@]}" "$@" 2>&1); fi
		code=$?
	else
		local exe conf q="" a
		if [[ $prog == server ]]; then exe='C:\cm\matriline-server.exe' conf='C:\cm\srv\server.conf'; else exe='C:\cm\matriline-client.exe' conf='C:\cm\cli\client.conf'; fi
		for a in "$@"; do q+=" '${a//\'/\'\'}'"; done
		out=$(timeout 300 "$W" ssh "$P" "& '$exe' -c '$conf'$q 2>&1 | Out-String -Width 300; exit \$LASTEXITCODE" 2>&1)
		code=$?
	fi
	local first=${out%%$'\n'*}
	if { [[ $want == ok ]] && ((code == 0)); } || { [[ $want == fail ]] && ((code != 0)); }; then
		pass=$((pass + 1)); printf 'PASS  %3d  %-6s %-40s %.90s\n' "$code" "$prog" "$*" "$first"
	else
		fail=$((fail + 1)); printf 'FAIL  %3d  %-6s %-40s %.200s\n' "$code" "$prog" "$*" "$(tr '\n' ' ' <<<"$out")"
	fi
}
sh_() { # a platform shell command (setup), not counted
	if [[ $P == linux ]]; then bash -c "$1"; else timeout 300 "$W" ssh "$P" "$1"; fi
}
waitfor() { # waitfor <seconds> <server status pattern>
	local end=$(($(date +%s) + $1))
	until run_quiet status | grep -q "$2"; do (($(date +%s) > end)) && return 1; sleep 5; done
}
run_quiet() {
	if [[ $P == linux ]]; then "${SRV[@]}" "$@" 2>&1; else timeout 120 "$W" ssh "$P" "& 'C:\\cm\\matriline-server.exe' -c 'C:\\cm\\srv\\server.conf' $* 2>&1 | Out-String -Width 300"; fi
}

# --- setup: fresh folders, inputs, the server started in the background
GOOD='! HF def2-SVP
* xyz 0 1
O 0 0 0
H 0 0.757 0.587
H 0 -0.757 0.587
*'
BAD='! NOTAKEYWORD def2-SVP
* xyz 0 1
H 0 0 0
H 0 0 0.74
*'
if [[ $P == linux ]]; then
	# leftovers of an interrupted run hold the port (matched by path: not this script's own command line)
	for p in /proc/[0-9]*; do [[ $(readlink "$p/exe" 2>/dev/null) == "$D/bin/matriline-"* ]] && kill "${p#/proc/}"; done
	sleep 1
	rm -rf "$D/srv" "$D/cli" "$D/in" "$D/backup" "$D/x.cred" "$D/y.cred"
	mkdir -p "$D/in" "$D/backup"
	printf '%s\n' "$GOOD" >"$D/in/water.inp"
	printf '%s\n' "$BAD" >"$D/in/bad.inp"
	"$D/bin/matriline-server" init "$D/srv" >/dev/null
	sed -i -e "s/^listen = .*/listen = 127.0.0.1:$PORT/" -e "s/^advertise =.*/advertise = 127.0.0.1:$PORT/" \
		-e 's/^scan_interval = .*/scan_interval = 1s/' -e 's/^settle_time = .*/settle_time = 0s/' -e 's/^enabled = true/enabled = false/' \
		-e 's/^orca_error_hosts = .*/orca_error_hosts = 1/' "$D/srv/server.conf"
	(cd "$D/srv" && exec "$D/bin/matriline-server" -c server.conf run >run.log 2>&1) &
	INPUTS=$D/in BACKUP=$D/backup CRED=$D/x.cred CRED2=$D/y.cred
else
	sh_ "Get-ScheduledTask -TaskName 'matriline-*' | Where-Object { \$_.Actions.Arguments -like '*C:\\cm\\*' } | ForEach-Object { Unregister-ScheduledTask -TaskName \$_.TaskName -Confirm:\$false }; Get-CimInstance Win32_Process | Where-Object { \$_.ExecutablePath -like 'C:\\cm\\*' } | ForEach-Object { Stop-Process -Id \$_.ProcessId -Force }; Start-Sleep 1; Remove-Item -Recurse -Force C:\\cm -ErrorAction SilentlyContinue; New-Item -ItemType Directory -Force C:\\cm\\in, C:\\cm\\backup | Out-Null" >/dev/null 2>&1
	scp -q -i "$LAB/keys/lab_ed25519" -P "$sshport" -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null \
		"$D/matriline-server.exe" "$D/matriline-client.exe" matriline@127.0.0.1:C:/cm/
	printf '%s\n' "$GOOD" | sh_ '[Console]::In.ReadToEnd() | Set-Content -Encoding ascii C:\cm\in\water.inp'
	printf '%s\n' "$BAD" | sh_ '[Console]::In.ReadToEnd() | Set-Content -Encoding ascii C:\cm\in\bad.inp'
	sh_ "C:\\cm\\matriline-server.exe init C:\\cm\\srv | Out-Null; \$f='C:\\cm\\srv\\server.conf'; (Get-Content \$f) -replace '^listen = .*','listen = 127.0.0.1:$PORT' -replace '^advertise =.*','advertise = 127.0.0.1:$PORT' -replace '^scan_interval = .*','scan_interval = 1s' -replace '^settle_time = .*','settle_time = 0s' -replace '^enabled = true','enabled = false' -replace '^orca_error_hosts = .*','orca_error_hosts = 1' | Set-Content -Encoding ascii \$f; Invoke-CimMethod -ClassName Win32_Process -MethodName Create -Arguments @{CommandLine='C:\\cm\\matriline-server.exe -c C:\\cm\\srv\\server.conf run'; CurrentDirectory='C:\\cm\\srv'} | Out-Null" >/dev/null
	INPUTS='C:\cm\in' BACKUP='C:\cm\backup' CRED='C:\cm\x.cred' CRED2='C:\cm\y.cred'
fi
for _ in $(seq 60); do run_quiet version >/dev/null 2>&1 && run_quiet status 2>/dev/null | grep -q "^server " && break; sleep 3; done

echo "### $P: server commands"
run ok server version
run ok server doctor
run ok server status
run ok server config show
run ok server config validate
run ok server config reload
run ok server keys issue x "$CRED"
run ok server keys issue y "$CRED2"
run fail server keys issue x "$CRED"   # the same name twice
run ok server keys token 1
run ok server keys revoke y
run ok server clients list
run ok server block 192.0.2.1
run ok server bans
run ok server unblock 192.0.2.1
run fail server alerts test                 # no e-mail and no alert program configured
run ok server add "$INPUTS" input/m
run ok server status input/m
run ok server priority input/m/in/water.inp 5
run ok server next input/m/in/water.inp
run ok server pause input/m/in/bad.inp
run ok server resume paused/m/in/bad.inp
run ok server cancel input/m/in/bad.inp
run ok server uncancel cancelled/m/in/bad.inp
run ok server history m/in/water.inp
run ok server rescan
run fail server redo output/does/not/exist
run fail server accept weird/does/not/exist

echo "### $P: client commands"
if [[ $P == linux ]]; then
	"$D/bin/matriline-client" init "$D/cli" --credential "$CRED" >/dev/null 2>&1
	sed -i 's/^cores = 0$/cores = 1/' "$D/cli/client.conf"
else
	sh_ "C:\\cm\\matriline-client.exe init C:\\cm\\cli --credential $CRED | Out-Null; \$f='C:\\cm\\cli\\client.conf'; (Get-Content \$f) -replace '^cores = 0\$','cores = 1' | Set-Content -Encoding ascii \$f" >/dev/null
fi
run ok client version
run ok client doctor
run ok client status
run ok client pause 2m
run ok client status
run ok client resume
run ok client service install
waitfor 600 "output 1 " || echo "(the good input did not finish in 10 min)"
waitfor 300 "errors 1 " || echo "(the bad input did not reach errors/ in 5 min)"
run ok client status
run ok client service remove

echo "### $P: server commands on results"
run ok server stats
run ok server model
run ok server review
run ok server reverify output/m/in/water
run ok server retry errors/m/in/bad
run ok server redo output/m/in/water
run ok server clean --dry-run '*.tmp'
run ok server check
run ok server events 20
run ok server verify
run ok server clients drain x
run ok server clients release x
run ok server clients quarantine x
run ok server clients release x
run ok server backup "$BACKUP"
run ok server clients disable x test
run ok server clients enable x
run ok client enable                         # nothing to undo here (it was offline): still rc 0
run ok server stop
sleep 5
run fail server status                      # stopped: nothing answers
if [[ $P == linux ]]; then b=$(ls -d "$D"/backup/matriline-backup-* | head -1); else b=$(sh_ '(Get-ChildItem C:\cm\backup | Select -First 1).FullName' | tr -d '\r'); fi
run ok server restore "$b"                  # the server is stopped
echo "### $P: $pass passed, $fail failed"
((fail == 0))
