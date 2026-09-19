// Package syncengine is brick's two-way folder sync: a full reconcile of the
// remote tree against the local folder (driven by a filesystem watcher, an
// incremental check-updates poll and a periodic full backstop), plus live
// status for the UI.
//
// Ported from brick-cli cmd/brick/sync.go @ f3ef7bd — the syncEngine,
// reconcileAll and runSyncLoop. Reconcile semantics are kept identical; see
// internal/PARITY.md. Differences are limited to plumbing: logging and
// activity go through a Sink instead of log.Printf, timings are Options, the
// state path is injected, and there is no TUI/signal handling.
package syncengine

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/webbite-io/brick-wails/internal/storage"
)

// Sink receives the engine's log lines, activity and status-change pings.
// Implementations must be safe for concurrent use and must not block.
type Sink interface {
	Logf(format string, args ...any)
	Activity(ev ActivityEvent)
	StatusChanged()
}

// NopSink discards everything.
type NopSink struct{}

func (NopSink) Logf(string, ...any)    {}
func (NopSink) Activity(ActivityEvent) {}
func (NopSink) StatusChanged()         {}

// Options are the loop timings. Zero values take brick-cli's defaults.
type Options struct {
	PollInterval       time.Duration // remote check-updates poll (20s)
	Debounce           time.Duration // coalesce watcher events (750ms)
	FullReconcileEvery int           // polls between forced full reconciles (90 ≈ 30 min)
	RecentWindow       time.Duration // ignore watcher echoes of our own writes (3s)
}

func (o Options) withDefaults() Options {
	if o.PollInterval <= 0 {
		o.PollInterval = 20 * time.Second
	}
	if o.Debounce <= 0 {
		o.Debounce = 750 * time.Millisecond
	}
	if o.FullReconcileEvery <= 0 {
		o.FullReconcileEvery = 90
	}
	if o.RecentWindow <= 0 {
		o.RecentWindow = 3 * time.Second
	}
	return o
}

// Config builds an Engine.
type Config struct {
	Storage   *storage.Client
	Folder    string // absolute sync folder
	AccountID string
	RootID    string
	// ExcludeDirs are slash-separated folder paths relative to Folder.
	ExcludeDirs []string
	// FirstSync/ConflictMode: see Engine.firstSync.
	FirstSync    bool
	ConflictMode string
	StatePath    string
	Sink         Sink
	Options      Options
}

// ErrPausedMidPass signals that a pass unwound early because the engine was
// paused. It is not a failure.
var ErrPausedMidPass = errors.New("sync paused mid-pass")

// ActivityEvent is one recent-activity entry. Kinds: upload, update,
// download, trash, trash-folder, remove, remove-folder, move, move-folder,
// keep-both.
type ActivityEvent struct {
	Kind    string    `json:"kind"`
	RelPath string    `json:"relPath"`
	At      time.Time `json:"at"`
}

// InFlight is the single transfer in progress.
type InFlight struct {
	RelPath   string `json:"relPath"`
	Direction string `json:"direction"` // upload | download
}

// Counters are cumulative since the engine was created.
type Counters struct {
	Uploaded   int64 `json:"uploaded"`
	Downloaded int64 `json:"downloaded"`
	Deleted    int64 `json:"deleted"`
	Moved      int64 `json:"moved"`
}

// Status is the live status (same JSON shape as brick-cli's /v1/status).
type Status struct {
	State               string    `json:"state"` // starting | syncing | idle | error | paused
	Folder              string    `json:"folder"`
	LastError           string    `json:"lastError,omitempty"`
	LastSyncCompletedAt time.Time `json:"lastSyncCompletedAt,omitempty"`
	Counters            Counters  `json:"counters"`
	InFlight            *InFlight `json:"inFlight"`
}

const activityCap = 200

// Engine is one account's sync engine.
type Engine struct {
	sc        *storage.Client
	folder    string
	accountID string
	rootID    string
	statePath string
	sink      Sink
	opts      Options

	excludeDirs []string

	// firstSync is true until the first reconcileAll pass *completes*.
	// conflictMode ("device", "brick" or "copy") says how to resolve a file
	// that exists on both sides with no sync history — only possible during
	// that first pass, when the folder was pre-populated.
	firstSync    bool
	conflictMode string

	mu    sync.Mutex // serializes reconcile + state access
	state *SyncState

	downloaded atomic.Int64
	uploaded   atomic.Int64
	deleted    atomic.Int64
	moved      atomic.Int64

	recentMu        sync.Mutex
	recentlyWritten map[string]time.Time

	paused  atomic.Bool
	trigger chan struct{}

	ctrlMu        sync.RWMutex
	ctrlState     string
	ctrlLastError string
	ctrlLastSync  time.Time
	ctrlInFlight  *InFlight
	ctrlActivity  []ActivityEvent
	ctrlQuota     *storage.Quota
	ctrlQuotaAt   time.Time

	quotaErrOnce sync.Once
}

// New builds an engine, loading the persisted state from cfg.StatePath.
func New(cfg Config) *Engine {
	sink := cfg.Sink
	if sink == nil {
		sink = NopSink{}
	}
	var st *SyncState
	if cfg.StatePath != "" {
		st = LoadState(cfg.StatePath, cfg.Folder)
	} else {
		st = NewState(cfg.Folder)
	}
	e := &Engine{
		sc:              cfg.Storage,
		folder:          cfg.Folder,
		accountID:       cfg.AccountID,
		rootID:          cfg.RootID,
		statePath:       cfg.StatePath,
		sink:            sink,
		opts:            cfg.Options.withDefaults(),
		excludeDirs:     cfg.ExcludeDirs,
		firstSync:       cfg.FirstSync,
		conflictMode:    cfg.ConflictMode,
		state:           st,
		recentlyWritten: map[string]time.Time{},
		trigger:         make(chan struct{}, 1),
	}
	e.ctrlState = "starting"
	return e
}

func (e *Engine) logf(format string, args ...any) { e.sink.Logf(format, args...) }

// Notify wakes the debounced reconcile worker.
func (e *Engine) Notify() {
	select {
	case e.trigger <- struct{}{}:
	default:
	}
}

func (e *Engine) setState(s string) {
	e.ctrlMu.Lock()
	e.ctrlState = s
	e.ctrlMu.Unlock()
	e.sink.StatusChanged()
}

func (e *Engine) setSynced() {
	e.ctrlMu.Lock()
	e.ctrlState = "idle"
	e.ctrlLastError = ""
	e.ctrlLastSync = time.Now()
	e.ctrlMu.Unlock()
	e.sink.StatusChanged()
}

func (e *Engine) setSyncError(err error) {
	e.ctrlMu.Lock()
	e.ctrlState = "error"
	e.ctrlLastError = err.Error()
	e.ctrlMu.Unlock()
	e.sink.StatusChanged()
}

func (e *Engine) setInFlight(relPath, direction string) {
	e.ctrlMu.Lock()
	e.ctrlInFlight = &InFlight{RelPath: relPath, Direction: direction}
	e.ctrlMu.Unlock()
	e.sink.StatusChanged()
}

func (e *Engine) clearInFlight() {
	e.ctrlMu.Lock()
	e.ctrlInFlight = nil
	e.ctrlMu.Unlock()
	e.sink.StatusChanged()
}

func (e *Engine) publishActivity(kind, relPath string) {
	ev := ActivityEvent{Kind: kind, RelPath: relPath, At: time.Now()}
	e.ctrlMu.Lock()
	e.ctrlActivity = append(e.ctrlActivity, ev)
	if len(e.ctrlActivity) > activityCap {
		e.ctrlActivity = e.ctrlActivity[len(e.ctrlActivity)-activityCap:]
	}
	e.ctrlMu.Unlock()
	e.sink.Activity(ev)
	e.sink.StatusChanged()
}

// RecentActivity returns up to limit events, newest first.
func (e *Engine) RecentActivity(limit int) []ActivityEvent {
	e.ctrlMu.RLock()
	defer e.ctrlMu.RUnlock()
	n := len(e.ctrlActivity)
	if limit > n || limit < 0 {
		limit = n
	}
	out := make([]ActivityEvent, limit)
	for i := 0; i < limit; i++ {
		out[i] = e.ctrlActivity[n-1-i]
	}
	return out
}

// SetPaused pauses or resumes. Resuming wakes the reconcile worker at once.
// A pass already in flight stops between files (see checkInterrupted).
func (e *Engine) SetPaused(p bool) {
	if !e.paused.CompareAndSwap(!p, p) {
		return
	}
	if p {
		e.logf("⏸ sync paused")
	} else {
		e.logf("▶ sync resumed")
		e.Notify()
	}
	e.sink.StatusChanged()
}

// PauseAndWait pauses and blocks until any in-flight reconcile pass has
// finished, so the caller may touch the sync folder out-of-band.
func (e *Engine) PauseAndWait() {
	e.SetPaused(true)
	e.mu.Lock()   //nolint:staticcheck // barrier
	e.mu.Unlock() //nolint:staticcheck
}

func (e *Engine) checkInterrupted(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if e.paused.Load() {
		return ErrPausedMidPass
	}
	return nil
}

// Status returns the live status; "paused" overlays the reconcile state.
func (e *Engine) Status() Status {
	e.ctrlMu.RLock()
	defer e.ctrlMu.RUnlock()
	state := e.ctrlState
	if e.paused.Load() {
		state = "paused"
	}
	var inflight *InFlight
	if e.ctrlInFlight != nil {
		c := *e.ctrlInFlight
		inflight = &c
	}
	return Status{
		State:               state,
		Folder:              e.folder,
		LastError:           e.ctrlLastError,
		LastSyncCompletedAt: e.ctrlLastSync,
		Counters: Counters{
			Uploaded:   e.uploaded.Load(),
			Downloaded: e.downloaded.Load(),
			Deleted:    e.deleted.Load(),
			Moved:      e.moved.Load(),
		},
		InFlight: inflight,
	}
}

// RefreshQuota fetches and caches the account's storage quota.
func (e *Engine) RefreshQuota(ctx context.Context) (*storage.Quota, error) {
	if e.sc == nil {
		return nil, errors.New("no storage client")
	}
	q, err := e.sc.Quota(ctx)
	if err != nil {
		return nil, err
	}
	e.ctrlMu.Lock()
	e.ctrlQuota, e.ctrlQuotaAt = q, time.Now()
	e.ctrlMu.Unlock()
	e.sink.StatusChanged()
	return q, nil
}

func (e *Engine) refreshQuotaAsync(ctx context.Context) {
	go func() {
		if _, err := e.RefreshQuota(ctx); err != nil && ctx.Err() == nil {
			e.quotaErrOnce.Do(func() { e.logf("could not read storage quota: %v", err) })
		}
	}()
}

// Quota returns the cached quota (nil until fetched) and its fetch time.
func (e *Engine) Quota() (*storage.Quota, time.Time) {
	e.ctrlMu.RLock()
	defer e.ctrlMu.RUnlock()
	return e.ctrlQuota, e.ctrlQuotaAt
}

func (e *Engine) markRecentlyWritten(abs string) {
	e.recentMu.Lock()
	e.recentlyWritten[abs] = time.Now()
	e.recentMu.Unlock()
}

func (e *Engine) isRecentlyWritten(abs string) bool {
	e.recentMu.Lock()
	defer e.recentMu.Unlock()
	t, ok := e.recentlyWritten[abs]
	if !ok {
		return false
	}
	if time.Since(t) > e.opts.RecentWindow {
		delete(e.recentlyWritten, abs)
		return false
	}
	return true
}

func (e *Engine) saveStateLocked() {
	if e.statePath == "" {
		return
	}
	if err := e.state.Save(e.statePath); err != nil {
		e.logf("could not save sync state: %v", err)
	}
}

func (e *Engine) saveState() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.saveStateLocked()
}

// Summary is a one-line counter summary for the log.
func (e *Engine) Summary() string {
	return fmt.Sprintf("downloaded %d, uploaded %d, removed %d, moved %d",
		e.downloaded.Load(), e.uploaded.Load(), e.deleted.Load(), e.moved.Load())
}
