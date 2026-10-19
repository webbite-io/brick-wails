package syncengine

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestIsIgnoredPath(t *testing.T) {
	for rel, want := range map[string]bool{
		".DS_Store":                   true,
		"photos/.DS_Store":            true,
		"photos/._IMG_0001.jpg":       true,
		".Spotlight-V100/Store-V2/db": true,
		".Trashes":                    true,
		"docs/Icon\r":                 true,
		"Thumbs.db":                   true,
		"docs/THUMBS.DB":              true,
		"docs/Desktop.ini":            true,
		"partial.bin" + TmpSuffix:     true,
		"a.txt":                       false,
		".DS_Store.txt":               false,
		"docs/Icon":                   false,
		"my.DS_Store/a.txt":           false,
		".env":                        false,
		"_notes/a.txt":                false,
	} {
		if got := isIgnoredPath(rel); got != want {
			t.Errorf("isIgnoredPath(%q) = %v, want %v", rel, got, want)
		}
	}
}

func TestOSMetadataFilesAreNeverUploaded(t *testing.T) {
	f := newFixture(t, nil)
	f.writeLocal("a.txt", "A")
	f.writeLocal(".DS_Store", "finder")
	f.writeLocal("photos/.DS_Store", "finder")
	f.writeLocal("photos/._p.jpg", "fork")
	f.writeLocal("photos/Thumbs.db", "thumbs")
	f.writeLocal(".Trashes/501/old.txt", "trash")
	f.reconcile()

	f.wantRemote("a.txt", "A")
	for _, rel := range []string{".DS_Store", "photos/.DS_Store", "photos/._p.jpg", "photos/Thumbs.db", ".Trashes"} {
		if f.fs.Exists(rel) {
			t.Errorf("%s uploaded", rel)
		}
	}
	if !f.fs.Exists("photos") {
		t.Error("photos/ not created remotely")
	}

	// Finder rewriting .DS_Store is not a change worth a transfer.
	f.writeLocal(".DS_Store", "finder, rearranged")
	f.fs.ResetRequests()
	f.reconcile()
	if n := f.transfers(); n != 0 {
		t.Errorf("%d transfers after a .DS_Store rewrite, want 0", n)
	}
}

func TestOSMetadataFilesAreNeverDownloaded(t *testing.T) {
	f := newFixture(t, nil)
	f.fs.PutFile("a.txt", "A")
	f.fs.PutFile(".DS_Store", "finder")
	f.fs.PutFile("photos/._p.jpg", "fork")
	f.fs.PutFile("photos/desktop.ini", "ini")
	f.fs.PutFile(".Spotlight-V100/Store-V2/db", "index")
	f.reconcile()

	f.wantLocal("a.txt", "A")
	for _, rel := range []string{".DS_Store", "photos/._p.jpg", "photos/desktop.ini", ".Spotlight-V100"} {
		if f.localExists(rel) {
			t.Errorf("%s downloaded", rel)
		}
	}
	// And their absence locally is never mistaken for a local delete.
	f.reconcile()
	for _, rel := range []string{".DS_Store", "photos/._p.jpg", ".Spotlight-V100/Store-V2/db"} {
		if !f.fs.Exists(rel) {
			t.Errorf("remote %s was trashed", rel)
		}
	}
}

// The watcher reports .DS_Store writes like any other; they must not reach the
// file pass, including inside a directory reported as a whole.
func TestWatcherReportedOSMetadataIsSkipped(t *testing.T) {
	f := newFixture(t, nil).watched()
	f.reconcile()
	f.writeLocal(".DS_Store", "finder")
	f.writeLocal("imported/.DS_Store", "finder")
	f.writeLocal("imported/a.txt", "A")

	f.reconcileChanged(".DS_Store", "imported")

	f.wantRemote("imported/a.txt", "A")
	if f.fs.Exists(".DS_Store") || f.fs.Exists("imported/.DS_Store") {
		t.Error(".DS_Store uploaded")
	}
	if _, ok := f.eng.localTreeFiles["imported/.DS_Store"]; ok {
		t.Error("imported/.DS_Store recorded in the local tree cache")
	}
}

// Files synced before their name was ignored (by an older version) are dropped
// from the index — never trashed remotely or removed locally.
func TestPreviouslySyncedOSMetadataIsForgottenNotDeleted(t *testing.T) {
	f := newFixture(t, nil)
	id := f.fs.PutFile(".DS_Store", "finder")
	folderID := f.fs.Mkdir(".Trashes")
	f.writeLocal(".DS_Store", "finder")
	if err := os.Mkdir(filepath.Join(f.folder, ".Trashes"), 0o755); err != nil {
		t.Fatal(err)
	}
	f.eng.state.Entries[".DS_Store"] = SyncEntry{RelPath: ".DS_Store", NodeID: id, SyncedAt: time.Now()}
	f.eng.state.Folders[".Trashes"] = true
	f.eng.state.FolderIDs[".Trashes"] = folderID

	f.reconcile()

	if !f.fs.Exists(".DS_Store") || !f.fs.Exists(".Trashes") {
		t.Error("previously synced metadata was trashed remotely")
	}
	if !f.localExists(".DS_Store") || !f.localExists(".Trashes") {
		t.Error("previously synced metadata was removed locally")
	}
	st := f.eng.State()
	if _, ok := st.Entries[".DS_Store"]; ok {
		t.Error(".DS_Store still in the sync index")
	}
	if st.Folders[".Trashes"] {
		t.Error(".Trashes still in the sync index")
	}
}

// A remote rename into an ignored name arrives through the change feed; the
// renamed node (and a renamed folder's cached contents) must leave the cache
// rather than linger under the old path.
func TestRemoteRenameIntoIgnoredNameLeavesTheCache(t *testing.T) {
	f := newFixture(t, nil)
	f.fs.PutFile("a.txt", "A")
	f.fs.PutFile("dir/x.txt", "X")
	f.reconcile()

	f.fs.Move("a.txt", ".DS_Store")
	f.fs.Move("dir", ".Trashes")
	f.fs.ResetRequests()
	f.reconcile()

	if n := f.walks(); n != 0 {
		t.Fatalf("%d children requests, want the change to come through the feed", n)
	}
	for _, rel := range []string{"a.txt", "dir/x.txt"} {
		if _, ok := f.eng.remoteTreeFiles[rel]; ok {
			t.Errorf("%s still in the remote tree cache", rel)
		}
		if f.localExists(rel) {
			t.Errorf("local %s kept after it was renamed away remotely", rel)
		}
	}
	if f.localExists(".DS_Store") || f.localExists(".Trashes") {
		t.Error("renamed node downloaded under its ignored name")
	}
	if f.fs.Exists("a.txt") || f.fs.Exists("dir/x.txt") {
		t.Error("renamed file re-uploaded under its old path")
	}
}
