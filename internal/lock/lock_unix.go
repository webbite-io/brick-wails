//go:build unix

package lock

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

// Lock is an exclusive advisory lock, released by the kernel if the process
// dies, so a crash never leaves a stale lock behind.
type Lock struct {
	f *os.File
}

// Acquire takes an exclusive non-blocking flock on path, creating it if
// necessary. Returns ErrLocked if another process holds it.
func Acquire(path string) (*Lock, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("could not open lock file: %w", err)
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		f.Close()
		if err == unix.EWOULDBLOCK {
			return nil, ErrLocked
		}
		return nil, fmt.Errorf("could not lock %s: %w", path, err)
	}
	return &Lock{f: f}, nil
}

// Release drops the lock.
func (l *Lock) Release() {
	unix.Flock(int(l.f.Fd()), unix.LOCK_UN) //nolint:errcheck
	l.f.Close()
}
