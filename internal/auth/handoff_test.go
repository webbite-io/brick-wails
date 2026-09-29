package auth

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/webbite-io/brick-wails/internal/brickcfg"
	"github.com/webbite-io/brick-wails/internal/testutil/fakeoidc"
)

// loggedIn returns a TokenSource holding a valid session against srv.
func loggedIn(t *testing.T, srv *fakeoidc.Server) *TokenSource {
	t.Helper()
	store := newStore(t)
	access, refresh := srv.IssueTokens()
	if _, err := store.Update(func(c *brickcfg.Config) error {
		c.AccessToken, c.RefreshToken = access, refresh
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	ts, err := NewTokenSource(store, srv.URL, srv.ClientID)
	if err != nil {
		t.Fatal(err)
	}
	return ts
}

func TestHandoffURLPutsTheVerifierInTheFragment(t *testing.T) {
	redirect, _ := url.Parse("https://brick.example/oauth2/callback?code=c-1&state=s-1")
	got := HandoffURL(&HandoffCode{Redirect: redirect, Verifier: "v-e_r-i_f-i_e-r"}, "acct-1", "/photos")

	want := "https://brick.example/oauth2/callback?code=c-1&state=s-1" +
		"#v=v-e_r-i_f-i_e-r&acct=acct-1&to=%2Fphotos"
	if got != want {
		t.Errorf("HandoffURL() = %q, want %q", got, want)
	}

	// The half that travels to a server must not carry the other half.
	query, _, _ := strings.Cut(got, "#")
	if strings.Contains(query, "v-e_r-i_f-i_e-r") {
		t.Error("the verifier leaked out of the fragment and into the query")
	}
}

func TestHandoffURLReplacesAnExistingFragment(t *testing.T) {
	redirect, _ := url.Parse("https://brick.example/oauth2/callback?code=c-1#stale")
	got := HandoffURL(&HandoffCode{Redirect: redirect, Verifier: "v"}, "a", "/")
	if strings.Contains(got, "stale") {
		t.Errorf("HandoffURL() = %q, still carries the old fragment", got)
	}
	if strings.Count(got, "#") != 1 {
		t.Errorf("HandoffURL() = %q, want exactly one fragment", got)
	}
}

func TestPlainWebURL(t *testing.T) {
	for _, tc := range []struct {
		name, webURL, target, account, want string
	}{
		{"with account", "https://brick.example", "/photos", "acct-1",
			"https://brick.example/photos?hq_account=acct-1"},
		{"without account", "https://brick.example", "/photos", "",
			"https://brick.example/photos"},
		{"trailing slash on the base", "https://brick.example/", "/", "acct-1",
			"https://brick.example/?hq_account=acct-1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := PlainWebURL(tc.webURL, tc.target, tc.account); got != tc.want {
				t.Errorf("PlainWebURL() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestWebCallbackURL(t *testing.T) {
	want := "https://brick.example/oauth2/callback"
	for _, base := range []string{"https://brick.example", "https://brick.example/"} {
		if got := WebCallbackURL(base); got != want {
			t.Errorf("WebCallbackURL(%q) = %q, want %q", base, got, want)
		}
	}
}

func TestMintHandoffCode(t *testing.T) {
	srv := fakeoidc.New()
	defer srv.Close()
	ts := loggedIn(t, srv)

	code, err := MintHandoffCode(context.Background(), ts,
		srv.HandoffClientID, "https://brick.example/oauth2/callback", "openid brick:manage")
	if err != nil {
		t.Fatal(err)
	}
	if code.Redirect.Query().Get("code") == "" {
		t.Error("redirect carries no authorization code")
	}
	// The code is only redeemable by the verifier we kept: the server holds
	// its S256 challenge, and nothing else went over the wire.
	sum := sha256.Sum256([]byte(code.Verifier))
	want := base64.RawURLEncoding.EncodeToString(sum[:])
	if got := srv.Challenge(code.Redirect.Query().Get("code")); got != want {
		t.Errorf("recorded challenge = %q, want S256(verifier) %q", got, want)
	}
}

func TestMintHandoffCodeRefreshesAnExpiredAccessToken(t *testing.T) {
	srv := fakeoidc.New()
	defer srv.Close()
	ts := loggedIn(t, srv)
	srv.ExpireAccess()

	if _, err := MintHandoffCode(context.Background(), ts,
		srv.HandoffClientID, "https://brick.example/oauth2/callback", "openid"); err != nil {
		t.Fatalf("mint after an expired access token: %v", err)
	}
	if n := srv.RefreshCalls.Load(); n != 1 {
		t.Errorf("refresh calls = %d, want exactly 1", n)
	}
}

func TestMintHandoffCodeSessionExpired(t *testing.T) {
	srv := fakeoidc.New()
	defer srv.Close()
	ts := loggedIn(t, srv)
	srv.ExpireAccess()
	srv.SetFailRefreshes(true)

	_, err := MintHandoffCode(context.Background(), ts,
		srv.HandoffClientID, "https://brick.example/oauth2/callback", "openid")
	if !errors.Is(err, ErrSessionExpired) {
		t.Errorf("err = %v, want it to wrap ErrSessionExpired", err)
	}
}

func TestMintHandoffCodeWithoutConsent(t *testing.T) {
	srv := fakeoidc.New()
	defer srv.Close()
	ts := loggedIn(t, srv)
	srv.SetDenyConsent(true)

	_, err := MintHandoffCode(context.Background(), ts,
		srv.HandoffClientID, "https://brick.example/oauth2/callback", "openid")
	if !errors.Is(err, ErrNoConsent) {
		t.Errorf("err = %v, want ErrNoConsent", err)
	}
}

func TestMintHandoffCodeNotLoggedIn(t *testing.T) {
	srv := fakeoidc.New()
	defer srv.Close()
	ts, err := NewTokenSource(newStore(t), srv.URL, srv.ClientID)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := MintHandoffCode(context.Background(), ts,
		srv.HandoffClientID, "https://brick.example/oauth2/callback", "openid"); !errors.Is(err, ErrSessionExpired) {
		t.Errorf("err = %v, want it to wrap ErrSessionExpired", err)
	}
}

// stubAuthorize serves discovery plus an authorize endpoint of the test's
// choosing, for the answers the fake server never gives.
func stubAuthorize(t *testing.T, h http.HandlerFunc) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	var srv *httptest.Server
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"authorization_endpoint":"` + srv.URL + `/oauth2/authorize","token_endpoint":"` + srv.URL + `/oauth2/token"}`))
	})
	mux.HandleFunc("/oauth2/authorize", h)
	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// mintAgainst runs a mint whose session is valid as far as this app knows, so
// the authorize response is the only thing under test.
func mintAgainst(t *testing.T, srv *httptest.Server) error {
	t.Helper()
	store := newStore(t)
	if _, err := store.Update(func(c *brickcfg.Config) error {
		c.AccessToken = "access-1"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	ts, err := NewTokenSource(store, srv.URL, "desktop-client")
	if err != nil {
		t.Fatal(err)
	}
	_, err = MintHandoffCode(context.Background(), ts, "web-client",
		"https://brick.example/oauth2/callback", "openid")
	return err
}

func TestMintHandoffCodeRejectsAMismatchedState(t *testing.T) {
	srv := stubAuthorize(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"redirect":"https://brick.example/oauth2/callback?code=c-1&state=somebody-elses"}`))
	})
	if err := mintAgainst(t, srv); err == nil || !strings.Contains(err.Error(), "different state") {
		t.Errorf("err = %v, want a state mismatch", err)
	}
}

// A 302 is what the endpoint sends a browser. Following it would carry our
// bearer to the web app's origin and redeem nothing, so it has to fail here.
func TestMintHandoffCodeDoesNotFollowARedirect(t *testing.T) {
	var followed bool
	srv := stubAuthorize(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("code") != "" {
			followed = true
		}
		http.Redirect(w, r, "/oauth2/authorize?code=c-1", http.StatusFound)
	})
	if err := mintAgainst(t, srv); err == nil {
		t.Error("a 302 should have failed the mint, not been followed")
	}
	if followed {
		t.Error("the authorize redirect was followed, carrying the bearer with it")
	}
}
