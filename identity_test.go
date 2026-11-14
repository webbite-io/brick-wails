package main

import (
	"io"
	"log"
	"path/filepath"
	"testing"
	"time"

	"github.com/webbite-io/brick-wails/internal/auth"
	"github.com/webbite-io/brick-wails/internal/brickcfg"
	"github.com/webbite-io/brick-wails/internal/testutil/fakeoidc"
)

func TestIdentityWatcher(t *testing.T) {
	idp := fakeoidc.New()
	t.Cleanup(idp.Close)
	store := brickcfg.NewStoreAt(filepath.Join(t.TempDir(), "config.yaml"))
	a, r := idp.IssueTokens()
	if _, err := store.Update(func(c *brickcfg.Config) error {
		c.AccessToken, c.RefreshToken, c.ActiveAccountID = a, r, "acct-1"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	ts, err := auth.NewTokenSource(store, idp.URL, idp.ClientID)
	if err != nil {
		t.Fatal(err)
	}
	heard := make(chan Identity, 4)
	w := &identityWatcher{store: store, client: auth.NewClient(ts), logger: log.New(io.Discard, "", 0)}
	w.onChange = func(id Identity) { heard <- id }

	next := func() Identity {
		t.Helper()
		w.refresh()
		select {
		case id := <-heard:
			return id
		case <-time.After(5 * time.Second):
			t.Fatal("no identity heard")
			return Identity{}
		}
	}
	if got, want := next(), (Identity{Account: "Acme", User: "ada@example.com"}); got != want {
		t.Errorf("identity = %+v, want %+v", got, want)
	}
	if got := w.current(); got.Account != "Acme" {
		t.Errorf("current = %+v", got)
	}

	// Logged out: cleared.
	if _, err := store.Update(func(c *brickcfg.Config) error {
		c.AccessToken, c.RefreshToken = "", ""
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if got := next(); got != (Identity{}) {
		t.Errorf("identity after logout = %+v", got)
	}
}
