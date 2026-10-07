#!/usr/bin/env bash
# nacomline-win.sh [win10|win11] - Nacomline (../nacomline, next to this repo) on Windows:
# init (finds ORCA), service install (scheduled task), 2 good inputs + 1 ORCA error,
# pause/resume, a settings change applied while running (client restarts), service
# remove with no Matriline process left behind.
set -uo pipefail
export MATRILINE_NO_REGISTRY=1 # lab projects stay off the person's list (common/projects)
LAB=$(cd "$(dirname "$(readlink -f "$0")")/.." && pwd)
SRC=$LAB/../src NACO=$LAB/../../nacomline
VM=${1:-win11}
W="$LAB/windows/winvm.sh"
D=$LAB/images/nacomline-$VM
sshport=$([[ $VM == win10 ]] && echo 2241 || echo 2242)
log() { echo "$(date +%H:%M:%S) $*"; }
fail() { log "FAIL: $*"; exit 1; }
vm() { timeout 300 "$W" ssh "$VM" "$1" 2>&1 | tr -d '\r'; }
N='C:\nc\bin\nacomline.exe -c C:\nc\proj\nacomline.conf'
rm -rf "$D" && mkdir -p "$D"
export GOOS=windows GOARCH=amd64
(cd "$SRC" && go build -o "$D/matriline-server.exe" ./server && go build -o "$D/matriline-client.exe" ./client) || fail build
(cd "$NACO" && go build -o "$D/nacomline.exe" .) || fail "build nacomline"
unset GOOS GOARCH
vm "Get-ScheduledTask -TaskName 'nacomline-*' -ErrorAction SilentlyContinue | ForEach-Object { Stop-ScheduledTask -TaskName \$_.TaskName; Unregister-ScheduledTask -TaskName \$_.TaskName -Confirm:\$false }; Get-CimInstance Win32_Process | Where-Object { \$_.ExecutablePath -like 'C:\\nc\\*' } | ForEach-Object { Stop-Process -Id \$_.ProcessId -Force }; Start-Sleep 1; Remove-Item -Recurse -Force C:\\nc -ErrorAction SilentlyContinue; New-Item -ItemType Directory -Force C:\\nc\\bin | Out-Null" >/dev/null
scp -q -i "$LAB/keys/lab_ed25519" -P "$sshport" -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null \
	"$D"/*.exe matriline@127.0.0.1:C:/nc/bin/ || fail scp
log "init: $(vm 'C:\nc\bin\nacomline.exe init C:\nc\proj' | head -2 | tr '\n' ' ')"
vm "Select-String -Path C:\\nc\\proj\\nacomline.conf -Pattern '^path = .+'" | grep -i orca >/dev/null || fail "ORCA not found by init"
E='%maxcore 768
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
printf '%s\n' "$E" | vm '$t=[Console]::In.ReadToEnd(); Set-Content -Encoding ascii C:\nc\proj\input\etoh_1.inp $t; Set-Content -Encoding ascii C:\nc\proj\input\etoh_2.inp $t; Set-Content -Encoding ascii C:\nc\proj\input\typo.inp "! NOTAKEYWORD def2-SVP`n* xyz 0 1`nH 0 0 0`nH 0 0 0.74`n*"' >/dev/null
log "service: $(vm "$N service install" | tail -2 | tr '\n' ' ')"
for _ in $(seq 120); do vm "$N status" | grep "output 2 .*errors 1" >/dev/null && break; sleep 5; done
vm "$N status" | grep -E "^results|this-computer"
vm "$N status" | grep "output 2 .*errors 1" >/dev/null || fail "results not filed: $(vm "Get-Content C:\\nc\\proj\\.nacomline\\*.out -Tail 5")"
log "pause: $(vm "$N pause 10m" | head -1)"
vm "$N status" | grep "this computer: paused" >/dev/null || fail "pause not shown"
log "resume: $(vm "$N resume")"
# a change the status shows: a slot count other than the current one
now=$(vm "$N status" | awk '$1 == "this-computer" {print $3}')
want=$([[ $now == 1 ]] && echo 2 || echo 1)
vm "(Get-Content C:\\nc\\proj\\nacomline.conf) -replace '^cores = .*','cores = $want' | Set-Content -Encoding ascii C:\\nc\\proj\\nacomline.conf; $N config apply" | tail -1
for _ in $(seq 30); do vm "$N status" | grep -E "this-computer +ml1-[a-z0-9]+ +$want " >/dev/null && break; sleep 3; done
vm "$N status" | grep -E "this-computer +ml1-[a-z0-9]+ +$want " >/dev/null || fail "the settings change did not reach the client: $(vm "$N status" | grep this-computer)"
log "client restarted with $want slot(s) (was $now)"
vm "$N events 8" | cut -c1-130
log "remove: $(vm "$N service remove")"
sleep 10
left=$(vm "Get-CimInstance Win32_Process | Where-Object { \$_.ExecutablePath -like 'C:\\nc\\*' } | Select-Object -ExpandProperty ExecutablePath")
[[ -z $left ]] || fail "processes left after service remove: $left"
log PASS
