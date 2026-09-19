package syncengine

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/webbite-io/brick-wails/internal/auth"
	"github.com/webbite-io/brick-wails/internal/brickcfg"
	"github.com/webbite-io/brick-wails/internal/storage"
	"github.com/webbite-io/brick-wails/internal/testutil/fakeoidc"
	"github.com/webbite-io/brick-wails/internal/testutil/fakestorage"
)

// When the refresh token is revoked mid-run, Run must stop and return
// auth.ErrSessionExpired (the app then routes the user back to login).
func TestRunReturnsSessionExpired(t *testing.T) {
	idp := fakeoidc.New()
	defer idp.Close()
	fs := fakestorage.New("acct-1")
	defer fs.Close()
	fs.Authorize = idp.ValidAccess

	store := brickcfg.NewStoreAt(filepath.Join(t.TempDir(), "config.yaml"))
	a, r := idp.IssueTokens()
	store.Update(func(c *brickcfg.Config) error { c.AccessToken, c.RefreshToken = a, r; return nil })
	ts, _ := auth.NewTokenSource(store, idp.URL, idp.ClientID)
	sc := &storage.Client{BaseURL: fs.URL, AccountID: "acct-1", Auth: auth.NewClient(ts)}

	eng := New(Config{Storage: sc, Folder: t.TempDir(), AccountID: "acct-1", RootID: "root",
		StatePath: filepath.Join(t.TempDir(), "s.json"),
		Options:   Options{PollInterval: 30 * time.Millisecond, Debounce: 10 * time.Millisecond}})

	done := make(chan error, 1)
	go func() { done <- eng.Run(context.Background()) }()
	waitFor(t, "idle", func() bool { return eng.Status().State == "idle" })

	// A first expiry is healed transparently by a refresh.
	idp.ExpireAccess()
	fs.PutFile("after-refresh.txt", "x")
	waitFor(t, "sync after silent refresh", func() bool { return eng.Status().Counters.Downloaded == 1 })

	idp.ExpireAccess()
	idp.SetFailRefreshes(true)
	select {
	case err := <-done:
		if !errors.Is(err, auth.ErrSessionExpired) {
			t.Fatalf("Run = %v, want ErrSessionExpired", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not stop on session expiry")
	}
}
