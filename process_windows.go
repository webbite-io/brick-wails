//go:build windows

package main

import "golang.org/x/sys/windows"

// stillActive is the Windows STILL_ACTIVE exit-code sentinel (259); not
// exported by golang.org/x/sys/windows, so defined here directly.
const stillActive = 259

// processAlive reports whether pid names a live process.
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return false
	}
	defer windows.CloseHandle(h)
	var code uint32
	if err := windows.GetExitCodeProcess(h, &code); err != nil {
		return false
	}
	return code == stillActive
}
