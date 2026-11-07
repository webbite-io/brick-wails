package main

import (
	"errors"
	"sync"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// RemoteControlService backs the window that confirms removing a remote
// control root, opened from the root's "Remove…" item in the tray
// (frontend/src/remove.ts). Bound via application.NewService in main.go.
type RemoteControlService struct {
	app *application.App
	// window returns the confirmation window (nil before it's created).
	window func() *application.WebviewWindow
	// remove takes the root out of config.yaml and the tray (set by main.go,
	// which owns the tray's remote control section).
	remove func(path string) error

	mu sync.Mutex
	// pending is the root the window asks about, "" while it's closed.
	pending string
}

// confirmRemove opens the window asking whether to remove the root at path.
func (s *RemoteControlService) confirmRemove(path string) {
	s.mu.Lock()
	s.pending = path
	s.mu.Unlock()
	// The page may be left over from an earlier removal, showing an error or
	// disabled buttons; this resets it.
	s.app.Event.Emit("remove:open")
	if w := s.window(); w != nil {
		w.Show()
		w.Focus()
	}
}

// Cancel closes the window without removing anything.
func (s *RemoteControlService) Cancel() {
	s.mu.Lock()
	s.pending = ""
	s.mu.Unlock()
	if w := s.window(); w != nil {
		w.Hide()
	}
}

// Delete removes the root the window asks about and closes it.
func (s *RemoteControlService) Delete() error {
	s.mu.Lock()
	path := s.pending
	s.mu.Unlock()
	if path == "" {
		return errors.New("no shared folder selected")
	}
	if err := s.remove(path); err != nil {
		return err
	}
	s.Cancel()
	return nil
}
