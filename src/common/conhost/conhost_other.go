//go:build !windows

package conhost

// Watch: only Windows programs run under a console host that can vanish.
func Watch(stop func()) {}
