//go:build !darwin

package main

// trayIconScale leaves the tray icon at its full size outside macOS (see
// trayicon_darwin.go).
const trayIconScale = 1.0
