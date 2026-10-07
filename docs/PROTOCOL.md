# Matriline Wire Protocol (MWP/1) and architecture

Status: draft 1 (2026-10-03). Normative words MUST/SHOULD follow RFC 2119.

## 1. Components

| Program | Directory | Role |
|---|---|---|
| `matriline-server` | `src/server` | Central coordinator daemon + admin CLI (same binary). Owns the spool, the journal and the integrity ledger. |
| `matriline-client` | `src/client` | Helper-host agent. Runs ORCA jobs in a sandbox, reports telemetry and signed results. Also installed on the server machine. |
| `matriline-relay`  | `src/relay`  | Optional public rendezvous that splices client<->server TCP streams it cannot decrypt. |
| shared library     | `src/common` | Wire framing, security handshake, manifests, ORCA parsing/fingerprints, config parser. |

Single Go module (`src/go.mod`), standard library only unless a dependency is justified in `go.mod`.
OS-specific code lives in `*_linux.go`, `*_darwin.go`, `*_windows.go` files (build tags).

## 2. Identities and keys

* Every node has an Ed25519 key pair. Its identity is `ml1-` + base32(SHA-256(pubkey))[:26].
* The server key is pinned by clients (fingerprint in the client credential file).
* Enrollment modes (server setting `enrollment.mode`):
  * `issued`  - admin runs `matriline-server keys issue <name>` and hands the produced credential
    file (client private key + server fingerprint + address) to the user. Default for tests.
  * `register` - client generates its key and connects with a one-time join token; it stays
    `pending` until `matriline-server clients approve <id>`.
  * `open` - any key is accepted (warning in config).
* Revocation: `keys revoke <id>` takes effect immediately (open sessions are closed).

## 3. Connection setup

All integers big-endian. Transport: TCP, default port 44100.

```
C -> S  ClientHello  : magic "MWP1", version u8, client_nonce[32], client_id (or empty)
S -> C  ServerHello  : magic, version, mode u8 {1=tls,2=auth,3=none}, server_nonce[32],
                       server_pubkey[32], sig = Ed25519(server_key, "mwp1-hello"||client_nonce||mode||server_nonce)
```
The client verifies `sig` against its pinned server key, which prevents downgrade attacks on the
mode (the mode is decided ONLY by the server, D11). Then:

* `tls`  (default): both sides run TLS 1.3 over the same socket. Certificates are self-signed and
  carry the node's Ed25519 key; each side verifies the peer key (client: pinned server key;
  server: authorized client registry). Every record is AEAD-protected.
* `auth`: X25519 ephemeral exchange signed with both Ed25519 keys over the transcript; HKDF-SHA256
  derives two HMAC keys. Every frame carries a 64-bit sequence number and an HMAC-SHA256 tag
  (truncated to 16 bytes). Payload is plaintext; forgery and replay are impossible.
* `none`: no protection at all (strong warning in config). Client identity is still asserted.

## 4. Framing

After setup the stream is a sequence of frames:

```
u32 length (of type+payload, max 1 MiB) | u8 type | payload
```
Control messages are compact JSON objects (small, debuggable). File data is sent as raw
`CHUNK` frames, deflate-compressed per file (level configurable), so bulk data has no
JSON/base64 overhead.

## 5. Messages

| Type | Dir | Purpose |
|---|---|---|
| `AUTH` | C->S | identity proof / join token |
| `WELCOME` | S->C | session policy: heartbeat interval, task timeout, result-file policy, metadata fields + percentiles, accepted ORCA fingerprint sets, log level, verification requests |
| `OFFER_RES` | C->S | client capacity: slots, RAM/slot, scratch, ORCA installs found (version, GIT hash, tree fingerprint) |
| `RECONCILE` | C->S | after (re)connect: attempts running / finished-not-acknowledged |
| `GET_TASK` | C->S | n free slots |
| `TASK` | S->C | task id, attempt id, lease ttl, resource needs, input file manifest, followed by CHUNKs |
| `TASK_ACCEPT` / `TASK_REJECT` | C->S | reject with reason (local policy, safety, resources) |
| `HEARTBEAT` | C->S | every 60 s: running attempts + progress counters, load, temps |
| `CANCEL` | S->C | stop an attempt (client may finish its local cleanup) |
| `RESULT` | C->S | signed result manifest, followed by CHUNKs per file |
| `RESULT_ACK` | S->C | receipt hash recorded in ledger; client may delete local copy |
| `NOTICE` | both | warnings, drain request, policy update |
| `BYE` | both | orderly close |

The server can only *request*; the client checks every request against its local options and
answers `TASK_REJECT`/`NOTICE` when it is outside them.

## 6. Task lifecycle (server)

Spool root contains `input/ output/ completed/ cancelled/ paused/ weird/ errors/`.
A task is identified by its path relative to `input/` (e.g. `1list/mol123.inp`).

```
input/ --assign--> running --result ok--> output/<rel-dir>/<stem>/*  + input moved to completed/
                           --verification doubtful--> weird/<rel>/<stem>/* (input copy included)
                           --ORCA failed on 2 distinct hosts--> errors/<rel>/<stem>/*
           pause  -> paused/<rel>     cancel -> cancelled/<rel>
```
Empty directories are pruned everywhere except `input/` and `output/`.
Order: natural alphanumeric (default) or random; explicit priorities override.
Leases: a connection heartbeat every 60 s; an attempt with no progress report for 1 h is
reassigned. When nothing is queued, running tasks may be duplicated on idle clients; the first
verified result wins and the others are cancelled.

## 7. Integrity

* Result manifest: task id, attempt id, input SHA-256, ORCA version + GIT hash, list of executables
  actually executed with their SHA-256, every output file (name, size, SHA-256), metadata report
  hash, timestamps; signed by the client key.
* Server ledger (`ledger.log`): append-only hash chain; each entry = manifest hash, verdict,
  previous entry hash, server signature. `matriline-server verify` re-hashes the spool against it,
  so tampering with outputs on the server itself is detected (also records `edit` operations).
* Optional verification layers (server policy, see config): output consistency, 1-iteration
  SCF check from .gbw, final-geometry gradient check, random-direction Hessian check, canaries,
  replication, reputation/quarantine.

## 8. Relay mode

The server keeps an authenticated control connection to the relay (`REGISTER server_id`, signed).
A client connects to the relay and sends `ROUTE server_id`; the relay asks the server to open a
data connection, then splices both TCP streams. The full MWP handshake (sections 3-5) runs end to
end through the splice, so the relay sees only lengths and timing.

## Lost hosts, reassignment and duplicate results (2026-10-04)

- **Lease.** A running attempt is kept alive by the client's heartbeats (every
  `tasks.heartbeat_seconds`, 60 s) listing it. With no report for `tasks.task_timeout`
  (1 h) the attempt is marked lost and the task is offered to other clients. A suspended or
  powered-off laptop sends nothing, so it keeps its tasks for up to an hour; its ORCA is
  frozen, not dead, and resumes with the computer (or from its checkpoint after a reboot).
- **The lost host comes back** (heartbeat or reconnect lists the attempt):
  - task still queued and not reassigned: the attempt is simply alive again;
  - task reassigned and running elsewhere: both copies are compared by progress (the size
    of their output so far, comparable for the same input); the one further ahead keeps
    it and the other is cancelled ("another host is further ahead");
  - task already completed by another host: a copy still running is cancelled (no CPU
    wasted); a copy already finished is allowed to upload and is compared (below).
- **Two results for one task.** The first valid result is stored; the check and the store
  happen under one lock (`fileResult`, moveMu), so two near-simultaneous uploads can never
  both be stored or overwrite each other. A later result from an attempt the server really
  assigned for that task is compared with the accepted one: within
  `verify.energy_tolerance` it counts as a free verification ("free replica" in the
  ledger); otherwise the accepted result gets a replica on a third host and the usual check
  path decides. Results for unknown attempts are discarded unread.
- Tested end to end on loopback (a frozen client standing in for a suspended laptop):
  the returning host further ahead kept the task and the newer copy was cancelled; a copy
  finished while its client was frozen arrived late and agreed (0.0e+00 Eh).
