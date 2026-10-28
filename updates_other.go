//go:build !linux && !darwin

package main

import (
	"log"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// appUpdater does nothing on Windows, which ships through the Microsoft
// Store: the Store installs updates itself, and a packaged (MSIX) app can't
// replace its own install directory anyway. So there's no check, no update
// window and no tray item. The UpdateService is still bound (see main.go) so
// the generated frontend bindings stay the same whichever OS generates them.
type appUpdater struct{}

func newAppUpdater(*log.Logger, *UpdateService) *appUpdater { return &appUpdater{} }

func (*appUpdater) addMenuItem(*application.Menu) *application.MenuItem { return nil }

func (*appUpdater) start(*application.App, *application.MenuItem, func(bool)) {}
