//go:build unix

package main

import (
	"os/exec"
	"syscall"
)

// processAlive reports whether pid names a live process. Sending signal 0
// performs no action but still fails with ESRCH if the process is gone.
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	return syscall.Kill(pid, 0) == nil
}

// detachProcess configures cmd to start in its own session, detached from
// this app's process group, so it isn't killed by a signal (e.g. SIGHUP,
// SIGINT from a terminal) sent to this app rather than to it directly.
func detachProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}
