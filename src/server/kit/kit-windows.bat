@echo off
rem Matriline helper kit for Windows, all in this one file: double-click it (or right-click >
rem Run as administrator, so it also works with nobody logged in). Install ORCA first
rem (INSTALL.md, section 2). If Windows warns about an unknown program: More info > Run anyway.
set "KITFILE=%~f0"
powershell -NoProfile -ExecutionPolicy Bypass -Command "$KitText=[IO.File]::ReadAllText($env:KITFILE); iex ($KitText -split ('#__P'+'S__#'))[1]"
exit /b
#__PS__#
# ======================= settings the admin may edit =======================
$Language = '@@LANGUAGE@@'            # en, es, fr, pt or ar; empty = this computer's
$WorkDir = "$HOME\Matriline\cli"      # the client's working folder (settings, key, calculations)
# Written into client.conf (same format; every option is explained in that file).
$Settings = @'
@@SETTINGS@@
'@
# ===========================================================================

$Cred = '@@CRED@@'
$ErrorActionPreference = "Stop"
function Pause-End { if (-not $env:MATRILINE_KIT_NO_PAUSE) { Read-Host "Press Enter to close" | Out-Null } }
function Fail($msg) { Write-Host "ERROR: $msg" -ForegroundColor Red; Pause-End; exit 1 }

# the program in a fixed place, taken from the end of this file
$binDir = "$HOME\Matriline"
New-Item -ItemType Directory -Force $binDir | Out-Null
$client = Join-Path $binDir "matriline-client.exe"
$payload = ($KitText -split ('#__PAY' + 'LOAD__#'))[1]
if (-not $payload) { Fail "the program is missing from this file (is it complete?)" }
try { [IO.File]::WriteAllBytes($client, [Convert]::FromBase64String(($payload -replace '\s', ''))) }
catch {
	if (-not (Test-Path $client)) { Fail "cannot write $client`: $_" }
	Write-Host "note: $client is in use by the running client, so it keeps its version (stop it with 'matriline-client service remove' and run this file again to update it)"
}
$tmp = Join-Path ([IO.Path]::GetTempPath()) ("matriline-kit-" + [guid]::NewGuid())
New-Item -ItemType Directory $tmp | Out-Null
$credFile = Join-Path $tmp "helper.cred"
[IO.File]::WriteAllBytes($credFile, [Convert]::FromBase64String($Cred))

# run again after an interruption (a closed window or session): it carries on, but only
# with a client of this kit's server; another server's client is left alone
$again = Test-Path (Join-Path $WorkDir "client.conf")
if ($again) {
	$kitServer = Select-String -Path $credFile -Pattern '^server_id' | ForEach-Object { $_.Line }
	$have = Join-Path $WorkDir "credential.conf"
	if (-not (Test-Path $have)) { Copy-Item $credFile $have }  # interrupted inside init
	elseif ((Select-String -Path $have -Pattern '^server_id' | ForEach-Object { $_.Line }) -ne $kitServer) {
		Remove-Item -Recurse -Force $tmp
		Fail "$WorkDir already holds a client of another server: set `$WorkDir at the top of this file to another folder"
	}
}
$setFile = Join-Path $tmp "settings.conf"
Set-Content -Encoding ascii $setFile $Settings

if ($again) {
	Write-Host "== $WorkDir is already set up: carrying on with the service and the check"
	Remove-Item -Recurse -Force $tmp
} else {
	$initArgs = @("init", $WorkDir, "--credential", $credFile, "--settings", $setFile)
	if ($Language) { $initArgs += @("--language", $Language) }
	Write-Host "== setting up $WorkDir"
	& $client @initArgs
	$ok = ($LASTEXITCODE -eq 0)
	Remove-Item -Recurse -Force $tmp
	if (-not $ok) { Fail "init failed (see above)" }
}
Set-Location $WorkDir
# the service first: if the window closes during the (slow) check below, the client already
# runs and comes back by itself
Write-Host "== installing the service"
& $client service install
if ($LASTEXITCODE -ne 0) { Fail "service install failed (see above); try again in $WorkDir`: $client service install" }
Write-Host "== checking ORCA, the sandbox and the server (the first time a minute or two per ORCA installation)"
& $client doctor
if ($LASTEXITCODE -ne 0) { Write-Host "WARNING: fix what doctor says above (ORCA: INSTALL.md section 2); the client retries by itself" -ForegroundColor Yellow }
# doctor only sees that the server answers; the service must also be let in (its key)
Write-Host "== waiting for the service to connect to the server (up to 5 minutes)"
$conn = Join-Path $WorkDir "state\connection.json"
$st = $null
for ($i = 0; $i -lt 100; $i++) {
	try { $st = Get-Content -Raw $conn -ErrorAction Stop | ConvertFrom-Json } catch { $st = $null }
	if ($st -and $st.connected) { break }
	Start-Sleep 3
}
if (-not ($st -and $st.connected)) {
	$err = if ($st -and $st.last_error) { ": " + $st.last_error } else { "" }
	Fail "the service has not connected to the server$err`nIf it says 'unknown client' or 'credential', ask the admin for a new kit; otherwise see $WorkDir\state\client.log, fix it and run this file again."
}
Write-Host "connected"
Write-Host ""
Write-Host "Done: this computer is a Matriline helper. It starts by itself and works in the background." -ForegroundColor Green
Write-Host "While it computes on mains power, it keeps this computer from going to sleep by itself"
Write-Host "(the screen may still turn off); keep_awake = false in client.conf turns that off."
Write-Host "  status:  cd $WorkDir; $client status"
Write-Host "  pause:   $client pause 2h    (or: pause --now, resume)"
Write-Host "  stop:    $client service remove"
Write-Host "Its settings are in $WorkDir\client.conf; its log in $WorkDir\state\client.log."
Write-Host "You may delete this file now."
Pause-End
#__PS__#
#__PAYLOAD__#
