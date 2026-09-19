package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/webbite-io/brick-wails/internal/brickcfg"
)

// TokenSource owns the tokens for one process and persists every change to
// config.yaml. All refreshes go through Rotate, serialized by mu: the auth
// server issues single-use refresh tokens with no grace period, so two
// goroutines spending the same refresh token would revoke the chain.
//
// Unlike brick-cli's tokenMu (which only serializes within one process),
// Rotate also re-reads config.yaml first: if another process — brick-cli, say
// — already rotated the token we presented, we adopt its result instead of
// replaying a refresh token it just spent.
type TokenSource struct {
	store    *brickcfg.Store
	apiURL   string
	clientID string

	mu      sync.Mutex
	access  string
	refresh string
	id      string
}

// NewTokenSource loads the current tokens from store.
func NewTokenSource(store *brickcfg.Store, apiURL, clientID string) (*TokenSource, error) {
	ts := &TokenSource{store: store, apiURL: apiURL, clientID: clientID}
	if err := ts.Reload(); err != nil {
		return nil, err
	}
	return ts, nil
}

// Reload re-reads tokens from disk (e.g. after an external login).
func (ts *TokenSource) Reload() error {
	cfg, err := ts.store.Load()
	if errors.Is(err, brickcfg.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	ts.mu.Lock()
	ts.access, ts.refresh, ts.id = cfg.AccessToken, cfg.RefreshToken, cfg.IDToken
	ts.mu.Unlock()
	return nil
}

// APIURL is the auth/accounts API base URL.
func (ts *TokenSource) APIURL() string { return ts.apiURL }

// Current returns the access token to present.
func (ts *TokenSource) Current() string {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	return ts.access
}

// HasCredentials reports whether any token is held.
func (ts *TokenSource) HasCredentials() bool {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	return ts.access != "" || ts.refresh != ""
}

// HasRefresh reports whether a refresh token is held.
func (ts *TokenSource) HasRefresh() bool {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	return ts.refresh != ""
}

// Set replaces the tokens (after a login) and persists them.
func (ts *TokenSource) Set(t Tokens) error {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	return ts.setLocked(t)
}

func (ts *TokenSource) setLocked(t Tokens) error {
	if t.Access != "" {
		ts.access = t.Access
	}
	if t.Refresh != "" {
		ts.refresh = t.Refresh
	}
	if t.ID != "" {
		ts.id = t.ID
	}
	access, refresh, id := ts.access, ts.refresh, ts.id
	_, err := ts.store.Update(func(c *brickcfg.Config) error {
		c.AccessToken, c.RefreshToken, c.IDToken = access, refresh, id
		return nil
	})
	return err
}

// Clear forgets all tokens (in memory and on disk).
func (ts *TokenSource) Clear() error {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	ts.access, ts.refresh, ts.id = "", "", ""
	_, err := ts.store.Update(func(c *brickcfg.Config) error {
		c.AccessToken, c.RefreshToken, c.IDToken = "", "", ""
		return nil
	})
	return err
}

// Rotate refreshes the tokens and returns the access token to retry with.
// presented is the token whose request was just rejected.
func (ts *TokenSource) Rotate(ctx context.Context, presented string) (string, error) {
	ts.mu.Lock()
	defer ts.mu.Unlock()

	// Another goroutine in this process already rotated past it.
	if ts.access != "" && ts.access != presented {
		return ts.access, nil
	}
	// Another process (e.g. brick-cli) already rotated past it.
	if cfg, err := ts.store.Load(); err == nil && cfg.AccessToken != "" && cfg.AccessToken != presented {
		ts.access, ts.refresh, ts.id = cfg.AccessToken, cfg.RefreshToken, cfg.IDToken
		return ts.access, nil
	}
	if ts.refresh == "" {
		return "", errors.New("no refresh token available")
	}
	t, err := RefreshTokens(ctx, ts.apiURL, ts.refresh, ts.clientID)
	if err != nil {
		return "", err
	}
	if err := ts.setLocked(t); err != nil {
		return "", err
	}
	return ts.access, nil
}

// EnsureAccess makes sure an access token is held, silently refreshing when
// only a refresh token is stored (brick-cli's ensureAuthenticated). Returns
// ErrSessionExpired (wrapped) if the refresh fails.
func (ts *TokenSource) EnsureAccess(ctx context.Context) error {
	ts.mu.Lock()
	access, refresh := ts.access, ts.refresh
	ts.mu.Unlock()
	if access != "" {
		return nil
	}
	if refresh == "" {
		return fmt.Errorf("%w; not logged in", ErrSessionExpired)
	}
	if _, err := ts.Rotate(ctx, ""); err != nil {
		return fmt.Errorf("%w; could not refresh credentials: %v", ErrSessionExpired, err)
	}
	return nil
}

// Client performs authenticated requests using a TokenSource.
type Client struct {
	TS   *TokenSource
	HTTP *http.Client
}

// NewClient returns a Client with brick-cli's 10-minute request timeout
// (large file transfers are sent as a single body).
func NewClient(ts *TokenSource) *Client {
	return &Client{TS: ts, HTTP: &http.Client{Timeout: 10 * time.Minute}}
}

// Do performs method against fullURL with the bearer token. A transport
// error is retried once (a pooled keep-alive connection the server closed);
// a 401 — or a 403 when refreshOn403, since the Storage API historically
// reports expired tokens as 403 — triggers one refresh-and-retry. A failed
// refresh returns an error wrapping ErrSessionExpired.
func (c *Client) Do(ctx context.Context, method, fullURL string, body []byte, headers map[string]string, refreshOn403 bool) (*http.Response, error) {
	do := func(token string) (*http.Response, error) {
		var r io.Reader
		if body != nil {
			r = bytes.NewReader(body)
		}
		req, err := http.NewRequestWithContext(ctx, method, fullURL, r)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		return c.HTTP.Do(req)
	}

	presented := c.TS.Current()
	resp, err := do(presented)
	if err != nil {
		if ctx.Err() != nil {
			return nil, err
		}
		if resp, err = do(presented); err != nil {
			return nil, err
		}
	}
	if (resp.StatusCode == http.StatusUnauthorized || (refreshOn403 && resp.StatusCode == http.StatusForbidden)) && c.TS.HasRefresh() {
		resp.Body.Close()
		newAccess, rerr := c.TS.Rotate(ctx, presented)
		if rerr != nil {
			return nil, fmt.Errorf("%w; token refresh failed: %v", ErrSessionExpired, rerr)
		}
		if newAccess == "" {
			return nil, fmt.Errorf("%w; no access token available", ErrSessionExpired)
		}
		return do(newAccess)
	}
	return resp, nil
}

func (c *Client) getJSON(ctx context.Context, path string, out any) error {
	resp, err := c.Do(ctx, "GET", strings.TrimRight(c.TS.apiURL, "/")+path, nil, nil, false)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		return fmt.Errorf("%w; %s returned 401", ErrSessionExpired, path)
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("%s returned status %d: %s", path, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// UserInfo fetches /oauth2/userinfo.
func (c *Client) UserInfo(ctx context.Context) (*UserInfo, error) {
	var u UserInfo
	if err := c.getJSON(ctx, "/oauth2/userinfo", &u); err != nil {
		return nil, err
	}
	return &u, nil
}

// Accounts fetches every account the user has access to.
func (c *Client) Accounts(ctx context.Context) ([]Account, error) {
	var body struct {
		Accounts []Account `json:"accounts"`
	}
	if err := c.getJSON(ctx, "/v1/accounts", &body); err != nil {
		return nil, err
	}
	if len(body.Accounts) == 0 {
		return nil, errors.New("no accounts found for this user")
	}
	return body.Accounts, nil
}
