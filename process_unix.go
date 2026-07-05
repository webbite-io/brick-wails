//go:build unix

package main

import "syscall"

// processAlive reports whether pid names a live process. Sending signal 0
// performs no action but still fails with ESRCH if the process is gone.
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	return syscall.Kill(pid, 0) == nil
}
