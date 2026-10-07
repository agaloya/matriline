# Porting: other architectures and operating systems

Matriline runs on **Linux only** for now (x86-64 tested; arm64 builds). Order agreed with the
user (2026-10-04): the client to Windows and macOS first, then the server. This file lists
the problems expected and the tests planned, so each port starts from a checklist. Add to it
whenever something new comes up.

## 1. Two ORCA builds of the same version (x86-64 and arm64): DONE

The first step, done on Linux: an aarch64 VM (QEMU TCG emulation, 2 cores, 3 GB) with the
official ORCA 6.1.1 arm64 build.

Expected problems:
- **Fingerprints.** The server pins accepted ORCA installations by version, git hash and
  tree hash (`orca.accepted_tree_hashes`). The arm64 build has another tree hash; the
  version and git hash should match. The admin must be able to accept both builds of one
  version explicitly, and the `doctor` and `status` output should say which build each
  client runs.
- **Verification across builds.** Different compilers, BLAS/LAPACK and vector units give
  last-digit differences. The SCF re-check (tolerance 1e-5 Eh), gradient check (4x TolMaxG)
  and Hessian probe (0.005 relative) should absorb them; the **replica** tolerance (2e-6 Eh
  was already too strict for one result on x86) may not. To measure: the same inputs on both
  builds, differences per method (HF, DFT with each grid, RIJCOSX, MP2), then set the
  tolerances from data, not by guess.
- **Canaries** compare against a known answer from possibly the other build: same tolerance
  question (canary tolerance 5e-5 Eh).
- **Timing plausibility and the learned scheduler**: an emulated ARM host is 5-20x slower;
  the per-host speed term of the model should absorb it; the timing check must not flag a
  slow host.
- **Sandbox**: Landlock and namespaces exist on arm64 Linux; the syscall numbers used
  directly (landlock_create_ruleset etc.) must be the arm64 ones (Go's syscall package
  differs per GOARCH: check every hand-written syscall number).
- **Process audit**: the arm64 ORCA may ship different helper binaries (otool_*, MPI),
  which the audit would flag as unknown programs.
- **ORCA arm64 package**: "shared, openmpi418": shared libraries must be found inside the
  sandbox (LD_LIBRARY_PATH or rpath) with the clean environment the client uses.

Tests: tiny inputs only (water/HF, small DFT SP, Opt+Freq of H2O, MP2 of N2), first
standalone in the VM, then the VM as a client of a test server together with x86 clients,
with every verification layer at 100 %: x86 results checked on ARM and vice versa, zero
false positives expected.

### Results (2026-10-04, ORCA 6.1.1 x86-64 on the void VM vs. the official arm64 build in
an emulated aarch64 VM; tight SCF; differences x86 - arm64)

| input | SCF energy | final energy | max gradient component |
|---|---|---|---|
| H2O HF/STO-3G | 1.6e-13 | 0 | |
| H2O HF/def2-SVP | 1.4e-13 | 0 | |
| H2O B3LYP/def2-SVP | 6.6e-11 | 6.6e-11 | |
| H2O PBE0/def2-SVP RIJCOSX | -7.4e-11 | -7.4e-11 | |
| N2 RI-MP2/def2-SVP | 3.7e-13 | 0 | |
| OH UKS B3LYP/def2-SVP (doublet) | -1.4e-10 | -1.4e-10 | |
| H2O B3LYP/def2-SVP EnGrad | 6.6e-11 | 6.6e-11 | 4.4e-8 |
| H2O B3LYP/def2-SVP Opt Freq | -5.3e-10 | 1.5e-9 | 7.8e-8 |

Energies in Eh, gradients in Eh/bohr. Every difference is thousands of times below the
verification tolerances (SCF re-check 1e-5, replica 2e-6, canary 5e-5): the two builds can
check each other. Emulated run time: 3-15 s per single point, 94 s for the Opt Freq.

Finding: **the arm64 build prints no GIT hash** ("Program Version 6.1.1 - RELEASE" only),
and the server required the reference's GIT hash and file hashes, so every arm64 result
would have failed verification. Fixed: `orca.reference_paths` may list several builds (an
arm64 tree unpacked on an x86 server is fingerprinted though it cannot run), and
`orca.accepted_tree_hashes` accepts a build by its tree hash; an output without GIT hash is
accepted only from such a tree, so stripping the GIT line from an x86 output still fails.

### Approving a build that prints no GIT hash (2026-10-04)

The admin can approve an installation by its full fingerprint instead of unpacking it on the
server: on a client, `matriline-client fingerprint > arm64-orca.json` (every file's SHA-256
plus the tree hash), then on the server `orca.accepted_fingerprints = arm64-orca.json`. The
server checks the tree hash against the file list when loading it, accepts outputs without a
GIT hash only from that tree, and checks every program a result ran against the list.
Tested with the x86-64 build: the exported tree hash equals the server's own
(2626427a032dbc8b, 3032 files), and a server with no ORCA of its own accepted a result
using only the exported fingerprint.

### End to end through Matriline (2026-10-04)

A loopback-only test server with every verification layer at 100 % (SCF re-check, gradient,
Hessian probe, replication, second opinion), one x86-64 client (the host) and the arm64 VM
as a client (sandbox `landlock-abi6+userns+netns` works on arm64). After approving the arm64
tree hash: 8 results produced on x86 and checked on arm64, then 8 produced on arm64 and
checked on x86: **16/16 checks passed, 0 false positives**. Before approving the tree, the
first arm64 result went to weird/ for 9 reasons (fingerprint, build, executed programs,
version line), as expected. Found on the way and fixed: attempts of accepted results stayed
in the store as "running" (6c2144a, f345b9c).

## 2. Client on Windows

Expected problems:
- **No sandbox yet.** Options: Job Objects (limits, kill-on-close), AppContainer or a
  restricted token (file isolation), Windows Filtering Platform rules for "no network"
  (needs admin), or WSL2 (then it is the Linux client). Until one works, Windows clients
  should be marked "unsandboxed" to the server, which could require more checks from them.
- **Paths**: ORCA calls its own helper programs with paths; spaces and backslashes;
  file names already restricted to safe characters (`orca.SafeFileName`).
- **Signals**: no SIGTERM/process groups; stopping ORCA and its children needs a Job
  Object. Power events (suspend) via a different API; the instant-reconnect clock check
  works anyway.
- **Disk free, temperature, CPU topology**: Windows APIs (GetDiskFreeSpaceEx, WMI or
  none for temperature, GetLogicalProcessorInformation for physical cores).
- **Service**: run as a Windows service or a scheduled task; lock file semantics differ.
- **ORCA on Windows** uses its own MPI (MS-MPI) for parallel runs: relevant for MPI later.

Tests: the existing protocol tests run as they are; a Windows VM in the lab (licence
permitting) running the client against the lab server; the chaos test (suspend, Wi-Fi
change) on a real Windows laptop.

### Groundwork done (2026-10-04, compiled but not yet run on Windows)

- Telemetry through kernel32/ntdll, standard library only: memory (GlobalMemoryStatusEx),
  free disk (GetDiskFreeSpaceExW), battery and mains (GetSystemPowerStatus; no system
  battery -> "none"), OS name and build (RtlGetVersion; build >= 22000 is Windows 11), CPU
  model (PROCESSOR_IDENTIFIER). CPU temperature and frequency: NA (need privileges).
- Every ORCA job runs in a Job Object with kill-on-close, so cancel, timeout or a remote
  disable end ORCA's helper programs too (on Windows killing the parent leaves them).
- ORCA's environment gets SystemRoot/windir (needed to load system DLLs) and TEMP/TMP in the
  job directory.
- Process audit: the job tree (Toolhelp snapshot) with full paths (QueryFullProcessImageNameW)
  and working set; cmd.exe is the allowed system shell. Physical cores from
  GetLogicalProcessorInformation.
- Still missing: the sandbox (the client refuses to run ORCA unless security.sandbox = false),
  CPU load sampling.
- VMs: `lab/windows/winvm.sh build win10|win11 <iso>` then `install`: unattended setup from
  an official ISO the user downloads (evaluation images: one edition, image index 1; other
  ISOs may need another index), SATA disk and e1000e NIC (no extra drivers), Windows 11's
  TPM/Secure Boot/RAM checks skipped with the LabConfig keys (no swtpm needed), OpenSSH server
  with the lab key (`winvm.sh ssh win11`). Written 2026-10-04, not yet run (no ISO yet).
- Test plan in the Windows 10 and 11 VMs: install ORCA for Windows, `doctor`, one job end to
  end with sandbox = false, cancel and timeout (the tree must disappear: check with Task
  Manager / tasklist), disable, battery report on a laptop, network drop.

### Windows 10 22H2 (VM, 2026-10-05, night)

Same binaries as on Windows 11, no version-specific code:
- ORCA from the official installer, silently: `Orca6.1.1.Win64.exe /exenoui /qn
  APPDIR=C:\ORCA_6.1.1`; fingerprint identical to Windows 11 (2b6c06f2...).
- Sandbox tests pass: low integrity, AppContainer without network (curl exit 7), and a
  2-process MS-MPI job (MS-MPI 10.1.1 runtime, `msmpisetup.exe -unattend -force`).
- The client as a service at boot (S4U, no logon) with the AppContainer.
- The server as a service at boot; init found C:\ORCA_6.1.1 and set accepted_fingerprints
  = builtin; a Linux client through a forwarded port computed 2 of 2 jobs.
- Client chaos test (link cut, suspend, a real second network card, task restart, power
  cut) PASS; the attack rounds ran with a cheater on Windows 10 too (collude).
- Lab note: Windows 10's OpenSSH capability from Windows Update failed again; installed
  from the Win32-OpenSSH release by typing into the VM (lab/windows/vmtype.py).

### First run on Windows 11 Pro (VM, 2026-10-05)

ORCA 6.1.1 Win64 (MS-MPI build, official MSI, silent install to C:\ORCA_6.1.1); client
cross-built from Linux; test server on the host (the VM reaches it as 10.0.2.2).
- `init --credential` found C:\ORCA_6.1.1 by itself; `doctor` passes (sandbox off).
- The server accepted the Windows build through `matriline-client fingerprint` (2622 files,
  no GIT hash, like arm64) saved by PowerShell's `>` as UTF-16: the server now reads UTF-16
  and BOM files.
- Bugs found and fixed: the client looked for `orca` instead of `orca.exe`; ORCA's
  system() needs %ComSpec% in its clean environment (every job ended in "error termination
  in Startup"); the probe used another environment than the jobs (now one orcaEnv); the
  allowed shell was compared case-sensitively (SystemRoot is C:\WINDOWS, the image path
  C:\Windows\System32\cmd.exe), and the server only knew Unix shells; the directory lock
  did nothing on Windows (now LockFileEx: a second instance is refused, `status` works);
  init suggested `nohup` (now Start-Process).
- Results: water HF/def2-SVP identical to Linux; formaldehyde r2SCAN-3c Opt differs by
  2.3e-10 Eh (tolerance 1e-5). Both accepted with fingerprints and consistency checks.
- Then: cancel ends ORCA's whole process tree (Job Object); `clients disable` stops the
  client and deletes its key, credential and job data; `service install` (scheduled task at
  logon, no window) starts it by itself; a second instance is refused.

### Windows 10 Pro 22H2 (VM, same day)

Same ORCA build (identical tree hash 2b6c06f2..., so one accepted fingerprint covers both
Windows versions), installed from a CD image so the VM disk does not grow. init, doctor,
service install and two jobs as on Windows 11, no new bug. Energies identical to Windows
11 (water -75.960975158486, formaldehyde Opt -114.479600359562). The first-logon OpenSSH
install had failed (network not ready yet): the answer file now retries it.
### Windows sandbox, first level (same day)

ORCA runs at low integrity (Mandatory Integrity Control): it cannot write to the user's
files, settings or programs, only to its job directory, which is created with the low
label (a label cannot be lowered later without WRITE_OWNER, which a normal user's limited
token lacks: the first version worked from an elevated SSH session and failed when the
scheduled task started the client). It can still read and use the network, so the client
reports "low-integrity", not isolation. Tested on Windows 11 under the limited token: a
program in the sandbox gets "Access is denied" writing to the user's profile and writes in
its job directory; two jobs through the scheduled task, energies identical to the
unsandboxed runs. All client tests pass on Windows. Multi-core (MS-MPI) jobs are not sandboxed yet.

### Windows sandbox, strong level: AppContainer (same day, Windows 11)

ORCA runs in an AppContainer with no capabilities: no network at all (curl from inside:
exit 7, the same address answers outside) and access only to its job directory and ORCA's.
The client starts itself as a helper that creates ORCA in the container (CreateProcessW
with SECURITY_CAPABILITIES). Found and fixed on the way:
- the container needs read access to ORCA's folder AND to every folder above it (cmd.exe,
  through which ORCA starts each module, checks the whole path; without the drive root:
  "Access is denied"): one administrator step, printed by the client:
  `icacls "C:\ORCA_6.1.1" /grant "*S-1-15-2-1:(OI)(CI)RX"` and `icacls "C:\" /grant "*S-1-15-2-1:(RX)"`;
  until then the client falls back to low integrity;
- creating a process in a container needs USERPROFILE/LOCALAPPDATA in the environment;
- the container's entry in a job directory's DACL stops a low-integrity process from
  writing there, so it is added only when the container is used;
- ORCA's system() calls failed inside the container when its stdout/stderr were files
  (r2SCAN-3c: "Calculation of the gCP correction failed"): the helper gives ORCA pipes and
  copies them to the files;
- Windows Error Reporting (WerFault.exe) is not treated as a foreign program.
Two jobs (HF SP, r2SCAN-3c Opt) through the scheduled task: accepted, energies identical
to Linux. All client tests pass on Windows under a normal user's token.

## 3. Client on macOS

### First attempt at a macOS x86-64 VM (2026-10-05)

lab/macos/macvm.sh (OpenCore from OSX-KVM). The user's Sequoia 15.1 image is an Apple
Partition Map disk, which UEFI firmware cannot read; exposing only its HFS+ partition to
QEMU (raw offset/size, no copy) makes OpenCore list "Install macOS Sequoia". The OpenCore
picker needs keys held ~250 ms (`sendkey right 250`). The installer then reboots the VM
within 20 s: most likely macOS on an AMD host (Ryzen 9 9950X3D; KVM cannot fully present
an Intel CPU). Next: run the same VM on an Intel host (the laptop, Core 5 120U), or boot
verbose (edit OpenCore's config.plist, needs root for qemu-nbd) to see the panic.

Expected problems:
- **Sandbox**: `sandbox-exec` profiles are deprecated but still work; the alternative is
  the App Sandbox (requires a signed app bundle). Network denial and file confinement are
  both possible with a profile.
- **Gatekeeper / notarization**: an unsigned downloaded binary is blocked; users would
  need `xattr -d com.apple.quarantine` or a signed release. Reproducible builds and code
  signing conflict (the signature changes the bytes): publish the unsigned hash plus the
  signature separately.
- **ORCA for macOS** (arm64 and x86-64 builds): same cross-build questions as section 1.
- **Temperature**: no public API without extra privileges (powermetrics needs root).
- **Sleep**: laptops sleep aggressively (App Nap, lid): the client should hold a power
  assertion while computing, if the user allows it.

### Groundwork done (2026-10-04, compiled for darwin/arm64 and amd64, not yet run on a Mac)

- Telemetry: total memory, physical cores, CPU model, macOS version (sysctl), free disk
  (statfs), battery and power source (`pmset -g batt`), job process tree with paths and RSS
  for the audit (`ps`). Temperature and frequency: NA. Process groups via Setpgid work as on
  Linux once the sandbox exists.
- Still missing: available memory.

### First run on a Mac (2026-10-07, macOS 14.6.1 VM "Apple M1 (Virtual)")

- Build, vet and tests pass on darwin/arm64, and on darwin/amd64 under Rosetta 2 (Apple's
  free x86-64 translator, the only way to run Intel macOS programs on Apple Silicon; an
  Intel macOS VM cannot run there). `tools/release.sh --check` reproduces every target.
- **Gatekeeper and ORCA**: ORCA 6.1.1 from the .tar.bz2 is ad-hoc signed and not notarized
  (`spctl` rejects it), and every extracted file carries `com.apple.quarantine`. Such an
  ORCA does not fail: it hangs before main() (0 % CPU, only dyld mapped, no output, never
  exits) while macOS waits for a Gatekeeper dialog, which nobody sees in a VM or a service.
  After `xattr -dr com.apple.quarantine <orca dir>` the first runs of each program still
  waited ~30 s (XProtect scanning), then ran normally. The client now refuses a quarantined
  ORCA and prints that command (gatekeeper_darwin.go).
- ORCA x86-64 under Rosetta 2 gives the same energies as arm64 to the last digit; the first
  run of each program is slower (translation), then ~1x.
- **Sandbox**: sandbox_darwin.go, a `sandbox-exec` profile per job ("deny default", no
  network rule at all, writes only in the job directory, reads only ORCA, the job and what
  dyld/libSystem/time zone/Rosetta need). Single-core jobs only: see "Multi-core jobs on
  macOS" below.
- `ps` prints "(name)" instead of the path for a few milliseconds around exec and exit, in
  any state; the audit took it for a foreign program and stopped every job. The darwin
  process tree now asks again for those processes (telemetry_darwin.go).
- `/bin/sh` on macOS is a small program that runs the shell named by the link
  /private/var/select/sh (bash): both must be executable in the sandbox, as ORCA starts its
  modules through system(3).

### Multi-core jobs on macOS: refused (decision of 2026-10-07)

Tested with OpenMPI 4.1.8 built from open-mpi.org's source (ORCA 6.1.1's macOS build links
`libmpi.40.dylib`, the 4.1 ABI; Homebrew ships 5.x) and `%pal nprocs 2`.

- Outside the sandbox, macOS needs three changes to what `mpiEnv` sets today:
  - `OMPI_MCA_mca_base_env_list=DYLD_LIBRARY_PATH=<openmpi>/lib`: ORCA starts mpirun
    through /bin/sh, a SIP-protected program, so every `DYLD_*` variable is removed and the
    ranks fail with `Library not loaded: libmpi.40.dylib` (ORCA links it by bare name;
    `LD_LIBRARY_PATH` means nothing on macOS). An MCA variable survives, and mpirun sets
    DYLD for the ranks.
  - `OMPI_MCA_oob_tcp_if_include=lo0`: the loopback is lo0 on macOS (`lo` fails).
  - `PMIX_MCA_ptl_tcp_disable_ipv6_family=1` (lo0 also has ::1 and fe80::1).
- Inside the profile, the minimum extra rules (each one removed and tested): metadata of every
  directory above the job directory (ORTE's session directory), `sysctl-read kern.hostname`
  and `net.routetable.*` (getifaddrs), TCP bind/listen and connect on localhost. Not needed:
  /etc/passwd, opendirectoryd, POSIX shared memory, unix sockets. The job then runs (same
  energy as outside), but 2-3 runs in 10 fail: PMIx sometimes binds the wildcard address
  (`deny(1) network-bind local:*:0`) after selecting lo0. PMIx in OpenMPI 4.1 has no
  unix-socket server (`PMIX_MCA_ptl=usock`: "ptl:usock not available"), so TCP is unavoidable.
- **Why it stays refused**: SBPL's localhost filter does not hold. A test program inside the
  profile, no ORCA:

  | rule | bind 127.0.0.1, 200 times | bind 0.0.0.0, 200 times | listen on 0.0.0.0, connect to the LAN address |
  |---|---|---|---|
  | `(local ip "localhost:*")` | 1-3 EPERM | all allowed | accepted |
  | `(local ip4 / tcp4 "localhost:*")` | all allowed | all allowed | accepted |
  | bind rule only, no network-inbound | | listen refused | |

  Any rule that lets PMIx listen on 127.0.0.1 also lets a sandboxed program listen on every
  interface and accept a connection that arrives on the LAN address (tested from the same Mac
  through its LAN IP). A socket audit (`lsof` each tick) would only detect, not prevent. So
  `sandboxCommand` refuses multi-core jobs on macOS; single-core jobs, whose profile has no
  network rule at all, are not affected. Reopen only with a way to confine the listener
  (pf needs root).

## 4. Server on other systems (after the clients)

- Landlock is not used by the server; the main issues are the service manager (launchd,
  Windows services), file permissions on the spool (0750/0700 semantics), disk free
  (`statfs` vs. GetDiskFreeSpaceEx) and the single-instance lock (flock vs. LockFileEx).
- The relay is plain TCP forwarding and should port easily.
