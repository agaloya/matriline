# Design decisions log

Decisions agreed with the project owner during the requirements Q&A.
(Repository language: English for all code, docs and config files.)

## Environment
- D01 Language: Go. Import only what is actually used (stdlib first; every third-party
  dependency must be justified in go.mod comments). Same rule for CLI commands/tools.
- D02 ORCA 6.1.1 (current) at /opt/orca-6.1.1, invoked as `orca` and `orca-611`.
  ORCA 6.1.0 (second most recent) installed next to it (/opt/orca-6.1.0), invoked as `orca-610`.
  ORCA installs do NOT count towards the 20/60 GB development budget.
  ORCA is shared read-only into all VMs.
- D03 Single-core calculations only for now (one job per logical core, 75 % of cores
  rounded down, client-adjustable). Code must be designed so multi-core (%pal / MPI)
  jobs can be added later (slot-based scheduling, not "1 job = 1 core" hardcoded).
- D04 OPI (ORCA Python Interface, github.com/faccts/opi) kept in referencias/opi as a
  reference only. Anything consulted or inspired by it (or any other work) is cited in
  the code, at block level.
- D05 Sudo granted temporarily for setup; revoked afterwards and verified revoked.

## Networking
- D06 Clients always dial out; no port forwarding needed on clients.
- D07 Default deployment mode: connection through a relay with a public IP (relay only
  forwards end-to-end encrypted streams; it cannot read them). Alternative mode: direct
  connection to the central server with ONE forwarded port. All current tests use the
  direct mode.
- D08 Port is configurable; a default port not registered for another service is chosen.

## Security
- D09 Encryption (TLS 1.3, mutual auth) on by default. Options: (a) encrypted+authenticated,
  (b) authenticated+integrity only (HMAC per frame, replay protection, plaintext payload),
  (c) fully off. Every option is documented with instructions/warnings in the config files.
- D10 Enrollment is customizable on the server: (a) admin issues keys/credentials to users,
  (b) users generate their own keys and register with a token + admin approval,
  (c) open enrollment. Easy revocation. Tests use (a).
- D11 All network-policy settings (encryption mode, enrollment mode, relay/direct, ...) live
  ONLY on the server. The client learns them during the handshake; nothing must be
  configured twice or "match" on both sides. The client cannot change them; it can only
  configure its own local resources/preferences, and the server cannot force anything
  outside the client's local options.

## Integrity verification (to be discussed in detail - main problem)
- Candidate layers, none discarded: binary fingerprints (all ORCA executables actually
  exec'd), output parsing (version, echoed input, expected modules), cheap recomputation
  (1-iteration SCF from returned .gbw, gradient check at final geometry), canary tasks
  (known answers, randomly rotated/translated), random replication, reputation/quarantine.

## Round 3-4 (2026-10-03)
- D12 Default port: 44100/TCP (verified unassigned in the IANA registry). Fully configurable.
  Default mode is DIRECT connection to the central server (tests use it too); the relay
  mode is fully implemented as an option.
- D13 Repository layout: root holds at most 10 entries. Code under src/{client,server,relay,common};
  lab/ (VMs, NAT profiles, capture), docs/, references/, trash/ (reviewed by owner before
  deletion), bitacora/ (the only non-English content).
- D14 Lab: 5 NAT/firewall profiles from easiest to most restrictive, rotated across ALL nodes
  (server, relay, clients) and tested pairwise:
  P1 public IP, no NAT; P2 permissive (full-cone) NAT + port forwarding allowed;
  P3 typical home router (restricted NAT, stateful firewall drops unsolicited inbound);
  P4 symmetric NAT + strict firewall + 60 s idle timeout;
  P5 CGNAT (provider shares one public IP among several homes) + home NAT, egress TCP/443 only.
  Fault injection (latency/loss/cuts) on the router.
- D15 Must work for every job type supported by the installed ORCA version.
- D16 Results: ALL files produced by ORCA are returned by default. What is returned is
  configured on the SERVER only (clients cannot configure it). Same for run metadata.
- D17 Run metadata report (it is an output file): simple key=value text, very light. Contains
  only what ORCA's output does not (ORCA already prints version, GIT hash, start time,
  host name, working dir, per-module max memory and timings). Fields: end time, wall vs CPU
  time, peak RSS, CPU temperature and frequency, load of other processes, scratch disk
  peak, OS/kernel, CPU model, total RAM, dedicated cores, agent version, ORCA fingerprint,
  exit code, connection interruptions (count, durations, RTT, packet loss, reconnects),
  power-loss/resume events, clock offset. Sampled series are reported as statistics
  (min/mean/max + percentiles configured on the server, e.g. p50/p90/p99). Values that
  cannot be measured are reported explicitly with a reason (e.g. `cpu_temp=NA:no_sensor`).
  Future: client-side temperature threshold that stops accepting new tasks.
- D18 No task chaining and no input generation: the system only takes ORCA inputs and returns
  outputs. Multi-step work is the input author's job ($new_job, %compound).
- D19 Server spool directories: input/, output/, cancelled/, paused/, weird/, errors/.
  Sub-directories are allowed and mirrored: input/listA/mol123.inp ->
  output/listA/mol123/<all output files incl. metadata report>. cancelled/ and paused/
  hold input files with the same mirrored structure; weird/ holds outputs whose integrity
  is doubtful; errors/ holds failed jobs.
- D20 Server CLI: one simple command-line program, every action works on a single file or a
  whole directory: add tasks (from any directory into input/ or a sub-dir), status,
  priority (single file or next batch), cancel/uncancel, pause all/one, resume, backup all
  spool dirs to another location, restore a backup, hot-edit config (with warnings),
  key/client management (issue, revoke, list, quarantine, drain), system actions.
  Storage limit (default unlimited) + warning threshold. Configurable e-mail alerts.
  Implemented as internal Go functions, not shell scripts. One cross-platform code base;
  OS-specific parts isolated with build tags (Linux now; macOS/Windows later).
- D21 Task order: random by default; alphanumeric optional; directories can be prioritized.
- D22 Local web GUI postponed (later: simple, elegant; JSmol tab to view molecules).
- D23 `edit <path>` command (nano, paths relative to the spool root, with warnings). The
  integrity chain must also reveal modifications of outputs made on the server itself.
- D24 Default task order is alphanumeric (natural sort, recursive); random optional. Finished
  inputs move to completed/. Empty dirs are pruned in all spool dirs except input/ and output/.
  No CSV export. Web GUI is built alongside but only as a front end to the CLI functions.
- D25 Requirements are kept organized by module (in the development repository).
- D26 VM OSes: router Alpine; server Debian 13; hosts Arch, Void Linux (glibc, runit -> service
  must be init-system portable) and Ubuntu Server (+ AlmaLinux pending owner choice).
- D27 Packet captures do NOT count towards the 20 GB budget, but warn the owner if the 20 GB
  budget is being exceeded. No TLS key export for captures.
- D28 Hosts: Arch (2c/3G), Ubuntu Server (4c/8G), Void glibc (6c/10G) + 4th small host
  AlmaLinux (1c/1.5G) to test the 1-core edge case (75 % rule gives 0 slots -> handle it).
  Packet capture STOPS when it reaches 6 GB.
- D29 Client resource handling: client rewrites %maxcore / nprocs within its OWN limits
  (dedicated RAM / concurrent jobs, with safety margin); rejects tasks needing more than it
  offers; watches scratch disk (pause + report when full).
- D30 Liveness: client sends a heartbeat every 60 s; a task with no progress report for 1 h
  is reassigned (both configurable on the server). On reconnect the client reports finished/
  running jobs. After power loss the job restarts from its last ORCA checkpoint (.gbw,
  last geometry). When the queue is empty, running tasks are duplicated on idle hosts
  (helps with very slow machines); first valid result wins.
- D31 4 helper hosts, same TOTAL resources as originally specified (12 cores / 21 GB),
  redistributed (proposed, pending confirmation): Ubuntu Server 1c/1.5G (1-core edge case),
  Arch 2c/3G, AlmaLinux 3c/6.5G, Void glibc 6c/10G.
- D32 The central server ALSO computes from day one, by installing the regular client on the
  same machine (separate program, same code as any other client).
- D33 Client local option defaults (supersedes the 75 % rule of D03 as the default):
  cores = n-1 logical cores; RAM = 1 GB per core; alert if the machine cannot cope (RAM or
  disk); dedicated disk unlimited; bandwidth limit 10 Mbit/s; schedule windows; pause when
  user active / on battery; max time per task; ONE server only (no multi-server);
  end date after which the service disables itself (default never).
  Log verbosity is NOT a client option: always the most detailed level the server requests.
- D34 Claude operates the lab VMs itself for testing.
- D35 Hosts (final): Void glibc 1c/1.5G (lightest distro; uses its single core but warns by
  default), Arch 2c/3G, AlmaLinux 3c/6.5G, Ubuntu Server 6c/10G (most resources).
  Default n-1 cores, but a 1-core machine uses its core and shows a warning.
- D36 Test workload must be scientifically interesting and varied (research-driven campaign,
  e.g. data-driven molecular design with RDKit descriptors); to be discussed with the owner.
- D37 Adversarial test suite: everything imaginable, done once a working model exists.
- D38 Use GitHub from the start.
- D39 Project name: **Matriline** (protocol + programs). Verified unused on GitHub/PyPI; avoids the
  "ORCA" trademark.
- D40 Science campaigns: no fixed pilot size (as many jobs as needed). Campaign idea 4 (bond
  dissociation energies / antioxidants) approved for testing; ideas 1 and 3 recommended.
- D41 GitHub repository is private for now.
- D42 License: AGPL-3.0-only + section 7(b) attribution term (docs/NOTICE) + CITATION.cff; Zenodo DOI per release later.
- D43 Input safety: by default NO external programs may run. "Internal" = every executable
  shipped inside the selected ORCA installation (orca_*, otool_xtb, otool_gcp, openCOSMORS, ...),
  all fingerprinted. Rejected by default: ExtOpt/ProgExt, interfaces to external codes, user
  scripts, paths outside the job directory. Layers: client-side allow-list parser, OS sandbox
  (unprivileged, no network, read-only FS outside scratch), hard limits (time, memory,
  processes, output size). A future server option may allow specific external programs, but
  each client must opt in locally (the server cannot force it).
- D44 Integrity verification: every layer implemented and configurable on the server, each
  documented in the config (what it does, why, cost). DEFAULT for users: only signatures
  (signed result manifests, append-only hash chain) and process/binary fingerprints.
  For OUR tests: be distrustful but compute-frugal; Claude picks the test configuration,
  attempts to cheat the server from a host with each attack, and measures which layer is
  most effective per unit of compute. Isolated failures are tolerated but the result goes
  to weird/ (policy to be tuned from test evidence).
  Layers: fingerprints, output-consistency parsing (version, GIT hash, echoed input,
  module sequence, plausible timing vs host benchmark), 1-Fock-build SCF check from .gbw,
  gradient-at-final-geometry check, random-direction Hessian check, canaries (rotated/
  translated/reordered), random replication on a different client, reputation/probation/
  quarantine with re-verification of past results, signed manifests + hash chain on server.
  Future option: TEE attestation (SEV-SNP/TDX).
- D45 Observation: ORCA 6.1.0 and 6.1.1 give bit-identical energies for HF/STO-3G water -> version cannot be detected from numbers; rely on GIT hash (6.1.0: 679e74b, 6.1.1: 487d211c) and binary fingerprints.
- D46 Helper host diversity (2026-10-04): arch -> Artix Linux (dinit + ConnMan) and alma -> Devuan 6 (sysvinit + ifupdown, dhcpcd as DHCP client), so the compute hosts cover systemd, runit, dinit and sysvinit and four network stacks (networkd, dhcpcd, ConnMan, ifupdown). Same vCPU/RAM/disk, index, IP plan and NAT profile (artix P4, devuan P5); both built in a throw-away builder VM (no official cloud images). Supersedes the Arch/AlmaLinux part of D26/D35.
- D47 Enrollment (2026-10-04): `keys issue` produces a one-time, name-bound ticket (default
  48 h, `network.credential_valid`; the registry keeps only its SHA-256). On first contact the
  client creates its device key (state/client.key, never leaves the computer), the server
  binds it to the name and burns the ticket. A credential copied later is useless; a ticket
  used by someone else first is reported to the legitimate device and alerted ("enrolled").
  Credentials with an embedded key (older servers) stay valid until revoked.
- D48 Automatic IP bans (2026-10-04): peers that never speak the protocol: 10 within 1 h ->
  7 days; peers that speak it but are not admitted: 20 within 24 h -> 24 h; authenticated
  reconnections never count; an address from which an admitted client connected within 24 h
  is never banned automatically (shared NAT); relayed connections never banned; IPv6 per
  /64. All in `[network] ban_*`; `matriline-server bans [lift]`.
- D49 Disk protection (2026-10-04): the server keeps `storage.min_free` free (auto: 5 % of the disk, at most 5G; a fixed 5G refused every result on the 8 GB lab server), counting
  uploads in progress; `results.max_bytes` defaults to 2G. The client's per-file limit in
  the sandbox is its own `limits.max_file_size` (unlimited), never the server's max_bytes
  (ORCA temporaries of several GB are never returned).
- D50 Reproducible builds (2026-10-04): releases are built by tools/release.sh with the
  official toolchain pinned through GOTOOLCHAIN (distribution builds differ), -trimpath,
  empty build id, -buildvcs=false and the last src/ commit stamped; SHA-256 per target in
  docs/releases/. Verified across two hosts. Binaries are never trusted because of who sent
  them: rebuild and compare.
- D51 Development cycle (user, 2026-10-04): each version = 1) service, 2) terminal and HTML
  interfaces, 3) Nacomline and its interfaces. Network tests before MPI; the live view near
  the end; ports: client (Windows, macOS) before server.
- D52 Interfaces (2026-10-05): one command catalogue (src/server/commands.go) produces the
  help text, the interactive `console` and the `web` page; both only build a command line and
  run it like the command line does (requirements section 3: the web is a mask of the
  commands). The web page binds to loopback only (SSH tunnel from elsewhere), needs the token
  printed at start (cookie SameSite=Strict plus a token field in every form), checks the Host
  header against DNS rebinding, uses no JavaScript and no external files (strict CSP), and
  leaves out what needs a terminal or would stop the server under it. Version mismatch
  between the interface and the running server is shown as a warning.
  After an independent review: three separate secrets (login link, session cookie, form
  token), sessions end after 12 h, and on Linux a random 127.x.y.z address by default,
  because a browser sends a 127.0.0.1 cookie to every port, including another local user's
  web page. Shown command lines are shell-quoted; config saves are serialized.
- D53 ORCA 6.1.0 removed (user, 2026-10-05): it only simulated "an unexpected ORCA
  version" in the lab (D45 tests, done) and took 20 GB. Version and build mismatches are
  still covered by altered copies and the arm64 build. Clients list only /opt/orca-6.1.1.
- D54 Multi-core (MPI) jobs, opt-in on both sides (2026-10-05): server tasks.parallel =
  honor, client resources.max_cores_per_job > 1 with a usable OpenMPI 4.1.x (fingerprinted
  like ORCA). In the sandbox: the helper keeps CAP_NET_ADMIN only to bring lo up and then
  drops every capability; TCP is allowed only inside a network namespace whose only
  interface is lo (MPI's launcher needs it); OpenMPI's session directory and all shared
  memory live in the work directory. The manifest marks OpenMPI programs (mpi = true, with
  the OpenMPI version and tree hash); the server accepts them only for jobs with nprocs >= 2.
  Measured on devuan (wB97M-V/def2-TZVP SP of phenol): 2 cores 29 s, 1 core 52 s (1.79x),
  energies identical to 1e-12 Eh. The earlier 1.25x (r2SCAN-3c) shows the gain depends on
  the method; independent single-core jobs remain the default.
- D55 Zero-configuration clients and memory pool (user, 2026-10-05): 'keys issue ...
  --preset settings.conf' puts suggested client settings (client.conf format) into the
  credential; 'matriline-client init <dir> --credential <file>' installs it, applies only
  settings that exist and never client.* or security.* (the owner's files and sandbox stay
  the owner's, D27), and finds ORCA (PATH incl. wrapper scripts, /opt, home, Windows and
  macOS install places) and OpenMPI. resources.memory_total (alternative to
  memory_per_core): one pool for all jobs, equal shares by default; a task known to need
  more per core, or a multi-core one, takes more while free; task requests carry the free
  pool and the server assigns only what fits.
- D56 Campaign ORCA version and how clients' ORCA is checked (user, 2026-10-05):
  orca.version is the campaign's version, set before the first inputs; state/campaign.json
  remembers it and a change is warned about (log, alert "orca_version", ledger, number of
  results computed with the old version). orca.check = fingerprint (default: the admin puts
  the ORCA build of every OS/architecture the clients use in reference_paths or
  accepted_fingerprints; every program a result ran must belong to one) | version (the admin
  trusts the installations: only the version/build in the manifest and in each output are
  checked). Kept orbitals are used once; an ORCA failure after them is not counted (1bf2de4).
- D57 Clients without the campaign's ORCA version (user, 2026-10-05), orca.other_versions:
  refuse (default: no tasks, the client is told which version to install, alert
  orca_version, a line in status) | separate (it computes with its own version; the result
  completes the input but lives in other-versions/<version>/output|weird|errors, never in
  output/) | errors (kept for reference in errors/other-versions/<version>; the input stays
  queued for the campaign's version). Results of another version are never cross-checked
  and outdated clients get no verification sub-tasks (a check by another version would
  compare two different programs). Before this, such a client kept asking for tasks and
  rejecting each one ("ORCA X is not installed") with nobody told.
- D58 No-security build (user, 2026-10-05): "go build -tags nosecurity" (common/build)
  turns off every integrity check on the server and the sandbox and process audit on the
  client, for one's own trusted machines. TLS and client keys stay (they cost nothing and
  keep strangers out). The version text says so, the client declares it in its offer, and
  a normal server gives such clients no task unless network.accept_nosecurity_clients.
  Releases (tools/release.sh) are never built with it.
- D59 Learned scheduler: earliest finish and scale (user, 2026-10-05). Among the 50 longest
  candidates of the top priority level, a job is held back for a faster client when that
  one (online, active, accepting, able to run it: memory, cores, not avoided) would finish
  it earlier counting the time until it has a free slot; held jobs add to that client's
  queue; otherwise LPT as before. Scale: samples are read from output/ once (newest 20000
  kept) and one is added per accepted result; refit after 10 results or 2 % of the samples.
  Measured: fit on 20000 samples 4.9 ms; one pick among 2000 queued tasks with 50 clients
  and 400 running attempts 0.36 ms. Still off by default (tasks.scheduler = order).
- D60 Where every task is, and logs of a reasonable size (user, 2026-10-05). An input
  name is in one place at a time: input/, paused/, cancelled/ or completed/ (completed/
  together with its result in output/, weird/ or errors/; completed/ and input/ together
  only while a recompute is pending, i.e. the task's last ledger entry is requeue or
  retry_weird). `check` (and every hour, a minute after start) compares the ledger with
  the disk: a task queued once and found nowhere (input or result copy) is reported once
  and recorded as "lost" in the ledger; duplicates are reported until solved; findings go
  to state/events.log and the "tasks" alert. state/events.log is the admin's short story
  of the project: one line per admin command that changes something ("admin ...") and per
  thing the system noticed ("system ...": results, alerts, inputs removed by hand, check
  findings), about 70 bytes per task: 100000 molecules stay under 10 MB. server.log (the
  technical log, ~560 B per task) now rotates at 10 MB (one old file kept). The ledger
  stays complete and unrotated (~2.8 KB per task, mostly the hash of every result file;
  ~285 MB for 100000): it is the integrity record. `redo <output/...|completed/...>`
  computes an accepted task again: the old result and its completed/ input move to
  outdated/<same path>[.N] (user), left out of the totals (the task counts where it is
  now). The status chart (terminal and web) shows every state: queued, running, output,
  weird, errors, other ORCA versions, paused, cancelled.
- D61 Smaller ledger, same check (user, 2026-10-05). A result entry no longer repeats the
  hash of every file the client's signed manifest already lists with the same content: it
  keeps the manifest, matriline.names and the server's own files, and names the manifest
  (from_manifest; absent in older entries, so their hashes and signatures are unchanged).
  verify rebuilds the full list from the manifest: a changed file, a changed manifest or
  names file shows as MODIFIED, and a manifest that went missing while a slim entry relies
  on it as MANIFEST MISSING (an integrity problem, also in weird/ and errors/). Tested
  file by file (ledgerfiles_test.go). Changing orca.version or results.include later does
  not matter: each manifest lists what its client actually returned. Measured on the real
  server (laptop client over the internet, bf802d104cde): a result entry 902 bytes instead
  of ~2.5 KB, and verify passes on a spool mixing old (full) and new (slim) entries.
- D62 Empty folders (user, 2026-10-05; replaces the D24 exception for output/). A
  sub-directory left empty when its last task or result moves on is removed in every
  spool directory except those in spool.keep_empty_dirs (default: input, the admin's own
  structure for new batches). Also covers output/, outdated/ and other-versions/.
- D63 Multi-core jobs on Windows (MS-MPI) (2026-10-05). ORCA for Windows runs its parallel
  modules through Microsoft MPI 10 (runtime msmpisetup.exe; mpiexec.exe and smpd.exe in
  MSMPI_BIN). The client finds and fingerprints it like OpenMPI (orca.mpi_path = auto:
  MSMPI_BIN, %ProgramFiles%\Microsoft MPI\Bin) and offers multi-core jobs when
  max_cores_per_job > 1; the server accepts msmpi-10.* and mpiexec/smpd as MPI launchers.
  MS-MPI's process manager cannot start in an app container (smpd dies with 0xC0000142
  while loading its libraries, MS-MPI 10.1.1, Windows 11), so **a multi-core job on
  Windows runs at low integrity**: it can write only in its job directory, but it is not
  cut off from the network (single-core jobs keep the app container). The job's sandbox
  level is reported as such. Tested: a 2-process B3LYP optimization, accepted, energy equal
  to the Linux 2-process run within 3e-9 Eh. Found on the way: the process audit followed
  parent process ids, which Windows reuses (a server started at boot "became" a child of
  a job and the job was stopped); it now asks the job object for its exact members; and
  Windows' console host (System32\conhost.exe, started for console programs without a
  console: clients started at boot, MS-MPI) is allowed, its children still audited.
- D64 One result per task (user, 2026-10-06). When a task gets an accepted result, its
  earlier results in weird/ move to outdated/ (with their matriline.verdict, so the
  evidence stays; one a running check still reads is left alone). Accepting a weird result
  by hand replaces the task's result in output/, which moves to outdated/. weird/ then
  holds only what still needs a decision. Moves are recorded in the ledger ("outdated")
  and verify follows them. Also (user's decision): security.orca_deep_check_interval is
  implemented: the client re-hashes ORCA from scratch every interval (24 h) and stops
  taking jobs if the tree changed since start (per job, programs are matched by path
  against the start-up fingerprint; their contents are not re-read).
  Refined after code review: earlier weird results move only once the new result
  is final (no check planned on it, or its check passed: a condemned new result leaves the
  old one as the possibly honest answer); an accept (by hand, or by a passed tie-break)
  refuses when the task's current result is being checked or cannot be moved, so output/
  never holds two results of one task, and it drops a queued retry of that task; results
  are matched to their task by the manifest (the input copy only as fallback).
- D65 Disabling a client keeps its key (user, 2026-10-06). `clients disable <client>
  [reason]` suspends a client remotely (project over, or the owner asked): it returns its
  tasks, deletes its job data and stays off (state/DISABLED), offline clients at their
  next connection (the admin is told). Its key and credential stay, so `clients enable`
  on the server plus `matriline-client enable` on the computer bring it back. `--wipe`
  also deletes the key and credential (the earlier behaviour); such a client needs a new
  credential. Keys are never shared: a credential for N computers is an enrollment ticket,
  each computer creates its own key, so revoking or disabling one never cuts off another.
- D66 Signed updates (user, 2026-10-06). A release is the binaries plus release.json
  (version, commit, date, SHA-256 of every binary) signed with the maintainer's ed25519
  key (src/relsign: keygen, sign, verify). The public key is built into the programs
  (src/common/release/keys.go); the private key never sits on GitHub or on a server, so
  neither a stolen GitHub account nor a compromised Matriline server can make any computer
  install a program the maintainer did not sign. Without a key built in, nothing ever
  updates. The server (update.mode: off by default, alert, auto) looks once a month
  (update.check_interval) at the latest GitHub release; alert tells the admin, who runs
  `update apply` when it suits; auto applies it. Applying requires every client to be
  able to update (it reports its platform and whether it would; revoked, disabled and
  clients unseen for 30 days do not count) and a binary for each platform, otherwise the
  admin is told who holds it back. Clients go first: the server relays the signed release,
  each client checks the signature itself, stops taking jobs, and once none is left
  downloads its binary from the release (checked against the signed SHA-256), replaces
  itself (the old one stays as <program>.previous) and restarts (exec on Unix; on Windows
  a new process on the same console). When no connected client runs the old version, the
  server does the same; clients that were offline get the release when they come back.
  Clients can refuse updates (security.updates = false), which blocks the rollout and is
  reported. Releases are only downloaded from the release page (not sent by the server):
  one path, and the server never handles client binaries. Tested end to end with real
  processes (lab/tests/selfupdate.sh on Linux, selfupdate-win.sh on Windows 10 and 11 with both
  programs as scheduled tasks): a release signed by another key refused; the client, then
  the server, restarted into the new version and kept working; verify consistent.
  Hardened after two security reviews: clients download only from their own
  security.update_url (GitHub by default), never from an address the server sends; the preflight runs
  "version --plain" (no configuration, no connections); versions are strictly X.Y.Z; files
  come from the version's own folder (download/v<version>/), never "latest", so a newer
  release published meanwhile cannot break a rollout; update.url must be https (or this
  computer); a signed release expires 120 days after it was signed (no replay of old
  releases; the maintainer re-signs if needed); pending and quarantined clients do not hold
  a rollout back, `update apply --force` does not wait for any; the server never installs a
  release that is not newer than itself. Before replacing anything the new program is run
  once ("version") and must report the release's version: if it does not run, it is not
  installed and never tried again on that computer. Only a real run that fails (or reports
  another version, or is built for another processor) is such a verdict; a program that
  could not be started, timed out or vanished is tried again later. A client reports a
  refusal (again at each re-offer, until sent); it counts only from a client that was
  offered that version, only for that client: the rollout waits for it and the admin is
  told; `update apply --force` goes on without it (third review: one client must never
  cancel a release for everyone). This replaced a
  start-counting probation with automatic rollback, whose state files had too many ways to
  fail (second review). On Unix the old program is hard-linked to .previous and the new one
  renamed over it in one step (never a moment without a program); .previous is the manual
  way back. Failed downloads wait 1 h, 2 h, 4 h ... (a day at most), on the client in the
  background; interrupted downloads are cleaned at start; a program restarted by an update
  waits for the old one's lock. What clients report (output lines for the live view,
  notices, platform, version) is reduced to printable ASCII and capped.
- D67 Interfaces and automation round (user, 2026-10-06). Web page: tabs per command group
  (the long menu went), settings as a form generated from the file itself (fields with
  their short explanation and "more", restart marks, check/save/save and close/cancel, a
  restart button; the text editor kept), add with input/'s folders, a new sub-folder and
  file upload (choose or drop, still no script), client lists in client fields, a
  'restart' command; the page is a separate program and keeps working while the server
  restarts. Several web pages on one computer use the next free port and a cookie per
  port. help <command> explains each command (arguments, notes); long output is paged
  in a terminal ($PAGER, less -FRX or more). Alerts: every alert has its own line in
  [alerts], off, now (the same alert at most once per now_limit) or summary, and the
  summary has a schedule in plain words (hourly, every 6h, daily 08:00, weekdays 08:00,
  mon,thu 09:00 ...), optionally also when nothing happened ("all is well" with the
  project's state); older files (events, digest) keep their meaning; client_lost and campaign_done, documented but never raised, now are. No
  network API (no new port): the folders, --json on status/live/clients/review/history/
  events, and [hooks] result/done programs (run one at a time, without a shell) make it
  scriptable from any language. verify.same_host lets checks run on the producing
  computer, for Nacomline's optional re-checks (faulty hardware, never cheating). Server
  and client wait up to 20 s for the lock of an instance that is ending (a service
  removed and installed again at once on Windows left no client running).
- D68 3D view and colours (user, 2026-10-06). The live page links a 3D view of the job's
  molecule drawn with 3Dmol.js 2.5.5 (BSD-3-Clause; src/server/web3d with its license and
  provenance; npm package checked against its published sha512). It is built into the
  program and served by the local page: nothing is fetched from the Internet and no
  molecule leaves the computer (embedding another web site would send the user's
  structures to a third party). It is the only page with scripts, its own files only
  (no inline code, no eval). Writing our own viewer would be a project of its own;
  JSmol is several MB. Web colours: [web] palette A (default) or B, five colours each
  replaceable in server.conf; dark marks what is selected or usable, light the rest.
