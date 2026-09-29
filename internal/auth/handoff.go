package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/google/uuid"
)

// ErrNoConsent means the user has never signed into the client a hand-off was
// minted for, so there is no silent hand-off to be had yet. Nothing is wrong:
// the caller falls back to the plain URL and the user signs in over there
// once, after which every later hand-off goes through silently.
var ErrNoConsent = errors.New("the user has not consented to this client")

// HandoffCode is an authorization code minted for another OIDC client, with
// the PKCE verifier that redeems it.
type HandoffCode struct {
	// Redirect is that client's callback, code and state already on it.
	Redirect *url.URL

	// Verifier is the code's other half. It must never travel anywhere a
	// server can see it — that is the whole security argument for splitting
	// the two (see HandoffURL).
	Verifier string
}

// handoffClient never follows redirects. The authorize endpoint answers a
// bearer-authenticated request with JSON, but if it ever answered with the
// 302 it sends real browsers, following it would carry our Authorization
// header to the web app's origin and redeem nothing. Refusing to follow turns
// that into a plain error, and the caller falls back to the plain URL.
var handoffClient = &http.Client{
	Timeout:       30 * time.Second,
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

// MintHandoffCode mints an authorization code that clientID — a different
// client to the one this app signed in as — can redeem on behalf of the user
// whose session ts holds.
//
// This is the same GET /oauth2/authorize call a browser would make, with two
// differences: our bearer authenticates the user in place of a session
// cookie, and because it is there the endpoint answers with the redirect as
// JSON instead of sending a 302 at a browser we do not have. The caller hands
// the redirect to a real browser along with the verifier (see HandoffURL) and
// that client exchanges it for tokens of its own, so the user crosses over
// already signed in.
//
// Returns ErrNoConsent when the user has not consented to clientID yet, and
// an error wrapping ErrSessionExpired when our own session is past saving.
func MintHandoffCode(ctx context.Context, ts *TokenSource, clientID, redirectURI, scopes string) (*HandoffCode, error) {
	if clientID == "" {
		return nil, errors.New("no OAuth client id for the web app")
	}
	if err := ts.EnsureAccess(ctx); err != nil {
		return nil, err
	}
	cfg, err := FetchOIDCConfig(ctx, ts.apiURL)
	if err != nil {
		return nil, err
	}

	// Fresh per hand-off, never reused: codes are single-use, and a verifier
	// that outlived its code would be a second chance at redeeming it.
	verifier, challenge, err := GeneratePKCE()
	if err != nil {
		return nil, err
	}
	state := uuid.New().String()

	q := url.Values{}
	q.Set("response_type", "code")
	q.Set("client_id", clientID)
	q.Set("redirect_uri", redirectURI)
	q.Set("scope", scopes)
	q.Set("state", state)
	q.Set("code_challenge", challenge)
	q.Set("code_challenge_method", "S256")
	authURL := cfg.AuthorizationEndpoint + "?" + q.Encode()

	// The access token here has usually been sitting in config.yaml since the
	// last sync, so a rejection is routine rather than exceptional: refresh
	// and try the one time. This is the one call the app makes without
	// Client.Do's refresh-and-replay behind it — Client.Do shares an
	// http.Client that follows redirects, which this must not do.
	presented := ts.Current()
	redirect, err := authorizeJSON(ctx, authURL, presented)
	if errors.Is(err, errHandoffUnauthorized) && ts.HasRefresh() {
		access, rerr := ts.Rotate(ctx, presented)
		if rerr != nil {
			return nil, fmt.Errorf("%w; token refresh failed: %v", ErrSessionExpired, rerr)
		}
		redirect, err = authorizeJSON(ctx, authURL, access)
	}
	if err != nil {
		if errors.Is(err, errHandoffUnauthorized) {
			return nil, fmt.Errorf("%w; the authorize endpoint rejected our access token", ErrSessionExpired)
		}
		return nil, err
	}

	u, err := url.Parse(redirect)
	if err != nil {
		return nil, fmt.Errorf("authorize returned an unparseable redirect: %w", err)
	}
	// Not a security control — PKCE binds the code to our verifier, so
	// someone else's code simply would not redeem. It is a mismatch check: a
	// redirect that is not answering the request we just made would otherwise
	// fail later, in the browser, as a web app that will not load.
	if u.Query().Get("state") != state {
		return nil, errors.New("authorize redirect came back with a different state")
	}
	return &HandoffCode{Redirect: u, Verifier: verifier}, nil
}

// errHandoffUnauthorized marks the one failure worth retrying after a refresh.
var errHandoffUnauthorized = errors.New("authorize endpoint rejected the access token")

// authorizeJSON performs the bearer-authenticated authorize call and returns
// the redirect it answers with.
func authorizeJSON(ctx context.Context, authURL, accessToken string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", authURL, nil)
	if err != nil {
		return "", err
	}
	// The access token, not the id token: this endpoint validates access
	// tokens, and an id token authenticates nothing here.
	req.Header.Set("Authorization", "Bearer "+accessToken)
	resp, err := handoffClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("authorize request failed: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	var parsed struct {
		Redirect  string `json:"redirect"`
		Error     string `json:"error"`
		ErrorDesc string `json:"error_description"`
	}
	_ = json.Unmarshal(body, &parsed)

	if resp.StatusCode != http.StatusOK {
		switch {
		case parsed.Error == "access_denied":
			return "", ErrNoConsent
		case resp.StatusCode == http.StatusUnauthorized, resp.StatusCode == http.StatusForbidden:
			return "", errHandoffUnauthorized
		case parsed.Error != "":
			return "", fmt.Errorf("authorize error %q (HTTP %d): %s", parsed.Error, resp.StatusCode, parsed.ErrorDesc)
		}
		return "", fmt.Errorf("authorize returned status %d", resp.StatusCode)
	}
	if parsed.Redirect == "" {
		return "", errors.New("authorize response did not contain a redirect")
	}
	return parsed.Redirect, nil
}
