package agent

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/webbite-io/brick-wails/internal/storage"
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

func TestSourceReadErrorRetriesWhileSourceReconnects(t *testing.T) {
	answer := func(status int, body string) *http.Response {
		return &http.Response{StatusCode: status, Status: http.StatusText(status), Body: io.NopCloser(strings.NewReader(body))}
	}
	if err := sourceReadError(answer(http.StatusConflict, `{"error":{"code":"conflict","message":"Client is not currently connected"}}`)); !storage.IsTransient(err) {
		t.Errorf("409 from the Storage API: %v, want it retried", err)
	}
	if err := sourceReadError(answer(http.StatusServiceUnavailable, `{"error":{"code":"service_unavailable","message":"Client did not respond"}}`)); !storage.IsTransient(err) {
		t.Errorf("503 from the Storage API: %v, want it retried", err)
	}
	if err := sourceReadError(answer(http.StatusNotFound, `{"error":{"code":"not_found","message":"No such file."}}`)); storage.IsTransient(err) {
		t.Errorf("404 from the source: %v, want it not retried", err)
	}
}

func TestAgentReadServesRanges(t *testing.T) {
	sc, fake := newTestTransferClient(t)
	ta := newTestAgent(t, sc, fake, true)
	path := filepath.Join(ta.root, "a.txt")
	if err := os.WriteFile(path, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	get := func(headers map[string]string) (int, string, http.Header) {
		req, _ := http.NewRequest("GET", ta.url+"/fs/read?path="+path, nil)
		req.Header.Set(agentSecretHeader, testAgentSecret)
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(body), resp.Header
	}

	status, body, hdr := get(nil)
	if status != http.StatusOK || body != "hello" || hdr.Get("Accept-Ranges") != "bytes" || hdr.Get("ETag") == "" {
		t.Fatalf("GET /fs/read = %d %q (Accept-Ranges %q, ETag %q), want 200 hello with ranges and an ETag",
			status, body, hdr.Get("Accept-Ranges"), hdr.Get("ETag"))
	}
	if status, body, _ := get(map[string]string{"Range": "bytes=2-", "If-Range": hdr.Get("ETag")}); status != http.StatusPartialContent || body != "llo" {
		t.Errorf("GET /fs/read with a matching If-Range = %d %q, want 206 llo", status, body)
	}
	if status, body, _ := get(map[string]string{"Range": "bytes=2-", "If-Range": `"stale"`}); status != http.StatusOK || body != "hello" {
		t.Errorf("GET /fs/read with a stale If-Range = %d %q, want 200 hello", status, body)
	}
}

func TestAgentDownloadRecordsRetries(t *testing.T) {
	fastRetries(t)
	fake := newFakeStorageAPI()
	addFakeFile(fake, "f1", "a.txt", []byte("hello"))
	inner := fake.mux("acct-1")
	var mu sync.Mutex
	failed := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		fail := !failed && r.Method == http.MethodGet && r.URL.Path == "/v1/accounts/acct-1/files/f1"
		failed = failed || fail
		mu.Unlock()
		if fail {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		inner.ServeHTTP(w, r)
	}))
	t.Cleanup(server.Close)
	sc := testStorageClient(t, server.URL)
	ta := newTestAgent(t, sc, fake, true)

	transferID := uuid.NewString()
	if status := ta.do("POST", "/transfer/download", map[string]string{
		"nodeId": "f1", "localPath": filepath.Join(ta.root, "a.txt"), "transferId": transferID,
	}, nil); status != http.StatusOK {
		t.Fatalf("POST /transfer/download: status %d, want 200", status)
	}
	rec := ta.transfer(transferID)
	if rec.State != transferCompleted || rec.Retries != 1 || rec.RetryError != "" {
		t.Errorf("transfer record = %+v, want completed after 1 retry, with retryError cleared", rec)
	}
	assertFileContent(t, filepath.Join(ta.root, "a.txt"), []byte("hello"))
}
