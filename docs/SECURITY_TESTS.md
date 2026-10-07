# Security and integrity tests

Results of the tests run in the VM lab (lab/README.md). Each entry gives the setup, what
happened and the fix, if one was needed. Attack harness: `lab/tests/adversarial.sh` with
the cheating client `src/client/cheat_on.go` (built only with `-tags cheat`; normal
builds contain no attack code).

## Round setup (integrity attacks, D37/D44)

- A separate test server (port 44200, spool ~/ml/adv) with every verification layer at
  100 %: SCF re-check, gradient check, Hessian probe, replication, canaries at 50 %.
- 12 small inputs: SP, Opt, Opt+Freq; HF, PBE, B3LYP with RIJCOSX, and MP2.
- The cheater (arch) connects alone first, so it produces every result. The honest
  verifiers (server and ubuntu VMs) join afterwards and run the checks. Checks never run
  on the producer.

## Control round (honest "cheater")

12/12 results accepted, every check passed, 0 false positives.

## Attack `energy`: honest run, every reported energy shifted by +2e-4 Eh (0.13 kcal/mol)

| layer | applicable | caught | relative cost |
|---|---|---|---|
| replica (recompute on another host) | 12 | 12 | 1 full job |
| SCF re-check (restart from returned .gbw) | 6 (SP) | 6 | a few SCF cycles |
| gradient at the final geometry | 9 (Opt) | 9 | one gradient |
| Hessian probe | 5 (Freq) | 0 | not applicable: the forgery leaves the Hessian intact |

All 12 forged results ended in weird/. 0 remained in output/.

**Bug found and fixed (df45555).** A failed check is confirmed on a third host that is
neither the producer nor a first-round verifier. With 3 hosts no such host exists, so
the confirmation waited, expired, and **the forged result stayed accepted in output/**
(3 of 12 in the first run). Now a failed check goes to weird/ ("unconfirmed: no
independent host") when no independent host exists, and also when its confirmation
expires.

**Conclusion so far.** For energy forgery, the SCF and gradient checks catch everything
the expensive replica catches, at a small fraction of the cost. They are the efficient
layers. Replication remains the generic fallback, for job types the cheaper checks
cannot rebuild.

## Attack `loose`: cheaper settings (LooseSCF LooseOpt), extra input line erased from the echo

| outcome | count | caught by |
|---|---|---|
| single points (LooseSCF) | 7 of 7 caught | replica (energies off by 1.5e-4 to 1.2e-3 Eh) |
| optimizations (LooseOpt) | 0 of 5 caught | none |

The echoed-input check did not notice the erased line (it compares content, not line
numbering).

The 5 loose optimizations stopped after 2 cycles with MAX gradient 2e-5 to 1.7e-3 Eh/bohr,
and their final energies match a normal optimization to < 1e-5 Eh, so the replica passes.
Honest ORCA optimizations (115 from the lab campaign) end with MAX gradient up to **2.9x**
ORCA's TolMaxG (3e-4 for NormalOpt), because ORCA also accepts convergence on small
energy and displacement changes. A loosely converged optimization is therefore mostly
indistinguishable from honest ORCA behaviour, and the scientific damage is small.

Change made: the gradient check now compares the MAX gradient at the final geometry with
4x the TolMaxG of the requested level (Loose/Normal/Tight/VeryTight; 4x clears the 2.9x
honest maximum), instead of a fixed norm of 2e-3 Eh/bohr that was far too lax for
TightOpt jobs. With the new rule, 1 of the 5 loose optimizations (c2h4, 1.7e-3) would
fail.

## Attack `copy`: no computation, the output of an earlier task returned renamed

The cheater keeps a private copy of every output (as a real cheater would) and returns it,
renamed, for the next tasks.

| layer | caught | cost |
|---|---|---|
| fingerprints ("no executed ORCA programs recorded") | 11/11 | free (passive) |
| output consistency (echoed input differs from the task input) | 11/11 | free (passive) |

The first task, computed honestly before anything could be copied, was accepted. A
cheater who also fakes the execution records and the echoed input would still return
numbers that belong to another molecule, and the replica or SCF check would catch them.

## Attack `hess`: honest run, returned Hessian scaled by 1.02 (frequencies +1 %)

A 2 % larger Hessian shifts the frequencies by ~1 % and a phenol's zero-point energy by
~0.6 kcal/mol, which matters for bond dissociation energies.

- With the original probe tolerance (0.02), 1 of 4 was caught (relative error 0.020,
  borderline): the tolerance was as large as the forgery.
- Honest probe errors measured with the honest control round: HF 0.00004, PBE 0.00056,
  B3LYP/RIJCOSX 0.00175 (numerical grids).
- New default `verify.hessian_tolerance = 0.005` (2.9x the worst honest value, now a
  configurable option). Rerun: **4 of 4 caught** (relative errors 0.019 to 0.020).
  Results without frequencies were untouched by this attack and accepted, as they should
  be.

## Attack `collude`: two cheaters (arch, void) forging every energy consistently

Both add +2e-4 Eh to every energy they report, including the checks they are given, so
a cheater verifying its partner always agrees.

**Before the fix: 6 of 12 forgeries accepted.** Two weaknesses:
1. All sub-checks of a result could land on one verifier, the partner, which approved them.
2. Confirmation hijack: the honest verifiers failed a forged result, the "independent"
   confirmation went to the partner, which passed it. The forgery was accepted and the two
   honest verifiers were penalized as "contradicted".

**Fix (verify.second_opinion, default on).** A result whose sub-checks all ran on one host
gets the cheapest sub-check repeated on another independent host. Verifiers that disagree
make a *dispute*: the result goes to weird/ with both opinions and nobody is penalized,
because a single vote no longer overrules another.

**After the fix (clean round): 12 of 12 forgeries in weird/, 0 accepted.** 7 were
disputes, 5 failed outright. The honest verifiers have 0 penalties.

## Local unprivileged user on the server machine

- Server key, server.conf and admin socket: 0600; not readable or usable. OK.
- **Bug found and fixed (744253e).** Spool directories were 0755. With the spool outside
  a private home (e.g. /srv), any local user could list input/ and read inputs. Now the
  spool is 0750 and state/ 0700, also applied to existing spools at start. Client state
  and scratch are 0700. After the fix the user gets "Permission denied".

## Restrictive firewall (client behind a carrier-grade NAT that only allows TCP/443)

The alma VM (profile P5) reaches the server through `listen = :44100, :443`; it works.

## Attack `forge`: a real output of another task, echoed input rewritten to match

- The forged echo passed the echoed-input check. The new **geometry consistency** check
  caught 11 of 11 ("the output did not start from the input geometry").
- **Bug found and fixed (fb420dc).** When the cheater acted as a VERIFIER it returned
  invalid results, which counted as failed checks of the honest producer and sent honest
  results to weird/. Now an invalid verifier result is the verifier's fault: it is
  penalized and the sub-check is reassigned (39 reassignments in the round).
- With `retry_weird` (default) the 11 forged tasks were requeued at the end of the queue,
  away from the cheater. **End state: 12 of 12 tasks have an honest result in output/**,
  the 11 forgeries are in weird/ for review, the cheater has 50 penalties and the honest
  clients 0.

## Automatic quarantine (energy attack replayed)

With `verify.quarantine_after = 3` (default) the cheater was quarantined automatically at
its third failed verification, and 9 of its other accepted results were scheduled for
re-verification on independent hosts. End state: 12 forgeries in weird/, 12 honest
re-computations in output/ (automatic retry), honest clients 0 penalties.

## Slander: a verifier that lies about honest results (`adversarial.sh run slander`)

The server VM produces 13 honest results; artix computes its own jobs honestly but adds
2e-4 Eh to the energies of every verification sub-task it recognizes, so honest results
look forged; ubuntu and void are honest verifiers.

| round | what happened | fix |
|---|---|---|
| 1 | 2 disputes rescued; others requeued (recomputed); hcn_optfreq condemned "unconfirmed" and the honest producer penalized | confirmation excluded every verifier of the check, also those that passed: with 4 hosts nobody was left. Now only the accusers are excluded |
| 1 | a rescued result landed in output/.../n2_mp2.2 | the output name comes from the task, not from the weird/ copy |
| 2 | 13/13 in output/, 3 false disputes rescued by one cheap check each, 0 recomputed, nobody penalized | the slanderer got away with it: an accuser overruled by a tie-break now counts as a failed verification |
| 3 | 13/13 in output/, 4 disputes rescued, 0 recomputed; artix quarantined automatically after its 3rd false accusation | it had no results of its own to re-check, so instead of an instant release its next 10 results (all checked) decide |

Quarantined clients no longer get verification sub-tasks of other clients' results (only
their own canaries): their verdicts cannot be trusted.

## Dodge: a cheater that is honest only on tasks it can recognize (`adversarial.sh run dodge`)

artix shifts every energy by +2e-4 Eh except on tasks that look like the server's own
(an xyz file, MORead). A first honest batch provides verified canary sources; then artix
alone computes a second batch.

- Before (slander round 1): canaries read `* xyzfile ... geom.xyz` plus a separate file,
  which ordinary inputs almost never do, so such a client could compute canaries honestly
  and forge everything else.
- Fix (2dd7a7a): canaries give their geometry exactly like the original input (inline, or
  the original's xyz file name).
- Final round (2026-10-04): 32 canary failures for artix, 0 canary passes; artix 0
  accepted, 13 in weird/, quarantined; honest clients 0 weird; its 13 tasks recomputed by
  ubuntu and void; round finished in 6 minutes.
- Found on the way and fixed: (1) a fixed `storage.min_free` of 5G refused every result
  on the 8 GB lab server (now auto, a28d5e0); (2) checks planned while a verifier was still
  connected waited up to 72 h after it left, and 4 forged results stayed accepted (only
  connected hosts count, stalled checks expire after 30 min, an expired check of a
  quarantined client's result goes to weird/: a026a03 and the next commit); (3) an orphan
  test client from an earlier round, running in a deleted directory, kept retrying an old
  credential (harness cleanup).

## Stereochemistry (real ORCA xTB optimizations)

| molecule | honest optimization | enantiomer passed off as the input |
|---|---|---|
| CHFClBr (R/S centre) | passes | rejected: opposite handedness at C1 |
| 1,3-difluoroallene (axial P/M) | passes | rejected: allene axis changed P/M |
| 2,2'-dichlorobiphenyl (axial P/M) | passes | rejected: twist sign changed |
| CH3PHF (pyramidal P) | passes | rejected: opposite handedness at P |
| C2H5FOS sulfoxide (pyramidal S) | passes | rejected: opposite handedness at S |
| 1-fluoro-2-methylferrocene (planar) | passes | rejected: opposite handedness at C2 (Fe-C counts as a bond); also by superposition |

Besides the local checks, every optimized geometry is superposed (Horn's quaternion method)
on the input and on its mirror image: if it matches the mirror image clearly better (RMSD
ratio < 0.6), the result is rejected whatever kind of chirality the molecule has. All six
enantiomers above are caught this way too (mirror RMSD 0.04-0.52 Å vs 1.04-2.45 Å); the 279
real campaign results raise no false positive.

## Mobility: a laptop on the move (lab/tests/chaos.sh)

30 minutes of random events on arch while the campaign ran: 2 power cuts, 5 suspends of
up to 172 s, 5 Wi-Fi changes (274-800 ms delay, 6-17 % loss), a signal dropout, a network
change and a client restart. The client always resumed its job and reconnected.

**Bug found and fixed.** After a network change the TCP connection could die silently: the
NAT forgot it, the client kept writing heartbeats, and Linux retransmits for ~15 minutes
before giving up (seen: 255 s with no data received, retransmission backoff 6). Now every
connection sets TCP_USER_TIMEOUT = 90 s (RFC 5482) on both sides. Test: link silently down
for 150 s; the dead connection was dropped during the outage and the client reconnected
**5 s after the link came back**.

## Windows cheater (lab/tests/winadversarial.sh, 2026-10-05)

The same attacks with the cheating client on Windows 11 (client built with -tags cheat,
AppContainer sandbox) and three honest Linux clients, every layer at 100 %:
- `none` (control): 8 of 8 in output/, 0 weird, 0 errors: no false positive.
- `energy`: 7 of 7 forged results caught (grad, SCF and replica checks), the cheater
  quarantined, 0 failed checks for the honest clients.
- `forge`: 7 caught on arrival (no ORCA run recorded, geometry not the input's).
- `copy`: 7 caught on arrival.
- `loose` (cheaper settings, echo edited): 6 of 6 caught on arrival (echoed input line
  numbers not consecutive), the cheater quarantined, honest clients 0 failed checks.
- `hess` (Hessian scaled by 1.02): the 2 jobs with frequencies both caught by the Hessian
  probe (relative error 0.020); 2 failures stay under quarantine_after = 3, as on Linux.
- `collude` (two cheaters, on Windows 11 and Windows 10, adding +2e-4 Eh to every energy and
  approving each other; the honest helpers registered but offline while they compute):
  first run, every result went to weird/ "unconfirmed" at once (no independent host was
  connected) and neither cheater was penalized. After the fix (a second opinion waits for
  a registered independent host), the honest hosts caught the forgeries when they came
  back: 0 forgeries accepted, both cheaters penalized, honest 0 failed checks; the 6
  results in output/ were all computed by honest clients (checked by their manifests), 2
  tasks left in weird/ for review.
- `slander` (an honest Linux producer; the Windows client, connected first and paused,
  lies on the checks it gets once resumed): it failed water_opt's gradient check, the
  result went to weird/ as a dispute (nobody penalized), a tie-break by an independent host
  confirmed it and brought it back to output/, and the slanderer got the failed check.
  End: 8 of 8 in output/, 0 weird, honest clients 0 failed checks.
- Control again after the canary and second-opinion fixes (`none`): 8 of 8 accepted, no
  client with a failed check.
- `dodge` (honest only on what looks like the server's checks; an honest first batch gives
  verified canary sources, then the Windows cheater computes a second batch alone): the
  canaries caught it (7 failed canaries, quarantined, 15 failed checks), its 8 forgeries in
  weird/, the honest recomputations in output/ (16). After the canary fix below, the 12
  canaries run on honest clients all passed (before it, 2 of them failed honest clients).
Found and fixed:
- **Canary false positive.** A canary is built from the geometry in the result directory
  at the time, but checked against the energy stored when the source was verified. In
  the copy round the result at that path had been replaced (the condemned result
  recomputed under the same name), so an honest client "failed" a canary by 6.4e-5 Eh.
  Sources now keep the hash of their output; a changed one is dropped from the pool.
  (Rotation noise for that molecule, B3LYP/def2-SVP, measured: 6e-6 Eh over 9 random
  orientations; the 5e-5 tolerance is fine.)
- **Canary false positive from the grid.** In the all-honest phase of the dodge round an
  honest client failed a LiF canary by 5.4e-5 Eh. Measured with ORCA (B3LYP/def2-SVP):
  random rotations move LiF's energy by up to 1.5e-4 Eh (the DFT/COSX integration grids
  are not rotation invariant), while the cube's symmetries (signed permutations of the
  axes) change it by 2e-8 Eh at most. Canaries now use those symmetries only (plus
  translation and atom order), and the tolerance went from 5e-5 to 1e-5 Eh.
- **Repeated automatic release.** A copy cheater is honest on its first task, so its
  re-check (its only accepted result) passed and it was released, then quarantined and
  released again within two minutes. Quarantined again within quarantine_window of an
  automatic release, a client now waits for the admin.

## Recommended defaults (proposal, based on the rounds above)

D44 ships only signatures and fingerprints by default. The data suggest turning on every
layer that costs no computation, and a small sample of the cheap active checks:

| layer | cost | caught in the lab | proposal |
|---|---|---|---|
| signatures + ledger | free | (always on) | on |
| fingerprints | free | copy 11/11 | on (already) |
| output consistency (echo, geometry, stereo, echo numbering) | free | copy 11/11, forge 11/11 | **on** |
| timing plausibility | free | (no attack exercised it yet) | **on** |
| unconverged optimization rule | free | (honest-error guard) | on (always) |
| SCF re-check | a few SCF cycles | energy 6/6 | **5 %** of results |
| gradient check (Opt) | one gradient | energy 9/9 | **5 %** of Opt results |
| Hessian probe (Freq) | two gradients | hess 4/4 | **5 %** of Freq results |
| replica | one full job | loose SP 7/7 | 0 (only for new clients, through probation) |
| canaries | one SP | (needs verified sources) | 0 by default |
| second opinion, disputes, invalid-verifier rule | small | collude 12/12 | on |
| automatic quarantine + forgiving release | small | quarantine test | on |

With 5 % sampling, a client that forges every result is caught after about 20 results on
average (1/0.05), and the automatic quarantine then re-checks its recent ones. The total
overhead is about 5 % x (a few SCF cycles), well under 1 % of the campaign's compute.

## Pending

Multi-core inputs (`%pal nprocs N`) are forced to one core by the client today; design
proper support (how many cores a client lends to one job, opt-out).

## Review of the 2026-10-05 features (by reading, no attack round yet)

- Multi-core jobs: the server cannot check OpenMPI's files against a reference, so a client
  could have marked any program as part of OpenMPI. Now only OpenMPI's launchers (mpirun,
  mpiexec, orterun, orted) may be marked so, and a result must list at least one ORCA program.
- Kept orbitals (checkpoints): a trusted client could poison them so that the next hosts
  fail; they are now used once and a failure after them is not counted (1bf2de4).
- orca.other_versions = separate: results of another version are not cross-checked, so a
  client could claim an old version to avoid checks. Documented in server.conf ("only with
  clients you trust") and written in each such result's verdict; refuse is the default.
