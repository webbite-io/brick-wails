package webapp

import (
	"io"
	"log"
	"path/filepath"
	"strings"
	"testing"

	"github.com/webbite-io/brick-wails/internal/auth"
	"github.com/webbite-io/brick-wails/internal/brickcfg"
	"github.com/webbite-io/brick-wails/internal/testutil/fakeoidc"
)

// newOpener returns an opener against srv, logged in when loggedIn.
func newOpener(t *testing.T, srv *fakeoidc.Server, loggedIn bool) *Opener {
	t.Helper()
	store := brickcfg.NewStoreAt(filepath.Join(t.TempDir(), "config.yaml"))
	if _, _, err := store.LoadOrCreate(); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Update(func(c *brickcfg.Config) error {
		c.ActiveAccountID = "acct-1"
		if loggedIn {
			c.AccessToken, c.RefreshToken = srv.IssueTokens()
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	tokens, err := auth.NewTokenSource(store, srv.URL, srv.ClientID)
	if err != nil {
		t.Fatal(err)
	}
	return &Opener{
		Env: brickcfg.Env{
			APIURL:           srv.URL,
			OAuthClientID:    srv.ClientID,
			WebOAuthClientID: srv.HandoffClientID,
			OAuthScopes:      "openid brick:manage",
			WebURL:           "https://brick.example",
		},
		Store:  store,
		Tokens: tokens,
		Logger: log.New(io.Discard, "", 0),
	}
}

func TestWebAppOpenerHandsTheSessionOver(t *testing.T) {
	srv := fakeoidc.New()
	defer srv.Close()

	got := newOpener(t, srv, true).url(auth.WebTargetFiles)

	if !strings.HasPrefix(got, "https://brick.example/oauth2/callback?") {
		t.Fatalf("url() = %q, want the web app's callback", got)
	}
	for _, want := range []string{"code=", "#v=", "&acct=acct-1", "&to=%2F"} {
		if !strings.Contains(got, want) {
			t.Errorf("url() = %q, missing %q", got, want)
		}
	}
}

func TestWebAppOpenerFallsBackWhenLoggedOut(t *testing.T) {
	srv := fakeoidc.New()
	defer srv.Close()

	// The account is still known, so the web app's own sign-in lands in the
	// same account this machine syncs.
	want := "https://brick.example/?hq_account=acct-1"
	if got := newOpener(t, srv, false).url(auth.WebTargetFiles); got != want {
		t.Errorf("url() = %q, want %q", got, want)
	}
}

func TestWebAppOpenerFallsBackWhenTheMintFails(t *testing.T) {
	srv := fakeoidc.New()
	defer srv.Close()
	o := newOpener(t, srv, true)
	// The web client id the auth server will not mint for — the shape a
	// misconfigured build takes.
	o.Env.WebOAuthClientID = "not-registered"

	want := "https://brick.example/?hq_account=acct-1"
	if got := o.url(auth.WebTargetFiles); got != want {
		t.Errorf("url() = %q, want the plain URL %q", got, want)
	}
}

// A browser must open either way: a hand-off failure is recoverable by
// signing in on the web, so it can never be the reason nothing happens.
func TestWebAppOpenerAlwaysOpensSomething(t *testing.T) {
	srv := fakeoidc.New()
	defer srv.Close()
	srv.Close() // unreachable auth server

	o := newOpener(t, srv, true)
	var opened string
	o.OpenURL = func(u string) error { opened = u; return nil }
	o.Open(auth.WebTargetFiles)

	if opened != "https://brick.example/?hq_account=acct-1" {
		t.Errorf("opened %q, want the plain URL", opened)
	}
}
