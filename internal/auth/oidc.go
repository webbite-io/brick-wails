// Package auth implements brick's OIDC login (Authorization Code + PKCE with
// a loopback callback), token refresh and authenticated requests.
//
// Ported from brick-cli cmd/brick/auth.go and the token half of sync.go @
// f3ef7bd. The terminal I/O is gone: login returns an authorization URL and a
// result to wait on, and account selection is left to the caller (the
// onboarding wizard).
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// ErrSessionExpired signals that both the access and refresh tokens are no
// longer valid, so the user must log in again.
var ErrSessionExpired = errors.New("session expired")

// httpClient is used for every auth-API call; overridable in tests.
var httpClient = &http.Client{Timeout: 30 * time.Second}

// OIDCConfig is the subset of .well-known/openid-configuration brick uses.
type OIDCConfig struct {
	AuthorizationEndpoint string `json:"authorization_endpoint"`
	TokenEndpoint         string `json:"token_endpoint"`
}

// FetchOIDCConfig retrieves apiURL/.well-known/openid-configuration.
func FetchOIDCConfig(ctx context.Context, apiURL string) (*OIDCConfig, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", strings.TrimRight(apiURL, "/")+"/.well-known/openid-configuration", nil)
	if err != nil {
		return nil, err
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("could not reach OIDC discovery endpoint: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("OIDC discovery returned status %d", resp.StatusCode)
	}
	var cfg OIDCConfig
	if err := json.NewDecoder(resp.Body).Decode(&cfg); err != nil {
		return nil, fmt.Errorf("could not parse OIDC discovery document: %w", err)
	}
	if cfg.AuthorizationEndpoint == "" || cfg.TokenEndpoint == "" {
		return nil, errors.New("OIDC discovery document is missing required endpoints")
	}
	return &cfg, nil
}

// GeneratePKCE returns a code_verifier and its S256 code_challenge.
func GeneratePKCE() (verifier, challenge string, err error) {
	buf := make([]byte, 32)
	if _, err = rand.Read(buf); err != nil {
		return "", "", fmt.Errorf("could not generate PKCE verifier: %w", err)
	}
	verifier = base64.RawURLEncoding.EncodeToString(buf)
	sum := sha256.Sum256([]byte(verifier))
	challenge = base64.RawURLEncoding.EncodeToString(sum[:])
	return verifier, challenge, nil
}

// Tokens is a token-endpoint result.
type Tokens struct {
	Access  string
	Refresh string
	ID      string
}

func postTokenForm(ctx context.Context, endpoint string, params url.Values, what string) (Tokens, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "POST", endpoint, strings.NewReader(params.Encode()))
	if err != nil {
		return Tokens{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := httpClient.Do(req)
	if err != nil {
		return Tokens{}, fmt.Errorf("%s request failed: %w", what, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return Tokens{}, fmt.Errorf("%s returned status %d: %s", what, resp.StatusCode, string(body))
	}
	var tr struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		IDToken      string `json:"id_token"`
		Error        string `json:"error"`
		ErrorDesc    string `json:"error_description"`
	}
	if err := json.Unmarshal(body, &tr); err != nil {
		return Tokens{}, fmt.Errorf("could not parse %s response: %w", what, err)
	}
	if tr.Error != "" {
		return Tokens{}, fmt.Errorf("%s error %q: %s", what, tr.Error, tr.ErrorDesc)
	}
	if tr.AccessToken == "" {
		return Tokens{}, fmt.Errorf("%s response did not contain an access_token", what)
	}
	return Tokens{Access: tr.AccessToken, Refresh: tr.RefreshToken, ID: tr.IDToken}, nil
}

// ExchangeCode exchanges an authorization code for tokens.
func ExchangeCode(ctx context.Context, tokenEndpoint, code, verifier, redirectURI, clientID string) (Tokens, error) {
	p := url.Values{}
	p.Set("grant_type", "authorization_code")
	p.Set("code", code)
	p.Set("redirect_uri", redirectURI)
	p.Set("client_id", clientID)
	p.Set("code_verifier", verifier)
	return postTokenForm(ctx, tokenEndpoint, p, "token endpoint")
}

// RefreshTokens trades a refresh token for new tokens at apiURL/oauth2/token
// (the same endpoint brick-cli uses). Refresh must use the same client_id the
// token was issued to, which is why this app reuses the CLI's OAuth client.
func RefreshTokens(ctx context.Context, apiURL, refreshToken, clientID string) (Tokens, error) {
	p := url.Values{}
	p.Set("grant_type", "refresh_token")
	p.Set("refresh_token", refreshToken)
	p.Set("client_id", clientID)
	return postTokenForm(ctx, strings.TrimRight(apiURL, "/")+"/oauth2/token", p, "token refresh")
}

// UserInfo is the subset of /oauth2/userinfo brick displays.
type UserInfo struct {
	Email      string `json:"email"`
	GivenName  string `json:"given_name"`
	FamilyName string `json:"family_name"`
}

// Account is one entry from /v1/accounts.
type Account struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}
