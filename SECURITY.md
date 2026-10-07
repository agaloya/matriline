# Security policy

Matriline runs calculations on computers its operators do not control and accepts their
results, so its security matters to every project that uses it. Thank you for helping.

## Reporting a vulnerability

Please report it **privately**, not in a public issue:

- On GitHub: the repository's **Security** tab → **Report a vulnerability** (private
  vulnerability reporting). Only the maintainers see it.

Include what you found, how to reproduce it (commands, configuration, the version from
`matriline-server version` / `matriline-client version`), and what an attacker could do
with it. A proof of concept is welcome; please do not run it against servers that are not
yours.

What to expect:

- an answer within **7 days**;
- a fix, or a plan, within **90 days** (sooner when users are at risk);
- credit in the release notes and in docs/SECURITY_TESTS.md, if you want it;
- we publish the details after the fix is released, together with you.

We will not take legal action against research done in good faith that follows this
policy: it stays on your own installations, does not touch other people's data, and is
reported to us first.

## Supported versions

Only the latest release receives security fixes (the project is young: there are no
long-term branches yet). Check your version with `matriline-server version`; releases and
their checksums are listed in docs/releases/ and on the GitHub releases page.

## What is in scope

- The server, client and relay (`src/`), their network protocol, the integrity ledger,
  result verification, the client's sandbox, the web page (`matriline-server web`), the
  update mechanism (signed releases) and the alerts.
- Nacomline (its own repository), which drives the same programs on one computer.

What Matriline defends against, and what it does not, is described in
[docs/DECISIONS.md](docs/DECISIONS.md); the
attacks already tried, with their results, are in
[docs/SECURITY_TESTS.md](docs/SECURITY_TESTS.md). Some limits by design, so you need not
report them:

- the web page listens only on this computer; anyone who can already act as the server's
  user on that computer controls the server;
- a client whose owner is malicious can refuse work or return wrong results: Matriline
  detects and isolates wrong results (checks, replicas, canaries, quarantine) rather than
  preventing them; reports of a way to make wrong results **pass undetected** are very
  welcome;
- builds made with `-tags nosecurity` turn the protections off on purpose (trusted
  machines only).

## Out of scope

ORCA itself (report to its authors, FACCTs / Max-Planck-Institut für Kohlenforschung), the
operating system, and third-party services such as an e-mail provider or Telegram.
3Dmol.js, built into the web page, belongs to its own project (docs/THIRD_PARTY.md); a
problem in how Matriline uses it is in scope.

## Verifying what you run

Releases are reproducible: `tools/release.sh --check docs/releases/SHA256SUMS-<commit>`
rebuilds them from source and compares, so you never have to trust a binary someone sent
you (INSTALL.md, section 1).
