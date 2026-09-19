// Package onboarding is the graphical counterpart of brick-cli's interactive
// setup (prepareSync @ f3ef7bd): the startup routing that decides where the
// user needs to go, and each wizard step's side effects on config.yaml. It
// has no UI and no Wails dependency; the Wails OnboardingService is a thin
// adapter over Flow.
//
// CLI step → Flow method:
//
//	loadOrCreateConfig greeting / "log in to get started?"  → Route (StepWelcome)
//	runLogin                                               → BeginLogin / AwaitLogin
//	selectAccount                                          → SelectAccount
//	promptForSyncFolder (+ promptCreateFolder)             → DefaultSyncFolder / CreateFolderInHome / ChooseSyncFolder
//	promptConflictMode + ensureStorageSyncFolder           → ConfirmSyncFolder
//	resolveRoot                                            → Connect
//	runSyncScopeOnboarding                                 → SetSyncScope
//	promptForRemoteControl                                 → SetRemoteAccess
//	checklist.done("Done and ready to go!")                → Finish
package onboarding

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/webbite-io/brick-wails/internal/auth"
	"github.com/webbite-io/brick-wails/internal/brickcfg"
	"github.com/webbite-io/brick-wails/internal/lock"
	"github.com/webbite-io/brick-wails/internal/runner"
	"github.com/webbite-io/brick-wails/internal/storage"
)

// Steps returned by Route.
const (
	StepWelcome      = "welcome"       // no credentials: offer to log in
	StepLogin        = "login"         // credentials expired: log in again
	StepAccount      = "account"       // logged in, no active account
	StepFolder       = "folder"        // no sync folder for the active account
	StepLocked       = "locked"        // brick-cli holds the instance lock
	StepConnectError = "connect-error" // API unreachable / unexpected error
	StepReady        = "ready"         // fully configured: start syncing
)

// Valid conflict modes (brick-cli's promptConflictMode values).
var conflictModes = map[string]bool{"device": true, "brick": true, "copy": true}

// LoginTimeout bounds waiting for the browser (brick-cli uses 5 minutes).
var LoginTimeout = 5 * time.Minute

// Route tells the UI where to go.
type Route struct {
	Step string `json:"step"`
	// FirstRun is true when config.yaml was just created (show the welcome
	// greeting rather than a plain "log in").
	FirstRun bool   `json:"firstRun"`
	Message  string `json:"message"`
	Detail   string `json:"detail,omitempty"`
}

// LoginResult is returned by AwaitLogin.
type LoginResult struct {
	GivenName  string         `json:"givenName"`
	FamilyName string         `json:"familyName"`
	Greeting   string         `json:"greeting"`
	Accounts   []auth.Account `json:"accounts"`
	// AccountSelected is true when there was exactly one account and it was
	// selected automatically (brick-cli's selectAccount).
	AccountSelected bool `json:"accountSelected"`
}

// FolderChoice is returned by ChooseSyncFolder.
type FolderChoice struct {
	Folder   string `json:"folder"`
	Display  string `json:"display"`
	HasFiles bool   `json:"hasFiles"`
}

// ScopeInfo is returned by Connect: whether to show the sync-scope step.
type ScopeInfo struct {
	ShowScope       bool     `json:"showScope"`
	ShowRemote      bool     `json:"showRemote"`
	TotalBytes      int64    `json:"totalBytes"`
	TotalHuman      string   `json:"totalHuman"`
	Folders         []string `json:"folders"`
	AlreadyExcluded []string `json:"alreadyExcluded"`
}

// Flow holds one onboarding session's in-memory decisions.
type Flow struct {
	env    brickcfg.Env
	store  *brickcfg.Store
	tokens *auth.TokenSource
	client *auth.Client

	mu           sync.Mutex
	login        *auth.LoginSession
	accounts     []auth.Account
	firstSetup   bool
	conflictMode string
	pendingDir   string
	rootID       string
	topFolders   []string
	checklist    []string
}

// New returns a Flow sharing tokens with the rest of the app.
func New(env brickcfg.Env, store *brickcfg.Store, tokens *auth.TokenSource) *Flow {
	return &Flow{env: env, store: store, tokens: tokens, client: auth.NewClient(tokens)}
}

func (f *Flow) done(format string, args ...any) {
	f.checklist = append(f.checklist, fmt.Sprintf(format, args...))
}

// Checklist returns the completed-steps list ("Logged in to account: Acme").
func (f *Flow) Checklist() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.checklist...)
}

// Reset clears in-memory progress (a new wizard session).
func (f *Flow) Reset() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cancelLoginLocked()
	f.accounts, f.firstSetup, f.conflictMode, f.pendingDir, f.rootID, f.topFolders, f.checklist = nil, false, "", "", "", nil, nil
}

func (f *Flow) storageClient(accountID string) *storage.Client {
	return &storage.Client{BaseURL: f.env.StorageAPIURL, AccountID: accountID, Auth: f.client}
}

// Route decides where the user needs to go — brick-cli's --self-test checks
// plus prepareSync's prompts, in the same order.
func (f *Flow) Route(ctx context.Context) Route {
	// 1. Another brick (the CLI) already syncing?
	if lp, err := lock.PathIn(f.store.Dir()); err == nil {
		if lk, err := lock.Acquire(lp); err != nil {
			if errors.Is(err, lock.ErrLocked) {
				return Route{Step: StepLocked, Message: runner.ErrLocked.Error() + ". Stop it (Ctrl+C in its terminal, or quit its background daemon) and press Retry."}
			}
		} else {
			lk.Release()
		}
	}

	// 2. Config + credentials.
	cfg, created, err := f.store.LoadOrCreate()
	if err != nil {
		return Route{Step: StepConnectError, Message: "Could not read Brick's configuration.", Detail: err.Error()}
	}
	if err := f.tokens.Reload(); err != nil {
		return Route{Step: StepConnectError, Message: "Could not read Brick's configuration.", Detail: err.Error()}
	}
	if !cfg.HasCredentials() {
		msg := "Log in to get started."
		if created {
			msg = "Hello and welcome to Brick - storage for all your devices! Log in to get started."
		}
		return Route{Step: StepWelcome, FirstRun: created, Message: msg}
	}

	// 3. Still authenticated? (refreshes silently if needed, like `brick whoami`)
	if err := f.tokens.EnsureAccess(ctx); err != nil {
		return Route{Step: StepLogin, Message: "Your session has expired. Log in again to continue.", Detail: err.Error()}
	}
	if _, err := f.client.UserInfo(ctx); err != nil {
		if errors.Is(err, auth.ErrSessionExpired) {
			return Route{Step: StepLogin, Message: "Your session has expired. Log in again to continue.", Detail: err.Error()}
		}
		return Route{Step: StepConnectError, Message: "Could not reach the Brick API at " + f.env.APIURL + ".", Detail: err.Error()}
	}

	// 4. Account.
	if cfg.ActiveAccountID == "" {
		accts, err := f.client.Accounts(ctx)
		if err != nil {
			return Route{Step: StepConnectError, Message: "Could not load your Brick accounts.", Detail: err.Error()}
		}
		f.mu.Lock()
		f.accounts = accts
		f.mu.Unlock()
		if len(accts) == 1 {
			if err := f.SelectAccount(accts[0].ID); err != nil {
				return Route{Step: StepConnectError, Message: "Could not save the account.", Detail: err.Error()}
			}
			return f.Route(ctx)
		}
		return Route{Step: StepAccount, Message: "You have access to more than one account, which one do you want to use?"}
	}

	// 5. Sync folder (first setup from here on).
	ac := cfg.ActiveAccount()
	if ac == nil || strings.TrimSpace(ac.StorageSyncFolder) == "" {
		f.mu.Lock()
		f.firstSetup = true
		f.mu.Unlock()
		return Route{Step: StepFolder, Message: "You have no sync folder configured."}
	}
	// A configured folder that has gone missing is recreated, as brick-cli's
	// ensureStorageSyncFolder does.
	if err := os.MkdirAll(ac.StorageSyncFolder, 0o755); err != nil {
		return Route{Step: StepConnectError, Message: "Could not create the sync folder " + ac.StorageSyncFolder + ".", Detail: err.Error()}
	}

	// 6. Storage API reachable?
	if _, err := f.storageClient(cfg.ActiveAccountID).ResolveRoot(ctx); err != nil {
		if errors.Is(err, auth.ErrSessionExpired) {
			return Route{Step: StepLogin, Message: "Your session has expired. Log in again to continue.", Detail: err.Error()}
		}
		return Route{Step: StepConnectError, Message: "Could not reach the Brick storage API at " + f.env.StorageAPIURL + ".", Detail: err.Error()}
	}
	return Route{Step: StepReady}
}

// --- login ---

// BeginLogin starts the loopback callback server and returns the URL to open
// in the browser.
func (f *Flow) BeginLogin(ctx context.Context) (string, error) {
	f.mu.Lock()
	f.cancelLoginLocked()
	f.mu.Unlock()
	s, err := auth.StartLogin(ctx, auth.LoginParams{
		APIURL: f.env.APIURL, ClientID: f.env.OAuthClientID, Scopes: f.env.OAuthScopes, CallbackURL: f.env.OAuthCallbackURL,
	})
	if err != nil {
		return "", err
	}
	f.mu.Lock()
	f.login = s
	f.mu.Unlock()
	return s.AuthURL, nil
}

// CancelLogin aborts a pending AwaitLogin.
func (f *Flow) CancelLogin() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cancelLoginLocked()
}

func (f *Flow) cancelLoginLocked() {
	if f.login != nil {
		f.login.Cancel()
		f.login = nil
	}
}

// AwaitLogin waits for the browser callback (up to LoginTimeout), stores the
// tokens and fetches the user and their accounts, auto-selecting the only
// account if there is just one.
func (f *Flow) AwaitLogin(ctx context.Context) (*LoginResult, error) {
	f.mu.Lock()
	s := f.login
	f.mu.Unlock()
	if s == nil {
		return nil, errors.New("no login in progress")
	}
	ctx, cancel := context.WithTimeout(ctx, LoginTimeout)
	defer cancel()
	tok, err := s.Wait(ctx)
	f.mu.Lock()
	if f.login == s {
		f.login = nil
	}
	f.mu.Unlock()
	if err != nil {
		return nil, err
	}
	if err := f.tokens.Set(tok); err != nil {
		return nil, err
	}

	res := &LoginResult{Greeting: "Login successful 🎉"}
	if u, err := f.client.UserInfo(ctx); err == nil {
		res.GivenName, res.FamilyName = u.GivenName, u.FamilyName
		if u.GivenName != "" && u.FamilyName != "" {
			res.Greeting = fmt.Sprintf("Hello %s %s 👋", u.GivenName, u.FamilyName)
		}
	} else {
		return nil, err
	}
	accts, err := f.client.Accounts(ctx)
	if err != nil {
		return nil, err
	}
	res.Accounts = accts
	f.mu.Lock()
	f.accounts = accts
	f.mu.Unlock()

	// Re-login for an already-selected account keeps it (and its settings).
	cfg, _ := f.store.Load()
	if cfg != nil && cfg.ActiveAccountID != "" {
		for _, a := range accts {
			if a.ID == cfg.ActiveAccountID {
				f.mu.Lock()
				f.done("Logged in to account: %s", a.Name)
				f.mu.Unlock()
				res.AccountSelected = true
				return res, nil
			}
		}
	}
	if len(accts) == 1 {
		if err := f.SelectAccount(accts[0].ID); err != nil {
			return nil, err
		}
		res.AccountSelected = true
	}
	return res, nil
}

// Accounts returns the accounts from the last login/route.
func (f *Flow) Accounts() []auth.Account {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]auth.Account(nil), f.accounts...)
}

// SelectAccount makes accountID active.
func (f *Flow) SelectAccount(accountID string) error {
	f.mu.Lock()
	var name string
	for _, a := range f.accounts {
		if a.ID == accountID {
			name = a.Name
		}
	}
	f.mu.Unlock()
	if name == "" {
		return fmt.Errorf("unknown account %q", accountID)
	}
	if _, err := f.store.Update(func(c *brickcfg.Config) error {
		c.ActiveAccountID = accountID
		return nil
	}); err != nil {
		return err
	}
	f.mu.Lock()
	f.done("Logged in to account: %s", name)
	f.mu.Unlock()
	return nil
}

// --- sync folder ---

// DefaultSyncFolder is ~/Brick.
func (f *Flow) DefaultSyncFolder() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("could not determine home directory: %w", err)
	}
	return filepath.Join(home, "Brick"), nil
}

// HomeDir is the user's home directory.
func (f *Flow) HomeDir() string {
	home, _ := os.UserHomeDir()
	return home
}

// CreateFolderInHome creates rel ("folder" or "folder1/folder2") under home
// (brick-cli's promptCreateFolder). An existing folder is fine.
func (f *Flow) CreateFolderInHome(rel string) (string, error) {
	rel = strings.TrimSpace(rel)
	if rel == "" {
		return "", errors.New("enter a folder name")
	}
	if filepath.IsAbs(rel) || strings.HasPrefix(rel, "~") {
		return "", errors.New("enter a folder name relative to your home folder")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	folder := filepath.Join(home, rel)
	if r, err := filepath.Rel(home, folder); err != nil || strings.HasPrefix(r, "..") {
		return "", errors.New("the folder must be inside your home folder")
	}
	if err := os.MkdirAll(folder, 0o755); err != nil {
		return "", fmt.Errorf("could not create folder %s: %w", folder, err)
	}
	return folder, nil
}

// ChooseSyncFolder records the picked folder (not yet saved) and reports
// whether it already contains files — if so, the UI asks for a conflict mode
// before ConfirmSyncFolder.
func (f *Flow) ChooseSyncFolder(path string) (*FolderChoice, error) {
	path = brickcfg.ExpandHome(strings.TrimSpace(path))
	if path == "" {
		return nil, errors.New("no folder chosen")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	hasFiles, err := dirHasEntries(abs)
	if err != nil {
		return nil, err
	}
	f.mu.Lock()
	f.pendingDir = abs
	f.mu.Unlock()
	return &FolderChoice{Folder: abs, Display: brickcfg.DisplayPath(abs), HasFiles: hasFiles}, nil
}

// ConfirmSyncFolder creates and saves the chosen folder. conflictMode
// ("device", "brick" or "copy") is required when the folder has files and is
// kept in memory for the first sync only (brick-cli parity).
func (f *Flow) ConfirmSyncFolder(conflictMode string) (string, error) {
	f.mu.Lock()
	dir := f.pendingDir
	f.mu.Unlock()
	if dir == "" {
		return "", errors.New("no folder chosen")
	}
	hasFiles, err := dirHasEntries(dir)
	if err != nil {
		return "", err
	}
	if hasFiles && !conflictModes[conflictMode] {
		return "", errors.New("choose how conflicts should be handled")
	}
	if !hasFiles {
		conflictMode = ""
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("could not create sync folder: %w", err)
	}
	if _, err := f.store.Update(func(c *brickcfg.Config) error {
		ac := c.EnsureActiveAccount()
		if ac == nil {
			return errors.New("no account selected")
		}
		ac.StorageSyncFolder = dir
		return nil
	}); err != nil {
		return "", err
	}
	f.mu.Lock()
	f.conflictMode = conflictMode
	f.firstSetup = true
	f.done("Sync folder selected (%s)", brickcfg.DisplayPath(dir))
	f.mu.Unlock()
	return dir, nil
}

// --- connect / scope / remote ---

// Connect resolves the account's storage root and, on a first setup, fetches
// what the sync-scope step needs.
func (f *Flow) Connect(ctx context.Context) (*ScopeInfo, error) {
	cfg, err := f.store.Load()
	if err != nil {
		return nil, err
	}
	sc := f.storageClient(cfg.ActiveAccountID)
	root, err := sc.ResolveRoot(ctx)
	if err != nil {
		return nil, fmt.Errorf("could not reach storage API at %s: %w", f.env.StorageAPIURL, err)
	}
	f.mu.Lock()
	f.rootID = root.ID
	first := f.firstSetup
	f.mu.Unlock()

	info := &ScopeInfo{ShowRemote: first, Folders: []string{}}
	if !first {
		return info, nil
	}
	top, total, err := sc.FolderSummary(ctx, root.ID)
	if err != nil {
		return nil, err
	}
	for _, n := range top {
		info.Folders = append(info.Folders, n.Name)
	}
	info.ShowScope = len(top) > 0
	info.TotalBytes, info.TotalHuman = total, storage.HumanSize(total)
	if ac := cfg.ActiveAccount(); ac != nil {
		info.AlreadyExcluded = append([]string{}, ac.ExcludeDirs...)
	}
	f.mu.Lock()
	f.topFolders = info.Folders
	f.mu.Unlock()
	return info, nil
}

// SetSyncScope saves the sync scope: all folders, or all but exclude.
func (f *Flow) SetSyncScope(all bool, exclude []string) error {
	f.mu.Lock()
	top := f.topFolders
	f.mu.Unlock()
	valid := map[string]bool{}
	for _, n := range top {
		valid[n] = true
	}
	var excl []string
	if !all {
		for _, e := range exclude {
			if !valid[e] {
				return fmt.Errorf("unknown folder %q", e)
			}
			excl = append(excl, e)
		}
	}
	if _, err := f.store.Update(func(c *brickcfg.Config) error {
		ac := c.EnsureActiveAccount()
		if ac == nil {
			return errors.New("no account selected")
		}
		ac.ExcludeDirs = excl
		return nil
	}); err != nil {
		return err
	}
	f.mu.Lock()
	if all || len(excl) == 0 {
		f.done("Folders selected (all)")
	} else {
		f.done("Folders selected (%d of %d)", len(top)-len(excl), len(top))
	}
	f.mu.Unlock()
	return nil
}

// SetRemoteAccess records the remote-file-access decision. When enabled,
// root (empty = home) is added to agentRoots.
func (f *Flow) SetRemoteAccess(enabled bool, root string) error {
	if !enabled {
		return nil
	}
	if strings.TrimSpace(root) == "" {
		root = f.HomeDir()
	}
	abs, err := filepath.Abs(brickcfg.ExpandHome(root))
	if err != nil {
		return err
	}
	if info, err := os.Stat(abs); err != nil || !info.IsDir() {
		return fmt.Errorf("%s is not a folder", abs)
	}
	if _, err := f.store.Update(func(c *brickcfg.Config) error {
		c.RemoteControl = true
		for _, r := range c.AgentRoots {
			if r == abs {
				return nil
			}
		}
		c.AgentRoots = append(c.AgentRoots, abs)
		return nil
	}); err != nil {
		return err
	}
	f.mu.Lock()
	f.done("Remote file access enabled (root folder: %s)", brickcfg.DisplayPath(abs))
	f.mu.Unlock()
	return nil
}

// Finish closes the wizard and returns what the first sync run needs.
func (f *Flow) Finish() runner.StartParams {
	f.mu.Lock()
	defer f.mu.Unlock()
	p := runner.StartParams{FirstSync: f.firstSetup, ConflictMode: f.conflictMode}
	if f.firstSetup {
		f.done("Done and ready to go!")
	}
	f.firstSetup, f.conflictMode, f.pendingDir = false, "", ""
	return p
}

func dirHasEntries(path string) (bool, error) {
	entries, err := os.ReadDir(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	return len(entries) > 0, nil
}
