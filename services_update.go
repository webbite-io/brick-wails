package main

import (
	"context"
	"log"
	"sync/atomic"

	"github.com/wailsapp/wails/v3/pkg/application"

	"github.com/webbite-io/brick-wails/internal/update"
)

// The states of the update window, as UpdateView.State. The window renders
// whichever one it's handed (frontend/src/update.ts).
const (
	updateChecking  = "checking"
	updateAvailable = "available"
	updateUpToDate  = "uptodate"
	updateFailed    = "failed"
)

// UpdateView is what the update window shows. Every path that opens the
// window sets one first, so the window itself holds no state of its own.
type UpdateView struct {
	State   string `json:"state"`
	Current string `json:"current"`
	// Latest is the newer version on offer, set only in the "available" state.
	Latest string `json:"latest,omitempty"`
	// Error explains a "failed" check, in the check's own words.
	Error string `json:"error,omitempty"`
}

// UpdateService owns everything about updates the UI can see: the window's
// state, its buttons, and the checks that feed both. Bound via
// application.NewService in main.go.
type UpdateService struct {
	app     *application.App
	logger  *log.Logger
	version string
	// window returns the update window (nil before it's created).
	window func() *application.WebviewWindow
	view   atomic.Pointer[UpdateView]
	// pending is the newer release the tray is advertising. Once set it stays
	// set: the dot is there until the app is actually replaced, so dismissing
	// the window doesn't lose track of an update that's still waiting.
	pending atomic.Pointer[update.Info]
	// onPending moves the tray icon and menu item onto a found update (set by
	// main.go, which owns both).
	onPending func(*update.Info)
	// checking keeps the periodic check from racing the user's own, which
	// would mean two requests to GitHub and two view updates.
	checking atomic.Bool
}

// View returns what the window should show. The window fetches it on load, in
// case it wasn't listening yet for the "update:view" event that set it.
func (s *UpdateService) View() *UpdateView { return s.view.Load() }

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

// checkOnStartup runs the check at launch: silent unless there's something to
// offer, and then it opens the window as well as badging the tray, since
// someone who has just started the app is the likeliest to accept an update.
func (s *UpdateService) checkOnStartup() {
	if info, err := s.check(); err == nil && info != nil {
		s.offer(info)
	}
}

// checkInBackground runs one of the periodic checks. It only badges the tray
// (via check → onPending), so a release found hours into a session never
// steals focus from whatever the user is in the middle of.
func (s *UpdateService) checkInBackground() {
	_, _ = s.check()
}

// checkOnDemand runs the check behind the tray's "Check for Updates" item.
// The user asked, so the window opens immediately on the "checking" state and
// then reports whatever comes back — up to date and failures included.
func (s *UpdateService) checkOnDemand() {
	s.setView(&UpdateView{State: updateChecking, Current: s.version})
	s.show()

	info, err := s.check()
	switch {
	case err != nil:
		s.setView(&UpdateView{State: updateFailed, Current: s.version, Error: err.Error()})
	case info == nil:
		s.setView(&UpdateView{State: updateUpToDate, Current: s.version})
	default:
		s.setView(&UpdateView{State: updateAvailable, Current: info.Current, Latest: info.Latest})
	}
}

// offerOrCheck backs the tray's update item in both its faces: it reopens the
// prompt for an update a check has already found ("Install Update"), and
// otherwise runs a check on the spot ("Check for Updates").
func (s *UpdateService) offerOrCheck() {
	if info := s.pending.Load(); info != nil {
		s.offer(info)
		return
	}
	s.checkOnDemand()
}

// check runs one update check, recording a newer release as pending so the
// tray gains its dot whichever path asked. A check already in flight wins:
// the second caller is answered from what's known rather than asking GitHub
// again.
func (s *UpdateService) check() (*update.Info, error) {
	if !s.checking.CompareAndSwap(false, true) {
		return s.pending.Load(), nil
	}
	defer s.checking.Store(false)

	info, err := update.Check(context.Background(), s.version)
	if err != nil {
		s.logger.Printf("update check failed: %v", err)
		return nil, err
	}
	if info == nil {
		return nil, nil
	}
	s.logger.Printf("update available: v%s -> v%s", info.Current, info.Latest)
	s.pending.Store(info)
	if s.onPending != nil {
		s.onPending(info)
	}
	return info, nil
}

// offer shows the window with the install prompt for info.
func (s *UpdateService) offer(info *update.Info) {
	s.setView(&UpdateView{State: updateAvailable, Current: info.Current, Latest: info.Latest})
	s.show()
}

func (s *UpdateService) setView(v *UpdateView) {
	s.view.Store(v)
	s.app.Event.Emit("update:view", v)
}

func (s *UpdateService) show() {
	if w := s.window(); w != nil {
		w.Show()
		w.Focus()
	}
}
