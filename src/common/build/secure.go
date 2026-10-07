//go:build !nosecurity

// Package build tells whether this binary was built without its security layers.
package build

// NoSecurity is true only in binaries built with "-tags nosecurity" (trusted networks):
// the server then skips every integrity check and the client runs ORCA without sandbox
// and without process audit. Release builds never have it (tools/release.sh).
const NoSecurity = false

// Label is appended to the version text.
const Label = ""
