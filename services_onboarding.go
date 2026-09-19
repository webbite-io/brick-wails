package main

import (
	"context"
	"errors"
	"sync/atomic"

	"github.com/wailsapp/wails/v3/pkg/application"

	"github.com/webbite-io/brick-wails/internal/auth"
	"github.com/webbite-io/brick-wails/internal/onboarding"
	"github.com/webbite-io/brick-wails/internal/runner"
)

// OnboardingService drives the setup window: startup routing and the
// onboarding wizard. All logic lives in internal/onboarding (tested without
// Wails); this is the thin adapter that adds native dialogs, the browser and
// window control. Bound via application.NewService in main.go.
type OnboardingService struct {
	app    *application.App
	flow   *onboarding.Flow
	runner *runner.Runner
	// window returns the setup window (nil before it's created).
	window func() *application.WebviewWindow
	// pending carries a finished wizard's decisions to StartSync.
	pending atomic.Pointer[runner.StartParams]
}

// StartResult is returned by StartSync.
type StartResult struct {
	Ok      bool   `json:"ok"`
	Step    string `json:"step,omitempty"` // route to show instead, on failure
	Message string `json:"message,omitempty"`
}

// Route decides which screen the setup window should show. When syncing is
// already running it returns "ready".
func (s *OnboardingService) Route(ctx context.Context) onboarding.Route {
	if s.runner.Running() {
		return onboarding.Route{Step: onboarding.StepReady}
	}
	return s.flow.Route(ctx)
}

// Restart clears in-memory wizard progress (a fresh session).
func (s *OnboardingService) Restart() { s.flow.Reset() }

// BeginLogin starts the login callback server, opens the browser and returns
// the authorization URL (shown in the UI as a fallback link).
func (s *OnboardingService) BeginLogin(ctx context.Context) (string, error) {
	url, err := s.flow.BeginLogin(ctx)
	if err != nil {
		return "", err
	}
	s.OpenURL(url)
	return url, nil
}

// AwaitLogin resolves once the browser login completes (or fails/times out).
func (s *OnboardingService) AwaitLogin(ctx context.Context) (*onboarding.LoginResult, error) {
	res, err := s.flow.AwaitLogin(ctx)
	if errors.Is(err, auth.ErrLoginCancelled) {
		return nil, errors.New("login cancelled")
	}
	return res, err
}

// CancelLogin aborts a pending AwaitLogin.
func (s *OnboardingService) CancelLogin() { s.flow.CancelLogin() }

// Accounts returns the accounts to pick from.
func (s *OnboardingService) Accounts() []auth.Account { return s.flow.Accounts() }

// SelectAccount makes an account active.
func (s *OnboardingService) SelectAccount(id string) error { return s.flow.SelectAccount(id) }

// DefaultSyncFolder is ~/Brick.
func (s *OnboardingService) DefaultSyncFolder() (string, error) { return s.flow.DefaultSyncFolder() }

// HomeDir is the user's home directory.
func (s *OnboardingService) HomeDir() string { return s.flow.HomeDir() }

// PickDirectory shows a native directory picker starting in startDir. Returns
// "" when cancelled.
func (s *OnboardingService) PickDirectory(startDir, title string) (string, error) {
	if s.app == nil {
		return "", errors.New("no application")
	}
	d := s.app.Dialog.OpenFile().
		CanChooseDirectories(true).
		CanChooseFiles(false).
		CanCreateDirectories(true).
		SetTitle(title).
		SetDirectory(startDir)
	if w := s.window(); w != nil {
		d = d.AttachToWindow(w)
	}
	path, err := d.PromptForSingleSelection()
	if err != nil {
		// Several platforms report a cancelled dialog as an error.
		return "", nil
	}
	return path, nil
}

// CreateFolderInHome creates a folder (or nested path) under home.
func (s *OnboardingService) CreateFolderInHome(rel string) (string, error) {
	return s.flow.CreateFolderInHome(rel)
}

// ChooseSyncFolder records the chosen folder and reports whether it has files.
func (s *OnboardingService) ChooseSyncFolder(path string) (*onboarding.FolderChoice, error) {
	return s.flow.ChooseSyncFolder(path)
}

// ConfirmSyncFolder saves the chosen folder with its first-sync conflict mode.
func (s *OnboardingService) ConfirmSyncFolder(conflictMode string) (string, error) {
	return s.flow.ConfirmSyncFolder(conflictMode)
}

// Connect checks the Storage API and returns what the scope step needs.
func (s *OnboardingService) Connect(ctx context.Context) (*onboarding.ScopeInfo, error) {
	return s.flow.Connect(ctx)
}

// SetSyncScope saves which folders to sync.
func (s *OnboardingService) SetSyncScope(all bool, exclude []string) error {
	return s.flow.SetSyncScope(all, exclude)
}

// SetRemoteAccess saves the remote-file-access decision.
func (s *OnboardingService) SetRemoteAccess(enabled bool, root string) error {
	return s.flow.SetRemoteAccess(enabled, root)
}

// Checklist returns the completed onboarding steps.
func (s *OnboardingService) Checklist() []string { return s.flow.Checklist() }

// FinishOnboarding records the end of the wizard (adds "Done and ready to
// go!") without starting sync yet.
func (s *OnboardingService) FinishOnboarding() []string {
	p := s.flow.Finish()
	s.pending.Store(&p)
	return s.flow.Checklist()
}

// StartSync starts syncing with any decisions from a just-finished wizard.
// On failure it says which route to show instead.
func (s *OnboardingService) StartSync() StartResult {
	var p runner.StartParams
	if v := s.pending.Swap(nil); v != nil {
		p = *v
	}
	err := s.runner.Start(p)
	if err != nil && p.FirstSync {
		// Keep the wizard's decisions (first sync + conflict mode) for the retry.
		s.pending.Store(&p)
	}
	switch {
	case err == nil:
		return StartResult{Ok: true}
	case errors.Is(err, runner.ErrLocked):
		return StartResult{Step: onboarding.StepLocked, Message: err.Error()}
	case errors.Is(err, runner.ErrNotConfigured):
		return StartResult{Step: onboarding.StepFolder, Message: err.Error()}
	case errors.Is(err, auth.ErrSessionExpired):
		return StartResult{Step: onboarding.StepLogin, Message: "Your session has expired. Log in again to continue."}
	default:
		return StartResult{Step: onboarding.StepConnectError, Message: err.Error()}
	}
}

// ShowWindow shows and focuses the setup window.
func (s *OnboardingService) ShowWindow() {
	if w := s.window(); w != nil {
		w.Show()
		w.Focus()
	}
}

// HideWindow hides the setup window.
func (s *OnboardingService) HideWindow() {
	if w := s.window(); w != nil {
		w.Hide()
	}
}

// OpenURL opens a URL in the default browser.
func (s *OnboardingService) OpenURL(url string) {
	if s.app != nil {
		_ = s.app.Browser.OpenURL(url)
	}
}

// QuitApp exits the app.
func (s *OnboardingService) QuitApp() {
	if s.app != nil {
		s.app.Quit()
	}
}
