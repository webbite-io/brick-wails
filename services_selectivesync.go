package main

import (
	"context"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"

	"github.com/webbite-io/brick-wails/internal/runner"
)

// SelectiveSyncService backs the tray's "Selective Sync…" window
// (frontend/src/selective.ts), which updates which top-level folders are
// synced like brick-cli's `brick sync -s`. Bound via application.NewService
// in main.go.
type SelectiveSyncService struct {
	app    *application.App
	runner *runner.Runner
	window func() *application.WebviewWindow
}

// open shows the window, which reloads the folder list each time.
func (s *SelectiveSyncService) open() {
	s.app.Event.Emit("selective:open")
	showWindow(s.window())
}

// Folders lists the active account's top-level folders on Brick and which of
// them are synced.
func (s *SelectiveSyncService) Folders() (*runner.SelectiveSync, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	return s.runner.SelectiveSyncFolders(ctx)
}

// Save syncs the ticked folders among listed and excludes the rest, deleting
// the local copies of newly excluded ones, then closes the window. A running
// sync adopts the change at once.
func (s *SelectiveSyncService) Save(listed, synced []string) error {
	if err := s.runner.SetSyncedFolders(listed, synced); err != nil {
		return err
	}
	s.Close()
	return nil
}

// Close closes the window without changing anything.
func (s *SelectiveSyncService) Close() {
	hideWindow(s.window())
}
