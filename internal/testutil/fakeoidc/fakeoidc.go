// Package fakeoidc is an in-memory stand-in for the Webbite accounts API as
// brick uses it: OIDC discovery, the token endpoint (authorization_code with
// PKCE verification, and single-use refresh tokens like the real server),
// /oauth2/userinfo and /v1/accounts.
package fakeoidc

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
)

// Account is one /v1/accounts entry.
type Account struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Server is the fake. Exported fields may be changed between requests.
type Server struct {
	*httptest.Server

	ClientID   string
	GivenName  string
	FamilyName string
	Email      string

	mu            sync.Mutex
	accounts      []Account
	pending       map[string]string // code -> PKCE challenge
	validAccess   map[string]bool
	validRefresh  map[string]bool
	n             int
	failRefreshes bool

	RefreshCalls  atomic.Int32
	ExchangeCalls atomic.Int32
}

// New starts a fake with one account on a random loopback port.
func New() *Server {
	s := newServer()
	s.Server = httptest.NewServer(s.handler())
	return s
}

// NewOn starts a fake on ln (e.g. a fixed port for manual testing).
func NewOn(ln net.Listener) *Server {
	s := newServer()
	s.Server = httptest.NewUnstartedServer(s.handler())
	s.Server.Listener.Close()
	s.Server.Listener = ln
	s.Server.Start()
	return s
}

func newServer() *Server {
	return &Server{
		ClientID:     "test-client",
		GivenName:    "Ada",
		FamilyName:   "Lovelace",
		Email:        "ada@example.com",
		accounts:     []Account{{ID: "acct-1", Name: "Acme"}},
		pending:      map[string]string{},
		validAccess:  map[string]bool{},
		validRefresh: map[string]bool{},
	}
}

// SetAccounts replaces the account list.
func (s *Server) SetAccounts(a []Account) {
	s.mu.Lock()
	s.accounts = a
	s.mu.Unlock()
}

// SetFailRefreshes makes every refresh fail with invalid_grant.
func (s *Server) SetFailRefreshes(v bool) {
	s.mu.Lock()
	s.failRefreshes = v
	s.mu.Unlock()
}

// IssueTokens mints a valid access/refresh pair directly (for tests that
// start from an already-logged-in config).
func (s *Server) IssueTokens() (access, refresh string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.issueLocked()
}

func (s *Server) issueLocked() (string, string) {
	s.n++
	a, r := fmt.Sprintf("access-%d", s.n), fmt.Sprintf("refresh-%d", s.n)
	s.validAccess[a] = true
	s.validRefresh[r] = true
	return a, r
}

// ExpireAccess invalidates every access token (forcing a refresh).
func (s *Server) ExpireAccess() {
	s.mu.Lock()
	s.validAccess = map[string]bool{}
	s.mu.Unlock()
}

// ValidAccess reports whether token is a currently valid access token. Pass
// it to fakestorage so both fakes agree on auth.
func (s *Server) ValidAccess(token string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.validAccess[token]
}

// CompleteLogin plays the browser: it reads state/redirect_uri/challenge from
// an authorization URL and hits the app's loopback callback with a code.
func (s *Server) CompleteLogin(authURL string) error {
	u, err := url.Parse(authURL)
	if err != nil {
		return err
	}
	q := u.Query()
	if q.Get("client_id") != s.ClientID {
		return fmt.Errorf("client_id = %q, want %q", q.Get("client_id"), s.ClientID)
	}
	if q.Get("code_challenge_method") != "S256" {
		return fmt.Errorf("code_challenge_method = %q", q.Get("code_challenge_method"))
	}
	s.mu.Lock()
	s.n++
	code := fmt.Sprintf("code-%d", s.n)
	s.pending[code] = q.Get("code_challenge")
	s.mu.Unlock()

	cb, _ := url.Parse(q.Get("redirect_uri"))
	cq := cb.Query()
	cq.Set("code", code)
	cq.Set("state", q.Get("state"))
	cb.RawQuery = cq.Encode()
	resp, err := http.Get(cb.String())
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("callback returned %d", resp.StatusCode)
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (s *Server) bearerOK(r *http.Request) bool {
	return s.ValidAccess(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
}

func (s *Server) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]string{
			"authorization_endpoint": s.URL + "/oauth2/authorize",
			"token_endpoint":         s.URL + "/oauth2/token",
		})
	})
	// A real browser can log in against the fake: authorize auto-approves by
	// redirecting straight back to the app's callback with a code.
	mux.HandleFunc("/oauth2/authorize", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("client_id") != s.ClientID {
			http.Error(w, "unknown client_id", 400)
			return
		}
		s.mu.Lock()
		s.n++
		code := fmt.Sprintf("code-%d", s.n)
		s.pending[code] = q.Get("code_challenge")
		s.mu.Unlock()
		cb, err := url.Parse(q.Get("redirect_uri"))
		if err != nil {
			http.Error(w, "bad redirect_uri", 400)
			return
		}
		cq := cb.Query()
		cq.Set("code", code)
		cq.Set("state", q.Get("state"))
		cb.RawQuery = cq.Encode()
		http.Redirect(w, r, cb.String(), http.StatusFound)
	})
	mux.HandleFunc("/oauth2/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.Form.Get("client_id") != s.ClientID {
			writeJSON(w, 400, map[string]string{"error": "invalid_client"})
			return
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		switch r.Form.Get("grant_type") {
		case "authorization_code":
			s.ExchangeCalls.Add(1)
			challenge, ok := s.pending[r.Form.Get("code")]
			delete(s.pending, r.Form.Get("code"))
			sum := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
			if !ok || base64.RawURLEncoding.EncodeToString(sum[:]) != challenge {
				writeJSON(w, 400, map[string]string{"error": "invalid_grant", "error_description": "bad code or verifier"})
				return
			}
		case "refresh_token":
			s.RefreshCalls.Add(1)
			rt := r.Form.Get("refresh_token")
			if s.failRefreshes || !s.validRefresh[rt] {
				writeJSON(w, 400, map[string]string{"error": "invalid_grant", "error_description": "refresh token revoked"})
				return
			}
			delete(s.validRefresh, rt) // single use
		default:
			writeJSON(w, 400, map[string]string{"error": "unsupported_grant_type"})
			return
		}
		a, rt := s.issueLocked()
		writeJSON(w, 200, map[string]string{"access_token": a, "refresh_token": rt, "id_token": "id-" + a})
	})
	mux.HandleFunc("/oauth2/userinfo", func(w http.ResponseWriter, r *http.Request) {
		if !s.bearerOK(r) {
			w.WriteHeader(401)
			return
		}
		writeJSON(w, 200, map[string]string{"given_name": s.GivenName, "family_name": s.FamilyName, "email": s.Email})
	})
	mux.HandleFunc("/v1/accounts", func(w http.ResponseWriter, r *http.Request) {
		if !s.bearerOK(r) {
			w.WriteHeader(401)
			return
		}
		s.mu.Lock()
		accts := s.accounts
		s.mu.Unlock()
		writeJSON(w, 200, map[string]any{"accounts": accts})
	})
	return mux
}
