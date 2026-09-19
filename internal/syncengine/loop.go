package syncengine

import (
	"context"
	"errors"
	"io/fs"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/webbite-io/brick-wails/internal/auth"
)

// NewWatcherWithRetry tries a few times to create an fsnotify watcher, since
// EMFILE against fs.inotify.max_user_instances is often transient.
func NewWatcherWithRetry() (*fsnotify.Watcher, error) {
	var lastErr error
	for i := 0; i < 3; i++ {
		if i > 0 {
			time.Sleep(500 * time.Millisecond)
		}
		w, err := fsnotify.NewWatcher()
		if err == nil {
			return w, nil
		}
		lastErr = err
	}
	return nil, lastErr
}

// AddWatchesRecursive watches every directory under the sync folder. No-op on
// a nil watcher (poll-only mode).
func (e *Engine) AddWatchesRecursive(w *fsnotify.Watcher) {
	if w == nil {
		return
	}
	_ = filepath.WalkDir(e.folder, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			_ = w.Add(path)
		}
		return nil
	})
}

// Run performs the initial full reconcile, then keeps the folder in sync —
// filesystem watcher + debounce, incremental remote poll, periodic full
// backstop — until ctx is cancelled (returns nil) or the session expires
// (returns an error wrapping auth.ErrSessionExpired). State is saved on exit.
//
// Ported from brick-cli's runSyncLoop, minus TUI, signals, detach and the
// control API (served separately by internal/controlapi).
func (e *Engine) Run(parent context.Context) error {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()

	var fatalMu sync.Mutex
	var fatal error
	fail := func(err error) {
		fatalMu.Lock()
		if fatal == nil {
			fatal = err
		}
		fatalMu.Unlock()
		cancel()
	}

	e.refreshQuotaAsync(ctx)

	// Capture the server clock *before* the initial walk, so anything that
	// changes during it is caught by the next poll.
	bootstrapTime, btErr := e.sc.ServerNow(ctx)
	if btErr != nil && ctx.Err() == nil {
		e.logf("could not read server time for incremental sync: %v", btErr)
	}

	if err := e.ReconcileAll(ctx); err != nil {
		if errors.Is(err, auth.ErrSessionExpired) {
			e.saveState()
			return err
		}
		if ctx.Err() == nil && !errors.Is(err, ErrPausedMidPass) {
			e.logf("initial sync error: %v", err)
		}
	}
	if bootstrapTime > 0 {
		e.setCursor(bootstrapTime)
	}

	watcher, watcherErr := NewWatcherWithRetry()
	if watcherErr != nil {
		e.logf("⚠ could not create a filesystem watcher (%v) — falling back to polling only; local changes may take up to ~30 minutes to sync", watcherErr)
	} else {
		defer watcher.Close()
		e.AddWatchesRecursive(watcher)
	}

	var wg sync.WaitGroup

	// Debounced reconcile worker.
	wg.Add(1)
	go func() {
		defer wg.Done()
		var timer *time.Timer
		var timerC <-chan time.Time
		for {
			select {
			case <-ctx.Done():
				if timer != nil {
					timer.Stop()
				}
				return
			case <-e.trigger:
				if timer == nil {
					timer = time.NewTimer(e.opts.Debounce)
					timerC = timer.C
				} else {
					timer.Reset(e.opts.Debounce)
				}
			case <-timerC:
				timer, timerC = nil, nil
				if e.paused.Load() {
					continue
				}
				if err := e.ReconcileAll(ctx); err != nil {
					if errors.Is(err, auth.ErrSessionExpired) {
						e.logf("session expired — log in again to resume syncing")
						fail(err)
						return
					}
					if ctx.Err() == nil && !errors.Is(err, ErrPausedMidPass) {
						e.logf("sync error: %v", err)
					}
				} else {
					e.refreshQuotaAsync(ctx)
				}
				e.AddWatchesRecursive(watcher)
			}
		}
	}()

	// Periodic remote poll with a forced full reconcile every
	// FullReconcileEvery ticks (catches purged nodes the feed can't report).
	wg.Add(1)
	go func() {
		defer wg.Done()
		t := time.NewTicker(e.opts.PollInterval)
		defer t.Stop()
		ticks := 0
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if e.paused.Load() {
					continue
				}
				ticks++
				var reconciled bool
				var pollErr error
				if ticks >= e.opts.FullReconcileEvery {
					ticks = 0
					pollErr = e.ForceReconcile(ctx)
					reconciled = pollErr == nil
				} else {
					reconciled, pollErr = e.PollRemoteChanges(ctx)
				}
				if pollErr != nil {
					if errors.Is(pollErr, auth.ErrSessionExpired) {
						e.logf("session expired — log in again to resume syncing")
						fail(pollErr)
						return
					}
					if ctx.Err() == nil && !errors.Is(pollErr, ErrPausedMidPass) {
						e.logf("remote poll error: %v", pollErr)
					}
					continue
				}
				if reconciled {
					e.AddWatchesRecursive(watcher)
					e.refreshQuotaAsync(ctx)
				}
			}
		}
	}()

	// Watcher event loop.
	if watcher != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				case ev, ok := <-watcher.Events:
					if !ok {
						return
					}
					if strings.HasSuffix(ev.Name, TmpSuffix) || e.isRecentlyWritten(ev.Name) {
						continue
					}
					e.Notify()
				case werr, ok := <-watcher.Errors:
					if !ok {
						return
					}
					e.logf("watcher error: %v", werr)
				}
			}
		}()
	}

	<-ctx.Done()
	wg.Wait()
	e.saveState()
	e.logf("sync stopped (%s)", e.Summary())

	fatalMu.Lock()
	defer fatalMu.Unlock()
	return fatal
}
