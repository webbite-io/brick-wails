package auth

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"sync"

	"github.com/google/uuid"
)

// LoginParams configures one login attempt.
type LoginParams struct {
	APIURL      string
	ClientID    string
	Scopes      string
	CallbackURL string
}

// ErrLoginCancelled is returned by Wait after Cancel.
var ErrLoginCancelled = errors.New("login cancelled")

// LoginSession is one in-progress Authorization Code + PKCE login. The
// callback listener is bound by StartLogin, so a port conflict (e.g. a
// concurrent `brick login`) surfaces immediately rather than after the user
// has already authorized in the browser.
type LoginSession struct {
	AuthURL string

	params   LoginParams
	oidc     *OIDCConfig
	verifier string
	srv      *http.Server
	codeCh   chan string
	errCh    chan error
	cancelCh chan struct{}
	once     sync.Once
}

// StartLogin discovers the OIDC endpoints, generates PKCE/state and starts the
// loopback callback server. Open AuthURL in a browser, then call Wait.
func StartLogin(ctx context.Context, p LoginParams) (*LoginSession, error) {
	if p.ClientID == "" {
		return nil, errors.New("OAUTH_CLIENT_ID is not set")
	}
	cb, err := url.Parse(p.CallbackURL)
	if err != nil || cb.Host == "" {
		return nil, fmt.Errorf("invalid OAUTH_CALLBACK_URL %q", p.CallbackURL)
	}
	oidc, err := FetchOIDCConfig(ctx, p.APIURL)
	if err != nil {
		return nil, err
	}
	verifier, challenge, err := GeneratePKCE()
	if err != nil {
		return nil, err
	}
	state := uuid.New().String()

	q := url.Values{}
	q.Set("response_type", "code")
	q.Set("client_id", p.ClientID)
	q.Set("redirect_uri", p.CallbackURL)
	q.Set("scope", p.Scopes)
	q.Set("state", state)
	q.Set("code_challenge", challenge)
	q.Set("code_challenge_method", "S256")

	s := &LoginSession{
		AuthURL:  oidc.AuthorizationEndpoint + "?" + q.Encode(),
		params:   p,
		oidc:     oidc,
		verifier: verifier,
		codeCh:   make(chan string, 1),
		errCh:    make(chan error, 1),
		cancelCh: make(chan struct{}),
	}

	path := cb.Path
	if path == "" {
		path = "/"
	}
	mux := http.NewServeMux()
	mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("state") != state {
			http.Error(w, "state mismatch", http.StatusBadRequest)
			s.fail(errors.New("state mismatch in callback"))
			return
		}
		code := r.URL.Query().Get("code")
		if code == "" {
			if e := r.URL.Query().Get("error"); e != "" {
				http.Error(w, "authorization denied", http.StatusBadRequest)
				s.fail(fmt.Errorf("authorization error %q: %s", e, r.URL.Query().Get("error_description")))
				return
			}
			http.Error(w, "missing authorization code", http.StatusBadRequest)
			s.fail(errors.New("callback did not contain an authorization code"))
			return
		}
		fmt.Fprintln(w, "Login successful. You may close this tab and return to Brick.")
		select {
		case s.codeCh <- code:
		default:
		}
	})

	ln, err := net.Listen("tcp", cb.Host)
	if err != nil {
		return nil, fmt.Errorf("could not listen for the login callback on %s (is another Brick login in progress?): %w", cb.Host, err)
	}
	s.srv = &http.Server{Handler: mux}
	go func() {
		if err := s.srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			s.fail(fmt.Errorf("callback server error: %w", err))
		}
	}()
	return s, nil
}

func (s *LoginSession) fail(err error) {
	select {
	case s.errCh <- err:
	default:
	}
}

// Wait blocks until the browser callback arrives, then exchanges the code for
// tokens. The callback server is always shut down before Wait returns. The
// caller bounds it with ctx (brick-cli waits 5 minutes).
func (s *LoginSession) Wait(ctx context.Context) (Tokens, error) {
	defer s.close()
	var code string
	select {
	case <-ctx.Done():
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return Tokens{}, errors.New("login timed out waiting for browser callback")
		}
		return Tokens{}, ctx.Err()
	case <-s.cancelCh:
		return Tokens{}, ErrLoginCancelled
	case err := <-s.errCh:
		return Tokens{}, err
	case code = <-s.codeCh:
	}
	return ExchangeCode(ctx, s.oidc.TokenEndpoint, code, s.verifier, s.params.CallbackURL, s.params.ClientID)
}

// Cancel aborts a pending Wait.
func (s *LoginSession) Cancel() {
	s.once.Do(func() { close(s.cancelCh) })
	s.close()
}

func (s *LoginSession) close() {
	if s.srv != nil {
		_ = s.srv.Shutdown(context.Background())
	}
}
