//go:build !linux

package main

import (
	"log"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// ensureDesktopEntry is Linux-only: elsewhere an app's name and icon come
// from its bundle or executable, not a .desktop entry.
func ensureDesktopEntry(*log.Logger) string { return "" }

// addStartAtLoginItem is Linux-only for now: macOS would register a login
// item instead.
func addStartAtLoginItem(*application.App, *application.Menu, string, *log.Logger) {}
