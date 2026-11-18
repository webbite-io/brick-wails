package syncengine

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// tempFiles lists the in-progress download files left anywhere in folder.
func tempFiles(t *testing.T, folder string) []string {
	t.Helper()
	var left []string
	_ = filepath.WalkDir(folder, func(path string, d os.DirEntry, err error) error {
		if err == nil && strings.HasSuffix(d.Name(), TmpSuffix) {
			left = append(left, path)
		}
		return nil
	})
	return left
}

// A download streams to disk and records the bytes it put there, the way the
// in-memory download it replaced did.
func TestDownloadRecordsWhatItWrote(t *testing.T) {
	f := newFixture(t, nil)
	f.fs.PutFile("docs/a.txt", "streamed content")
	f.reconcile()

	f.wantLocal("docs/a.txt", "streamed content")
	entry, ok := f.eng.State().Entries["docs/a.txt"]
	if !ok {
		t.Fatal("docs/a.txt has no sync entry after downloading it")
	}
	if entry.LocalHash != sha256Hex("streamed content") || entry.LocalSize != int64(len("streamed content")) {
		t.Errorf("entry hash %s size %d, want the downloaded content's", entry.LocalHash, entry.LocalSize)
	}
	if entry.RemoteEtag == "" {
		t.Error("entry has no remote ETag")
	}
	if left := tempFiles(t, f.folder); len(left) != 0 {
		t.Errorf("temp files left behind: %v", left)
	}
}

// A download that fails, even with an error worth retrying, is not retried
// within the pass — the next pass retries it — and leaves nothing
// half-written behind.
func TestFailedDownloadIsLeftForTheNextPass(t *testing.T) {
	f := newFixture(t, nil)
	f.fs.PutFile("a.txt", "A")
	f.fs.FailNextWith("GET /files/", 1, http.StatusServiceUnavailable)
	f.reconcile()

	if n := f.fs.Requests("GET /files/"); n != 1 {
		t.Errorf("%d download requests, want 1", n)
	}
	if f.localExists("a.txt") {
		t.Error("a.txt exists after its download failed")
	}
	if left := tempFiles(t, f.folder); len(left) != 0 {
		t.Errorf("temp files left behind: %v", left)
	}

	f.reconcile()
	f.wantLocal("a.txt", "A")
}

// An upload records the hash of the bytes it sent, not of the file afterwards.
func TestUploadRecordsWhatItSent(t *testing.T) {
	f := newFixture(t, nil)
	f.writeLocal("a.txt", "local content")
	f.reconcile()

	f.wantRemote("a.txt", "local content")
	entry := f.eng.State().Entries["a.txt"]
	if entry.LocalHash != sha256Hex("local content") || entry.LocalSize != int64(len("local content")) {
		t.Errorf("entry hash %s size %d, want the uploaded content's", entry.LocalHash, entry.LocalSize)
	}

	f.writeLocal("a.txt", "edited")
	f.reconcile()
	f.wantRemote("a.txt", "edited")
	if entry := f.eng.State().Entries["a.txt"]; entry.LocalHash != sha256Hex("edited") {
		t.Errorf("entry hash after replace %s, want the edited content's", entry.LocalHash)
	}
}

// uploadBody sends only the size it was opened at, even if the file grows,
// and starts its hash over when a resent request reads it again.
func TestUploadBodyHashesWhatIsSent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "f.txt")
	if err := os.WriteFile(path, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	body, size, err := openUpload(path)
	if err != nil {
		t.Fatal(err)
	}
	defer body.Close()
	if size != 5 {
		t.Fatalf("size %d, want 5", size)
	}
	if err := os.WriteFile(path, []byte("hello, world"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := io.CopyN(io.Discard, body, 3); err != nil { // a first attempt cut off part way
		t.Fatal(err)
	}
	if _, err := body.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	sent, err := io.ReadAll(body)
	if err != nil {
		t.Fatal(err)
	}
	if string(sent) != "hello" || body.n != 5 || body.sum() != sha256Hex("hello") {
		t.Errorf("sent %q (n=%d, sum %s), want the 5 bytes the file had when opened", sent, body.n, body.sum())
	}
	if _, err := body.Seek(2, io.SeekStart); err == nil {
		t.Error("seeking to the middle succeeded, want an error")
	}
}
