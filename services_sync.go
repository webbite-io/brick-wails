package main

import (
	"github.com/wailsapp/wails/v3/pkg/application"

	"github.com/webbite-io/brick-wails/internal/runner"
	"github.com/webbite-io/brick-wails/internal/syncengine"
)

// SyncService exposes the in-process sync engine to the status popover,
// with the method names and JSON shapes the old control-API client used.
// Bound via application.NewService in main.go.
type SyncService struct {
	app    *application.App
	runner *runner.Runner
	// openSetup shows the setup window (for "Finish setup"/"Log in again").
	openSetup func()
}

// Status returns the current sync status (see runner.Status).
func (s *SyncService) Status() runner.Status { return s.runner.Status() }

// Activity returns up to limit recent sync events, newest first.
func (s *SyncService) Activity(limit int) []syncengine.ActivityEvent {
	if limit <= 0 {
		limit = 50
	}
	return s.runner.Activity(limit)
}

// Pause pauses syncing (the watcher keeps running, so resuming is instant).
func (s *SyncService) Pause() { s.runner.SetPaused(true) }

// Resume resumes syncing.
func (s *SyncService) Resume() { s.runner.SetPaused(false) }

// OpenFolder opens the sync folder in the file manager.
func (s *SyncService) OpenFolder() error {
	folder := s.runner.Status().Folder
	if folder == "" || s.app == nil {
		return nil
	}
	return s.app.Browser.OpenFile(folder)
}

// OpenSetup opens the setup window (not-configured / auth-required /
// locked / stopped states offer this from the popover).
func (s *SyncService) OpenSetup() {
	if s.openSetup != nil {
		s.openSetup()
	}
}
