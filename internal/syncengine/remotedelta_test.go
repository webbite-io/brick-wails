package syncengine

import (
	"testing"

	"github.com/webbite-io/brick-wails/internal/storage"
)

// newDeltaEngine builds an Engine with a pre-populated remote-tree cache, as if
// a prior walk or delta had seeded it. No HTTP wiring: applyRemoteDelta and
// rewriteRemotePrefix are pure operations on that cache.
func newDeltaEngine() *Engine {
	e := &Engine{
		remoteTreeFiles: map[string]storage.Node{
			"docs/a.txt": {ID: "a1", NodeType: "file", Path: "/docs/a.txt", Etag: "e-a1"},
			"docs/b.txt": {ID: "b1", NodeType: "file", Path: "/docs/b.txt", Etag: "e-b1"},
		},
		remoteTreeFolders: map[string]storage.Node{
			"docs": {ID: "f1", NodeType: "folder", Path: "/docs"},
		},
		remoteTreeFolderID: map[string]string{"docs": "f1"},
	}
	e.remoteTreeIDToRel = buildRemoteIDIndex(e.remoteTreeFiles, e.remoteTreeFolders)
	return e
}

// A folder move carries only the folder's own row: the server rewrites each
// descendant's ancestor chain without bumping its timestamp, so the feed never
// mentions them even though every one of their resolved paths just changed.
// applyRemoteDelta must relocate the whole cached subtree, not just the folder.
func TestApplyRemoteDeltaRelocatesFolderDescendantsOnMove(t *testing.T) {
	e := newDeltaEngine()

	e.applyRemoteDelta([]storage.Node{{ID: "f1", NodeType: "folder", Path: "/archive"}})

	if _, ok := e.remoteTreeFolders["docs"]; ok {
		t.Error(`remoteTreeFolders["docs"] still present, want it gone after the move`)
	}
	if n, ok := e.remoteTreeFolders["archive"]; !ok || n.ID != "f1" {
		t.Errorf(`remoteTreeFolders["archive"] = %+v (ok=%v), want the moved folder`, n, ok)
	}
	if got := e.remoteTreeFolderID["archive"]; got != "f1" {
		t.Errorf(`remoteTreeFolderID["archive"] = %q, want "f1"`, got)
	}

	for oldRel, newRel := range map[string]string{"docs/a.txt": "archive/a.txt", "docs/b.txt": "archive/b.txt"} {
		if _, ok := e.remoteTreeFiles[oldRel]; ok {
			t.Errorf("remoteTreeFiles[%q] still present, want it relocated to %q", oldRel, newRel)
		}
		if _, ok := e.remoteTreeFiles[newRel]; !ok {
			t.Errorf("remoteTreeFiles[%q] missing, want the descendant carried along by its folder", newRel)
		}
	}

	// The index has to follow too, or the next move of any of these would look
	// like a brand-new node appearing.
	for id, wantRel := range map[string]string{"f1": "archive", "a1": "archive/a.txt", "b1": "archive/b.txt"} {
		if got := e.remoteTreeIDToRel[id]; got != wantRel {
			t.Errorf("remoteTreeIDToRel[%q] = %q, want %q", id, got, wantRel)
		}
	}
}

// A trashed node must be dropped from the rel it was last known under, not
// (only) from whatever path its own row reports.
func TestApplyRemoteDeltaRemovesTrashedFile(t *testing.T) {
	e := newDeltaEngine()

	e.applyRemoteDelta([]storage.Node{{ID: "b1", NodeType: "file", Path: "/docs/b.txt", IsDeleted: true}})

	if _, ok := e.remoteTreeFiles["docs/b.txt"]; ok {
		t.Error(`remoteTreeFiles["docs/b.txt"] still present, want it removed`)
	}
	if _, ok := e.remoteTreeIDToRel["b1"]; ok {
		t.Error(`remoteTreeIDToRel["b1"] still present, want it cleared`)
	}
	if _, ok := e.remoteTreeFiles["docs/a.txt"]; !ok {
		t.Error(`remoteTreeFiles["docs/a.txt"] removed, want the unrelated file untouched`)
	}
}

// A trashed folder needs no cascade here: the server bumps every descendant on a
// soft delete, so each arrives as its own row.
func TestApplyRemoteDeltaRemovesTrashedFolderAndReportedDescendants(t *testing.T) {
	e := newDeltaEngine()

	e.applyRemoteDelta([]storage.Node{
		{ID: "f1", NodeType: "folder", Path: "/docs", IsDeleted: true},
		{ID: "a1", NodeType: "file", Path: "/docs/a.txt", IsDeleted: true},
		{ID: "b1", NodeType: "file", Path: "/docs/b.txt", IsDeleted: true},
	})

	if len(e.remoteTreeFolders) != 0 || len(e.remoteTreeFiles) != 0 {
		t.Errorf("cache not empty after the whole subtree was trashed: folders=%v files=%v",
			e.remoteTreeFolders, e.remoteTreeFiles)
	}
	if len(e.remoteTreeFolderID) != 0 || len(e.remoteTreeIDToRel) != 0 {
		t.Errorf("stale index entries left behind: folderID=%v idToRel=%v",
			e.remoteTreeFolderID, e.remoteTreeIDToRel)
	}
}

// A file moved independently of its folder (same ID, new path) must be relocated
// rather than duplicated.
func TestApplyRemoteDeltaRelocatesMovedFile(t *testing.T) {
	e := newDeltaEngine()

	e.applyRemoteDelta([]storage.Node{{ID: "a1", NodeType: "file", Path: "/docs/renamed.txt", Etag: "e-a1"}})

	if _, ok := e.remoteTreeFiles["docs/a.txt"]; ok {
		t.Error(`remoteTreeFiles["docs/a.txt"] still present, want it relocated`)
	}
	if n, ok := e.remoteTreeFiles["docs/renamed.txt"]; !ok || n.ID != "a1" {
		t.Errorf(`remoteTreeFiles["docs/renamed.txt"] = %+v (ok=%v), want the moved node`, n, ok)
	}
	if got := e.remoteTreeIDToRel["a1"]; got != "docs/renamed.txt" {
		t.Errorf(`remoteTreeIDToRel["a1"] = %q, want "docs/renamed.txt"`, got)
	}
}

// A node arriving for the first time is simply added — there is no prior rel to
// move it away from.
func TestApplyRemoteDeltaAddsUnseenNode(t *testing.T) {
	e := newDeltaEngine()

	e.applyRemoteDelta([]storage.Node{{ID: "n9", NodeType: "file", Path: "/docs/new.txt", Etag: "e-n9"}})

	if n, ok := e.remoteTreeFiles["docs/new.txt"]; !ok || n.ID != "n9" {
		t.Errorf(`remoteTreeFiles["docs/new.txt"] = %+v (ok=%v), want the new node`, n, ok)
	}
	if got := e.remoteTreeIDToRel["n9"]; got != "docs/new.txt" {
		t.Errorf(`remoteTreeIDToRel["n9"] = %q, want "docs/new.txt"`, got)
	}
}

// A folder this engine created itself and that is then moved remotely before the
// feed ever mentions it. The move's row carries only the new path, so the ID
// index ensureRemoteFolder maintains is the only thing that can say which rel
// the folder moved away from — without it the old rel stays cached as a folder
// that no longer exists, gets recreated locally by the folder pass, and then two
// rels claim one node ID for applyRemoteFolderMoves to fight over.
func TestOwnCreatedFolderMovedRemotelyLeavesNoPhantom(t *testing.T) {
	f := newFixture(t, nil)
	f.writeLocal("Docs/a.txt", "A")
	f.reconcile() // creates Docs remotely, via ensureRemoteFolder
	if !f.fs.Exists("Docs") {
		t.Fatal("Docs was not created remotely")
	}

	f.fs.Move("Docs", "Archive")
	f.reconcile()

	if _, ok := f.eng.remoteTreeFolders["Docs"]; ok {
		t.Error(`remoteTreeFolders["Docs"] still cached after the move: a phantom folder`)
	}
	if _, ok := f.eng.remoteTreeFolders["Archive"]; !ok {
		t.Error(`remoteTreeFolders["Archive"] missing after the move`)
	}
	if f.localExists("Docs") {
		t.Error("local Docs/ still present: the phantom was recreated on disk")
	}
	f.wantLocal("Archive/a.txt", "A")

	// And it stays settled: with one rel per node ID, the next pass has no
	// phantom to mistake for a move back.
	f.reconcile()
	if f.localExists("Docs") {
		t.Error("local Docs/ reappeared on a later pass: the phantom is driving a rename flip-flop")
	}
	f.wantLocal("Archive/a.txt", "A")
}
