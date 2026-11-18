//go:build !linux

package main

import "github.com/wailsapp/wails/v3/pkg/application"

// newCheckMenuItem returns a tray item that setMenuItemCheck can check: a
// checkbox item, since these platforms draw the check in the same place for
// every item.
func newCheckMenuItem(label string) *application.MenuItem {
	return application.NewMenuItemCheckbox(label, false)
}

// setMenuItemCheck shows or hides item's check.
func setMenuItemCheck(item *application.MenuItem, _ string, on bool) {
	item.SetChecked(on)
}
