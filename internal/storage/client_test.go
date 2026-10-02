package storage_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/webbite-io/brick-wails/internal/storage"
	"github.com/webbite-io/brick-wails/internal/testutil"
)

func rawClient(t *testing.T, h http.HandlerFunc) *storage.Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return &storage.Client{BaseURL: srv.URL, AccountID: "acct-1", Auth: testutil.NewAuthClient(t, "test-token")}
}

func writeUpdatesPage(w http.ResponseWriter, data []storage.Node, serverTime int64, next string) {
	_ = json.NewEncoder(w).Encode(map[string]any{"data": data, "count": len(data), "serverTime": serverTime, "nextCursor": next})
}

// --- ported from brick-cli checkupdates_test.go ---

func TestCheckUpdates_NoChangesIsSingleRequest(t *testing.T) {
	calls := 0
	c := rawClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if got := r.URL.Query().Get("since"); got != "1000" {
			t.Errorf("since = %q", got)
		}
		writeUpdatesPage(w, nil, 1234, "")
	})
	changed, st, err := c.CheckUpdates(context.Background(), 1000)
	if err != nil || changed || st != 1234 || calls != 1 {
		t.Fatalf("changed=%v st=%d err=%v calls=%d", changed, st, err, calls)
	}
}

func TestCheckUpdates_ShortCircuitsOnFirstChange(t *testing.T) {
	calls := 0
	c := rawClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		writeUpdatesPage(w, []storage.Node{{ID: "x"}}, 50, "more")
	})
	changed, st, err := c.CheckUpdates(context.Background(), 1)
	if err != nil || !changed || st != 50 || calls != 1 {
		t.Fatalf("changed=%v st=%d err=%v calls=%d", changed, st, err, calls)
	}
}

func TestCheckUpdates_FollowsCursorPastFilteredPages(t *testing.T) {
	var cursors []string
	c := rawClient(t, func(w http.ResponseWriter, r *http.Request) {
		cur := r.URL.Query().Get("cursor")
		cursors = append(cursors, cur)
		switch cur {
		case "":
			writeUpdatesPage(w, nil, 100, "p2")
		case "p2":
			writeUpdatesPage(w, nil, 200, "p3")
		default:
			writeUpdatesPage(w, []storage.Node{{ID: "visible"}}, 300, "")
		}
	})
	changed, st, err := c.CheckUpdates(context.Background(), 1)
	if err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	if st != 100 {
		t.Errorf("serverTime = %d, want first page's 100", st)
	}
	if fmt.Sprint(cursors) != "[ p2 p3]" {
		t.Errorf("cursors = %v", cursors)
	}
}

func TestCheckUpdates_PropagatesHTTPError(t *testing.T) {
	c := rawClient(t, func(w http.ResponseWriter, r *http.Request) { http.Error(w, "boom", 500) })
	if _, _, err := c.CheckUpdates(context.Background(), 1); err == nil {
		t.Fatal("expected error")
	}
}

func TestCheckUpdatesDelta_CollectsEveryPage(t *testing.T) {
	var cursors []string
	c := rawClient(t, func(w http.ResponseWriter, r *http.Request) {
		cur := r.URL.Query().Get("cursor")
		cursors = append(cursors, cur)
		switch cur {
		case "":
			writeUpdatesPage(w, []storage.Node{{ID: "a", Path: "/a.txt"}}, 100, "p2")
		case "p2":
			writeUpdatesPage(w, nil, 200, "p3") // an entirely filtered-out page
		default:
			writeUpdatesPage(w, []storage.Node{{ID: "b", Path: "/b.txt"}}, 300, "")
		}
	})

	nodes, st, tooMany, err := c.CheckUpdatesDelta(context.Background(), 1)
	if err != nil || tooMany {
		t.Fatalf("err=%v tooMany=%v", err, tooMany)
	}
	if len(nodes) != 2 || nodes[0].ID != "a" || nodes[1].ID != "b" {
		t.Errorf("nodes = %+v, want a and b across both non-empty pages", nodes)
	}
	// The first page's clock, not the last — a change landing mid-pagination
	// must not be skipped by adopting a later snapshot as the cursor.
	if st != 100 {
		t.Errorf("serverTime = %d, want the first page's 100", st)
	}
	if fmt.Sprint(cursors) != "[ p2 p3]" {
		t.Errorf("cursors = %v", cursors)
	}
}

func TestCheckUpdatesDelta_GivesUpPastPageBudget(t *testing.T) {
	calls := 0
	c := rawClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		// Never stops handing back a cursor: an unbounded backlog.
		writeUpdatesPage(w, []storage.Node{{ID: fmt.Sprint(calls), Path: "/f.txt"}}, 100, "more")
	})

	nodes, st, tooMany, err := c.CheckUpdatesDelta(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if !tooMany {
		t.Error("tooManyPages = false, want true for a feed that never ends")
	}
	if nodes != nil {
		t.Errorf("nodes = %+v, want nil so a partial delta can't be mistaken for a complete one", nodes)
	}
	if st != 100 {
		t.Errorf("serverTime = %d, want it still usable as a walk seed", st)
	}
	if calls != storage.CheckUpdatesDeltaMaxPages {
		t.Errorf("fetched %d pages, want to stop at CheckUpdatesDeltaMaxPages (%d)", calls, storage.CheckUpdatesDeltaMaxPages)
	}
}

func TestCheckUpdatesDelta_PropagatesHTTPError(t *testing.T) {
	c := rawClient(t, func(w http.ResponseWriter, r *http.Request) { http.Error(w, "boom", 500) })
	if _, _, _, err := c.CheckUpdatesDelta(context.Background(), 1); err == nil {
		t.Fatal("expected error")
	}
}

// The delta is only patchable because each row says where the node now lives;
// a path-less feed would leave a client unable to place a change without the
// walk it is trying to avoid. brick-api resolves one per row
// (filterReadableWithPaths), and so must the fake.
func TestCheckUpdatesDelta_RowsCarryResolvedPaths(t *testing.T) {
	c, fs := testutil.NewStorage(t)
	fs.PutFile("docs/deep/a.txt", "A")
	fs.Mkdir("docs/empty")

	nodes, _, tooMany, err := c.CheckUpdatesDelta(context.Background(), 0)
	if err != nil || tooMany {
		t.Fatalf("err=%v tooMany=%v", err, tooMany)
	}
	got := map[string]string{}
	for _, n := range nodes {
		got[n.Path] = n.NodeType
	}
	for path, wantType := range map[string]string{
		"/docs": "folder", "/docs/deep": "folder",
		"/docs/deep/a.txt": "file", "/docs/empty": "folder",
	} {
		if got[path] != wantType {
			t.Errorf("path %q = %q, want a %s row", path, got[path], wantType)
		}
	}
}

// A trashed node still resolves to the path it was trashed from — that path is
// the only thing telling a client which local file to remove.
func TestCheckUpdatesDelta_TrashedRowsKeepTheirPath(t *testing.T) {
	c, fs := testutil.NewStorage(t)
	fs.PutFile("docs/a.txt", "A")
	cursor, err := c.ServerNow(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	fs.Trash("docs")

	nodes, _, _, err := c.CheckUpdatesDelta(context.Background(), cursor)
	if err != nil {
		t.Fatal(err)
	}
	// The folder and its descendant are both reported: the real server bumps
	// every descendant on a soft delete, so no client-side cascade is needed.
	want := map[string]bool{"/docs": false, "/docs/a.txt": false}
	for _, n := range nodes {
		if _, ok := want[n.Path]; !ok {
			t.Errorf("unexpected row %q", n.Path)
			continue
		}
		if !n.IsDeleted {
			t.Errorf("row %q: isDeleted = false, want true", n.Path)
		}
		want[n.Path] = true
	}
	for path, seen := range want {
		if !seen {
			t.Errorf("no row for %q", path)
		}
	}
}

func TestServerNow_ProbesWithFutureSince(t *testing.T) {
	c := rawClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("since") != fmt.Sprint(int64(1)<<62) {
			t.Errorf("since = %s", r.URL.Query().Get("since"))
		}
		writeUpdatesPage(w, nil, 777, "")
	})
	if st, err := c.ServerNow(context.Background()); err != nil || st != 777 {
		t.Fatalf("st=%d err=%v", st, err)
	}
}

// --- against the full fake ---

func TestListChildrenPaginates(t *testing.T) {
	c, fs := testutil.NewStorage(t)
	for i := 0; i < 450; i++ {
		fs.PutFile(fmt.Sprintf("f%03d.txt", i), "x")
	}
	fs.ResetRequests()
	kids, err := c.ListChildren(context.Background(), "root")
	if err != nil {
		t.Fatal(err)
	}
	if len(kids) != 450 {
		t.Errorf("got %d children, want 450", len(kids))
	}
	if n := fs.Requests("GET /nodes/root/children"); n != 3 {
		t.Errorf("requests = %d, want 3 pages", n)
	}
}

func TestFileRoundTripAndEtags(t *testing.T) {
	c, fs := testutil.NewStorage(t)
	ctx := context.Background()
	root, err := c.ResolveRoot(ctx)
	if err != nil || root.ID != "root" {
		t.Fatalf("root %+v %v", root, err)
	}
	n, err := c.Upload(ctx, "root", "a.txt", []byte("one"))
	if err != nil {
		t.Fatal(err)
	}
	data, etag, err := c.Download(ctx, n.ID)
	if err != nil || string(data) != "one" || etag != n.Etag {
		t.Fatalf("download %q %q %v (want etag %q)", data, etag, err, n.Etag)
	}
	n2, err := c.Replace(ctx, n.ID, []byte("two"))
	if err != nil || n2.Etag == n.Etag {
		t.Fatalf("replace %+v %v", n2, err)
	}
	if got, _ := fs.Read("a.txt"); got != "two" {
		t.Errorf("server content %q", got)
	}
	if err := c.Delete(ctx, n.ID); err != nil {
		t.Fatal(err)
	}
	if fs.Exists("a.txt") {
		t.Error("file still live after delete")
	}
	// Deleting again (404) is success.
	if err := c.Delete(ctx, n.ID); err != nil {
		t.Errorf("second delete: %v", err)
	}
}

func TestCreateFolderReusesOn409(t *testing.T) {
	c, fs := testutil.NewStorage(t)
	existing := fs.Mkdir("Docs")
	n, err := c.CreateFolder(context.Background(), "root", "Docs")
	if err != nil || n.ID != existing {
		t.Fatalf("got %+v %v, want reuse of %s", n, err, existing)
	}
}

func TestFolderSummary(t *testing.T) {
	c, fs := testutil.NewStorage(t)
	fs.PutFile("A/x.bin", "12345")
	fs.PutFile("A/deep/y.bin", "123")
	fs.PutFile("B/z.bin", "12")
	fs.PutFile("top.bin", "1")
	fs.PutFile("Gone/w.bin", "1234567890")
	fs.Trash("Gone")
	folders, total, err := c.FolderSummary(context.Background(), "root")
	if err != nil {
		t.Fatal(err)
	}
	if len(folders) != 2 || folders[0].Name != "A" || folders[1].Name != "B" {
		t.Errorf("folders = %+v", folders)
	}
	if total != 11 {
		t.Errorf("total = %d, want 11", total)
	}
}

func TestQuotaAndErrors(t *testing.T) {
	c, fs := testutil.NewStorage(t)
	fs.PutFile("a", "abc")
	q, err := c.Quota(context.Background())
	if err != nil || q.UsedBytes != 3 || q.QuotaBytes == 0 {
		t.Fatalf("quota %+v %v", q, err)
	}
	fs.FailNext("GET /quota", 1)
	if _, err := c.Quota(context.Background()); err == nil {
		t.Error("expected injected failure")
	}
}

func TestHumanSize(t *testing.T) {
	cases := map[int64]string{0: "0 B", 1023: "1023 B", 1024: "1.0 KB", 1536: "1.5 KB", 5 << 30: "5.0 GB"}
	for in, want := range cases {
		if got := storage.HumanSize(in); got != want {
			t.Errorf("HumanSize(%d) = %q, want %q", in, got, want)
		}
	}
}
