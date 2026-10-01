package syncengine

import (
	"context"
	"crypto/md5"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/webbite-io/brick-wails/internal/auth"
	"github.com/webbite-io/brick-wails/internal/storage"
)

// TmpSuffix marks in-progress downloads; the tree walk and watcher skip them.
const TmpSuffix = ".brick-tmp"

// Ported 1:1 from brick-cli sync.go @ f3ef7bd: buildRemoteTree … recordUpload.

func (e *Engine) buildRemoteTree(ctx context.Context) (files, folders map[string]storage.Node, folderID map[string]string, err error) {
	files = map[string]storage.Node{}
	folders = map[string]storage.Node{}
	folderID = map[string]string{"": e.rootID}

	type item struct{ id, rel string }
	queue := []item{{e.rootID, ""}}
	for len(queue) > 0 {
		if err := ctx.Err(); err != nil {
			return nil, nil, nil, err
		}
		cur := queue[0]
		queue = queue[1:]
		children, listErr := e.sc.ListChildren(ctx, cur.id)
		if listErr != nil {
			return nil, nil, nil, listErr
		}
		for _, ch := range children {
			if ch.IsDeleted {
				continue
			}
			rel := ch.Name
			if cur.rel != "" {
				rel = cur.rel + "/" + ch.Name
			}
			switch ch.NodeType {
			case "folder":
				folders[rel] = ch
				folderID[rel] = ch.ID
				queue = append(queue, item{ch.ID, rel})
			case "file":
				files[rel] = ch
			}
		}
	}
	return files, folders, folderID, nil
}

// buildRemoteIDIndex returns the ID -> rel path index applyRemoteDelta needs,
// covering both files and folders. Rebuilt from scratch alongside every full
// buildRemoteTree walk.
func buildRemoteIDIndex(files, folders map[string]storage.Node) map[string]string {
	idx := make(map[string]string, len(files)+len(folders))
	for rel, n := range files {
		idx[n.ID] = rel
	}
	for rel, n := range folders {
		idx[n.ID] = rel
	}
	return idx
}

// applyRemoteDelta patches the cached remote tree in place from a batch of
// changed nodes CheckUpdatesDelta returned, in place of a full recursive
// ListChildren walk of every folder just to rediscover the handful of nodes the
// server already named. nodes is one CheckUpdatesDelta call's worth; a backlog
// too big for that is handled by falling back to a real walk instead of calling
// this, not by calling it repeatedly.
//
// A node with IsDeleted true is a trash (a soft delete, or a purge of something
// already trashed). The server bumps updated_at on every descendant of a
// trashed folder as well as the folder itself, so each one arrives here as its
// own row — no manual subtree cascade is needed, unlike pruneRemoteSubtree,
// which needs one because a trash this client performs is never reported back
// to it in time.
//
// A live node whose path differs from the rel it was cached under has moved or
// been renamed. For a folder that also has to carry its already-cached
// descendants along (rewriteRemotePrefix), because a move bumps only the moved
// row's own timestamp: the server's repointSubtree rewrites each descendant's
// ancestor chain without touching its updated_at, so the feed never mentions
// them even though all of their resolved paths just changed.
//
// Caller must hold e.mu.
func (e *Engine) applyRemoteDelta(nodes []storage.Node) {
	for _, n := range nodes {
		rel := strings.Trim(n.Path, "/")
		oldRel, hadOld := e.remoteTreeIDToRel[n.ID]

		if n.IsDeleted {
			if hadOld {
				delete(e.remoteTreeFiles, oldRel)
				delete(e.remoteTreeFolders, oldRel)
				delete(e.remoteTreeFolderID, oldRel)
				delete(e.remoteTreeIDToRel, n.ID)
			} else if rel != "" {
				// Never cached under any rel (it arrived and left inside one
				// delta, or predates this cache) — still delete at the
				// reported path, in case it sits there under a stale
				// assumption.
				delete(e.remoteTreeFiles, rel)
				delete(e.remoteTreeFolders, rel)
				delete(e.remoteTreeFolderID, rel)
			}
			continue
		}

		if rel == "" {
			// A live node with no readable path shouldn't happen; skip it
			// rather than mis-file the whole subtree at the root.
			continue
		}
		switch n.NodeType {
		case "folder":
			if hadOld && oldRel != rel {
				e.rewriteRemotePrefix(oldRel, rel)
				delete(e.remoteTreeFolders, oldRel)
				delete(e.remoteTreeFolderID, oldRel)
			}
			e.remoteTreeFolders[rel] = n
			e.remoteTreeFolderID[rel] = n.ID
			e.remoteTreeIDToRel[n.ID] = rel
		case "file":
			if hadOld && oldRel != rel {
				delete(e.remoteTreeFiles, oldRel)
			}
			e.remoteTreeFiles[rel] = n
			e.remoteTreeIDToRel[n.ID] = rel
		}
	}
}

// rewriteRemotePrefix re-keys every cached remote entry under oldRel's subtree
// to sit under newRel instead — the remote-cache counterpart of rewritePrefix,
// and the step a folder move needs because the change feed reports only the
// moved folder's own row (see applyRemoteDelta). The folder's own entry is the
// caller's to handle.
//
// Caller must hold e.mu.
func (e *Engine) rewriteRemotePrefix(oldRel, newRel string) {
	oldPrefix, newPrefix := oldRel+"/", newRel+"/"
	// Re-keying while ranging is safe here: a rewritten key always starts with
	// newPrefix, and a folder can never be moved inside its own subtree (the
	// server rejects that), so a key this loop inserts can never match
	// oldPrefix and be rewritten twice.
	for k, n := range e.remoteTreeFolders {
		if !strings.HasPrefix(k, oldPrefix) {
			continue
		}
		nk := newPrefix + strings.TrimPrefix(k, oldPrefix)
		delete(e.remoteTreeFolders, k)
		e.remoteTreeFolders[nk] = n
		if id, ok := e.remoteTreeFolderID[k]; ok {
			delete(e.remoteTreeFolderID, k)
			e.remoteTreeFolderID[nk] = id
		}
		e.remoteTreeIDToRel[n.ID] = nk
	}
	for k, n := range e.remoteTreeFiles {
		if !strings.HasPrefix(k, oldPrefix) {
			continue
		}
		nk := newPrefix + strings.TrimPrefix(k, oldPrefix)
		delete(e.remoteTreeFiles, k)
		e.remoteTreeFiles[nk] = n
		e.remoteTreeIDToRel[n.ID] = nk
	}
}

// remoteTree is one pass's view of the remote side: buildRemoteTree's three
// maps, however this pass came by them.
type remoteTree struct {
	files    map[string]storage.Node
	folders  map[string]storage.Node
	folderID map[string]string
}

// fetchRemoteTree returns the remote tree for one reconcile pass without
// walking it where it can be avoided. A cheap CheckUpdates probe against
// remoteTreeAsOf settles which of three things happens: nothing changed and the
// cache is handed back as-is; something changed but it fits CheckUpdatesDelta's
// page budget, so those nodes are patched straight into the cache
// (applyRemoteDelta); or the backlog is too big to patch safely (or the probe
// failed outright), and only then is a real recursive ListChildren walk of every
// folder paid for. See the remoteTree* fields for why carrying the cache across
// passes is safe, and reconcileAll's forceFullRemoteWalk for the one case that
// must bypass all of this.
//
// Caller must hold e.mu.
func (e *Engine) fetchRemoteTree(ctx context.Context, forceFullRemoteWalk bool) (remoteTree, error) {
	var walkSeed int64
	haveSeed := false
	if !forceFullRemoteWalk && e.remoteTreeAsOf > 0 {
		deltaNodes, serverTime, tooMany, checkErr := e.sc.CheckUpdatesDelta(ctx, e.remoteTreeAsOf)
		if checkErr == nil && !tooMany {
			e.applyRemoteDelta(deltaNodes)
			e.remoteTreeAsOf = serverTime
			return remoteTree{e.remoteTreeFiles, e.remoteTreeFolders, e.remoteTreeFolderID}, nil
		}
		if checkErr == nil {
			// Too big a backlog to patch safely -> a real walk is needed
			// below, but this probe's serverTime still seeds the refreshed
			// cache, sparing a second request just to ask the same question
			// again.
			walkSeed, haveSeed = serverTime, true
		}
		// A failed probe falls through to a real walk too: the cache can't be
		// trusted without being able to ask whether it's still valid.
	}
	if !haveSeed {
		if serverTime, stErr := e.sc.ServerNow(ctx); stErr == nil {
			walkSeed, haveSeed = serverTime, true
		}
	}

	files, folders, folderID, err := e.buildRemoteTree(ctx)
	if err != nil {
		return remoteTree{}, err
	}
	e.remoteTreeFiles, e.remoteTreeFolders, e.remoteTreeFolderID = files, folders, folderID
	e.remoteTreeIDToRel = buildRemoteIDIndex(files, folders)
	if haveSeed {
		e.remoteTreeAsOf = walkSeed
	} else {
		// Couldn't establish a safe "as of" timestamp for this walk (the
		// ServerNow probe failed too) -> don't cache it, so the next
		// reconcile pays for a fresh walk rather than trusting an unstamped
		// snapshot.
		e.remoteTreeAsOf = 0
	}
	return remoteTree{files, folders, folderID}, nil
}

func (e *Engine) buildLocalTree() (files map[string]int64, dirs map[string]bool, err error) {
	files = map[string]int64{}
	dirs = map[string]bool{}
	err = filepath.WalkDir(e.folder, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == e.folder {
			return nil
		}
		if strings.HasSuffix(path, TmpSuffix) {
			return nil
		}
		rel := filepath.ToSlash(mustRel(e.folder, path))
		if d.IsDir() {
			dirs[rel] = true
			return nil
		}
		info, infoErr := d.Info()
		if infoErr != nil {
			return infoErr
		}
		files[rel] = info.Size()
		return nil
	})
	return files, dirs, err
}

// applyRemoteFolderMoves mirrors server-side folder moves/renames (same node
// ID, new path) with one local os.Rename, shallowest first, re-pointing any
// nested pending move at wherever its ancestor's rename carried it.
func (e *Engine) applyRemoteFolderMoves(remoteFolders map[string]storage.Node, localDirs map[string]bool, localFiles map[string]int64) {
	oldRelByID := map[string]string{}
	for rel, id := range e.state.FolderIDs {
		oldRelByID[id] = rel
	}

	type move struct{ oldRel, newRel string }
	var moves []move
	for newRel, node := range remoteFolders {
		if oldRel, ok := oldRelByID[node.ID]; ok && oldRel != newRel {
			moves = append(moves, move{oldRel, newRel})
		}
	}
	sort.Slice(moves, func(i, j int) bool {
		return strings.Count(moves[i].newRel, "/") < strings.Count(moves[j].newRel, "/")
	})

	for i := range moves {
		m := moves[i]
		if m.oldRel == m.newRel {
			continue
		}
		if !localDirs[m.oldRel] || localDirs[m.newRel] {
			continue
		}
		oldAbs := filepath.Join(e.folder, filepath.FromSlash(m.oldRel))
		newAbs := filepath.Join(e.folder, filepath.FromSlash(m.newRel))
		if err := os.MkdirAll(filepath.Dir(newAbs), 0o755); err != nil {
			e.logf("move folder %s -> %s: %v", m.oldRel, m.newRel, err)
			continue
		}
		if err := os.Rename(oldAbs, newAbs); err != nil {
			e.logf("move folder %s -> %s: %v", m.oldRel, m.newRel, err)
			continue
		}
		e.rewritePrefix(m.oldRel, m.newRel, localDirs, localFiles)
		oldPrefix := m.oldRel + "/"
		for j := i + 1; j < len(moves); j++ {
			if moves[j].oldRel == m.oldRel || strings.HasPrefix(moves[j].oldRel, oldPrefix) {
				moves[j].oldRel = m.newRel + moves[j].oldRel[len(m.oldRel):]
			}
		}
		e.moved.Add(1)
		e.logf("→ moved %s to %s", m.oldRel, m.newRel)
		e.publishActivity("move-folder", m.newRel)
	}
}

// applyRemoteFileMoves mirrors server-side file moves with a local rename.
func (e *Engine) applyRemoteFileMoves(remoteFiles map[string]storage.Node, localFiles map[string]int64) {
	oldRelByID := map[string]string{}
	for rel, entry := range e.state.Entries {
		oldRelByID[entry.NodeID] = rel
	}

	for newRel, node := range remoteFiles {
		oldRel, ok := oldRelByID[node.ID]
		if !ok || oldRel == newRel {
			continue
		}
		if _, stillLocal := localFiles[oldRel]; !stillLocal {
			continue
		}
		if _, occupied := localFiles[newRel]; occupied {
			continue
		}
		oldAbs := filepath.Join(e.folder, filepath.FromSlash(oldRel))
		newAbs := filepath.Join(e.folder, filepath.FromSlash(newRel))
		if err := os.MkdirAll(filepath.Dir(newAbs), 0o755); err != nil {
			e.logf("move %s -> %s: %v", oldRel, newRel, err)
			continue
		}
		if err := os.Rename(oldAbs, newAbs); err != nil {
			e.logf("move %s -> %s: %v", oldRel, newRel, err)
			continue
		}
		size := localFiles[oldRel]
		delete(localFiles, oldRel)
		localFiles[newRel] = size
		entry := e.state.Entries[oldRel]
		delete(e.state.Entries, oldRel)
		entry.RelPath = newRel
		entry.RemoteEtag = node.Etag
		e.state.Entries[newRel] = entry
		e.moved.Add(1)
		e.logf("→ moved %s to %s", oldRel, newRel)
		e.publishActivity("move", newRel)
	}
}

func (e *Engine) rewritePrefix(oldRel, newRel string, localDirs map[string]bool, localFiles map[string]int64) {
	matches := func(rel string) bool { return rel == oldRel || strings.HasPrefix(rel, oldRel+"/") }
	rewrite := func(rel string) string {
		if rel == oldRel {
			return newRel
		}
		return newRel + rel[len(oldRel):]
	}

	var dirKeys []string
	for rel := range localDirs {
		if matches(rel) {
			dirKeys = append(dirKeys, rel)
		}
	}
	for _, rel := range dirKeys {
		delete(localDirs, rel)
		localDirs[rewrite(rel)] = true
	}

	var fileKeys []string
	for rel := range localFiles {
		if matches(rel) {
			fileKeys = append(fileKeys, rel)
		}
	}
	for _, rel := range fileKeys {
		size := localFiles[rel]
		delete(localFiles, rel)
		localFiles[rewrite(rel)] = size
	}

	var folderKeys []string
	for rel := range e.state.Folders {
		if matches(rel) {
			folderKeys = append(folderKeys, rel)
		}
	}
	for _, rel := range folderKeys {
		delete(e.state.Folders, rel)
		e.state.Folders[rewrite(rel)] = true
	}

	var folderIDKeys []string
	for rel := range e.state.FolderIDs {
		if matches(rel) {
			folderIDKeys = append(folderIDKeys, rel)
		}
	}
	for _, rel := range folderIDKeys {
		id := e.state.FolderIDs[rel]
		delete(e.state.FolderIDs, rel)
		e.state.FolderIDs[rewrite(rel)] = id
	}

	var entryKeys []string
	for rel := range e.state.Entries {
		if matches(rel) {
			entryKeys = append(entryKeys, rel)
		}
	}
	for _, rel := range entryKeys {
		entry := e.state.Entries[rel]
		delete(e.state.Entries, rel)
		newKey := rewrite(rel)
		entry.RelPath = newKey
		e.state.Entries[newKey] = entry
	}
}

// ReconcileAll performs one full two-way reconciliation. Idempotent; the sync
// index makes already-synced files free. Deletions push from whichever side
// removed a file to the other; on content conflicts remote wins.
func (e *Engine) ReconcileAll(ctx context.Context) error {
	return e.reconcileAll(ctx, false)
}

// reconcileAll is ReconcileAll's body. forceFullRemoteWalk, when true, makes
// the remote-tree fetch below always perform a real recursive ListChildren
// walk of every folder, bypassing the remote tree cache entirely — set only
// by ForceReconcile, for its periodic hard-delete backstop pass (see its doc
// comment for why a cached or check-updates-gated walk can't substitute for a
// true one there). Deliberately a parameter rather than an engine field: e.mu
// below only serializes the body, not however a caller decided to invoke it,
// and ForceReconcile runs on a different goroutine than the debounce worker —
// a field set outside the lock could race a concurrent call reading it inside
// the lock.
func (e *Engine) reconcileAll(ctx context.Context, forceFullRemoteWalk bool) (err error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	if err := e.checkInterrupted(ctx); err != nil {
		return err
	}

	e.setState("syncing")
	defer func() {
		if err != nil && !errors.Is(err, auth.ErrSessionExpired) && !errors.Is(err, ErrPausedMidPass) && ctx.Err() == nil {
			e.setSyncError(err)
		} else if err == nil {
			e.setSynced()
		}
	}()

	rt, err := e.fetchRemoteTree(ctx, forceFullRemoteWalk)
	if err != nil {
		return err
	}
	remoteFiles, remoteFolders, folderID := rt.files, rt.folders, rt.folderID
	localFiles, localDirs, err := e.buildLocalTree()
	if err != nil {
		return err
	}

	// 0. Mirror server-side moves/renames with local renames.
	e.applyRemoteFolderMoves(remoteFolders, localDirs, localFiles)
	e.applyRemoteFileMoves(remoteFiles, localFiles)

	// 1. Previously-synced folders now missing locally -> trash remotely
	//    (shallowest first; the server cascades, so prune the subtree).
	var deletedDirs []string
	for rel := range e.state.Folders {
		if isExcludedPath(rel, e.excludeDirs) {
			continue
		}
		if _, ok := remoteFolders[rel]; !ok {
			continue
		}
		if localDirs[rel] {
			continue
		}
		deletedDirs = append(deletedDirs, rel)
	}
	sort.Slice(deletedDirs, func(i, j int) bool {
		return strings.Count(deletedDirs[i], "/") < strings.Count(deletedDirs[j], "/")
	})
	handledDeletes := map[string]bool{}
	for _, rel := range deletedDirs {
		if err := e.checkInterrupted(ctx); err != nil {
			return err
		}
		if isUnderAny(rel, handledDeletes) {
			continue
		}
		if err := e.sc.Delete(ctx, remoteFolders[rel].ID); err != nil {
			if errors.Is(err, auth.ErrSessionExpired) {
				return err
			}
			e.logf("delete folder %s: %v", rel, err)
			continue
		}
		handledDeletes[rel] = true
		e.pruneRemoteSubtree(rel, remoteFolders, remoteFiles, folderID)
		e.deleted.Add(1)
		e.logf("🗑  trashed folder %s (deleted locally)", rel)
		e.publishActivity("trash-folder", rel)
	}

	// 2. Create local dirs for remote folders (never for excluded ones) and
	//    record every remote folder in the index.
	for rel, node := range remoteFolders {
		if !isExcludedPath(rel, e.excludeDirs) {
			if err := os.MkdirAll(filepath.Join(e.folder, filepath.FromSlash(rel)), 0o755); err != nil {
				e.logf("mkdir %s: %v", rel, err)
			}
		}
		e.state.Folders[rel] = true
		e.state.FolderIDs[rel] = node.ID
	}

	// 3. Reconcile every file across remote ∪ local ∪ index.
	keys := map[string]struct{}{}
	for k := range remoteFiles {
		keys[k] = struct{}{}
	}
	for k := range localFiles {
		keys[k] = struct{}{}
	}
	for k := range e.state.Entries {
		keys[k] = struct{}{}
	}
	// A file present on both sides with no sync-state entry — whether that's
	// because this is the account's very first sync, or because the state
	// file was reset/lost, or a folder was repopulated from another
	// already-synced device after onboarding — would otherwise always be
	// blindly transferred (per e.conflictMode on a true first sync, or
	// "remote wins" otherwise; see reconcileFile). Check up front, once,
	// whether such files are already byte-identical to their remote
	// counterpart (by comparing the content-MD5 the remote tree walk above
	// already returned — no extra request) so the transfer can be skipped.
	verifiedIdentical := e.verifyUnsyncedFileMatches(remoteFiles, localFiles)
	for rel := range keys {
		if err := e.checkInterrupted(ctx); err != nil {
			return err
		}
		if err := e.reconcileFile(ctx, rel, remoteFiles, localFiles, remoteFolders, folderID, verifiedIdentical); err != nil {
			if errors.Is(err, auth.ErrSessionExpired) {
				return err
			}
			e.logf("sync %s: %v", rel, err)
		}
	}

	// 4. Push genuinely new local folders (carries up empty directories).
	for rel := range localDirs {
		if err := e.checkInterrupted(ctx); err != nil {
			return err
		}
		if _, ok := remoteFolders[rel]; ok {
			continue
		}
		if e.state.Folders[rel] {
			continue
		}
		if _, err := e.ensureRemoteFolder(ctx, rel, remoteFolders, folderID); err != nil {
			if errors.Is(err, auth.ErrSessionExpired) {
				return err
			}
			e.logf("create folder %s: %v", rel, err)
		}
	}

	// 5. Remove local folders that were synced but are gone remotely (deepest
	//    first; only if empty — non-empty ones are dropped from the index so
	//    the next pass re-pushes their unsynced content).
	var goneDirs []string
	for rel := range e.state.Folders {
		if _, ok := remoteFolders[rel]; !ok {
			goneDirs = append(goneDirs, rel)
		}
	}
	sort.Slice(goneDirs, func(i, j int) bool {
		return strings.Count(goneDirs[i], "/") > strings.Count(goneDirs[j], "/")
	})
	for _, rel := range goneDirs {
		delete(e.state.Folders, rel)
		delete(e.state.FolderIDs, rel)
		abs := filepath.Join(e.folder, filepath.FromSlash(rel))
		info, err := os.Stat(abs)
		if err != nil || !info.IsDir() {
			continue
		}
		if err := os.Remove(abs); err != nil {
			if !os.IsNotExist(err) {
				e.logf("keeping %s: %v", rel, err)
			}
			continue
		}
		e.deleted.Add(1)
		e.logf("🗑  removed folder %s (deleted on server)", rel)
		e.publishActivity("remove-folder", rel)
	}

	e.saveStateLocked()
	// Cleared only on genuine completion, so a paused first pass keeps
	// applying the onboarding conflict mode on resume.
	e.firstSync = false
	return nil
}

func (e *Engine) cursor() int64 {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.state.ServerTime
}

// setCursor advances (never rewinds) the check-updates cursor and persists it.
func (e *Engine) setCursor(serverTime int64) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if serverTime > e.state.ServerTime {
		e.state.ServerTime = serverTime
		e.saveStateLocked()
	}
}

// PollRemoteChanges asks check-updates whether anything changed since the
// cursor and runs a full reconcile only if so. The cursor advances only after
// a triggered reconcile succeeds.
func (e *Engine) PollRemoteChanges(ctx context.Context) (reconciled bool, err error) {
	changed, serverTime, err := e.sc.CheckUpdates(ctx, e.cursor())
	if err != nil {
		return false, err
	}
	if !changed {
		e.setCursor(serverTime)
		return false, nil
	}
	if err := e.ReconcileAll(ctx); err != nil {
		return false, err
	}
	e.setCursor(serverTime)
	return true, nil
}

// ForceReconcile runs a full reconcile unconditionally — the backstop for
// hard-deletes the incremental feed can't report (a permanently deleted or
// purged node leaves no row to surface as changed). Forces a real remote tree
// walk rather than letting reconcileAll reuse the remote tree cache via its
// own CheckUpdates probe, which is exactly the kind of check that can't see a
// hard delete either — this pass exists specifically to not be satisfied by
// one.
func (e *Engine) ForceReconcile(ctx context.Context) error {
	serverTime, stErr := e.sc.ServerNow(ctx)
	if stErr != nil {
		serverTime = 0
	}
	if err := e.reconcileAll(ctx, true); err != nil {
		return err
	}
	if serverTime > 0 {
		e.setCursor(serverTime)
	}
	return nil
}

func (e *Engine) pruneRemoteSubtree(rel string, remoteFolders, remoteFiles map[string]storage.Node, folderID map[string]string) {
	prefix := rel + "/"
	if n, ok := remoteFolders[rel]; ok {
		delete(e.remoteTreeIDToRel, n.ID)
	}
	delete(remoteFolders, rel)
	delete(folderID, rel)
	delete(e.state.Folders, rel)
	delete(e.state.FolderIDs, rel)
	for k, n := range remoteFolders {
		if strings.HasPrefix(k, prefix) {
			delete(e.remoteTreeIDToRel, n.ID)
			delete(remoteFolders, k)
			delete(folderID, k)
			delete(e.state.Folders, k)
			delete(e.state.FolderIDs, k)
		}
	}
	for k, n := range remoteFiles {
		if strings.HasPrefix(k, prefix) {
			delete(e.remoteTreeIDToRel, n.ID)
			delete(remoteFiles, k)
			delete(e.state.Entries, k)
		}
	}
}

func isUnderAny(rel string, handled map[string]bool) bool {
	for h := range handled {
		if strings.HasPrefix(rel, h+"/") {
			return true
		}
	}
	return false
}

// isExcludedPath reports whether rel is one of excludeDirs or nested under one.
func isExcludedPath(rel string, excludeDirs []string) bool {
	for _, dir := range excludeDirs {
		dir = strings.Trim(filepath.ToSlash(dir), "/")
		if dir == "" {
			continue
		}
		if rel == dir || strings.HasPrefix(rel, dir+"/") {
			return true
		}
	}
	return false
}

// verifyUnsyncedFileMatches checks, for every file present on both sides with
// no sync-state entry (the case reconcileFile would otherwise resolve by
// blind transfer — per e.conflictMode on a true first sync, or "remote wins"
// on any later pass), whether its content already matches the specific remote
// file at that path — so a folder that ends up holding files with no
// sync-state entry (the account's very first sync, the state file having been
// reset, or files copied in from another already-synced device well after
// onboarding) doesn't pay to re-transfer every one of them just to record
// them as synced.
//
// This costs no extra request: the remote tree walk that produced remoteFiles
// (buildRemoteTree, backed by ListChildren) already returns each node's own
// stored content-MD5 (storage.Node.ContentMD5), so verifying is just hashing
// the local file and comparing two strings.
//
// Returns the set of rel paths confirmed identical. A rel's absence from the
// returned set is not proof its content differs: the remote node may simply
// have no stored MD5 to compare against — callers must keep falling back to
// the ordinary resolution for those.
func (e *Engine) verifyUnsyncedFileMatches(remoteFiles map[string]storage.Node, localFiles map[string]int64) map[string]bool {
	var verified map[string]bool
	for rel := range localFiles {
		if isExcludedPath(rel, e.excludeDirs) {
			continue // reconcileExcludedFile handles these, never the conflict case
		}
		remoteNode, hasRemote := remoteFiles[rel]
		if !hasRemote || remoteNode.ContentMD5 == "" {
			continue
		}
		if _, hasEntry := e.state.Entries[rel]; hasEntry {
			continue
		}
		h, err := hashFileMD5(filepath.Join(e.folder, filepath.FromSlash(rel)))
		if err != nil {
			continue // unreadable right now -> let the ordinary path handle/report it
		}
		if h == remoteNode.ContentMD5 {
			if verified == nil {
				verified = map[string]bool{}
			}
			verified[rel] = true
		}
	}
	return verified
}

func (e *Engine) reconcileFile(ctx context.Context, rel string, remoteFiles map[string]storage.Node, localFiles map[string]int64, remoteFolders map[string]storage.Node, folderID map[string]string, verifiedIdentical map[string]bool) error {
	if isExcludedPath(rel, e.excludeDirs) {
		return e.reconcileExcludedFile(rel, remoteFiles, localFiles)
	}

	abs := filepath.Join(e.folder, filepath.FromSlash(rel))
	remoteNode, hasRemote := remoteFiles[rel]
	_, localExists := localFiles[rel]
	entry, hasEntry := e.state.Entries[rel]

	switch {
	case hasRemote && !localExists:
		if hasEntry {
			// Synced before, gone locally -> user deleted it -> trash remotely.
			return e.deleteRemoteFile(ctx, rel, remoteNode.ID)
		}
		return e.downloadFile(ctx, rel, remoteNode)

	case !hasRemote && localExists:
		localHash, err := hashFile(abs)
		if err != nil {
			return err
		}
		if hasEntry && entry.LocalHash == localHash {
			// Deleted on server, unchanged locally -> remove local.
			if err := os.Remove(abs); err != nil && !os.IsNotExist(err) {
				return err
			}
			delete(e.state.Entries, rel)
			e.deleted.Add(1)
			e.logf("🗑  removed %s (deleted on server)", rel)
			e.publishActivity("remove", rel)
			return nil
		}
		return e.uploadNewFile(ctx, rel, remoteFolders, folderID)

	case hasRemote && localExists:
		localHash, err := hashFile(abs)
		if err != nil {
			return err
		}
		remoteChanged := !hasEntry || entry.RemoteEtag != remoteNode.Etag
		localChanged := !hasEntry || entry.LocalHash != localHash
		switch {
		case !remoteChanged && !localChanged:
			return nil
		case !hasEntry && verifiedIdentical[rel]:
			// No local record of this file being synced, but the remote
			// node's own content-MD5 confirms it's byte-identical to the
			// local file at this exact path (see verifyUnsyncedFileMatches)
			// -> nothing to transfer, just record it as synced. Applies
			// whether or not this is the account's very first sync: a folder
			// can end up holding files with no sync-state entry well after
			// onboarding too.
			e.state.Entries[rel] = SyncEntry{
				RelPath:    rel,
				NodeID:     remoteNode.ID,
				RemoteEtag: remoteNode.Etag,
				LocalHash:  localHash,
				LocalSize:  localFiles[rel],
				SyncedAt:   time.Now(),
			}
			e.logf("✓ %s already in sync (content verified)", rel)
			e.publishActivity("verify", rel)
			return nil
		case e.firstSync && !hasEntry:
			// Present on both sides with no prior sync history and not
			// verified identical above: this is the pre-existing-folder
			// conflict the onboarding wizard asked about.
			return e.applyFirstSyncConflict(ctx, rel, remoteNode, remoteFolders, folderID)
		case localChanged && !remoteChanged:
			return e.replaceFile(ctx, rel, remoteNode.ID)
		default:
			return e.downloadFile(ctx, rel, remoteNode)
		}

	default:
		if hasEntry {
			delete(e.state.Entries, rel)
		}
		return nil
	}
}

func (e *Engine) reconcileExcludedFile(rel string, remoteFiles map[string]storage.Node, localFiles map[string]int64) error {
	remoteNode, hasRemote := remoteFiles[rel]
	_, localExists := localFiles[rel]
	entry, hasEntry := e.state.Entries[rel]

	if !hasRemote && !localExists {
		if hasEntry {
			delete(e.state.Entries, rel)
		}
		return nil
	}

	var localHash string
	if localExists {
		h, err := hashFile(filepath.Join(e.folder, filepath.FromSlash(rel)))
		if err != nil {
			return err
		}
		localHash = h
	}
	if hasEntry && entry.LocalHash == localHash && entry.RemoteEtag == remoteNode.Etag {
		return nil
	}
	e.state.Entries[rel] = SyncEntry{RelPath: rel, NodeID: remoteNode.ID, RemoteEtag: remoteNode.Etag, LocalHash: localHash, SyncedAt: time.Now()}
	return nil
}

func (e *Engine) applyFirstSyncConflict(ctx context.Context, rel string, remoteNode storage.Node, remoteFolders map[string]storage.Node, folderID map[string]string) error {
	switch e.conflictMode {
	case "brick":
		return e.replaceFile(ctx, rel, remoteNode.ID)
	case "copy":
		return e.keepBothFile(ctx, rel, remoteNode, remoteFolders, folderID)
	default: // "device" or unset
		return e.downloadFile(ctx, rel, remoteNode)
	}
}

func (e *Engine) keepBothFile(ctx context.Context, rel string, remoteNode storage.Node, remoteFolders map[string]storage.Node, folderID map[string]string) error {
	abs := filepath.Join(e.folder, filepath.FromSlash(rel))
	copyRel := DupPath(rel)
	copyAbs := filepath.Join(e.folder, filepath.FromSlash(copyRel))
	if err := os.Rename(abs, copyAbs); err != nil {
		return err
	}
	if err := e.downloadFile(ctx, rel, remoteNode); err != nil {
		return err
	}
	if err := e.uploadNewFile(ctx, copyRel, remoteFolders, folderID); err != nil {
		return err
	}
	e.logf("⧉ kept both copies of %s (as %s)", rel, copyRel)
	e.publishActivity("keep-both", rel)
	return nil
}

// DupPath inserts " (copy)" before the extension.
func DupPath(rel string) string {
	dir, base := parentOf(rel), baseName(rel)
	ext := filepath.Ext(base)
	newBase := strings.TrimSuffix(base, ext) + " (copy)" + ext
	if dir == "" {
		return newBase
	}
	return dir + "/" + newBase
}

func (e *Engine) downloadFile(ctx context.Context, rel string, node storage.Node) error {
	e.setInFlight(rel, "download")
	defer e.clearInFlight()

	data, etag, err := e.sc.Download(ctx, node.ID)
	if err != nil {
		return err
	}
	if etag == "" {
		etag = node.Etag
	}
	abs := filepath.Join(e.folder, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return err
	}
	e.markRecentlyWritten(abs)
	if err := AtomicWrite(abs, data); err != nil {
		return err
	}
	e.state.Entries[rel] = SyncEntry{RelPath: rel, NodeID: node.ID, RemoteEtag: etag, LocalHash: hashBytes(data), LocalSize: int64(len(data)), SyncedAt: time.Now()}
	e.downloaded.Add(1)
	e.logf("↓ downloaded %s", rel)
	e.publishActivity("download", rel)
	return nil
}

func (e *Engine) deleteRemoteFile(ctx context.Context, rel, nodeID string) error {
	if err := e.sc.Delete(ctx, nodeID); err != nil {
		return err
	}
	delete(e.state.Entries, rel)
	e.deleted.Add(1)
	e.logf("🗑  trashed %s (deleted locally)", rel)
	e.publishActivity("trash", rel)
	return nil
}

func (e *Engine) ensureRemoteFolder(ctx context.Context, rel string, remoteFolders map[string]storage.Node, folderID map[string]string) (string, error) {
	if rel == "" {
		return e.rootID, nil
	}
	if id, ok := folderID[rel]; ok {
		return id, nil
	}
	parentID, err := e.ensureRemoteFolder(ctx, parentOf(rel), remoteFolders, folderID)
	if err != nil {
		return "", err
	}
	node, err := e.sc.CreateFolder(ctx, parentID, baseName(rel))
	if err != nil {
		return "", err
	}
	folderID[rel] = node.ID
	remoteFolders[rel] = *node
	// The ID index has to learn about a folder we create as surely as about
	// one the change feed reports, because the two can race: if this folder is
	// moved remotely before its creation ever shows up in a delta, the delta
	// would carry only its new path, with nothing to say which rel it is
	// moving *from* — leaving the old rel cached as a folder that no longer
	// exists, recreated locally by pass 2 and then fought over by
	// applyRemoteFolderMoves, which would see two rels claiming one node ID.
	e.remoteTreeIDToRel[node.ID] = rel
	e.state.Folders[rel] = true
	e.state.FolderIDs[rel] = node.ID
	return node.ID, nil
}

func (e *Engine) uploadNewFile(ctx context.Context, rel string, remoteFolders map[string]storage.Node, folderID map[string]string) error {
	e.setInFlight(rel, "upload")
	defer e.clearInFlight()

	data, err := os.ReadFile(filepath.Join(e.folder, filepath.FromSlash(rel)))
	if err != nil {
		return err
	}
	parentID, err := e.ensureRemoteFolder(ctx, parentOf(rel), remoteFolders, folderID)
	if err != nil {
		return err
	}
	node, err := e.sc.Upload(ctx, parentID, baseName(rel), data)
	if err != nil {
		return err
	}
	e.recordUpload(rel, node, data)
	e.logf("↑ uploaded %s", rel)
	e.publishActivity("upload", rel)
	return nil
}

func (e *Engine) replaceFile(ctx context.Context, rel, nodeID string) error {
	e.setInFlight(rel, "upload")
	defer e.clearInFlight()

	data, err := os.ReadFile(filepath.Join(e.folder, filepath.FromSlash(rel)))
	if err != nil {
		return err
	}
	node, err := e.sc.Replace(ctx, nodeID, data)
	if err != nil {
		return err
	}
	e.recordUpload(rel, node, data)
	e.logf("↑ uploaded %s (updated)", rel)
	e.publishActivity("update", rel)
	return nil
}

func (e *Engine) recordUpload(rel string, node *storage.Node, data []byte) {
	e.state.Entries[rel] = SyncEntry{RelPath: rel, NodeID: node.ID, RemoteEtag: node.Etag, LocalHash: hashBytes(data), LocalSize: int64(len(data)), SyncedAt: time.Now()}
	e.uploaded.Add(1)
}

// --- helpers ---

// AtomicWrite writes via a TmpSuffix temp file + rename.
func AtomicWrite(path string, data []byte) error {
	tmp := path + TmpSuffix
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

func hashBytes(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// hashFileMD5 returns the whole-file MD5 of path, hex-encoded, in the same
// form the server stores as a node's contentMd5 — compared directly against
// storage.Node.ContentMD5 by verifyUnsyncedFileMatches. Distinct from
// hashFile's sha256, which is purely local bookkeeping and never sent to the
// server.
func hashFileMD5(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := md5.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func mustRel(base, target string) string {
	rel, err := filepath.Rel(base, target)
	if err != nil {
		return target
	}
	return rel
}

func parentOf(rel string) string {
	if i := strings.LastIndex(rel, "/"); i >= 0 {
		return rel[:i]
	}
	return ""
}

func baseName(rel string) string {
	if i := strings.LastIndex(rel, "/"); i >= 0 {
		return rel[i+1:]
	}
	return rel
}
