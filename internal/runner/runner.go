// Package runner owns the lifecycle of syncing inside the app: the instance
// lock (shared with brick-cli), the sync engine, the remote-file agent and
// the local control API — and the app-level state shown in the UI when no
// engine is running (not-configured, auth-required, locked, stopped).
package runner

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/webbite-io/brick-wails/internal/agent"
	"github.com/webbite-io/brick-wails/internal/auth"
	"github.com/webbite-io/brick-wails/internal/brickcfg"
	"github.com/webbite-io/brick-wails/internal/controlapi"
	"github.com/webbite-io/brick-wails/internal/lock"
	"github.com/webbite-io/brick-wails/internal/storage"
	"github.com/webbite-io/brick-wails/internal/syncengine"
)

// App-level states (in addition to the engine's starting/syncing/idle/
// error/paused).
const (
	StateNotConfigured = "not-configured"
	StateAuthRequired  = "auth-required"
	StateLocked        = "locked"
	StateStopped       = "stopped"
)

// Errors returned by Start.
var (
	ErrNotConfigured = errors.New("brick is not set up yet")
	ErrLocked        = errors.New("the Brick CLI is already syncing on this computer")
)

// Status is what the UI renders (engine status plus app-level state). JSON
// shape is a superset of brick-cli's /v1/status.
type Status struct {
	syncengine.Status
	Running bool `json:"running"`
}

// Events receives status/activity/log output. Must not block.
type Events interface {
	Status(Status)
	Activity(syncengine.ActivityEvent)
	Logf(format string, args ...any)
}

// Config wires a Runner.
type Config struct {
	Env     brickcfg.Env
	Store   *brickcfg.Store
	Tokens  *auth.TokenSource
	Version string
	Events  Events
	// EngineOptions override loop timings (tests).
	EngineOptions syncengine.Options
	// DisableAgent / DisableControlAPI turn those side services off (tests).
	DisableAgent      bool
	DisableControlAPI bool
}

// StartParams carry the onboarding decisions into the first run.
type StartParams struct {
	FirstSync    bool
	ConflictMode string
}

// Runner manages at most one running engine.
type Runner struct {
	cfg Config

	mu        sync.Mutex
	eng       *syncengine.Engine
	cancel    context.CancelFunc
	done      chan struct{}
	state     string
	lastError string
	accountID string
	clientID  string

	statusMu      sync.Mutex
	lastEmit      time.Time
	pendingStatus bool
}

// New returns a Runner in the not-configured state.
func New(cfg Config) *Runner {
	return &Runner{cfg: cfg, state: StateNotConfigured}
}

func (r *Runner) logf(format string, args ...any) {
	if r.cfg.Events != nil {
		r.cfg.Events.Logf(format, args...)
	}
}

// Running reports whether an engine is running.
func (r *Runner) Running() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.eng != nil
}

// SetState records an app-level state while no engine runs.
func (r *Runner) SetState(state, lastError string) {
	r.mu.Lock()
	r.state, r.lastError = state, lastError
	r.mu.Unlock()
	r.emitStatus()
}

// Status returns the current status.
func (r *Runner) Status() Status {
	r.mu.Lock()
	eng, state, lastErr := r.eng, r.state, r.lastError
	r.mu.Unlock()
	if eng != nil {
		return Status{Status: eng.Status(), Running: true}
	}
	folder := ""
	if cfg, err := r.cfg.Store.Load(); err == nil {
		if ac := cfg.ActiveAccount(); ac != nil {
			folder = ac.StorageSyncFolder
		}
	}
	return Status{Status: syncengine.Status{State: state, Folder: folder, LastError: lastErr}}
}

// Activity returns recent activity (empty when not running).
func (r *Runner) Activity(limit int) []syncengine.ActivityEvent {
	r.mu.Lock()
	eng := r.eng
	r.mu.Unlock()
	if eng == nil {
		return []syncengine.ActivityEvent{}
	}
	return eng.RecentActivity(limit)
}

// Quota returns the cached quota, if any.
func (r *Runner) Quota() *storage.Quota {
	r.mu.Lock()
	eng := r.eng
	r.mu.Unlock()
	if eng == nil {
		return nil
	}
	q, _ := eng.Quota()
	return q
}

// Account returns (accountId, clientId) of the running engine.
func (r *Runner) Account() (string, string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.accountID, r.clientID
}

// SetPaused pauses/resumes the running engine (no-op when stopped).
func (r *Runner) SetPaused(p bool) {
	r.mu.Lock()
	eng := r.eng
	r.mu.Unlock()
	if eng != nil {
		eng.SetPaused(p)
	}
}

// Start acquires the instance lock and starts syncing the configured account.
// Returns ErrLocked, ErrNotConfigured, an error wrapping
// auth.ErrSessionExpired, or a Storage API error; the app-level state is set
// accordingly.
func (r *Runner) Start(p StartParams) error {
	r.mu.Lock()
	if r.eng != nil {
		r.mu.Unlock()
		return nil
	}
	r.mu.Unlock()

	fail := func(state string, err error) error {
		r.SetState(state, err.Error())
		return err
	}

	lockPath, err := lock.PathIn(r.cfg.Store.Dir())
	if err != nil {
		return fail(StateStopped, err)
	}
	lk, err := lock.Acquire(lockPath)
	if err != nil {
		if errors.Is(err, lock.ErrLocked) {
			return fail(StateLocked, ErrLocked)
		}
		return fail(StateStopped, err)
	}
	ok := false
	defer func() {
		if !ok {
			lk.Release()
		}
	}()

	cfg, _, err := r.cfg.Store.LoadOrCreate()
	if err != nil {
		return fail(StateStopped, err)
	}
	ac := cfg.ActiveAccount()
	if cfg.ActiveAccountID == "" || ac == nil || strings.TrimSpace(ac.StorageSyncFolder) == "" {
		return fail(StateNotConfigured, ErrNotConfigured)
	}
	folder := ac.StorageSyncFolder
	if err := os.MkdirAll(folder, 0o755); err != nil {
		return fail(StateStopped, fmt.Errorf("could not create sync folder: %w", err))
	}

	if err := r.cfg.Tokens.Reload(); err != nil {
		return fail(StateStopped, err)
	}
	ctx0, cancel0 := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel0()
	if err := r.cfg.Tokens.EnsureAccess(ctx0); err != nil {
		return fail(StateAuthRequired, err)
	}
	sc := &storage.Client{BaseURL: r.cfg.Env.StorageAPIURL, AccountID: cfg.ActiveAccountID, Auth: auth.NewClient(r.cfg.Tokens)}
	root, err := sc.ResolveRoot(ctx0)
	if err != nil {
		if errors.Is(err, auth.ErrSessionExpired) {
			return fail(StateAuthRequired, err)
		}
		return fail(StateStopped, fmt.Errorf("could not reach the Brick storage API at %s: %w", r.cfg.Env.StorageAPIURL, err))
	}

	eng := syncengine.New(syncengine.Config{
		Storage:      sc,
		Folder:       folder,
		AccountID:    cfg.ActiveAccountID,
		RootID:       root.ID,
		ExcludeDirs:  ac.ExcludeDirs,
		FirstSync:    p.FirstSync,
		ConflictMode: p.ConflictMode,
		StatePath:    syncengine.StatePath(r.cfg.Store.Dir(), cfg.ActiveAccountID),
		Sink:         &sink{r: r},
		Options:      r.cfg.EngineOptions,
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	r.mu.Lock()
	r.eng, r.cancel, r.done = eng, cancel, done
	r.state, r.lastError = "", ""
	r.accountID, r.clientID = cfg.ActiveAccountID, cfg.ClientID
	r.mu.Unlock()
	ok = true

	roots := agent.ResolveRoots(cfg.AgentRoots)
	var ctrl *controlapi.Server
	if !r.cfg.DisableControlAPI {
		ctrl, err = controlapi.Start(controlapi.Options{ConfigDir: r.cfg.Store.Dir(), Version: r.cfg.Version, RemoteControl: cfg.RemoteControl, AgentRoots: roots}, controlapi.Hooks{
			Engine:  eng,
			Account: r.Account,
			Quit: func() {
				r.logf("sync stopped by the Brick CLI")
				r.stop(StateStopped, "Syncing was stopped by the Brick CLI.")
			},
		})
		if err != nil {
			r.logf("could not start control API: %v", err)
		}
	}
	var agentWG sync.WaitGroup
	if !r.cfg.DisableAgent {
		agentWG.Add(1)
		go func() {
			defer agentWG.Done()
			if err := agent.Run(ctx, agent.Options{Storage: sc, Tokens: r.cfg.Tokens, ClientID: cfg.ClientID, Roots: roots, RemoteControl: cfg.RemoteControl, Logf: r.logf}); err != nil {
				r.logf("%v", err)
			}
		}()
	}

	r.logf("syncing %s (account %s)", folder, cfg.ActiveAccountID)
	go func() {
		runErr := eng.Run(ctx)
		cancel()
		agentWG.Wait()
		if ctrl != nil {
			ctrl.Close()
		}
		lk.Release()

		r.mu.Lock()
		r.eng, r.cancel = nil, nil
		switch {
		case errors.Is(runErr, auth.ErrSessionExpired):
			r.state, r.lastError = StateAuthRequired, "Your session has expired. Log in again to resume syncing."
		case r.state == "":
			r.state = StateStopped
		}
		r.mu.Unlock()
		close(done)
		r.emitStatus()
	}()
	r.emitStatus()
	return nil
}

// Stop stops syncing and waits (up to 15s) for a clean shutdown.
func (r *Runner) Stop() { r.stop(StateStopped, "") }

func (r *Runner) stop(state, msg string) {
	r.mu.Lock()
	cancel, done := r.cancel, r.done
	if cancel != nil {
		r.state, r.lastError = state, msg
	}
	r.mu.Unlock()
	if cancel == nil {
		return
	}
	cancel()
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		r.logf("timed out waiting for sync to stop")
	}
}

// Done returns a channel closed when the current engine run ends (nil when
// nothing runs).
func (r *Runner) Done() <-chan struct{} {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.eng == nil {
		return nil
	}
	return r.done
}

// emitStatus pushes status, throttled to ~10/s with a trailing emit.
func (r *Runner) emitStatus() {
	if r.cfg.Events == nil {
		return
	}
	r.statusMu.Lock()
	since := time.Since(r.lastEmit)
	if since < 100*time.Millisecond {
		if !r.pendingStatus {
			r.pendingStatus = true
			time.AfterFunc(100*time.Millisecond-since, func() {
				r.statusMu.Lock()
				r.pendingStatus = false
				r.lastEmit = time.Now()
				r.statusMu.Unlock()
				r.cfg.Events.Status(r.Status())
			})
		}
		r.statusMu.Unlock()
		return
	}
	r.lastEmit = time.Now()
	r.statusMu.Unlock()
	r.cfg.Events.Status(r.Status())
}

type sink struct{ r *Runner }

func (s *sink) Logf(format string, args ...any) { s.r.logf(format, args...) }
func (s *sink) Activity(ev syncengine.ActivityEvent) {
	if s.r.cfg.Events != nil {
		s.r.cfg.Events.Activity(ev)
	}
}
func (s *sink) StatusChanged() { s.r.emitStatus() }
