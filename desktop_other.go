//go:build !linux

package main

import "log"

// ensureDesktopEntry is Linux-only: elsewhere an app's name and icon come
// from its bundle or executable, not a .desktop entry.
func ensureDesktopEntry(*log.Logger) {}
