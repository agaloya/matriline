#!/usr/bin/env bash
# services-win.sh [win10|win11] - services.sh on a Windows VM: two servers (A, B), three
# clients (A1, A2 -> A; B1 -> B) and two Nacomline projects (N1, N2) on one computer, as
# scheduled tasks handled carelessly (installed twice, removed twice, ended and installed
# again, run by hand while running); each answers for itself (status, console, web page on
# its own port), jobs end in their own folders, nothing is left after removing everything.
set -uo pipefail
export MATRILINE_NO_REGISTRY=1 # lab projects stay off the person's list (common/projects)
LAB=$(cd "$(dirname "$(readlink -f "$0")")/.." && pwd)
SRC=$LAB/../src NACO=$LAB/../../nacomline
VM=${1:-win11}
W="$LAB/windows/winvm.sh"
D=$LAB/images/services-$VM
sshport=$([[ $VM == win10 ]] && echo 2241 || echo 2242)
pass=0 fail=0
ok() { pass=$((pass + 1)); echo "PASS  $*"; }
bad() { fail=$((fail + 1)); echo "FAIL  $*"; }
vm() { timeout 300 "$W" ssh "$VM" "$1" 2>&1 | tr -d '\r'; }
# vmok <description> <powershell>: PASS when the command's exit code is 0
vmok() { local out; out=$(vm "$2; exit \$LASTEXITCODE"); if [[ $? == 0 ]]; then ok "$1"; else bad "$1: $(tail -2 <<<"$out" | tr '\n' ' ')"; fi; }
S() { echo "C:\\sv\\bin\\matriline-server.exe -c C:\\sv\\$1\\server.conf"; }
C() { echo "C:\\sv\\bin\\matriline-client.exe -c C:\\sv\\$1\\client.conf"; }
N() { echo "C:\\sv\\bin\\nacomline.exe -c C:\\sv\\$1\\nacomline.conf"; }

rm -rf "$D" && mkdir -p "$D"
export GOOS=windows GOARCH=amd64
(cd "$SRC" && go build -o "$D/matriline-server.exe" ./server && go build -o "$D/matriline-client.exe" ./client) || exit 1
(cd "$NACO" && go build -o "$D/nacomline.exe" .) || exit 1
unset GOOS GOARCH
vm "Get-ScheduledTask -TaskName 'matriline-*','nacomline-*' -ErrorAction SilentlyContinue | Where-Object { \$_.Actions.Execute -like 'C:\\sv\\*' -or \$_.Actions.Arguments -like '*C:\\sv\\*' } | ForEach-Object { Stop-ScheduledTask -TaskName \$_.TaskName; Unregister-ScheduledTask -TaskName \$_.TaskName -Confirm:\$false }; Get-CimInstance Win32_Process | Where-Object { \$_.ExecutablePath -like 'C:\\sv\\*' } | ForEach-Object { Stop-Process -Id \$_.ProcessId -Force }; Start-Sleep 1; Remove-Item -Recurse -Force C:\\sv -ErrorAction SilentlyContinue; New-Item -ItemType Directory -Force C:\\sv\\bin, C:\\sv\\in | Out-Null; Set-Content -Encoding ascii C:\\sv\\in\\water.inp \"! HF def2-SVP\`n* xyz 0 1\`nO 0 0 0\`nH 0 0.757 0.587\`nH 0 -0.757 0.587\`n*\"" >/dev/null
scp -q -i "$LAB/keys/lab_ed25519" -P "$sshport" -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null \
	"$D"/*.exe matriline@127.0.0.1:C:/sv/bin/ || exit 1

# --- two servers
port=44440
for s in A B; do
	port=$((port + 1))
	vm "C:\\sv\\bin\\matriline-server.exe init C:\\sv\\$s | Out-Null; \$f='C:\\sv\\$s\\server.conf'; (Get-Content \$f) -replace '^listen = .*','listen = 127.0.0.1:$port' -replace '^advertise =.*','advertise = 127.0.0.1:$port' -replace '^scan_interval = .*','scan_interval = 2s' -replace '^settle_time = .*','settle_time = 0s' -replace '^enabled = true','enabled = false' | Set-Content -Encoding ascii \$f" >/dev/null
	vmok "server $s service install" "$(S $s) service install"
	vmok "server $s service install again" "$(S $s) service install"
	vmok "server $s service remove" "$(S $s) service remove"
	vm "$(S $s) service remove; \"rc=\$LASTEXITCODE\"" | tail -1 | sed "s/^/info  server $s remove again -> /"
	vmok "server $s service install after remove" "$(S $s) service install"
done
for s in A B; do
	for _ in $(seq 90); do vm "$(S $s) status" | grep -q "^server " && break; sleep 3; done
	vm "$(S $s) status" | grep -q "^server " && ok "server $s answers" || bad "server $s does not answer"
	out=$(vm "$(S $s) run 2>&1 | Out-String")
	[[ $out == *"already running"* || $out == *"lock"* ]] && ok "server $s: a second 'run' by hand is refused" || bad "server $s second run: $(head -c 200 <<<"$out")"
done
task=$(vm "(Get-ScheduledTask -TaskName 'matriline-server-*' | Where-Object { \$_.Actions.Arguments -like '*C:\\sv\\A\\*' }).TaskName" | tail -1)
vm "Stop-ScheduledTask -TaskName '$task'" >/dev/null; sleep 5
vm "$(S A) status" | grep -q "^server " && bad "server A still answers after its task was ended" || ok "server A ended with its task"
vmok "server A service install while ended (starts it)" "$(S A) service install"
for _ in $(seq 90); do vm "$(S A) status" | grep -q "^server " && break; sleep 3; done
vm "$(S A) status" | grep -q "^server " && ok "server A answers again" || bad "server A does not come back"

# --- clients
for c in A1 A2 B1; do
	s=${c:0:1}
	vm "$(S $s) keys issue $c C:\\sv\\$c.cred | Out-Null; C:\\sv\\bin\\matriline-client.exe init C:\\sv\\$c --credential C:\\sv\\$c.cred | Out-Null; \$f='C:\\sv\\$c\\client.conf'; (Get-Content \$f) -replace '^cores = 0\$','cores = 1' | Set-Content -Encoding ascii \$f" >/dev/null
	vmok "client $c service install" "$(C $c) service install"
	vmok "client $c service install again" "$(C $c) service install"
	vmok "client $c service remove" "$(C $c) service remove"
	vmok "client $c service install after remove" "$(C $c) service install"
done
for c in A1 A2 B1; do
	want=$([[ $c == B1 ]] && echo 44442 || echo 44441)
	# the first start fingerprints ORCA (a minute or two; three clients at once take longer)
	for _ in $(seq 60); do vm "$(C $c) status" | head -1 | grep -q "connected to" && break; sleep 5; done
	st=$(vm "$(C $c) status" | head -1)
	[[ $st == *"connected to 127.0.0.1:$want"* ]] && ok "client $c connected to its own server" || bad "client $c: $st"
done
la=$(vm "$(S A) clients") lb=$(vm "$(S B) clients")
[[ $(grep -cE '^(A1|A2) ' <<<"$la") == 2 && $(grep -c '^B1 ' <<<"$lb") == 1 && $(grep -c '^B1 ' <<<"$la") == 0 ]] && ok "each server lists only its own clients" || bad "clients: A: $la / B: $lb"

# --- two Nacomline projects
for n in N1 N2; do
	vm "C:\\sv\\bin\\nacomline.exe init C:\\sv\\$n" >/dev/null
	vmok "nacomline $n service install" "$(N $n) service install"
	vmok "nacomline $n service install again" "$(N $n) service install"
done
for n in N1 N2; do for _ in $(seq 90); do vm "$(N $n) status" | grep -q this-computer && break; sleep 3; done; done
for n in N1 N2; do vm "$(N $n) status" | grep -q this-computer && ok "nacomline $n running" || bad "nacomline $n not running"; done

# --- jobs end in their own folders
vm "$(S A) add C:\\sv\\in input/a | Out-Null; $(S B) add C:\\sv\\in input/b | Out-Null; Copy-Item C:\\sv\\in\\water.inp C:\\sv\\N1\\input\\n1.inp; Copy-Item C:\\sv\\in\\water.inp C:\\sv\\N2\\input\\n2.inp" >/dev/null
dirs='C:\sv\A\output\a\in\water','C:\sv\B\output\b\in\water','C:\sv\N1\output\n1','C:\sv\N2\output\n2'
for _ in $(seq 120); do vm "if ((@($dirs) | Where-Object { -not (Test-Path \$_) }).Count -eq 0) { 'all' }" | grep -q all && break; sleep 5; done
for d in 'A\output\a\in\water' 'B\output\b\in\water' 'N1\output\n1' 'N2\output\n2'; do
	vm "Test-Path 'C:\\sv\\$d'" | grep -q True && ok "result in $d" || bad "no result in $d"
done
vm "(Test-Path C:\\sv\\A\\output\\b) -or (Test-Path C:\\sv\\B\\output\\a) -or (Test-Path C:\\sv\\N1\\output\\n2) -or (Test-Path C:\\sv\\N2\\output\\n1)" | grep -q False && ok "no result in another instance's folder" || bad "results crossed instances"

# --- consoles and web pages (8484 and the next free port)
for s in A B; do
	out=$(vm "'status','quit' | $(S $s) console | Out-String")
	[[ $out == *"spool C:\\sv\\$s"* ]] && ok "server $s console shows its own spool" || bad "server $s console: $(grep spool <<<"$out")"
done
# both started and read in one ssh session: Windows' OpenSSH ends a session's processes
# when the session closes
pages=$(vm "foreach (\$s in 'A','B') { Start-Process -WindowStyle Hidden -FilePath C:\\sv\\bin\\matriline-server.exe -ArgumentList '-c',\"C:\\sv\\\$s\\server.conf\",'web' -RedirectStandardOutput \"C:\\sv\\web-\$s.log\"; Start-Sleep 4 }; foreach (\$s in 'A','B') { \$l = (Select-String -Path \"C:\\sv\\web-\$s.log\" -Pattern 'http://\\S+').Matches[0].Value; \$r = Invoke-WebRequest -UseBasicParsing -SessionVariable ws \$l; \"== \$s \$l\"; \$r.Content | Select-String -Pattern 'spool [^ ]+' | ForEach-Object { \$_.Matches[0].Value } }")
for s in A B; do
	sec=$(awk -v s="$s" '$1=="=="{on=($2==s)} on' <<<"$pages")
	[[ $sec == *"spool C:\\sv\\$s"* ]] && ok "web page of server $s shows its own spool ($(grep -o ':84[0-9][0-9]' <<<"$sec"))" || bad "web $s: $sec"
done

# --- remove everything
for n in N1 N2; do vmok "nacomline $n service remove" "$(N $n) service remove"; done
for c in A1 A2 B1; do vmok "client $c service remove" "$(C $c) service remove"; done
for s in A B; do vmok "server $s service remove" "$(S $s) service remove"; done
vm "Get-CimInstance Win32_Process | Where-Object { \$_.ExecutablePath -like 'C:\\sv\\*' -and \$_.CommandLine -like '* web' } | ForEach-Object { Stop-Process -Id \$_.ProcessId -Force }" >/dev/null
sleep 10
left=$(vm "Get-CimInstance Win32_Process | Where-Object { \$_.ExecutablePath -like 'C:\\sv\\*' } | ForEach-Object { \$_.CommandLine }")
[[ -z $left ]] && ok "no process left" || bad "left: $left"
echo "### services ($VM): $pass passed, $fail failed"
((fail == 0))
