package syncengine

import (
	"context"
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
func (e *Engine) ReconcileAll(ctx context.Context) (err error) {
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

	remoteFiles, remoteFolders, folderID, err := e.buildRemoteTree(ctx)
	if err != nil {
		return err
	}
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
	for rel := range keys {
		if err := e.checkInterrupted(ctx); err != nil {
			return err
		}
		if err := e.reconcileFile(ctx, rel, remoteFiles, localFiles, remoteFolders, folderID); err != nil {
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
// hard-deletes the incremental feed can't report.
func (e *Engine) ForceReconcile(ctx context.Context) error {
	serverTime, stErr := e.sc.ServerNow(ctx)
	if stErr != nil {
		serverTime = 0
	}
	if err := e.ReconcileAll(ctx); err != nil {
		return err
	}
	if serverTime > 0 {
		e.setCursor(serverTime)
	}
	return nil
}

func (e *Engine) pruneRemoteSubtree(rel string, remoteFolders, remoteFiles map[string]storage.Node, folderID map[string]string) {
	prefix := rel + "/"
	delete(remoteFolders, rel)
	delete(folderID, rel)
	delete(e.state.Folders, rel)
	delete(e.state.FolderIDs, rel)
	for k := range remoteFolders {
		if strings.HasPrefix(k, prefix) {
			delete(remoteFolders, k)
			delete(folderID, k)
			delete(e.state.Folders, k)
			delete(e.state.FolderIDs, k)
		}
	}
	for k := range remoteFiles {
		if strings.HasPrefix(k, prefix) {
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

func (e *Engine) reconcileFile(ctx context.Context, rel string, remoteFiles map[string]storage.Node, localFiles map[string]int64, remoteFolders map[string]storage.Node, folderID map[string]string) error {
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
		case e.firstSync && !hasEntry:
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
