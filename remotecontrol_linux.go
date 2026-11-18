//go:build linux

package main

import "github.com/wailsapp/wails/v3/pkg/application"

// newCheckMenuItem returns a tray item that setMenuItemCheck can check. On
// Linux it's a plain item with the check in its label, for the reason given
// at startAtLoginLabel.
func newCheckMenuItem(label string) *application.MenuItem {
	return application.NewMenuItem(label)
}

// setMenuItemCheck shows or hides item's check; label is its text without one.
func setMenuItemCheck(item *application.MenuItem, label string, on bool) {
	if on {
		label += " ✓"
	}
	item.SetLabel(label)
}
