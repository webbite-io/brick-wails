package main

import (
	"errors"
	"os"
	"sync"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// RemoteControlService backs the tray's two remote control windows: the one
// that confirms removing a root, opened from the root's "Remove…" item
// (frontend/src/remove.ts), and the one that shares a new folder, opened from
// "New Shared Folder…" (frontend/src/add.ts). Bound via
// application.NewService in main.go.
type RemoteControlService struct {
	app *application.App
	// removeWindow and addWindow return the two windows (nil before they're
	// created).
	removeWindow func() *application.WebviewWindow
	addWindow    func() *application.WebviewWindow
	// remove takes a root out of config.yaml and the tray, and add puts one
	// in (set by main.go, which owns the tray's remote control section).
	remove func(path string) error
	add    func(path string, readOnly bool) error

	mu sync.Mutex
	// pending is the root the remove window asks about, "" while it's closed.
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
	showWindow(s.removeWindow())
}

// Cancel closes the remove window without removing anything.
func (s *RemoteControlService) Cancel() {
	s.mu.Lock()
	s.pending = ""
	s.mu.Unlock()
	hideWindow(s.removeWindow())
}

// Delete removes the root the remove window asks about and closes it.
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

// pickNewRoot opens the window that shares a new folder, reset to its
// defaults (the home folder, read-only).
func (s *RemoteControlService) pickNewRoot() {
	s.app.Event.Emit("add:open")
	showWindow(s.addWindow())
}

// HomeDir is the folder the add window offers first.
func (s *RemoteControlService) HomeDir() string {
	home, _ := os.UserHomeDir()
	return home
}

// PickFolder shows a native folder picker over the add window, starting in
// startDir. Returns "" when cancelled.
func (s *RemoteControlService) PickFolder(startDir string) string {
	d := s.app.Dialog.OpenFile().
		CanChooseDirectories(true).
		CanChooseFiles(false).
		CanCreateDirectories(true).
		SetTitle("Pick a folder to share via remote control").
		SetDirectory(startDir)
	if w := s.addWindow(); w != nil {
		d = d.AttachToWindow(w)
	}
	path, err := d.PromptForSingleSelection()
	if err != nil {
		// Several platforms report a cancelled dialog as an error.
		return ""
	}
	return path
}

// CloseAdd closes the add window without sharing anything.
func (s *RemoteControlService) CloseAdd() {
	hideWindow(s.addWindow())
}

// Save shares path over remote control at the given access level, hands it
// to the running agent, and closes the add window.
func (s *RemoteControlService) Save(path string, readOnly bool) error {
	if err := s.add(path, readOnly); err != nil {
		return err
	}
	s.CloseAdd()
	return nil
}

func showWindow(w *application.WebviewWindow) {
	if w != nil {
		w.Show()
		w.Focus()
	}
}

func hideWindow(w *application.WebviewWindow) {
	if w != nil {
		w.Hide()
	}
}
