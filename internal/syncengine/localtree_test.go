package syncengine

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// watched marks the engine as having a live filesystem watcher — what lets a
// pass patch the cached local tree from reported paths instead of walking the
// folder. Run does this for real once its watcher is up.
func (f *fixture) watched() *fixture {
	f.eng.watching.Store(true)
	return f
}

// reconcileChanged runs a pass as the debounce worker would for a watcher that
// reported exactly these rel paths — and nothing else.
func (f *fixture) reconcileChanged(rels ...string) {
	f.t.Helper()
	if err := f.eng.reconcileLocalChanges(context.Background(), localScope{changed: rels}); err != nil {
		f.t.Fatalf("reconcileLocalChanges(%v): %v", rels, err)
	}
}

// The point of the local cache: a pass reconciles what it was told changed, not
// every file that happens to exist. Both files below are written straight to
// disk, standing in for one the watcher reported and one it hasn't (yet).
func TestOnlyReportedPathsAreReconciled(t *testing.T) {
	f := newFixture(t, nil).watched()
	f.reconcile() // seeds both caches
	f.writeLocal("reported.txt", "R")
	f.writeLocal("unreported.txt", "U")

	f.reconcileChanged("reported.txt")

	if !f.fs.Exists("reported.txt") {
		t.Error("reported.txt was not uploaded")
	}
	if f.fs.Exists("unreported.txt") {
		t.Error("unreported.txt was uploaded, but no pass was ever told it changed")
	}

	// And it is picked up as soon as it is reported.
	f.reconcileChanged("unreported.txt")
	f.wantRemote("unreported.txt", "U")
}

// A directory moved or copied in as a whole gets one Create event for the
// directory itself, never one per file already inside it, so the pass has to
// expand it into a walk of that subtree.
func TestNewDirectoryExpandsToItsContents(t *testing.T) {
	f := newFixture(t, nil).watched()
	f.reconcile()
	f.writeLocal("imported/a.txt", "A")
	f.writeLocal("imported/nested/b.txt", "B")

	f.reconcileChanged("imported") // only the directory, as fsnotify would

	f.wantRemote("imported/a.txt", "A")
	f.wantRemote("imported/nested/b.txt", "B")
	if !f.eng.localTreeDirs["imported/nested"] {
		t.Error(`localTreeDirs["imported/nested"] = false, want the subtree walk to have recorded it`)
	}
}

// A directory removed locally is likewise one event, and there is no disk copy
// left to walk — so the files under it have to come from what the engine already
// knew, and each one still be pushed as a remote deletion.
func TestRemovedDirectoryExpandsToKnownContents(t *testing.T) {
	f := newFixture(t, nil).watched()
	f.writeLocal("gone/a.txt", "A")
	f.writeLocal("gone/b.txt", "B")
	f.reconcile()
	if !f.fs.Exists("gone/a.txt") {
		t.Fatal("gone/a.txt was not uploaded by the seeding pass")
	}

	if err := os.RemoveAll(filepath.Join(f.folder, "gone")); err != nil {
		t.Fatal(err)
	}
	f.reconcileChanged("gone")

	for _, rel := range []string{"gone/a.txt", "gone/b.txt"} {
		if f.fs.Exists(rel) {
			t.Errorf("%s still live remotely, want it trashed with its folder", rel)
		}
		if _, ok := f.eng.State().Entries[rel]; ok {
			t.Errorf("%s kept its index entry after the remote delete", rel)
		}
	}
	if f.eng.State().Folders["gone"] {
		t.Error(`state.Folders["gone"] still true, want it dropped once the folder was trashed`)
	}
}

// A file this engine downloads itself is deliberately hidden from the watcher,
// so nothing would ever report it — and a later scoped pass that finds the file
// missing from the cache while the index says it was synced would conclude the
// user deleted it and trash it remotely. downloadFile patching the cache is what
// prevents that.
func TestDownloadedFileIsRecordedInTheLocalCache(t *testing.T) {
	f := newFixture(t, nil).watched()
	f.fs.PutFile("from-web.txt", "one")
	f.reconcile()
	f.wantLocal("from-web.txt", "one")

	// Edited remotely again, so the next pass has this rel in scope.
	f.fs.PutFile("from-web.txt", "two")
	f.reconcileChanged()

	if !f.fs.Exists("from-web.txt") {
		t.Fatal("the file we had just downloaded was trashed remotely: the cache never learned we wrote it")
	}
	f.wantLocal("from-web.txt", "two")
}

// A failed transfer reappears in no later delta or watcher event — nothing about
// it changes again — so the pass that failed must leave the next one on the full
// key union, which is what retries it.
func TestFailedTransferIsRetriedByTheNextScopedPass(t *testing.T) {
	f := newFixture(t, nil).watched()
	f.fs.PutFile("a.txt", "A")
	f.fs.FailNext("GET /files/", 1)
	f.reconcile() // per-file errors are logged, not fatal
	if f.localExists("a.txt") {
		t.Fatal("the download should have failed")
	}

	// A pass with nothing reported on either side: scoping it would check
	// nothing at all, and a.txt would wait for the ~30-minute backstop.
	f.reconcileChanged()

	f.wantLocal("a.txt", "A")
}

// An interrupted pass leaves files it never got to, and those reappear in no
// later delta or watcher event either. Same guarantee as a failed transfer: the
// next pass has to fall back to the full union. The pass is interrupted *after*
// a converged one, so the convergence flag genuinely has to be cleared at the
// start of a pass rather than merely never set by it.
func TestPauseMidPassForcesTheNextScopedPassToFullScan(t *testing.T) {
	f := newFixture(t, nil).watched()
	f.reconcile() // a pass that completes: the engine is now converged

	for i := 0; i < 10; i++ {
		f.fs.PutFile(fmt.Sprintf("file%d.txt", i), fmt.Sprintf("content-%d", i))
	}
	var mu sync.Mutex
	downloads := 0
	f.fs.OnDownload = func(string) {
		mu.Lock()
		defer mu.Unlock()
		downloads++
		if downloads == 3 {
			f.eng.SetPaused(true)
		}
	}

	err := f.eng.reconcileLocalChanges(context.Background(), localScope{})
	if !errors.Is(err, ErrPausedMidPass) {
		t.Fatalf("err = %v, want ErrPausedMidPass", err)
	}
	f.eng.SetPaused(false)
	f.fs.OnDownload = nil

	// Nothing new is reported on either side — the 7 files this pass never got
	// to were already known to it, so only a full scan comes back to them.
	f.reconcileChanged()

	for i := 0; i < 10; i++ {
		f.wantLocal(fmt.Sprintf("file%d.txt", i), fmt.Sprintf("content-%d", i))
	}
	if got := len(f.eng.State().Entries); got != 10 {
		t.Errorf("entries = %d, want all 10 reconciled", got)
	}
}

// A remote-triggered pass runs off the cached local tree — except while the
// watcher has a report the debounce worker hasn't acted on, which makes the
// cache knowably stale. Acting on it anyway would see a file that was just
// created locally as existing only remotely, and treat it accordingly.
//
// The two sides are given identical content, because that is what tells the
// outcomes apart: seeing the local file, the pass confirms the two match by MD5
// and transfers nothing (verifyUnsyncedFileMatches); missing it, the pass calls
// the file remote-only and downloads it. Both leave the same bytes on disk, so
// only the transfer distinguishes them.
func TestRemotePollWalksWhileLocalReportsArePending(t *testing.T) {
	f := newFixture(t, nil).watched()
	f.reconcile()

	f.writeLocal("both.txt", "same")
	f.eng.notifyPath("both.txt") // reported, but the debounce hasn't fired yet
	f.fs.PutFile("both.txt", "same")
	f.fs.ResetRequests()

	if _, err := f.eng.PollRemoteChanges(context.Background()); err != nil {
		t.Fatalf("PollRemoteChanges: %v", err)
	}

	if n := f.fs.Requests("GET /files/"); n != 0 {
		t.Errorf("%d downloads, want 0: the local copy was already identical, but a stale cache hid it", n)
	}
	if !f.sink.has("verify:both.txt") {
		t.Error("no verify for both.txt: the pass never saw the local copy at all")
	}
	// The report itself stays pending — coalescing is the debounce worker's job.
	if !f.eng.hasPendingLocal() {
		t.Error("the poll drained the watcher's reports")
	}
}

// A pass that doesn't finish may have drained reports it never patched in, so it
// must leave the cache distrusted rather than let a later pass work from one
// that is quietly missing them.
func TestInterruptedPassInvalidatesTheLocalCache(t *testing.T) {
	f := newFixture(t, nil).watched()
	f.reconcile()
	if !f.eng.localTreeValid {
		t.Fatal("the seeding pass should have left a valid cache")
	}

	f.fs.FailNext("GET /nodes/root/children", 1)
	f.eng.remoteTreeAsOf = 0 // force the walk this failure applies to
	if err := f.eng.ReconcileAll(context.Background()); err == nil {
		t.Fatal("expected the pass to fail")
	}

	if f.eng.localTreeValid {
		t.Error("localTreeValid = true after a failed pass, want the cache distrusted")
	}
}

// A change the caller could not locate (an out-of-band write, a resume) must put
// the pass back on a full walk rather than be taken for "nothing changed".
func TestUnlocatedChangeFallsBackToAFullWalk(t *testing.T) {
	f := newFixture(t, nil).watched()
	f.reconcile()
	f.writeLocal("out-of-band.txt", "O")

	// What Notify records, and the debounce worker passes on.
	if err := f.eng.reconcileLocalChanges(context.Background(), localScope{unknown: true}); err != nil {
		t.Fatalf("reconcileLocalChanges: %v", err)
	}

	f.wantRemote("out-of-band.txt", "O")
}

// Notify is the engine's public "something changed, I can't say what" wake, so
// it must mark the pass unknown rather than leave it believing the cache is
// complete — otherwise an out-of-band change waits for the backstop.
func TestNotifyMarksTheNextPassUnknown(t *testing.T) {
	f := newFixture(t, nil).watched()
	f.eng.notifyPath("located.txt")
	f.eng.Notify()

	changed, unknown := f.eng.drainLocalChanges()

	if !unknown {
		t.Error("unknown = false after Notify, want true")
	}
	if len(changed) != 1 || changed[0] != "located.txt" {
		t.Errorf("changed = %v, want the located path to survive alongside it", changed)
	}
	// Draining clears both.
	if changed, unknown := f.eng.drainLocalChanges(); len(changed) != 0 || unknown {
		t.Errorf("second drain returned changed=%v unknown=%v, want empty", changed, unknown)
	}
}

// With no watcher there is nothing to report paths, so no pass may trust the
// cache however empty the reported set looks — poll-only mode has to keep
// walking the folder.
func TestPollOnlyModeAlwaysWalksTheFolder(t *testing.T) {
	f := newFixture(t, nil) // deliberately not watched()
	f.reconcile()
	f.writeLocal("unwatched.txt", "U")

	f.reconcileChanged() // nothing reported, because nothing can be

	f.wantRemote("unwatched.txt", "U")
}

// ForceReconcile is the backstop for both sides, so it must walk the folder even
// when the cache looks usable — that is the only thing that catches a local
// change a watcher gap dropped.
func TestForceReconcileWalksTheFolderDespiteTheCache(t *testing.T) {
	f := newFixture(t, nil).watched()
	f.reconcile()
	f.writeLocal("missed-by-the-watcher.txt", "M")

	if err := f.eng.ForceReconcile(context.Background()); err != nil {
		t.Fatalf("ForceReconcile: %v", err)
	}

	f.wantRemote("missed-by-the-watcher.txt", "M")
}

// A dry run reports on the folder as it is now, and must leave no cache behind
// for a later real pass to trust.
func TestDryRunDoesNotSeedTheLocalCache(t *testing.T) {
	f := newFixture(t, nil).watched()
	f.writeLocal("pending.txt", "P")

	if _, err := f.eng.DryRun(context.Background()); err != nil {
		t.Fatalf("DryRun: %v", err)
	}
	if f.eng.localTreeValid {
		t.Error("localTreeValid = true after a dry run, want the cache untouched")
	}
	if f.fs.Exists("pending.txt") {
		t.Fatal("the dry run uploaded a file")
	}

	// The first real pass still walks and picks it up.
	f.reconcileChanged()
	f.wantRemote("pending.txt", "P")
}
