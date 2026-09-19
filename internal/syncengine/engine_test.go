package syncengine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/webbite-io/brick-wails/internal/testutil"
	"github.com/webbite-io/brick-wails/internal/testutil/fakestorage"
)

// recSink records activity kinds for assertions.
type recSink struct {
	mu    sync.Mutex
	kinds []string
	logs  []string
}

func (r *recSink) Logf(f string, a ...any) {
	r.mu.Lock()
	r.logs = append(r.logs, fmt.Sprintf(f, a...))
	r.mu.Unlock()
}
func (r *recSink) Activity(ev ActivityEvent) {
	r.mu.Lock()
	r.kinds = append(r.kinds, ev.Kind+":"+ev.RelPath)
	r.mu.Unlock()
}
func (r *recSink) StatusChanged() {}
func (r *recSink) has(kind string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, k := range r.kinds {
		if k == kind {
			return true
		}
	}
	return false
}
func (r *recSink) reset() { r.mu.Lock(); r.kinds = nil; r.mu.Unlock() }

type fixture struct {
	t      *testing.T
	eng    *Engine
	fs     *fakestorage.Server
	folder string
	sink   *recSink
}

func newFixture(t *testing.T, mutate func(c *Config)) *fixture {
	t.Helper()
	sc, fs := testutil.NewStorage(t)
	folder := t.TempDir()
	sink := &recSink{}
	cfg := Config{
		Storage: sc, Folder: folder, AccountID: "acct-1", RootID: "root",
		StatePath: filepath.Join(t.TempDir(), "sync-state-acct-1.json"),
		Sink:      sink,
		Options:   Options{PollInterval: 40 * time.Millisecond, Debounce: 20 * time.Millisecond, RecentWindow: 200 * time.Millisecond},
	}
	if mutate != nil {
		mutate(&cfg)
	}
	return &fixture{t: t, eng: New(cfg), fs: fs, folder: folder, sink: sink}
}

func (f *fixture) reconcile() {
	f.t.Helper()
	if err := f.eng.ReconcileAll(context.Background()); err != nil {
		f.t.Fatalf("ReconcileAll: %v", err)
	}
}

func (f *fixture) writeLocal(rel, content string) {
	f.t.Helper()
	abs := filepath.Join(f.folder, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) readLocal(rel string) (string, bool) {
	data, err := os.ReadFile(filepath.Join(f.folder, filepath.FromSlash(rel)))
	if err != nil {
		return "", false
	}
	return string(data), true
}

func (f *fixture) localExists(rel string) bool {
	_, err := os.Stat(filepath.Join(f.folder, filepath.FromSlash(rel)))
	return err == nil
}

func (f *fixture) wantLocal(rel, content string) {
	f.t.Helper()
	got, ok := f.readLocal(rel)
	if !ok || got != content {
		f.t.Errorf("local %s = %q (exists=%v), want %q", rel, got, ok, content)
	}
}

func (f *fixture) wantRemote(rel, content string) {
	f.t.Helper()
	got, ok := f.fs.Read(rel)
	if !ok || got != content {
		f.t.Errorf("remote %s = %q (exists=%v), want %q", rel, got, ok, content)
	}
}

func (f *fixture) transfers() int {
	return f.fs.Requests("GET /files/") + f.fs.Requests("POST /files") + f.fs.Requests("PUT /files/")
}

// --- reconcile matrix ---

func TestRemoteOnlyIsDownloaded(t *testing.T) {
	f := newFixture(t, nil)
	f.fs.PutFile("docs/a.txt", "A")
	f.reconcile()
	f.wantLocal("docs/a.txt", "A")
	if !f.sink.has("download:docs/a.txt") {
		t.Errorf("activity %v", f.sink.kinds)
	}
	st := f.eng.State()
	if st.Entries["docs/a.txt"].NodeID == "" || !st.Folders["docs"] || st.FolderIDs["docs"] == "" {
		t.Errorf("index not recorded: %+v", st)
	}
	if f.eng.Status().Counters.Downloaded != 1 || f.eng.Status().State != "idle" {
		t.Errorf("status %+v", f.eng.Status())
	}
}

func TestLocalOnlyIsUploadedWithParents(t *testing.T) {
	f := newFixture(t, nil)
	f.writeLocal("a/b/c.txt", "C")
	f.reconcile()
	f.wantRemote("a/b/c.txt", "C")
	if !f.sink.has("upload:a/b/c.txt") {
		t.Errorf("activity %v", f.sink.kinds)
	}
}

func TestUnchangedSecondPassTransfersNothing(t *testing.T) {
	f := newFixture(t, nil)
	f.fs.PutFile("r.txt", "R")
	f.writeLocal("l.txt", "L")
	f.reconcile()
	f.fs.ResetRequests()
	f.reconcile()
	if n := f.transfers(); n != 0 {
		t.Errorf("second pass made %d transfer requests, want 0", n)
	}
}

func TestLocalDeleteTrashesRemote(t *testing.T) {
	f := newFixture(t, nil)
	f.fs.PutFile("x.txt", "X")
	f.reconcile()
	os.Remove(filepath.Join(f.folder, "x.txt"))
	f.reconcile()
	if f.fs.Exists("x.txt") {
		t.Error("remote not trashed")
	}
	if !f.sink.has("trash:x.txt") {
		t.Errorf("activity %v", f.sink.kinds)
	}
}

func TestRemoteDeleteRemovesUnchangedLocal(t *testing.T) {
	f := newFixture(t, nil)
	f.fs.PutFile("x.txt", "X")
	f.reconcile()
	f.fs.Trash("x.txt")
	f.reconcile()
	if f.localExists("x.txt") {
		t.Error("local not removed")
	}
	if !f.sink.has("remove:x.txt") {
		t.Errorf("activity %v", f.sink.kinds)
	}
}

func TestRemoteDeleteKeepsLocallyChangedFile(t *testing.T) {
	f := newFixture(t, nil)
	f.fs.PutFile("x.txt", "X")
	f.reconcile()
	f.fs.Trash("x.txt")
	f.writeLocal("x.txt", "edited")
	f.reconcile()
	f.wantLocal("x.txt", "edited")
	f.wantRemote("x.txt", "edited")
}

func TestLocalChangeReplacesRemote(t *testing.T) {
	f := newFixture(t, nil)
	id := f.fs.PutFile("x.txt", "v1")
	f.reconcile()
	f.writeLocal("x.txt", "v2")
	f.reconcile()
	f.wantRemote("x.txt", "v2")
	if f.fs.ID("x.txt") != id {
		t.Error("replace should keep the node ID")
	}
	if !f.sink.has("update:x.txt") {
		t.Errorf("activity %v", f.sink.kinds)
	}
}

func TestRemoteChangeDownloads(t *testing.T) {
	f := newFixture(t, nil)
	f.fs.PutFile("x.txt", "v1")
	f.reconcile()
	f.fs.PutFile("x.txt", "v2")
	f.reconcile()
	f.wantLocal("x.txt", "v2")
}

func TestBothChangedRemoteWins(t *testing.T) {
	f := newFixture(t, nil)
	f.fs.PutFile("x.txt", "v1")
	f.reconcile()
	f.fs.PutFile("x.txt", "remote-v2")
	f.writeLocal("x.txt", "local-v2")
	f.reconcile()
	f.wantLocal("x.txt", "remote-v2")
	f.wantRemote("x.txt", "remote-v2")
}

func TestFirstSyncConflictModes(t *testing.T) {
	cases := []struct {
		mode        string
		local       string
		remote      string
		copyContent string
	}{
		{"device", "remote", "remote", ""},
		{"brick", "local", "local", ""},
		{"copy", "remote", "remote", "local"},
		{"", "remote", "remote", ""},
	}
	for _, tc := range cases {
		t.Run("mode="+tc.mode, func(t *testing.T) {
			f := newFixture(t, func(c *Config) { c.FirstSync = true; c.ConflictMode = tc.mode })
			f.fs.PutFile("dup.txt", "remote")
			f.writeLocal("dup.txt", "local")
			f.reconcile()
			f.wantLocal("dup.txt", tc.local)
			f.wantRemote("dup.txt", tc.remote)
			if tc.copyContent != "" {
				f.wantLocal("dup (copy).txt", tc.copyContent)
				f.wantRemote("dup (copy).txt", tc.copyContent)
				if !f.sink.has("keep-both:dup.txt") {
					t.Errorf("activity %v", f.sink.kinds)
				}
			} else if f.localExists("dup (copy).txt") {
				t.Error("unexpected copy")
			}
			if f.eng.FirstSyncPending() {
				t.Error("firstSync still pending after a completed pass")
			}
		})
	}
}

// Once the first pass is done, a coincidental double-create is plain
// remote-wins, whatever the onboarding mode was.
func TestConflictModeOnlyAppliesToFirstPass(t *testing.T) {
	f := newFixture(t, func(c *Config) { c.FirstSync = true; c.ConflictMode = "brick" })
	f.reconcile()
	f.fs.PutFile("late.txt", "remote")
	f.writeLocal("late.txt", "local")
	f.reconcile()
	f.wantLocal("late.txt", "remote")
}

func TestRemoteFolderRenameIsLocalRename(t *testing.T) {
	f := newFixture(t, nil)
	f.fs.PutFile("A/B/deep.txt", "D")
	f.fs.PutFile("A/top.txt", "T")
	f.reconcile()
	f.fs.Move("A", "X")
	f.fs.Move("X/B", "X/Y") // nested rename in the same poll cycle
	f.fs.ResetRequests()
	f.reconcile()
	f.wantLocal("X/Y/deep.txt", "D")
	f.wantLocal("X/top.txt", "T")
	if f.localExists("A") {
		t.Error("old folder still present")
	}
	if n := f.fs.Requests("GET /files/"); n != 0 {
		t.Errorf("rename caused %d re-downloads", n)
	}
	st := f.eng.State()
	if _, ok := st.Entries["X/Y/deep.txt"]; !ok {
		t.Errorf("index not rewritten: %v", keys(st.Entries))
	}
	if !f.sink.has("move-folder:X") || !f.sink.has("move-folder:X/Y") {
		t.Errorf("activity %v", f.sink.kinds)
	}
}

func TestRemoteFileMoveIsLocalRename(t *testing.T) {
	f := newFixture(t, nil)
	f.fs.PutFile("a.txt", "A")
	f.reconcile()
	f.fs.Move("a.txt", "sub/renamed.txt")
	f.fs.ResetRequests()
	f.reconcile()
	f.wantLocal("sub/renamed.txt", "A")
	if f.localExists("a.txt") || f.fs.Requests("GET /files/") != 0 {
		t.Error("move should be a local rename without download")
	}
	if !f.sink.has("move:sub/renamed.txt") {
		t.Errorf("activity %v", f.sink.kinds)
	}
}

func TestRemoteMoveOntoOccupiedLocalPathDoesNotClobber(t *testing.T) {
	f := newFixture(t, nil)
	f.fs.PutFile("a.txt", "A")
	f.reconcile()
	f.writeLocal("b.txt", "local-b")
	f.fs.Move("a.txt", "b.txt")
	f.reconcile()
	// Local b.txt (never synced) collides with remote b.txt: remote wins as
	// for any both-sides change, but the rename path must not have silently
	// overwritten it — a.txt is handled by the normal passes.
	if got, _ := f.readLocal("b.txt"); got != "A" {
		t.Errorf("b.txt = %q", got)
	}
}

func TestLocalFolderDeleteTrashesOnceAndPrunes(t *testing.T) {
	f := newFixture(t, nil)
	f.fs.PutFile("P/one.txt", "1")
	f.fs.PutFile("P/sub/two.txt", "2")
	f.reconcile()
	os.RemoveAll(filepath.Join(f.folder, "P"))
	f.fs.ResetRequests()
	f.reconcile()
	if n := f.fs.Requests("DELETE /nodes/"); n != 1 {
		t.Errorf("DELETE requests = %d, want 1 (server cascades)", n)
	}
	if f.fs.Exists("P") {
		t.Error("remote folder still live")
	}
	if !f.sink.has("trash-folder:P") {
		t.Errorf("activity %v", f.sink.kinds)
	}
	if len(f.eng.State().Entries) != 0 {
		t.Errorf("index entries left: %v", keys(f.eng.State().Entries))
	}
}

func TestRemoteFolderGone(t *testing.T) {
	f := newFixture(t, nil)
	f.fs.Mkdir("Empty")
	f.fs.PutFile("Full/a.txt", "A")
	f.reconcile()
	f.fs.Trash("Empty")
	f.fs.Trash("Full")
	f.writeLocal("Full/new-local.txt", "unsynced work")
	f.reconcile()
	if f.localExists("Empty") {
		t.Error("empty folder gone remotely should be removed locally")
	}
	if !f.localExists("Full/new-local.txt") {
		t.Fatal("unsynced local work was deleted")
	}
	f.reconcile()
	f.wantRemote("Full/new-local.txt", "unsynced work")
}

func TestNewEmptyLocalFolderIsCreatedRemotely(t *testing.T) {
	f := newFixture(t, nil)
	os.MkdirAll(filepath.Join(f.folder, "brand", "new"), 0o755)
	f.reconcile()
	if !f.fs.Exists("brand/new") {
		t.Error("empty folder not pushed")
	}
}

func TestExcludedFoldersAreNeverSynced(t *testing.T) {
	f := newFixture(t, func(c *Config) { c.ExcludeDirs = []string{"Camera Uploads", "/nested/skip/"} })
	f.fs.PutFile("Camera Uploads/p.jpg", "photo")
	f.fs.PutFile("nested/skip/s.txt", "s")
	f.fs.PutFile("nested/keep.txt", "k")
	f.writeLocal("nested/skip/local.txt", "l")
	f.reconcile()
	if f.localExists("Camera Uploads") {
		t.Error("excluded folder created locally")
	}
	if f.localExists("nested/skip/s.txt") {
		t.Error("excluded file downloaded")
	}
	if f.fs.Exists("nested/skip/local.txt") {
		t.Error("excluded local file uploaded")
	}
	f.wantLocal("nested/keep.txt", "k")
	// And excluded folders missing locally are never mistaken for local deletes.
	f.reconcile()
	if !f.fs.Exists("Camera Uploads/p.jpg") {
		t.Error("excluded remote folder was trashed")
	}
}

func TestTmpFilesIgnored(t *testing.T) {
	f := newFixture(t, nil)
	f.writeLocal("partial.bin"+TmpSuffix, "x")
	f.reconcile()
	if f.fs.Exists("partial.bin" + TmpSuffix) {
		t.Error("tmp file uploaded")
	}
}

func TestTransferErrorIsLoggedAndRetriedNextPass(t *testing.T) {
	f := newFixture(t, nil)
	f.fs.PutFile("a.txt", "A")
	f.fs.FailNext("GET /files/", 1)
	f.reconcile() // per-file errors are logged, not fatal
	if f.localExists("a.txt") {
		t.Fatal("download should have failed")
	}
	f.reconcile()
	f.wantLocal("a.txt", "A")
}

func TestTreeErrorSetsErrorStatus(t *testing.T) {
	f := newFixture(t, nil)
	f.fs.FailNext("GET /nodes/root/children", 1)
	if err := f.eng.ReconcileAll(context.Background()); err == nil {
		t.Fatal("expected error")
	}
	if st := f.eng.Status(); st.State != "error" || st.LastError == "" {
		t.Errorf("status %+v", st)
	}
	f.reconcile()
	if st := f.eng.Status(); st.State != "idle" || st.LastError != "" {
		t.Errorf("status after recovery %+v", st)
	}
}

// --- pause (ported from brick-cli sync_pause_test.go) ---

func newPauseFixture(t *testing.T, total, pauseAfter int) *fixture {
	f := newFixture(t, func(c *Config) { c.FirstSync = true; c.ConflictMode = "device" })
	for i := 0; i < total; i++ {
		f.fs.PutFile(fmt.Sprintf("file%d.txt", i), fmt.Sprintf("content-%d", i))
	}
	var mu sync.Mutex
	n := 0
	f.fs.OnDownload = func(string) {
		mu.Lock()
		defer mu.Unlock()
		n++
		if pauseAfter > 0 && n == pauseAfter {
			f.eng.SetPaused(true)
		}
	}
	return f
}

func TestPauseMidPassStopsBetweenFiles(t *testing.T) {
	f := newPauseFixture(t, 10, 3)
	err := f.eng.ReconcileAll(context.Background())
	if !errors.Is(err, ErrPausedMidPass) {
		t.Fatalf("err = %v, want ErrPausedMidPass", err)
	}
	if got := f.eng.Status().Counters.Downloaded; got != 3 {
		t.Errorf("downloaded = %d, want 3", got)
	}
	if !f.eng.FirstSyncPending() {
		t.Error("firstSync cleared by an aborted pass")
	}
	f.eng.SetPaused(false)
	if st := f.eng.Status(); st.LastError != "" || st.State == "idle" {
		t.Errorf("a pause is not an error nor a completion: %+v", st)
	}
	f.reconcile()
	if got := len(f.eng.State().Entries); got != 10 {
		t.Errorf("entries after resume = %d", got)
	}
	for i := 0; i < 10; i++ {
		f.wantLocal(fmt.Sprintf("file%d.txt", i), fmt.Sprintf("content-%d", i))
	}
	if f.eng.FirstSyncPending() || f.eng.Status().State != "idle" {
		t.Error("not converged after resume")
	}
}

func TestPausedEngineRefusesToStartPass(t *testing.T) {
	f := newFixture(t, nil)
	f.eng.SetPaused(true)
	if err := f.eng.ReconcileAll(context.Background()); !errors.Is(err, ErrPausedMidPass) {
		t.Fatalf("err = %v", err)
	}
	if f.eng.Status().State != "paused" {
		t.Errorf("state = %q, want paused overlay", f.eng.Status().State)
	}
}

func TestPauseAndWaitBlocksUntilPassEnds(t *testing.T) {
	f := newFixture(t, nil)
	f.fs.PutFile("slow.txt", "x")
	release := make(chan struct{})
	f.fs.OnDownload = func(string) { <-release }
	done := make(chan struct{})
	go func() { _ = f.eng.ReconcileAll(context.Background()); close(done) }()
	time.Sleep(50 * time.Millisecond)
	waited := make(chan struct{})
	go func() { f.eng.PauseAndWait(); close(waited) }()
	select {
	case <-waited:
		t.Fatal("PauseAndWait returned while a pass was in flight")
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	<-done
	select {
	case <-waited:
	case <-time.After(2 * time.Second):
		t.Fatal("PauseAndWait never returned")
	}
}

// --- cursor / poll ---

func TestPollRemoteChanges(t *testing.T) {
	f := newFixture(t, nil)
	ctx := context.Background()
	f.reconcile()

	reconciled, err := f.eng.PollRemoteChanges(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// Cursor starts at 0 so the first poll sees old changes; after that it's quiet.
	reconciled, err = f.eng.PollRemoteChanges(ctx)
	if err != nil || reconciled {
		t.Fatalf("quiet poll: reconciled=%v err=%v", reconciled, err)
	}
	before := f.eng.cursor()

	f.fs.PutFile("new.txt", "N")
	f.fs.FailNext("GET /nodes/root/children", 1)
	if _, err := f.eng.PollRemoteChanges(ctx); err == nil {
		t.Fatal("expected failed reconcile")
	}
	if f.eng.cursor() != before {
		t.Error("cursor advanced despite failed reconcile")
	}
	reconciled, err = f.eng.PollRemoteChanges(ctx)
	if err != nil || !reconciled {
		t.Fatalf("retry: reconciled=%v err=%v", reconciled, err)
	}
	f.wantLocal("new.txt", "N")
	if f.eng.cursor() <= before {
		t.Error("cursor did not advance")
	}
	f.eng.setCursor(1)
	if f.eng.cursor() <= before {
		t.Error("cursor rewound")
	}
}

func TestPurgeCaughtOnlyByForceReconcile(t *testing.T) {
	f := newFixture(t, nil)
	ctx := context.Background()
	f.fs.PutFile("p.txt", "P")
	f.reconcile()
	f.eng.PollRemoteChanges(ctx) // settle cursor
	f.fs.Purge("p.txt")
	if reconciled, _ := f.eng.PollRemoteChanges(ctx); reconciled {
		t.Fatal("purge should be invisible to check-updates")
	}
	if !f.localExists("p.txt") {
		t.Fatal("premature removal")
	}
	if err := f.eng.ForceReconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if f.localExists("p.txt") {
		t.Error("purged file not removed by the backstop")
	}
}

// --- state file compatibility ---

func TestStateFileRoundTripAndCLIKeys(t *testing.T) {
	f := newFixture(t, nil)
	f.fs.PutFile("d/a.txt", "A")
	f.reconcile()
	f.eng.setCursor(12345)

	data, err := os.ReadFile(f.eng.statePath)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	json.Unmarshal(data, &raw)
	for _, k := range []string{"folder", "entries", "folders", "folderIds", "serverTime"} {
		if _, ok := raw[k]; !ok {
			t.Errorf("state missing key %q", k)
		}
	}
	entry := raw["entries"].(map[string]any)["d/a.txt"].(map[string]any)
	for _, k := range []string{"relPath", "nodeId", "remoteEtag", "localHash", "localSize", "syncedAt"} {
		if _, ok := entry[k]; !ok {
			t.Errorf("entry missing key %q", k)
		}
	}

	loaded := LoadState(f.eng.statePath, "/moved/folder")
	if loaded.ServerTime != 12345 || loaded.Entries["d/a.txt"].NodeID == "" || loaded.Folder != "/moved/folder" {
		t.Errorf("reload %+v", loaded)
	}
}

// A state file as brick-cli writes it (older, without folderIds) loads with
// every map initialized.
func TestLoadStateTolerance(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "s.json")
	if st := LoadState(p, "/f"); st.Entries == nil || st.Folders == nil || st.FolderIDs == nil {
		t.Error("missing file: nil maps")
	}
	os.WriteFile(p, []byte("{not json"), 0o600)
	if st := LoadState(p, "/f"); st.Entries == nil {
		t.Error("corrupt file: nil maps")
	}
	os.WriteFile(p, []byte(`{"folder":"/old","entries":{"a":{"relPath":"a","nodeId":"n"}},"serverTime":5}`), 0o600)
	st := LoadState(p, "/f")
	if st.Folders == nil || st.FolderIDs == nil || st.Entries["a"].NodeID != "n" || st.ServerTime != 5 {
		t.Errorf("old format: %+v", st)
	}
	if StatePath("/c", "acct") != filepath.Join("/c", "sync-state-acct.json") {
		t.Error("StatePath")
	}
}

// Restarting with the persisted index must not re-transfer anything.
func TestRestartWithStateDoesNotRetransfer(t *testing.T) {
	f := newFixture(t, nil)
	f.fs.PutFile("a.txt", "A")
	f.writeLocal("b.txt", "B")
	f.reconcile()

	eng2 := New(Config{Storage: f.eng.sc, Folder: f.folder, AccountID: "acct-1", RootID: "root", StatePath: f.eng.statePath})
	f.fs.ResetRequests()
	if err := eng2.ReconcileAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := f.transfers(); n != 0 {
		t.Errorf("restart transferred %d files", n)
	}
}

// --- status / activity ---

func TestActivityRingNewestFirstAndCapped(t *testing.T) {
	e := New(Config{Folder: "/x"})
	for i := 0; i < activityCap+10; i++ {
		e.publishActivity("upload", fmt.Sprint(i))
	}
	all := e.RecentActivity(1000)
	if len(all) != activityCap || all[0].RelPath != fmt.Sprint(activityCap+9) {
		t.Errorf("len=%d first=%v", len(all), all[0])
	}
	if got := e.RecentActivity(3); len(got) != 3 {
		t.Errorf("limit: %d", len(got))
	}
}

func TestHelpers(t *testing.T) {
	if DupPath("a/b.txt") != "a/b (copy).txt" || DupPath("x") != "x (copy)" {
		t.Error("DupPath")
	}
	if !IsExcludedPath("a/b/c", []string{"a/b"}) || IsExcludedPath("a/bc", []string{"a/b"}) || !IsExcludedPath("a", []string{"/a/"}) {
		t.Error("IsExcludedPath")
	}
}

// --- Run loop (real fsnotify, fast timings) ---

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestRunSyncsBothWaysUntilCancelled(t *testing.T) {
	f := newFixture(t, nil)
	f.fs.PutFile("start.txt", "S")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- f.eng.Run(ctx) }()

	waitFor(t, "initial download", func() bool { _, ok := f.readLocal("start.txt"); return ok })

	f.writeLocal("from-local.txt", "L")
	waitFor(t, "watcher-driven upload", func() bool { _, ok := f.fs.Read("from-local.txt"); return ok })

	f.fs.PutFile("from-remote.txt", "R")
	waitFor(t, "poll-driven download", func() bool { _, ok := f.readLocal("from-remote.txt"); return ok })

	f.eng.SetPaused(true)
	f.fs.PutFile("while-paused.txt", "P")
	time.Sleep(200 * time.Millisecond)
	if f.localExists("while-paused.txt") {
		t.Error("synced while paused")
	}
	f.eng.SetPaused(false)
	waitFor(t, "catch-up after resume", func() bool { _, ok := f.readLocal("while-paused.txt"); return ok })

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Run returned %v on cancel", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not stop")
	}
	if _, err := os.Stat(f.eng.statePath); err != nil {
		t.Errorf("state not saved: %v", err)
	}
}

func keys[V any](m map[string]V) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
