package main

import (
	"sync/atomic"

	"github.com/wailsapp/wails/v3/pkg/application"

	"github.com/webbite-io/brick-wails/internal/update"
)

// UpdateService drives the update window: what it shows (Info, set once the
// startup check in main.go finds a newer release) and its two buttons.
// Bound via application.NewService in main.go.
type UpdateService struct {
	app *application.App
	// window returns the update window (nil before it's created).
	window func() *application.WebviewWindow
	info   atomic.Pointer[update.Info]
}

// Info returns the update found at startup, or nil if none (up to date, or
// the check failed/timed out). The window loads hidden and calls this itself
// in case it missed the "update:available" event emitted when the check
// finishes.
func (s *UpdateService) Info() *update.Info { return s.info.Load() }

// Continue dismisses the update window without installing.
func (s *UpdateService) Continue() {
	if w := s.window(); w != nil {
		w.Hide()
	}
}

// InstallUpdate launches the installer in the user's default terminal and
// quits the app, which closes every window. The terminal survives the quit
// since it's a separate process.
func (s *UpdateService) InstallUpdate() error {
	if err := update.OpenInTerminal(); err != nil {
		return err
	}
	s.app.Quit()
	return nil
}
