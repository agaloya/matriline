//go:build !unix && !windows

package lock

import (
	"os"
	"path/filepath"
)

// Acquire on non-Unix systems only creates the file (no exclusive lock yet).
func Acquire(dir, name string) (*os.File, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	return os.OpenFile(filepath.Join(dir, name), os.O_RDWR|os.O_CREATE, 0o600)
}
