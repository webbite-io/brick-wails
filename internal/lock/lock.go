// Package lock enforces a single running sync engine per user — shared with
// brick-cli, which takes the very same file lock, so this app and a `brick
// sync` can never reconcile the same folder at once.
//
// Ported from brick-cli cmd/brick/lock*.go @ f3ef7bd.
package lock

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// ErrLocked is returned by Acquire when another process holds the lock.
var ErrLocked = errors.New("another brick instance is already running")

// PathIn returns <configDir>/brick.lock (brick-cli's instanceLockPath),
// creating configDir if needed.
func PathIn(configDir string) (string, error) {
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		return "", fmt.Errorf("could not create config directory: %w", err)
	}
	return filepath.Join(configDir, "brick.lock"), nil
}
