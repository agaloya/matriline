# Release checklist

What to test before publishing any update of Matriline (and Nacomline), so that nothing
already working is forgotten. Each line names the script or command that checks it; record
the result (PASS/FAIL, commit, date) in the release notes. Platforms: Linux, Windows 10,
Windows 11, macOS (when the Mac is available).

## 1. Build and code

- [ ] `cd src && go vet ./...` for GOOS linux, windows, darwin, freebsd
- [ ] `go test -race -count=1 ./...` (Matriline) and `go test ./...` (Nacomline)
- [ ] `tools/release.sh dist` and `tools/release.sh --check docs/releases/SHA256SUMS-<commit>`
      on a second machine (reproducible builds)
- [ ] An independent code review of the diff since the last release
- [ ] Translations: `cd src && go run ./i18nextract` (strings.txt current, committed) and
      `go run ./i18nextract -check es` (new texts without a translation are shown in English;
      ask for them before the release); `go test ./common/i18n` checks the %-verbs

- [ ] After publishing: `matriline-server kit x ~/kits` without --clients downloads this
      version's clients from the release (checked like an update)
- [ ] Sign and publish (maintainer's computer; the key never leaves it):
      `tools/release.sh dist && (cd src && go run ./relsign sign ~/.keys/matriline-release.key ../dist && go run ./relsign verify ../dist)`,
      then `gh release create v<version> dist/*`

## 2. Every command, every platform

- [ ] `lab/tests/cmdmatrix.sh linux|win10|win11`: every server and client command, with
      its exit code (58 checks)
- [ ] `lab/tests/kit-win.sh win10|win11`: a helper kit made with `matriline-server kit`, run in the VM
      (real scheduled task), one job (keep_awake listed by `powercfg /requests` while it
      computes, released after), run again (carries on), `status` from another folder.
      2026-10-07: PASS on both. Linux and macOS kits: by hand
- [ ] `help`, `help <command>` and `console` of the server and the client; the pager in a
      terminal without scrollback (tmux)
- [ ] Nacomline: `lab/tests/nacomline-linux.sh` (lab VM with systemd, never this desktop)
      and `lab/tests/nacomline-win.sh win10|win11`: init finds ORCA, service install, 2
      results + 1 ORCA error filed, pause/resume, a settings change applied while running
      (the slot count changes), service remove with no process or unit left.
      2026-10-06 (49fc57e, nacomline 585bb0d): PASS on Linux, win10, win11

## 3. Services and several instances

- [ ] `lab/tests/services.sh` (Linux, win10, win11): install, remove, reinstall, stop and
      start the services in careless orders; two servers, two clients and two Nacomline
      projects on one computer at once, each console and web page talking to its own
- [ ] After a reboot: services come back (Windows: at start-up without logon when
      installed by an administrator, otherwise at logon)

## 4. Network and failures

- [ ] `lab/tests/winchaos.sh client|server` and `lab/tests/chaos.sh`: dropouts, suspend,
      roaming, restarts, power cuts; nothing lost (`check`, `verify` clean)
- [ ] Relay, every system (2026-10-07: all PASS):
      - Windows client through a relay reachable only on port 443:
        `lab/tests/winnet.sh relay443 5 win10|win11` (win10 9/9, win11 17/17)
      - Windows server behind a relay, Linux clients: `lab/tests/winrelay-server.sh win11` (6/6)
      - macOS client through a relay, arm64 and x86_64 under Rosetta (5/5 each)
      - Linux server and clients: the lab's NAT profiles (`lab/tests/labtests.sh`)
- [ ] `lab/tests/winnet.sh badnet`: bad Wi-Fi
- [ ] Real network: an overnight run on the laptop over the internet
- [ ] Long real test: a batch of long jobs (benzene-sized, ~1 h each) for a day or more on
      a real network, ending with check/verify clean. Run it for each version; skipped for
      the first publication (2026-10-07, user): the 2026-10-05/07 run reached 502 results,
      0 errors, before the machines were cleaned for the from-scratch install test

- [ ] `lab/tests/loadtest.sh run 120 1200 off` and `run 120 1200 on`: 120 clients on one
      computer, 1200 short jobs; every job in output/ once, `check`/`verify` clean, no client
      disconnects, no ERROR. 2026-10-06 (02b3a00 + fixes): 1200/1200 in 90 s (off) and
      103 s (on, ~11.7 jobs/s), server 53-57 MB RSS and 34-43 s CPU, 0 disconnects

## 5. Integrity

- [ ] `lab/tests/adversarial.sh` / `winadversarial.sh`: every attack round (energy, loose,
      copy, forge, hess, collude, slander, dodge) and the control round (no honest client
      punished); docs/SECURITY_TESTS.md updated
- [ ] `verify` and `check` clean on the real server after the upgrade

## 6. Updates (experimental)

- [ ] `lab/tests/selfupdate.sh` and `selfupdate-win.sh win10|win11`: a release signed by
      another key refused; client then server restart into the new version
- [ ] An upgrade by hand from the previous release keeps the spool, ledger and settings
      (`verify` clean before and after); if the configuration or folder format changed,
      the release notes say what to do: `lab/tests/upgrade.sh <previous-commit>` (server
      and 3 clients, same directories, keys, settings; a batch before and after).
      2026-10-06: f2d0a40f7a0d -> 396ee418f3f6 PASS

## 7. Interfaces

- [ ] Every command appears on the web page and its form opens (`go test -run
      TestWebShowsEveryCommand ./server`, automatic; new commands need `NoWeb` only if they
      need a terminal), and running a few of them from the page gives the same output as
      the command line
- [ ] Web page: status, live (3D view), every command form, settings (save, cancel,
      restart), add with file drop; the four palettes; on a phone-width window too
- [ ] `watch` and `live` with jobs running; a check sub-task shows the task it verifies

## 8. Alerts

- [ ] `alerts test` with a real SMTP account and with an alert program; the grouped
      (digest) mode; a configuration with e-mail on but incomplete warns at start

## 9. Documentation

- [ ] README, INSTALL, docs/DECISIONS.md and the EULA notes match what
      the release does; screenshots current
