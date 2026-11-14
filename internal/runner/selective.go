package runner

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/webbite-io/brick-wails/internal/auth"
	"github.com/webbite-io/brick-wails/internal/brickcfg"
	"github.com/webbite-io/brick-wails/internal/lock"
	"github.com/webbite-io/brick-wails/internal/storage"
)

// SelectiveSync is what the tray's Selective Sync window lists: the active
// account's top-level folders on Brick, and which of them are synced (every
// one not in excludeDirs).
type SelectiveSync struct {
	Folders []string `json:"folders"`
	Synced  []string `json:"synced"`
}

// SelectiveSyncFolders fetches the active account's top-level folders, like
// brick-cli's `brick sync -s` does before asking which to sync.
func (r *Runner) SelectiveSyncFolders(ctx context.Context) (*SelectiveSync, error) {
	cfg, err := r.cfg.Store.Load()
	if err != nil {
		return nil, err
	}
	ac := cfg.ActiveAccount()
	if cfg.ActiveAccountID == "" || ac == nil || strings.TrimSpace(ac.StorageSyncFolder) == "" {
		return nil, ErrNotConfigured
	}
	sc := &storage.Client{BaseURL: r.cfg.Env.StorageAPIURL, AccountID: cfg.ActiveAccountID, Auth: auth.NewClient(r.cfg.Tokens)}
	root, err := sc.ResolveRoot(ctx)
	if err != nil {
		return nil, fmt.Errorf("could not reach Brick: %w", err)
	}
	top, err := sc.TopFolders(ctx, root.ID)
	if err != nil {
		return nil, fmt.Errorf("could not list your folders on Brick: %w", err)
	}
	excluded := NormalizeExcludeDirs(ac.ExcludeDirs)
	out := &SelectiveSync{Folders: []string{}, Synced: []string{}}
	for _, n := range top {
		out.Folders = append(out.Folders, n.Name)
		if !slices.Contains(excluded, n.Name) {
			out.Synced = append(out.Synced, n.Name)
		}
	}
	return out, nil
}

// SetSyncedFolders saves which of the listed top-level folders to sync, as
// `brick sync -s` does: the unticked ones are excluded and their local copies
// deleted, and every other excludeDirs entry is kept (see
// SelectiveSyncExcludes). A running engine adopts the new list at once,
// without a restart.
//
// Deleting local copies is only safe while no engine still works from the
// old list (it would read them as deleted locally and trash them on the
// server), so a running engine does it between passes (ExcludeAndRemove),
// and otherwise it happens under the sync lock, which also keeps brick-cli
// from syncing meanwhile.
func (r *Runner) SetSyncedFolders(listed, synced []string) error {
	cfg, err := r.cfg.Store.Load()
	if err != nil {
		return err
	}
	ac := cfg.ActiveAccount()
	if cfg.ActiveAccountID == "" || ac == nil || strings.TrimSpace(ac.StorageSyncFolder) == "" {
		return ErrNotConfigured
	}
	accountID, folder := cfg.ActiveAccountID, ac.StorageSyncFolder
	exclude, newly := SelectiveSyncExcludes(NormalizeExcludeDirs(ac.ExcludeDirs), listed, synced)

	r.startMu.Lock()
	defer r.startMu.Unlock()
	r.mu.Lock()
	eng, engAccount := r.eng, r.accountID
	r.mu.Unlock()
	if eng != nil {
		if engAccount != accountID {
			return errors.New("brick is syncing another account than the active one; restart Brick and try again")
		}
		if err := r.saveExcludeDirs(accountID, exclude); err != nil {
			return err
		}
		err := eng.ExcludeAndRemove(exclude, newly)
		r.logExcludeDirs(exclude)
		return err
	}

	lockPath, err := lock.PathIn(r.cfg.Store.Dir())
	if err != nil {
		return err
	}
	lk, err := lock.Acquire(lockPath)
	if err != nil {
		if errors.Is(err, lock.ErrLocked) {
			return errors.New("the Brick CLI is syncing this folder; stop it and try again")
		}
		return err
	}
	defer lk.Release()
	if err := r.saveExcludeDirs(accountID, exclude); err != nil {
		return err
	}
	r.logExcludeDirs(exclude)
	var errs []error
	for _, rel := range newly {
		if !filepath.IsLocal(filepath.FromSlash(rel)) {
			continue
		}
		abs := filepath.Join(folder, filepath.FromSlash(rel))
		if _, err := os.Lstat(abs); errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err := os.RemoveAll(abs); err != nil {
			errs = append(errs, fmt.Errorf("could not remove %s: %w", rel, err))
			continue
		}
		r.logf("Removed local copy of %s (excluded from sync)", rel)
	}
	return errors.Join(errs...)
}

func (r *Runner) saveExcludeDirs(accountID string, dirs []string) error {
	_, err := r.cfg.Store.Update(func(c *brickcfg.Config) error {
		ac := c.Accounts[accountID]
		if ac == nil {
			return errors.New("the account is no longer configured")
		}
		ac.ExcludeDirs = dirs
		return nil
	})
	return err
}

func (r *Runner) logExcludeDirs(dirs []string) {
	if len(dirs) == 0 {
		r.logf("Excluded folders updated: none")
	} else {
		r.logf("Excluded folders updated: %s", strings.Join(dirs, ", "))
	}
}

// SelectiveSyncExcludes works out the new exclude list after the user ticked
// synced among the listed top-level folders, and which of those it newly
// excludes. Only the listed folders' entries change: any other entry in prev
// (a folder not on Brick yet, such as the Camera Uploads onboarding leaves
// out by default, a nested path added by hand, or a folder currently missing
// on the server) is kept, since the user was never shown it to untick.
// Ported from brick-cli's selectiveSyncExcludes.
func SelectiveSyncExcludes(prev, listed, synced []string) (exclude, newlyExcluded []string) {
	for _, d := range prev {
		if !slices.Contains(listed, d) || !slices.Contains(synced, d) {
			exclude = append(exclude, d)
		}
	}
	for _, name := range listed {
		if !slices.Contains(synced, name) && !slices.Contains(prev, name) {
			exclude = append(exclude, name)
			newlyExcluded = append(newlyExcluded, name)
		}
	}
	return exclude, newlyExcluded
}

// NormalizeExcludeDirs trims the slashes the sync engine ignores anyway
// ("/Music/" excludes the same folder as "Music"), so an entry written by
// hand still matches the folder name it is compared with, and drops empty
// and duplicate entries.
func NormalizeExcludeDirs(dirs []string) []string {
	var out []string
	for _, d := range dirs {
		if d = strings.Trim(filepath.ToSlash(d), "/"); d != "" && !slices.Contains(out, d) {
			out = append(out, d)
		}
	}
	return out
}
