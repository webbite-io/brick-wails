package storage_test

// Ported from brick-cli cmd/brick/transfer_retry_test.go, transfer_test.go
// and agent_security_test.go @ 54eafd2.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/webbite-io/brick-wails/internal/auth"
	"github.com/webbite-io/brick-wails/internal/storage"
	"github.com/webbite-io/brick-wails/internal/testutil"
	"github.com/webbite-io/brick-wails/internal/testutil/fakestorage"
)

// fastRetries shortens the retry timing for the duration of the test.
func fastRetries(t *testing.T) {
	t.Helper()
	oldMin, oldMax, oldWindow, oldStall := storage.TransferRetryMin, storage.TransferRetryMax, storage.TransferRetryWindow, storage.TransferStallTimeout
	storage.TransferRetryMin, storage.TransferRetryMax, storage.TransferRetryWindow, storage.TransferStallTimeout = time.Millisecond, 5*time.Millisecond, 5*time.Second, 200*time.Millisecond
	t.Cleanup(func() {
		storage.TransferRetryMin, storage.TransferRetryMax, storage.TransferRetryWindow, storage.TransferStallTimeout = oldMin, oldMax, oldWindow, oldStall
	})
}

// flakyFileServer serves one file, f1, the way the Storage API does (ETag,
// Range, If-Range), except that each request takes the next step in steps
// first. Steps:
//
//   - "503", "500": answer with that status
//   - "drop": send the first half of the file, then cut the connection
//   - "drop-change": as "drop", then replace the file's content
//   - "stall": send the first half, then send nothing more
type flakyFileServer struct {
	mu       sync.Mutex
	content  []byte
	etag     string
	steps    []string
	requests []*http.Request
	stalled  chan struct{} // closed at cleanup, releasing stalled handlers
}

func newFlakyFileServer(t *testing.T, content []byte, steps ...string) (*flakyFileServer, *storage.Client) {
	t.Helper()
	fs := &flakyFileServer{content: content, etag: `"v1"`, steps: steps, stalled: make(chan struct{})}
	server := httptest.NewServer(fs)
	t.Cleanup(func() {
		close(fs.stalled)
		server.Close()
	})
	return fs, &storage.Client{BaseURL: server.URL, AccountID: "acct-1", Auth: testutil.NewAuthClient(t, "test-token")}
}

func (fs *flakyFileServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	fs.mu.Lock()
	fs.requests = append(fs.requests, r.Clone(context.Background()))
	step := ""
	if len(fs.steps) > 0 {
		step, fs.steps = fs.steps[0], fs.steps[1:]
	}
	content, etag := fs.content, fs.etag
	if step == "drop-change" {
		fs.content, fs.etag = []byte("completely different content"), `"v2"`
	}
	fs.mu.Unlock()

	switch step {
	case "503":
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	case "500":
		w.WriteHeader(http.StatusInternalServerError)
		return
	case "drop", "drop-change", "stall":
		w.Header().Set("ETag", etag)
		w.Header().Set("Content-Length", strconv.Itoa(len(content)))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(content[:len(content)/2])
		w.(http.Flusher).Flush()
		if step == "stall" {
			select {
			case <-r.Context().Done():
			case <-fs.stalled:
			}
			return
		}
		conn, _, err := w.(http.Hijacker).Hijack()
		if err == nil {
			conn.Close()
		}
		return
	}
	w.Header().Set("ETag", etag)
	http.ServeContent(w, r, "f1", time.Time{}, bytes.NewReader(content))
}

func (fs *flakyFileServer) requestCount() int {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	return len(fs.requests)
}

func (fs *flakyFileServer) request(i int) *http.Request {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	return fs.requests[i]
}

var retryTestContent = []byte("0123456789abcdefghijklmnopqrstuvwxyz")

func downloadTo(t *testing.T, ctx context.Context, sc *storage.Client, total int64) (string, error) {
	t.Helper()
	dest := filepath.Join(t.TempDir(), "out.bin")
	return dest, sc.DownloadToFile(ctx, "f1", dest, total, nil)
}

// tempFileFor returns a download's temporary file for dest, if one is left.
func tempFileFor(dest string) string {
	entries, _ := os.ReadDir(filepath.Dir(dest))
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), filepath.Base(dest)+".") && strings.HasSuffix(e.Name(), storage.TmpSuffix) {
			return filepath.Join(filepath.Dir(dest), e.Name())
		}
	}
	return ""
}

func assertFileContent(t *testing.T, path string, want []byte) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("%s = %q, want %q", path, got, want)
	}
	if left := tempFileFor(path); left != "" {
		t.Errorf("temp file %s left behind", left)
	}
}

func TestDownloadResumesAfterDroppedConnection(t *testing.T) {
	fastRetries(t)
	fs, sc := newFlakyFileServer(t, retryTestContent, "drop")
	dest, err := downloadTo(t, context.Background(), sc, int64(len(retryTestContent)))
	if err != nil {
		t.Fatalf("DownloadToFile: %v", err)
	}
	assertFileContent(t, dest, retryTestContent)
	if n := fs.requestCount(); n != 2 {
		t.Fatalf("server saw %d requests, want 2", n)
	}
	second := fs.request(1)
	wantRange := "bytes=" + strconv.Itoa(len(retryTestContent)/2) + "-"
	if got := second.Header.Get("Range"); got != wantRange {
		t.Errorf("resumed request Range = %q, want %q", got, wantRange)
	}
	if got := second.Header.Get("If-Range"); got != `"v1"` {
		t.Errorf("resumed request If-Range = %q, want the first response's ETag", got)
	}
}

func TestDownloadStartsOverWhenFileChanged(t *testing.T) {
	fastRetries(t)
	_, sc := newFlakyFileServer(t, retryTestContent, "drop-change")
	dest, err := downloadTo(t, context.Background(), sc, 0)
	if err != nil {
		t.Fatalf("DownloadToFile: %v", err)
	}
	assertFileContent(t, dest, []byte("completely different content"))
}

func TestDownloadResumesAfterStall(t *testing.T) {
	fastRetries(t)
	fs, sc := newFlakyFileServer(t, retryTestContent, "stall")
	dest, err := downloadTo(t, context.Background(), sc, int64(len(retryTestContent)))
	if err != nil {
		t.Fatalf("DownloadToFile: %v", err)
	}
	assertFileContent(t, dest, retryTestContent)
	if got := fs.request(1).Header.Get("Range"); got == "" {
		t.Error("the request after the stall had no Range header, want it to resume")
	}
}

func TestDownloadRetriesWhileServerUnavailable(t *testing.T) {
	fastRetries(t)
	fs, sc := newFlakyFileServer(t, retryTestContent, "503", "503")
	var retries []error
	ctx := storage.WithRetryObserver(context.Background(), func(err error, _ time.Duration) {
		retries = append(retries, err)
	})
	dest, err := downloadTo(t, ctx, sc, int64(len(retryTestContent)))
	if err != nil {
		t.Fatalf("DownloadToFile: %v", err)
	}
	assertFileContent(t, dest, retryTestContent)
	if len(retries) != 2 || fs.requestCount() != 3 {
		t.Errorf("observer saw %d retries over %d requests, want 2 over 3", len(retries), fs.requestCount())
	}
}

func TestDownloadNotRetriedOnServerError(t *testing.T) {
	fastRetries(t)
	fs, sc := newFlakyFileServer(t, retryTestContent, "500")
	dest, err := downloadTo(t, context.Background(), sc, int64(len(retryTestContent)))
	if err == nil {
		t.Fatal("DownloadToFile succeeded, want the 500 to fail it")
	}
	if n := fs.requestCount(); n != 1 {
		t.Errorf("server saw %d requests, want 1", n)
	}
	if left := tempFileFor(dest); left != "" {
		t.Errorf("temp file %s left behind", left)
	}
}

func TestDownloadGivesUpAfterRetryWindow(t *testing.T) {
	fastRetries(t)
	storage.TransferRetryWindow = 30 * time.Millisecond
	steps := make([]string, 1000)
	for i := range steps {
		steps[i] = "503"
	}
	_, sc := newFlakyFileServer(t, retryTestContent, steps...)
	dest, err := downloadTo(t, context.Background(), sc, int64(len(retryTestContent)))
	if err == nil || !strings.Contains(err.Error(), "gave up") {
		t.Fatalf("DownloadToFile = %v, want it to give up", err)
	}
	if left := tempFileFor(dest); left != "" {
		t.Errorf("temp file %s left behind", left)
	}
}

func TestDownloadStopsRetryingWhenCanceled(t *testing.T) {
	fastRetries(t)
	storage.TransferRetryMin, storage.TransferRetryMax = time.Hour, time.Hour
	_, sc := newFlakyFileServer(t, retryTestContent, "503")
	ctx, cancel := context.WithCancel(context.Background())
	ctx = storage.WithRetryObserver(ctx, func(error, time.Duration) { cancel() })
	done := make(chan error, 1)
	go func() {
		_, err := downloadTo(t, ctx, sc, int64(len(retryTestContent)))
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Error("DownloadToFile succeeded, want the cancellation to end it")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("DownloadToFile kept waiting to retry after its context was canceled")
	}
}

func TestUploadRetriedWhileServerUnavailable(t *testing.T) {
	fastRetries(t)
	fake := fakestorage.New("acct-1")
	t.Cleanup(fake.Close)
	var mu sync.Mutex
	posts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/v1/accounts/acct-1/files" {
			mu.Lock()
			posts++
			first := posts == 1
			mu.Unlock()
			if first {
				_, _ = io.Copy(io.Discard, r.Body)
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
		}
		fake.Config.Handler.ServeHTTP(w, r)
	}))
	t.Cleanup(server.Close)
	sc := &storage.Client{BaseURL: server.URL, AccountID: "acct-1", Auth: testutil.NewAuthClient(t, "test-token")}

	node, err := sc.UploadStream(context.Background(), "root", "a.bin", bytes.NewReader(retryTestContent), int64(len(retryTestContent)), nil)
	if err != nil {
		t.Fatalf("UploadStream: %v", err)
	}
	if posts != 2 {
		t.Errorf("server saw %d uploads, want 2", posts)
	}
	if got, _ := fake.Data(node.ID); !bytes.Equal(got, retryTestContent) {
		t.Errorf("uploaded content = %q, want %q", got, retryTestContent)
	}
}

func TestIsTransient(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{"503", &storage.StatusError{Code: 503}, true},
		{"502", &storage.StatusError{Code: 502}, true},
		{"500", &storage.StatusError{Code: 500}, false},
		{"404", &storage.StatusError{Code: 404}, false},
		{"stalled", storage.ErrTransferStalled, true},
		{"unexpected EOF", io.ErrUnexpectedEOF, true},
		{"session expired", auth.ErrSessionExpired, false},
		{"local disk", &os.PathError{Op: "write", Path: "/x", Err: errors.New("no space left on device")}, false},
		{"marked", &storage.TransientError{Err: errors.New("x")}, true},
	} {
		if got := storage.IsTransient(tc.err); got != tc.want {
			t.Errorf("IsTransient(%s) = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// The status error keeps the text the engine has always logged.
func TestStatusErrorText(t *testing.T) {
	c := rawClient(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusNotFound)
	})
	_, err := c.GetNode(context.Background(), "x")
	var se *storage.StatusError
	if !errors.As(err, &se) || se.Code != 404 || err.Error() != "storage API 404 Not Found: nope" {
		t.Errorf("err = %v (%T)", err, err)
	}
}

func TestDownloadToFileAndUploadStreamRoundTrip(t *testing.T) {
	sc, _ := testutil.NewStorage(t)
	ctx := context.Background()
	content := []byte(strings.Repeat("hello brick ", 1000))

	dir := t.TempDir()
	src := filepath.Join(dir, "src.bin")
	if err := os.WriteFile(src, content, 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(src)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	var progressCalls []int64
	node, err := sc.UploadStream(ctx, "root", "src.bin", f, int64(len(content)), func(written int64) {
		progressCalls = append(progressCalls, written)
	})
	if err != nil {
		t.Fatalf("UploadStream: %v", err)
	}
	if node.SizeBytes != int64(len(content)) {
		t.Errorf("uploaded size = %d, want %d", node.SizeBytes, len(content))
	}
	if len(progressCalls) == 0 || progressCalls[len(progressCalls)-1] != int64(len(content)) {
		t.Errorf("progress calls = %v, want to end at %d", progressCalls, len(content))
	}
	if got, err := sc.GetNode(ctx, node.ID); err != nil || got.Name != "src.bin" {
		t.Errorf("GetNode = %+v, %v", got, err)
	}

	dest := filepath.Join(dir, "dst.bin")
	var dlProgress []int64
	if err := sc.DownloadToFile(ctx, node.ID, dest, node.SizeBytes, func(written, total int64) {
		dlProgress = append(dlProgress, written)
		if total != int64(len(content)) {
			t.Errorf("DownloadToFile total = %d, want %d", total, len(content))
		}
	}); err != nil {
		t.Fatalf("DownloadToFile: %v", err)
	}
	assertFileContent(t, dest, content)
	if len(dlProgress) == 0 || dlProgress[len(dlProgress)-1] != int64(len(content)) {
		t.Errorf("download progress calls = %v, want to end at %d", dlProgress, len(content))
	}

	// ReplaceStream overwrites the same node rather than creating a new one.
	newContent := []byte("replaced content")
	replaced, err := sc.ReplaceStream(ctx, node.ID, bytes.NewReader(newContent), int64(len(newContent)), nil)
	if err != nil {
		t.Fatalf("ReplaceStream: %v", err)
	}
	if replaced.ID != node.ID || replaced.SizeBytes != int64(len(newContent)) {
		t.Errorf("ReplaceStream returned %+v", replaced)
	}

	// DownloadToNewFile keeps what's already there.
	if err := sc.DownloadToNewFile(ctx, node.ID, dest, replaced.SizeBytes, nil); !errors.Is(err, storage.ErrDestExists) {
		t.Errorf("DownloadToNewFile onto a file: %v, want ErrDestExists", err)
	}
	assertFileContent(t, dest, content)
}

func TestCreateTempForIsNewEachTime(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "file")
	a, err := storage.CreateTempFor(dest)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := storage.CreateTempFor(dest)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if a.Name() == b.Name() {
		t.Errorf("two downloads to %s share %s", dest, a.Name())
	}
	for _, f := range []*os.File{a, b} {
		if !strings.HasPrefix(filepath.Base(f.Name()), "file.") || !strings.HasSuffix(f.Name(), storage.TmpSuffix) {
			t.Errorf("temporary file %s isn't named after file with %s", f.Name(), storage.TmpSuffix)
		}
	}
}

// Two downloads to the same place at once each write their own temporary
// file: the first to finish puts its whole file there and the other fails,
// rather than the first putting the other's half-written file in place.
func TestConcurrentDownloadsToSamePlace(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "file")
	errFor := func(*http.Response) error { return errors.New("unexpected") }
	// download starts a download of data that sends half of it, then waits
	// for release before sending the rest.
	download := func(data string, release <-chan struct{}) (halfway <-chan struct{}, done <-chan error) {
		half, result := make(chan struct{}), make(chan error, 1)
		fetch := func(context.Context, map[string]string) (*http.Response, error) {
			pr, pw := io.Pipe()
			go func() {
				pw.Write([]byte(data[:len(data)/2]))
				close(half)
				<-release
				pw.Write([]byte(data[len(data)/2:]))
				pw.Close()
			}()
			return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: pr}, nil
		}
		go func() {
			result <- storage.ResumableDownload(context.Background(), dest, int64(len(data)), true, nil, fetch, errFor)
		}()
		return half, result
	}

	releaseA, releaseB := make(chan struct{}), make(chan struct{})
	halfA, doneA := download("AAAAAAAAAA", releaseA)
	<-halfA // A's temporary file exists and holds half its data
	halfB, doneB := download("BBBBBBBBBB", releaseB)
	<-halfB
	close(releaseA)
	if err := <-doneA; err != nil {
		t.Fatalf("first download: %v", err)
	}
	if got, _ := os.ReadFile(dest); string(got) != "AAAAAAAAAA" {
		t.Errorf("file = %q after the first download, want its whole file", got)
	}
	close(releaseB)
	if err := <-doneB; !errors.Is(err, storage.ErrDestExists) {
		t.Errorf("second download: err %v, want ErrDestExists", err)
	}
	if got, _ := os.ReadFile(dest); string(got) != "AAAAAAAAAA" {
		t.Errorf("file = %q after the second download, want the first one's kept", got)
	}
	if left := tempFileFor(dest); left != "" {
		t.Errorf("temp file %s left behind", left)
	}
}

func TestPlaceNoClobber(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	tmp := write("a.brick-tmp", "new")
	dest := filepath.Join(dir, "a")
	if err := storage.PlaceNoClobber(tmp, dest); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(dest); string(got) != "new" {
		t.Errorf("dest = %q, want new", got)
	}
	if _, err := os.Lstat(tmp); !os.IsNotExist(err) {
		t.Errorf("tmp still there: %v", err)
	}

	tmp = write("a.brick-tmp", "newer")
	if err := storage.PlaceNoClobber(tmp, dest); !errors.Is(err, storage.ErrDestExists) {
		t.Errorf("onto an existing file: err %v, want ErrDestExists", err)
	}
	if got, _ := os.ReadFile(dest); string(got) != "new" {
		t.Errorf("dest = %q, want it kept", got)
	}
}

func TestSingleAttemptIsNotRetried(t *testing.T) {
	fastRetries(t)
	fs, sc := newFlakyFileServer(t, retryTestContent, "503")
	dest, err := downloadTo(t, storage.WithSingleAttempt(context.Background()), sc, int64(len(retryTestContent)))
	if err == nil {
		t.Fatal("DownloadToFile succeeded, want the 503 to fail it")
	}
	if n := fs.requestCount(); n != 1 {
		t.Errorf("server saw %d requests, want 1", n)
	}
	if left := tempFileFor(dest); left != "" {
		t.Errorf("temp file %s left behind", left)
	}
}

func TestDownloadForSyncReportsWhatItWrote(t *testing.T) {
	fastRetries(t)
	_, sc := newFlakyFileServer(t, retryTestContent, "drop")
	dest := filepath.Join(t.TempDir(), "out.bin")
	got, err := sc.DownloadForSync(context.Background(), "f1", dest, int64(len(retryTestContent)))
	if err != nil {
		t.Fatalf("DownloadForSync: %v", err)
	}
	assertFileContent(t, dest, retryTestContent)
	sum := sha256.Sum256(retryTestContent)
	if got.Size != int64(len(retryTestContent)) || got.SHA256 != hex.EncodeToString(sum[:]) || got.ETag != "v1" {
		t.Errorf("DownloadForSync = %+v, want the whole resumed file's size and hash, and ETag v1", got)
	}
}
