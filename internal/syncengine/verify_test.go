package syncengine

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// A file present on both sides at first sync, whose content-MD5 (as already
// returned by the children listing, on the specific remote node at that exact
// path) matches the local file's own MD5, must not be transferred at all —
// neither downloaded nor uploaded — just recorded as already synced. This is
// the "populate the folder from another device, then start Brick" scenario
// that used to re-transfer every file regardless of whether it was already
// identical.
func TestFirstSyncSkipsTransferWhenMD5Verified(t *testing.T) {
	f := newFixture(t, func(c *Config) { c.FirstSync = true; c.ConflictMode = "device" })
	f.fs.PutFile("same.txt", "identical content")
	f.writeLocal("same.txt", "identical content")
	f.fs.ResetRequests()

	f.reconcile()

	if n := f.transfers(); n != 0 {
		t.Errorf("%d file transfers, want 0: content was verified identical", n)
	}
	f.wantLocal("same.txt", "identical content")
	f.wantRemote("same.txt", "identical content")
	if !f.sink.has("verify:same.txt") {
		t.Errorf("activity %v, want a verify event", f.sink.kinds)
	}
	entry, ok := f.eng.State().Entries["same.txt"]
	if !ok {
		t.Fatal("same.txt has no sync entry after a verified-identical pass")
	}
	if entry.NodeID != f.fs.ID("same.txt") || entry.RemoteEtag == "" || entry.LocalHash == "" {
		t.Errorf("entry = %+v, want the remote node id, etag and local hash recorded", entry)
	}
}

// The verification must not be limited to the account's very first pass. A
// folder can hold a file with no sync-state entry well after onboarding too —
// the state file was reset, or the file was copied in from another
// already-synced device — and that case must be verified just the same, not
// silently re-downloaded under plain remote-wins.
func TestUnsyncedFileSkipsTransferWhenMD5VerifiedAfterOnboarding(t *testing.T) {
	f := newFixture(t, nil) // FirstSync false: an ordinary post-onboarding pass
	f.fs.PutFile("same.txt", "identical content")
	f.writeLocal("same.txt", "identical content")
	f.fs.ResetRequests()

	f.reconcile()

	if n := f.transfers(); n != 0 {
		t.Errorf("%d file transfers, want 0: content was verified identical", n)
	}
	if !f.sink.has("verify:same.txt") {
		t.Errorf("activity %v, want a verify event", f.sink.kinds)
	}
	if _, ok := f.eng.State().Entries["same.txt"]; !ok {
		t.Error("same.txt has no sync entry after a verified-identical pass")
	}
}

// Verification costs no extra request: the content-MD5 comes back with the
// ordinary children listing the tree walk already makes.
func TestVerifyMakesNoExtraRequest(t *testing.T) {
	f := newFixture(t, nil)
	f.fs.PutFile("same.txt", "identical content")
	f.writeLocal("same.txt", "identical content")
	f.fs.ResetRequests()

	f.reconcile()

	if n := f.fs.Requests("POST /files/exists"); n != 0 {
		t.Errorf("%d existence lookups, want 0: the children listing already carries the MD5", n)
	}
}

// A remote node carrying no stored content-MD5 (one uploaded before the
// server recorded hashes) is unverifiable, never proof of a difference: the
// pass must fall back to the ordinary conflict-mode resolution rather than
// skipping the transfer.
func TestUnverifiableFileFallsBackToConflictMode(t *testing.T) {
	f := newFixture(t, func(c *Config) { c.FirstSync = true; c.ConflictMode = "brick" })
	f.fs.PutFile("legacy.txt", "identical content")
	f.fs.ClearMD5("legacy.txt")
	f.writeLocal("legacy.txt", "identical content")
	f.fs.ResetRequests()

	f.reconcile()

	if f.fs.Requests("PUT /files/") == 0 {
		t.Error("local copy was not uploaded: an unverifiable file should still honor conflictMode \"brick\"")
	}
	if f.sink.has("verify:legacy.txt") {
		t.Errorf("activity %v: a file with no stored MD5 must not be reported as verified", f.sink.kinds)
	}
}

// Content that genuinely differs must never be verified away, even though
// both sides have a stored MD5 to compare.
func TestDifferingContentIsNotVerified(t *testing.T) {
	f := newFixture(t, func(c *Config) { c.FirstSync = true; c.ConflictMode = "device" })
	f.fs.PutFile("dup.txt", "remote")
	f.writeLocal("dup.txt", "local")

	f.reconcile()

	f.wantLocal("dup.txt", "remote")
	if f.sink.has("verify:dup.txt") {
		t.Errorf("activity %v: differing content must not be reported as verified", f.sink.kinds)
	}
}

// An excluded file is the exclusion path's business (reconcileExcludedFile),
// never the conflict case — so it is not a verification candidate either.
func TestExcludedFileIsNotVerified(t *testing.T) {
	f := newFixture(t, func(c *Config) { c.ExcludeDirs = []string{"secret"} })
	f.fs.PutFile("secret/a.txt", "identical content")
	f.writeLocal("secret/a.txt", "identical content")

	f.reconcile()

	if f.sink.has("verify:secret/a.txt") {
		t.Errorf("activity %v: excluded paths are never verification candidates", f.sink.kinds)
	}
}

// --- dry run ---

func (f *fixture) removeLocal(rel string) {
	f.t.Helper()
	if err := os.Remove(filepath.Join(f.folder, filepath.FromSlash(rel))); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) dryRun() []Change {
	f.t.Helper()
	changes, err := f.eng.DryRun(context.Background())
	if err != nil {
		f.t.Fatalf("DryRun: %v", err)
	}
	return changes
}

func (f *fixture) wantDryRun(want map[string]string) {
	f.t.Helper()
	got := map[string]string{}
	for _, c := range f.dryRun() {
		got[c.RelPath] = c.Label
	}
	for rel, label := range want {
		if got[rel] != label {
			f.t.Errorf("dry run %s = %q, want %q", rel, got[rel], label)
		}
	}
	for rel := range got {
		if _, ok := want[rel]; !ok {
			f.t.Errorf("dry run reported unexpected %s: %q", rel, got[rel])
		}
	}
}

func TestDryRunReportsPendingTransfers(t *testing.T) {
	f := newFixture(t, nil)
	f.fs.PutFile("remote-only.txt", "R")
	f.writeLocal("local-only.txt", "L")

	f.wantDryRun(map[string]string{
		"remote-only.txt": "To be downloaded. Exists remotely but not locally.",
		"local-only.txt":  "To be uploaded. Exists locally but not remotely.",
	})
}

// The whole point of the dry run: it must not transfer, delete or write
// anything — not even the sync-state file.
func TestDryRunChangesNothing(t *testing.T) {
	f := newFixture(t, nil)
	f.fs.PutFile("remote-only.txt", "R")
	f.writeLocal("local-only.txt", "L")
	f.fs.ResetRequests()

	f.dryRun()

	if n := f.transfers(); n != 0 {
		t.Errorf("%d file transfers during a dry run, want 0", n)
	}
	if f.localExists("remote-only.txt") {
		t.Error("dry run wrote remote-only.txt locally")
	}
	if f.fs.Exists("local-only.txt") {
		t.Error("dry run uploaded local-only.txt")
	}
	if len(f.eng.State().Entries) != 0 {
		t.Errorf("dry run recorded sync entries: %+v", f.eng.State().Entries)
	}
}

// A file the real pass would skip via MD5 verification must not be reported
// as pending — a dry run reports the decisions the real pass would make.
func TestDryRunSkipsMD5VerifiedFile(t *testing.T) {
	f := newFixture(t, func(c *Config) { c.FirstSync = true; c.ConflictMode = "device" })
	f.fs.PutFile("same.txt", "identical content")
	f.writeLocal("same.txt", "identical content")

	f.wantDryRun(map[string]string{})
}

func TestDryRunReportsUnverifiableFirstSyncPerConflictMode(t *testing.T) {
	cases := map[string]string{
		"device": "To be downloaded. Exists both locally and remotely but lacks MD5 checksum.",
		"brick":  "To be uploaded. Exists both locally and remotely but lacks MD5 checksum.",
		"copy":   "To be kept as both copies (local renamed aside). Exists both locally and remotely but lacks MD5 checksum.",
	}
	for mode, want := range cases {
		t.Run("mode="+mode, func(t *testing.T) {
			f := newFixture(t, func(c *Config) { c.FirstSync = true; c.ConflictMode = mode })
			f.fs.PutFile("legacy.txt", "identical content")
			f.fs.ClearMD5("legacy.txt")
			f.writeLocal("legacy.txt", "identical content")

			f.wantDryRun(map[string]string{"legacy.txt": want})
		})
	}
}

// Outside the first pass, an unverifiable file with no sync history is a
// plain remote-wins — the dry run must report that, not the onboarding mode.
func TestDryRunUnverifiableAfterOnboardingIsRemoteWins(t *testing.T) {
	f := newFixture(t, func(c *Config) { c.ConflictMode = "brick" }) // FirstSync false
	f.fs.PutFile("legacy.txt", "identical content")
	f.fs.ClearMD5("legacy.txt")
	f.writeLocal("legacy.txt", "identical content")

	f.wantDryRun(map[string]string{
		"legacy.txt": "To be downloaded. Exists both locally and remotely but lacks MD5 checksum.",
	})
}

func TestDryRunReportsDeletionsAndChanges(t *testing.T) {
	f := newFixture(t, nil)
	f.fs.PutFile("gone-locally.txt", "A")
	f.fs.PutFile("gone-remotely.txt", "B")
	f.fs.PutFile("changed-remotely.txt", "C")
	f.fs.PutFile("changed-locally.txt", "D")
	f.reconcile() // everything in sync

	f.removeLocal("gone-locally.txt")
	f.fs.Trash("gone-remotely.txt")
	f.fs.PutFile("changed-remotely.txt", "C2")
	f.writeLocal("changed-locally.txt", "D2")

	f.wantDryRun(map[string]string{
		"gone-locally.txt":     "To be deleted remotely. Removed locally.",
		"gone-remotely.txt":    "To be removed locally. Deleted on the server.",
		"changed-remotely.txt": "To be downloaded. Remote copy changed since last sync.",
		"changed-locally.txt":  "To be uploaded. Local copy changed since last sync.",
	})
}

func TestDryRunIgnoresExcludedAndInSyncFiles(t *testing.T) {
	f := newFixture(t, func(c *Config) { c.ExcludeDirs = []string{"secret"} })
	f.fs.PutFile("synced.txt", "S")
	f.reconcile()
	f.fs.PutFile("secret/hidden.txt", "H")

	f.wantDryRun(map[string]string{})
}
