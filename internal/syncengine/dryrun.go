package syncengine

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"

	"github.com/webbite-io/brick-wails/internal/storage"
)

// Ported from brick-cli sync.go: dryRunClassify, runSyncDryRun.

// Change is one pending change a dry run found: the file, and what a real
// pass would do to it.
type Change struct {
	RelPath string `json:"relPath"`
	Label   string `json:"label"`
}

// DryRun reports what one ReconcileAll pass would do right now — every file
// that would be uploaded, downloaded, or otherwise changed — without
// transferring, deleting, or writing anything: no blob moves in either
// direction, and the sync-state file is never saved. It runs the same tree
// comparison and content-MD5 verification the real pass uses (see
// verifyUnsyncedFileMatches), then classifies each file instead of acting on
// it. The result is sorted by path.
func (e *Engine) DryRun(ctx context.Context) ([]Change, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	remoteFiles, _, _, err := e.buildRemoteTree(ctx)
	if err != nil {
		return nil, fmt.Errorf("could not read remote files: %w", err)
	}
	localFiles, _, err := e.buildLocalTree()
	if err != nil {
		return nil, fmt.Errorf("could not read local files: %w", err)
	}
	// Same content-MD5 comparison the real pass would make, so a dry run
	// reports the same skip decisions it would actually make.
	verifiedIdentical := e.verifyUnsyncedFileMatches(remoteFiles, localFiles)

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
	rels := make([]string, 0, len(keys))
	for k := range keys {
		rels = append(rels, k)
	}
	sort.Strings(rels)

	var changes []Change
	for _, rel := range rels {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		label, ok := e.dryRunClassify(rel, remoteFiles, localFiles, verifiedIdentical)
		if !ok {
			continue
		}
		changes = append(changes, Change{RelPath: rel, Label: label})
	}
	return changes, nil
}

// dryRunClassify is the read-only counterpart of reconcileFile's decision
// tree: it reports what a real pass would do to rel without doing it. It
// mirrors reconcileFile's cases one-for-one (folder plumbing aside — a dry
// run reports file-level transfers, not the remote parent folders an upload
// would create on demand) and must be kept in step with it if that logic ever
// changes.
//
// Returns ok=false for a path reconcileFile would leave untouched: excluded,
// already in sync, verified identical with nothing to record, or gone on both
// sides.
func (e *Engine) dryRunClassify(rel string, remoteFiles map[string]storage.Node, localFiles map[string]int64, verifiedIdentical map[string]bool) (label string, ok bool) {
	if isExcludedPath(rel, e.excludeDirs) {
		return "", false
	}

	abs := filepath.Join(e.folder, filepath.FromSlash(rel))
	remoteNode, hasRemote := remoteFiles[rel]
	_, localExists := localFiles[rel]
	entry, hasEntry := e.state.Entries[rel]

	switch {
	case hasRemote && !localExists:
		if hasEntry {
			return "To be deleted remotely. Removed locally.", true
		}
		return "To be downloaded. Exists remotely but not locally.", true

	case !hasRemote && localExists:
		if hasEntry {
			if localHash, err := hashFile(abs); err == nil && entry.LocalHash == localHash {
				return "To be removed locally. Deleted on the server.", true
			}
		}
		return "To be uploaded. Exists locally but not remotely.", true

	case hasRemote && localExists:
		localHash, err := hashFile(abs)
		if err != nil {
			return fmt.Sprintf("Could not be checked: %v", err), true
		}
		remoteChanged := !hasEntry || entry.RemoteEtag != remoteNode.Etag
		localChanged := !hasEntry || entry.LocalHash != localHash
		switch {
		case !remoteChanged && !localChanged:
			return "", false // already in sync
		case !hasEntry && verifiedIdentical[rel]:
			return "", false // confirmed identical via md5, nothing to transfer
		case !hasEntry:
			// No sync history and the content is unverifiable either way (no
			// stored MD5 on the remote node, or the two genuinely differ) —
			// reconcileFile resolves this per e.conflictMode, but only on a
			// true first sync; afterwards it's a plain remote-wins, same as
			// any other remote-changed file below.
			verb := "downloaded"
			if e.firstSync {
				switch e.conflictMode {
				case "brick":
					verb = "uploaded"
				case "copy":
					verb = "kept as both copies (local renamed aside)"
				}
			}
			return fmt.Sprintf("To be %s. Exists both locally and remotely but lacks MD5 checksum.", verb), true
		case localChanged && !remoteChanged:
			return "To be uploaded. Local copy changed since last sync.", true
		default:
			return "To be downloaded. Remote copy changed since last sync.", true
		}

	default:
		return "", false // gone on both sides
	}
}
