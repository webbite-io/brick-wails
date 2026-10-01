package syncengine

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/webbite-io/brick-wails/internal/storage"
)

// walks counts the recursive ListChildren requests the remote tree walk makes
// — one per folder, so this is exactly what the remote tree cache exists to
// avoid paying on every single pass.
func (f *fixture) walks() int { return f.fs.Requests("GET /nodes/") }

// A pass with nothing changed remotely must not re-walk the tree: a single
// cheap check-updates probe confirms the last walk is still good. This is the
// fix for "every local file add triggers a GET /children on every folder".
func TestReconcileReusesRemoteTreeCacheWhenNothingChanged(t *testing.T) {
	f := newFixture(t, nil)
	f.reconcile() // seeds the cache
	f.fs.ResetRequests()

	f.reconcile()

	if n := f.walks(); n != 0 {
		t.Errorf("%d children requests on a pass with nothing changed, want 0 (the cache should have been reused)", n)
	}
	if n := f.fs.Requests("GET /check-updates"); n == 0 {
		t.Error("no check-updates probe: the cache must be validated before it is trusted")
	}
}

// When check-updates reports a change small enough to collect in full, the
// next pass must patch those rows straight into the cached tree rather than
// re-walking every folder to rediscover what it was just told — and must of
// course still act on the change.
func TestReconcilePatchesRemoteTreeFromDeltaInsteadOfWalking(t *testing.T) {
	f := newFixture(t, nil)
	f.reconcile()
	f.fs.PutFile("from-web.txt", "W")
	f.fs.ResetRequests()

	f.reconcile()

	if n := f.walks(); n != 0 {
		t.Errorf("%d children requests for a change the feed already described in full, want 0", n)
	}
	f.wantLocal("from-web.txt", "W")
}

// A backlog too big to collect within the page budget must fall back to a real
// walk rather than risk patching from half a feed.
func TestReconcileFallsBackToWalkWhenDeltaTooLarge(t *testing.T) {
	f := newFixture(t, nil)
	f.reconcile()

	// One row per page, so a handful of changes outruns the page budget.
	f.fs.PageLimit = 1
	for i := 0; i <= storage.CheckUpdatesDeltaMaxPages; i++ {
		f.fs.PutFile(fmt.Sprintf("bulk/f%d.txt", i), "B")
	}
	f.fs.ResetRequests()

	f.reconcile()

	if n := f.walks(); n == 0 {
		t.Error("a backlog too big to collect was patched in anyway, want a real walk")
	}
	f.wantLocal("bulk/f0.txt", "B")
}

// The point of the cache: a pass triggered purely by a local change costs one
// check-updates probe instead of a walk of every folder, however deep the
// tree is — and still uploads the new file.
func TestLocalOnlyChangeDoesNotWalkRemoteTree(t *testing.T) {
	f := newFixture(t, nil)
	f.fs.PutFile("A/B/C/deep.txt", "D")
	f.fs.PutFile("A/top.txt", "T")
	f.reconcile() // seeds the cache; 4 folders to walk

	f.writeLocal("local-new.txt", "L")
	f.fs.ResetRequests()
	f.reconcile()

	if n := f.walks(); n != 0 {
		t.Errorf("%d children requests for a purely local change, want 0", n)
	}
	if c, ok := f.fs.Read("local-new.txt"); !ok || c != "L" {
		t.Errorf("local-new.txt = %q (exists=%v), want %q uploaded", c, ok, "L")
	}
}

// A change the pass makes itself (an upload) is never reflected back into the
// cached maps, so the next pass has to learn of it from the change feed — which
// reports a client's own writes like anyone else's — rather than work from a
// tree missing its own upload and transfer it all over again. No walk is needed
// for that: the delta describes the upload as precisely as any other change.
func TestOwnUploadIsPatchedBackFromTheFeed(t *testing.T) {
	f := newFixture(t, nil)
	f.reconcile()
	f.writeLocal("mine.txt", "M")
	f.reconcile() // uploads mine.txt, from the cached tree
	f.fs.ResetRequests()

	f.reconcile()

	if n := f.walks(); n != 0 {
		t.Errorf("%d children requests, want 0: the feed describes our own upload too", n)
	}
	if _, ok := f.eng.remoteTreeFiles["mine.txt"]; !ok {
		t.Error("the engine's own upload never made it into the cached tree")
	}
	if n := f.fs.Requests("POST /files"); n != 0 {
		t.Errorf("%d re-uploads of an already-uploaded file, want 0", n)
	}
	if _, ok := f.eng.State().Entries["mine.txt"]; !ok {
		t.Error("mine.txt lost its sync entry")
	}
}

// ForceReconcile is the periodic backstop for hard deletes, which
// check-updates can never report — so it must always perform a real walk,
// never be satisfied by a cache a probe calls fresh.
func TestForceReconcileAlwaysWalksRemoteTree(t *testing.T) {
	f := newFixture(t, nil)
	f.fs.PutFile("a.txt", "A")
	f.reconcile()
	f.wantLocal("a.txt", "A")
	f.fs.ResetRequests()

	if err := f.eng.ForceReconcile(context.Background()); err != nil {
		t.Fatalf("ForceReconcile: %v", err)
	}

	if n := f.walks(); n == 0 {
		t.Error("ForceReconcile reused the remote tree cache; it must always walk")
	}
}

// The case ForceReconcile exists for, end to end: a purged node leaves no
// check-updates row, so an ordinary pass would keep trusting the cache and
// never notice. The backstop's real walk must find it and remove it locally.
func TestForceReconcileCatchesPurgeTheCacheWouldHide(t *testing.T) {
	f := newFixture(t, nil)
	f.fs.PutFile("a.txt", "A")
	f.reconcile()
	f.wantLocal("a.txt", "A")

	f.fs.Purge("a.txt") // hard delete: no change row, so no probe can see it

	f.reconcile()
	if !f.localExists("a.txt") {
		t.Fatal("an ordinary pass removed a.txt: this test no longer covers the purge case")
	}

	if err := f.eng.ForceReconcile(context.Background()); err != nil {
		t.Fatalf("ForceReconcile: %v", err)
	}
	if f.localExists("a.txt") {
		t.Error("purged file still present locally after the forced backstop pass")
	}
}

// A probe that fails must not leave the engine trusting a cache it could not
// validate — it falls back to a real walk, exactly as if there were no cache.
func TestFailedProbeFallsBackToFullWalk(t *testing.T) {
	f := newFixture(t, nil)
	f.reconcile()
	f.fs.ResetRequests()
	f.fs.FailNext("GET /check-updates", 1)

	f.reconcile()

	if n := f.walks(); n == 0 {
		t.Error("a cache that could not be validated was reused anyway")
	}
}

// A dry run reports on the tree as it is now, so it always walks — and must
// not leave a cache behind that a later real pass would trust.
func TestDryRunDoesNotDisturbTheCache(t *testing.T) {
	f := newFixture(t, nil)
	f.reconcile()
	f.fs.PutFile("from-web.txt", "W")

	if _, err := f.eng.DryRun(context.Background()); err != nil {
		t.Fatalf("DryRun: %v", err)
	}
	if f.localExists("from-web.txt") {
		t.Fatal("dry run transferred a file")
	}

	f.reconcile()
	f.wantLocal("from-web.txt", "W")
}

func TestCacheSurvivesLocalDeleteAndTrashesRemotely(t *testing.T) {
	f := newFixture(t, nil)
	f.fs.PutFile("Docs/a.txt", "A")
	f.reconcile()
	f.wantLocal("Docs/a.txt", "A")

	if err := os.Remove(filepath.Join(f.folder, "Docs", "a.txt")); err != nil {
		t.Fatal(err)
	}
	f.fs.ResetRequests()
	f.reconcile()

	if n := f.walks(); n != 0 {
		t.Errorf("%d children requests for a purely local delete, want 0", n)
	}
	if f.fs.Exists("Docs/a.txt") {
		t.Error("locally deleted file was not trashed remotely")
	}
}
