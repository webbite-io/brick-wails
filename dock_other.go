//go:build !darwin

package main

import "github.com/wailsapp/wails/v3/pkg/application"

// trackAppSwitcher is a no-op outside macOS: elsewhere a visible window is in
// the window switcher on its own.
func trackAppSwitcher(*application.App, ...*application.WebviewWindow) {}
