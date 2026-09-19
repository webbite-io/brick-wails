package auth

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/webbite-io/brick-wails/internal/brickcfg"
	"github.com/webbite-io/brick-wails/internal/testutil/fakeoidc"
)

func freeCallbackURL(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	return "http://" + addr + "/auth/callback"
}

func newStore(t *testing.T) *brickcfg.Store {
	t.Helper()
	s := brickcfg.NewStoreAt(filepath.Join(t.TempDir(), "config.yaml"))
	if _, _, err := s.LoadOrCreate(); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestGeneratePKCE(t *testing.T) {
	v, c, err := GeneratePKCE()
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(v))
	if want := base64.RawURLEncoding.EncodeToString(sum[:]); c != want {
		t.Errorf("challenge = %q, want S256(verifier) %q", c, want)
	}
	if strings.ContainsAny(v+c, "=+/") {
		t.Error("verifier/challenge must be unpadded base64url")
	}
	v2, _, _ := GeneratePKCE()
	if v == v2 {
		t.Error("verifier not random")
	}
}

func TestLoginHappyPath(t *testing.T) {
	idp := fakeoidc.New()
	defer idp.Close()
	p := LoginParams{APIURL: idp.URL, ClientID: idp.ClientID, Scopes: "openid", CallbackURL: freeCallbackURL(t)}

	s, err := StartLogin(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(s.AuthURL)
	if !strings.HasPrefix(s.AuthURL, idp.URL+"/oauth2/authorize?") || u.Query().Get("redirect_uri") != p.CallbackURL || u.Query().Get("scope") != "openid" {
		t.Fatalf("bad auth URL %s", s.AuthURL)
	}
	go func() {
		if err := idp.CompleteLogin(s.AuthURL); err != nil {
			t.Errorf("CompleteLogin: %v", err)
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	tok, err := s.Wait(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if tok.Access == "" || tok.Refresh == "" || tok.ID == "" {
		t.Fatalf("tokens incomplete: %+v", tok)
	}
	if !idp.ValidAccess(tok.Access) {
		t.Error("issued access token not valid at the IdP")
	}
	// The callback server must be gone afterwards (port released).
	ln, err := net.Listen("tcp", strings.TrimSuffix(strings.TrimPrefix(p.CallbackURL, "http://"), "/auth/callback"))
	if err != nil {
		t.Errorf("callback port still bound after Wait: %v", err)
	} else {
		ln.Close()
	}
}

func callbackWith(t *testing.T, s *LoginSession, mutate func(q url.Values)) {
	t.Helper()
	u, _ := url.Parse(s.AuthURL)
	cb, _ := url.Parse(u.Query().Get("redirect_uri"))
	q := url.Values{}
	q.Set("state", u.Query().Get("state"))
	mutate(q)
	cb.RawQuery = q.Encode()
	resp, err := http.Get(cb.String())
	if err == nil {
		resp.Body.Close()
	}
}

func TestLoginCallbackFailures(t *testing.T) {
	idp := fakeoidc.New()
	defer idp.Close()
	cases := []struct {
		name   string
		mutate func(q url.Values)
		want   string
	}{
		{"state mismatch", func(q url.Values) { q.Set("state", "evil"); q.Set("code", "x") }, "state mismatch"},
		{"denied", func(q url.Values) { q.Set("error", "access_denied"); q.Set("error_description", "nope") }, "access_denied"},
		{"missing code", func(q url.Values) {}, "authorization code"},
		{"bad code", func(q url.Values) { q.Set("code", "never-issued") }, "invalid_grant"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, err := StartLogin(context.Background(), LoginParams{APIURL: idp.URL, ClientID: idp.ClientID, CallbackURL: freeCallbackURL(t)})
			if err != nil {
				t.Fatal(err)
			}
			go callbackWith(t, s, tc.mutate)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_, err = s.Wait(ctx)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want containing %q", err, tc.want)
			}
		})
	}
}

func TestLoginCancelTimeoutAndConfigErrors(t *testing.T) {
	idp := fakeoidc.New()
	defer idp.Close()

	s, err := StartLogin(context.Background(), LoginParams{APIURL: idp.URL, ClientID: idp.ClientID, CallbackURL: freeCallbackURL(t)})
	if err != nil {
		t.Fatal(err)
	}
	go func() { time.Sleep(20 * time.Millisecond); s.Cancel() }()
	if _, err := s.Wait(context.Background()); !errors.Is(err, ErrLoginCancelled) {
		t.Errorf("cancel: err = %v", err)
	}

	s, _ = StartLogin(context.Background(), LoginParams{APIURL: idp.URL, ClientID: idp.ClientID, CallbackURL: freeCallbackURL(t)})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if _, err := s.Wait(ctx); err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Errorf("timeout: err = %v", err)
	}

	if _, err := StartLogin(context.Background(), LoginParams{APIURL: idp.URL, CallbackURL: freeCallbackURL(t)}); err == nil || !strings.Contains(err.Error(), "OAUTH_CLIENT_ID") {
		t.Errorf("missing client id: err = %v", err)
	}

	// Port already in use (e.g. a concurrent `brick login`).
	busy, _ := net.Listen("tcp", "127.0.0.1:0")
	defer busy.Close()
	if _, err := StartLogin(context.Background(), LoginParams{APIURL: idp.URL, ClientID: idp.ClientID, CallbackURL: "http://" + busy.Addr().String() + "/cb"}); err == nil || !strings.Contains(err.Error(), "another Brick login") {
		t.Errorf("port busy: err = %v", err)
	}

	if _, err := StartLogin(context.Background(), LoginParams{APIURL: "http://127.0.0.1:1", ClientID: "x", CallbackURL: freeCallbackURL(t)}); err == nil {
		t.Error("unreachable API: expected error")
	}
}

func TestRefreshTokens(t *testing.T) {
	idp := fakeoidc.New()
	defer idp.Close()
	_, r := idp.IssueTokens()
	tok, err := RefreshTokens(context.Background(), idp.URL, r, idp.ClientID)
	if err != nil || tok.Access == "" || tok.Refresh == "" {
		t.Fatalf("refresh: %+v %v", tok, err)
	}
	// Single use: replaying the old refresh token fails.
	if _, err := RefreshTokens(context.Background(), idp.URL, r, idp.ClientID); err == nil {
		t.Error("replayed refresh token accepted")
	}
	// A different client can't refresh it — why the app must reuse the CLI's client.
	if _, err := RefreshTokens(context.Background(), idp.URL, tok.Refresh, "other-client"); err == nil {
		t.Error("refresh with another client_id accepted")
	}
}

func TestTokenSourcePersistsAndEnsureAccess(t *testing.T) {
	idp := fakeoidc.New()
	defer idp.Close()
	store := newStore(t)
	_, r := idp.IssueTokens()
	if _, err := store.Update(func(c *brickcfg.Config) error { c.RefreshToken = r; return nil }); err != nil {
		t.Fatal(err)
	}
	ts, err := NewTokenSource(store, idp.URL, idp.ClientID)
	if err != nil {
		t.Fatal(err)
	}
	if err := ts.EnsureAccess(context.Background()); err != nil {
		t.Fatal(err)
	}
	cfg, _ := store.Load()
	if cfg.AccessToken == "" || cfg.AccessToken != ts.Current() || cfg.RefreshToken == r {
		t.Errorf("rotated tokens not persisted: %+v", cfg)
	}

	idp.SetFailRefreshes(true)
	_ = ts.Clear()
	_ = ts.Set(Tokens{Refresh: "dead"})
	if err := ts.EnsureAccess(context.Background()); !errors.Is(err, ErrSessionExpired) {
		t.Errorf("err = %v, want ErrSessionExpired", err)
	}
	_ = ts.Clear()
	if err := ts.EnsureAccess(context.Background()); !errors.Is(err, ErrSessionExpired) {
		t.Errorf("no creds: err = %v, want ErrSessionExpired", err)
	}
}

func authedFixture(t *testing.T) (*fakeoidc.Server, *Client, *brickcfg.Store) {
	t.Helper()
	idp := fakeoidc.New()
	t.Cleanup(idp.Close)
	store := newStore(t)
	a, r := idp.IssueTokens()
	if _, err := store.Update(func(c *brickcfg.Config) error { c.AccessToken, c.RefreshToken = a, r; return nil }); err != nil {
		t.Fatal(err)
	}
	ts, err := NewTokenSource(store, idp.URL, idp.ClientID)
	if err != nil {
		t.Fatal(err)
	}
	return idp, NewClient(ts), store
}

// protected returns a server that 401s/403s any token the IdP no longer
// accepts, like the Storage API.
func protected(idp *fakeoidc.Server, status int, hits *atomic.Int32) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits != nil {
			hits.Add(1)
		}
		if !idp.ValidAccess(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")) {
			w.WriteHeader(status)
			return
		}
		w.WriteHeader(200)
	}))
}

func TestClientDoRefreshesOn401And403(t *testing.T) {
	for _, status := range []int{401, 403} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			idp, c, _ := authedFixture(t)
			srv := protected(idp, status, nil)
			defer srv.Close()
			idp.ExpireAccess()
			resp, err := c.Do(context.Background(), "GET", srv.URL, nil, nil, true)
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			if resp.StatusCode != 200 || idp.RefreshCalls.Load() != 1 {
				t.Errorf("status=%d refreshes=%d, want 200/1", resp.StatusCode, idp.RefreshCalls.Load())
			}
		})
	}
}

func TestClientDo403WithoutFlagDoesNotRefresh(t *testing.T) {
	idp, c, _ := authedFixture(t)
	srv := protected(idp, 403, nil)
	defer srv.Close()
	idp.ExpireAccess()
	resp, err := c.Do(context.Background(), "GET", srv.URL, nil, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 403 || idp.RefreshCalls.Load() != 0 {
		t.Errorf("status=%d refreshes=%d", resp.StatusCode, idp.RefreshCalls.Load())
	}
}

func TestClientDoFailedRefreshIsSessionExpired(t *testing.T) {
	idp, c, _ := authedFixture(t)
	srv := protected(idp, 401, nil)
	defer srv.Close()
	idp.ExpireAccess()
	idp.SetFailRefreshes(true)
	_, err := c.Do(context.Background(), "GET", srv.URL, nil, nil, true)
	if !errors.Is(err, ErrSessionExpired) {
		t.Fatalf("err = %v, want ErrSessionExpired", err)
	}
}

func TestClientDoRetriesTransportErrorOnce(t *testing.T) {
	_, c, _ := authedFixture(t)
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if n.Add(1) == 1 {
			hj, _ := w.(http.Hijacker)
			conn, _, _ := hj.Hijack()
			conn.Close() // drop the connection: transport-level error
			return
		}
		w.WriteHeader(204)
	}))
	defer srv.Close()
	resp, err := c.Do(context.Background(), "GET", srv.URL, nil, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 204 || n.Load() != 2 {
		t.Errorf("status=%d attempts=%d", resp.StatusCode, n.Load())
	}
}

// Many goroutines hitting 401 at once must spend the single-use refresh token
// exactly once.
func TestConcurrentRotationRefreshesOnce(t *testing.T) {
	idp, c, _ := authedFixture(t)
	srv := protected(idp, 401, nil)
	defer srv.Close()
	idp.ExpireAccess()

	var wg sync.WaitGroup
	errs := make(chan error, 10)
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp, err := c.Do(context.Background(), "GET", srv.URL, nil, nil, true)
			if err != nil {
				errs <- err
				return
			}
			resp.Body.Close()
			if resp.StatusCode != 200 {
				errs <- fmt.Errorf("status %d", resp.StatusCode)
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	if got := idp.RefreshCalls.Load(); got != 1 {
		t.Errorf("refresh calls = %d, want 1", got)
	}
}

// If another process (brick-cli) already rotated the token we presented, we
// adopt the on-disk token instead of replaying the spent refresh token.
func TestRotateAdoptsTokenRotatedByAnotherProcess(t *testing.T) {
	idp, c, store := authedFixture(t)
	presented := c.TS.Current()

	cfg, _ := store.Load()
	cliTok, err := RefreshTokens(context.Background(), idp.URL, cfg.RefreshToken, idp.ClientID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Update(func(c *brickcfg.Config) error {
		c.AccessToken, c.RefreshToken = cliTok.Access, cliTok.Refresh
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	before := idp.RefreshCalls.Load()

	got, err := c.TS.Rotate(context.Background(), presented)
	if err != nil {
		t.Fatal(err)
	}
	if got != cliTok.Access || idp.RefreshCalls.Load() != before {
		t.Errorf("got %q (refreshes %d→%d), want adopted %q with no refresh", got, before, idp.RefreshCalls.Load(), cliTok.Access)
	}
}

func TestUserInfoAndAccounts(t *testing.T) {
	idp, c, _ := authedFixture(t)
	idp.SetAccounts([]fakeoidc.Account{{ID: "a", Name: "A"}, {ID: "b", Name: "B"}})
	u, err := c.UserInfo(context.Background())
	if err != nil || u.GivenName != "Ada" || u.FamilyName != "Lovelace" {
		t.Fatalf("userinfo %+v %v", u, err)
	}
	accts, err := c.Accounts(context.Background())
	if err != nil || len(accts) != 2 || accts[1].Name != "B" {
		t.Fatalf("accounts %+v %v", accts, err)
	}
	idp.SetAccounts(nil)
	if _, err := c.Accounts(context.Background()); err == nil {
		t.Error("empty accounts should error")
	}
}
