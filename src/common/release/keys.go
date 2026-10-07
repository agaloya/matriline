package release

// TrustedKeys are the ed25519 public keys (base64) allowed to sign releases. The private
// key stays with the maintainer, off GitHub (relsign keygen prints the line to put here).
// Empty: programs built from this source never update themselves.
var TrustedKeys = []string{
	"8f31cOW+CzTwz25/UaGn/rGO7U7xvJP+Sk/MaOT7Ia8=", // Adrián Gallardo Loya, 2026-10-06 (~/.keys/matriline-release.key)
}
