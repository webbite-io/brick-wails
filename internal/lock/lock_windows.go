//go:build windows

package lock

import (
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

// Lock is an exclusive lock, released by the OS if the process dies.
type Lock struct {
	f *os.File
}

// Acquire takes an exclusive non-blocking LockFileEx on path. Returns
// ErrLocked if another process holds it.
func Acquire(path string) (*Lock, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("could not open lock file: %w", err)
	}
	ol := new(windows.Overlapped)
	flags := uint32(windows.LOCKFILE_EXCLUSIVE_LOCK | windows.LOCKFILE_FAIL_IMMEDIATELY)
	if err := windows.LockFileEx(windows.Handle(f.Fd()), flags, 0, 1, 0, ol); err != nil {
		f.Close()
		if err == windows.ERROR_LOCK_VIOLATION {
			return nil, ErrLocked
		}
		return nil, fmt.Errorf("could not lock %s: %w", path, err)
	}
	return &Lock{f: f}, nil
}

// Release drops the lock.
func (l *Lock) Release() {
	ol := new(windows.Overlapped)
	windows.UnlockFileEx(windows.Handle(l.f.Fd()), 0, 1, 0, ol) //nolint:errcheck
	l.f.Close()
}
