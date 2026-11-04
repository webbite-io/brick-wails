package agent

import (
	"testing"

	"github.com/webbite-io/brick-wails/internal/storage"
	"github.com/webbite-io/brick-wails/internal/testutil"
)

// testStorageClient is a storage.Client for the fake Storage API at url,
// presenting a static access token.
func testStorageClient(t *testing.T, url string) *storage.Client {
	t.Helper()
	return &storage.Client{BaseURL: url, AccountID: "acct-1", Auth: testutil.NewAuthClient(t, "test-token")}
}

// testOptions are Run's Options for a Storage API at url, as account acct-1
// and client client-1.
func testOptions(t *testing.T, url string) Options {
	t.Helper()
	sc := testStorageClient(t, url)
	return Options{Storage: sc, Tokens: sc.Auth.TS, ClientID: "client-1"}
}
