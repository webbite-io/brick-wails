// Package agent is brick's remote-file agent: it registers this device with
// the Storage API over a WebSocket + yamux tunnel (so it appears in the Brick
// webapp) and, only when remote control is enabled, serves the configured
// roots to the same user through that tunnel.
//
// Ported from brick-cli cmd/brick/agent.go @ f3ef7bd.
package agent

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/hashicorp/yamux"

	"github.com/webbite-io/brick-wails/internal/auth"
	"github.com/webbite-io/brick-wails/internal/storage"
)

// agentSecretHeader is a protocol constant shared with the Storage API.
const agentSecretHeader = "X-RBite-Agent-Secret"

func yamuxConfig() *yamux.Config {
	cfg := yamux.DefaultConfig()
	cfg.ConnectionWriteTimeout = 30 * time.Second
	return cfg
}

// Options configure Run.
type Options struct {
	Storage       *storage.Client
	Tokens        *auth.TokenSource
	ClientID      string
	Roots         []string
	RemoteControl bool
	Logf          func(format string, args ...any)
}

// Run starts the local agent API, keeps the tunnel connected (reconnecting
// with backoff) until ctx is cancelled, then deregisters the device.
func Run(ctx context.Context, o Options) error {
	logf := o.Logf
	if logf == nil {
		logf = func(string, ...any) {}
	}
	secret := uuid.NewString()
	_, ln, err := startAgentServer(o.Roots, secret, o.Storage, o.RemoteControl)
	if err != nil {
		return fmt.Errorf("could not start remote file agent: %w", err)
	}
	defer ln.Close()
	if o.RemoteControl {
		logf("Remote control enabled (roots: %s)", strings.Join(o.Roots, ", "))
	}
	connectWithReconnect(ctx, o, secret, ln.Addr().String(), logf)
	deregister(o)
	return nil
}

func connectOnce(ctx context.Context, o Options, secret, agentAddr string, logf func(string, ...any)) error {
	hostname, _ := os.Hostname()
	q := url.Values{}
	q.Set("clientId", o.ClientID)
	q.Set("hostname", hostname)
	q.Set("os", runtime.GOOS)
	q.Set("arch", runtime.GOARCH)
	q.Set("remoteControl", strconv.FormatBool(o.RemoteControl))
	muxURL := toWSURL(strings.TrimRight(o.Storage.BaseURL, "/")) + "/v1/accounts/" + url.PathEscape(o.Storage.AccountID) + "/agent/connect?" + q.Encode()

	presented := o.Tokens.Current()
	hdr := http.Header{}
	hdr.Set("Authorization", "Bearer "+presented)
	hdr.Set(agentSecretHeader, secret)

	dialer := *websocket.DefaultDialer
	ws, resp, err := dialer.DialContext(ctx, muxURL, hdr)
	if err != nil {
		if resp != nil {
			resp.Body.Close()
		}
		if resp != nil && (resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden) && o.Tokens.HasRefresh() {
			_, _ = o.Tokens.Rotate(ctx, presented)
			return fmt.Errorf("dial rejected (HTTP %d); refreshed token, will retry", resp.StatusCode)
		}
		if resp != nil {
			return fmt.Errorf("dial failed (HTTP %d): %w", resp.StatusCode, err)
		}
		return fmt.Errorf("dial failed: %w", err)
	}
	defer ws.Close()

	conn := newWSConn(ws)
	session, err := yamux.Server(conn, yamuxConfig())
	if err != nil {
		return fmt.Errorf("connection session failed: %w", err)
	}
	defer session.Close()

	go func() {
		t := time.NewTicker(20 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if err := conn.ping(); err != nil {
					return
				}
			}
		}
	}()

	logf("Brick connected to Brick Online (clientId=%s)", o.ClientID)

	streamCh := make(chan net.Conn)
	errCh := make(chan error, 1)
	go func() {
		for {
			stream, err := session.Accept()
			if err != nil {
				errCh <- err
				return
			}
			select {
			case streamCh <- stream:
			case <-ctx.Done():
				stream.Close()
				return
			}
		}
	}()
	for {
		select {
		case <-ctx.Done():
			return nil
		case err := <-errCh:
			return fmt.Errorf("session closed: %w", err)
		case stream := <-streamCh:
			go proxyAgentStream(stream, agentAddr, logf)
		}
	}
}

func connectWithReconnect(ctx context.Context, o Options, secret, agentAddr string, logf func(string, ...any)) {
	attempt := 0
	for {
		err := connectOnce(ctx, o, secret, agentAddr, logf)
		if ctx.Err() != nil || err == nil {
			return
		}
		delay := reconnectBackoff[min(attempt, len(reconnectBackoff)-1)]
		logf("Brick Online disconnected (%v); reconnecting in %v...", err, delay)
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}
		attempt++
	}
}

// deregister best-effort removes this client from the Storage API.
func deregister(o Options) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if resp, err := o.Storage.RawRequest(ctx, "DELETE", "/v1/accounts/"+o.Storage.AccountID+"/clients/"+o.ClientID); err == nil {
		resp.Body.Close()
	}
}

// ResolveRoots returns the absolute, de-duplicated agent roots.
func ResolveRoots(roots []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, p := range roots {
		if strings.HasPrefix(p, "~") {
			if home, err := os.UserHomeDir(); err == nil {
				p = filepath.Join(home, strings.TrimPrefix(p, "~"))
			}
		}
		abs, err := filepath.Abs(p)
		if err != nil {
			continue
		}
		abs = filepath.Clean(abs)
		if !seen[abs] {
			seen[abs] = true
			out = append(out, abs)
		}
	}
	return out
}
