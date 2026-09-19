// Package controlapi serves brick's local status/control API (protocol v1)
// for the sync engine running inside this app.
//
// The app itself never *uses* this API. It exists so brick-cli commands that
// act on "the running brick instance" — `brick sync -s` (pauses before
// deleting newly excluded folders), `brick switch-accounts` and `brick
// restart` (stop it) — also work, safely, when the running instance is this
// app rather than a CLI daemon. Without it, `brick sync -s` would delete
// folders under a running engine that still has the old exclude list, which
// could push those deletions to Brick as trash.
//
// Ported from brick-cli cmd/brick/controlapi.go (server half) @ f3ef7bd.
// Protocol, paths, discovery-file shape and header are identical.
package controlapi

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"time"

	"github.com/webbite-io/brick-wails/internal/brickcfg"
	"github.com/webbite-io/brick-wails/internal/storage"
	"github.com/webbite-io/brick-wails/internal/syncengine"
)

// ProtocolVersion matches brick-cli's controlProtocolVersion.
const ProtocolVersion = 1

// SecretHeader carries the discovery token.
const SecretHeader = "X-Brick-Control-Secret"

// Discovery mirrors brick-cli's controlDiscovery.
type Discovery struct {
	PID             int       `json:"pid"`
	Version         string    `json:"version"`
	ProtocolVersion int       `json:"protocolVersion"`
	Transport       string    `json:"transport"`
	Address         string    `json:"address"`
	Token           string    `json:"token"`
	StartedAt       time.Time `json:"startedAt"`
	// Background is always false for this app: brick-cli only relaunches
	// *background* instances (as a CLI daemon) after stopping them, which
	// would be wrong here — the app decides itself when to resume.
	Background    bool     `json:"background"`
	RemoteControl bool     `json:"remoteControl"`
	AgentRoots    []string `json:"agentRoots,omitempty"`
}

// RuntimeDir returns the per-user runtime directory (brick-cli's
// controlRuntimeDir), creating it with mode 0700. configDir is the fallback.
func RuntimeDir(configDir string) (string, error) {
	var base string
	switch {
	case brickcfg.Isolated():
		// Not sharing state with brick-cli: keep runtime files private too.
		base = filepath.Join(configDir, "run")
	default:
		base = platformRuntimeDir()
	}
	if base == "" {
		base = filepath.Join(configDir, "run")
	}
	if err := os.MkdirAll(base, 0o700); err != nil {
		return "", fmt.Errorf("could not create control runtime dir: %w", err)
	}
	return base, nil
}

func platformRuntimeDir() string {
	var base string
	switch runtime.GOOS {
	case "linux":
		if v := os.Getenv("XDG_RUNTIME_DIR"); v != "" {
			base = filepath.Join(v, "brick")
		}
	case "darwin":
		if home, err := os.UserHomeDir(); err == nil {
			base = filepath.Join(home, "Library", "Application Support", "brick", "run")
		}
	case "windows":
		if v := os.Getenv("LOCALAPPDATA"); v != "" {
			base = filepath.Join(v, "brick", "run")
		}
	}
	return base
}

// Hooks connect the server to the app.
type Hooks struct {
	Engine *syncengine.Engine
	// Account returns (accountId, clientId).
	Account func() (string, string)
	// Quit stops syncing (the engine), as `brick switch-accounts`/`restart`
	// expect of the running instance. Must not block on the HTTP request.
	Quit func()
}

// Server is a running control API.
type Server struct {
	ln            net.Listener
	socketPath    string
	discoveryPath string
	token         string
	httpSrv       *http.Server
}

// Options for Start.
type Options struct {
	ConfigDir     string
	Version       string
	RemoteControl bool
	AgentRoots    []string
}

// Start binds the unix socket, writes the discovery file and serves. Callers
// must hold the instance lock (so clearing a stale socket is safe).
func Start(opts Options, h Hooks) (*Server, error) {
	dir, err := RuntimeDir(opts.ConfigDir)
	if err != nil {
		return nil, err
	}
	socketPath := filepath.Join(dir, "control.sock")
	_ = os.Remove(socketPath)
	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		return nil, fmt.Errorf("could not listen on control socket: %w", err)
	}
	if err := os.Chmod(socketPath, 0o600); err != nil && runtime.GOOS != "windows" {
		ln.Close()
		return nil, err
	}
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		ln.Close()
		return nil, err
	}
	token := base64.RawURLEncoding.EncodeToString(buf)

	disc := Discovery{
		PID: os.Getpid(), Version: opts.Version, ProtocolVersion: ProtocolVersion,
		Transport: "unix", Address: socketPath, Token: token, StartedAt: time.Now().UTC(),
		RemoteControl: opts.RemoteControl, AgentRoots: opts.AgentRoots,
	}
	data, _ := json.MarshalIndent(disc, "", "  ")
	discoveryPath := filepath.Join(dir, "agent.json")
	if err := os.WriteFile(discoveryPath, data, 0o600); err != nil {
		ln.Close()
		return nil, fmt.Errorf("could not write discovery file: %w", err)
	}

	s := &Server{ln: ln, socketPath: socketPath, discoveryPath: discoveryPath, token: token}
	s.httpSrv = &http.Server{Handler: s.handler(h)}
	go s.httpSrv.Serve(ln) //nolint:errcheck
	return s, nil
}

// SocketPath is the unix socket address.
func (s *Server) SocketPath() string { return s.socketPath }

// DiscoveryPath is the discovery file.
func (s *Server) DiscoveryPath() string { return s.discoveryPath }

// Token is the shared secret.
func (s *Server) Token() string { return s.token }

// Close stops serving and removes the socket and discovery file — brick-cli's
// stopRunningInstance waits for the discovery file to disappear.
func (s *Server) Close() {
	_ = s.httpSrv.Close()
	_ = os.Remove(s.socketPath)
	_ = os.Remove(s.discoveryPath)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

type quotaPayload struct {
	storage.Quota
	FetchedAt time.Time `json:"fetchedAt"`
}

func (s *Server) handler(h Hooks) http.Handler {
	eng := h.Engine
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"ok": true, "protocolVersion": ProtocolVersion})
	})
	authed := func(fn http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if subtle.ConstantTimeCompare([]byte(r.Header.Get(SecretHeader)), []byte(s.token)) != 1 {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
			fn(w, r)
		}
	}
	post := func(fn http.HandlerFunc) http.HandlerFunc {
		return authed(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost {
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			fn(w, r)
		})
	}
	mux.HandleFunc("/v1/status", authed(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, eng.Status())
	}))
	mux.HandleFunc("/v1/activity", authed(func(w http.ResponseWriter, r *http.Request) {
		limit := 50
		if n, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && n > 0 {
			limit = n
		}
		writeJSON(w, 200, eng.RecentActivity(limit))
	}))
	mux.HandleFunc("/v1/account", authed(func(w http.ResponseWriter, r *http.Request) {
		acct, client := "", ""
		if h.Account != nil {
			acct, client = h.Account()
		}
		writeJSON(w, 200, map[string]any{"accountId": acct, "clientId": client})
	}))
	mux.HandleFunc("/v1/quota", authed(func(w http.ResponseWriter, r *http.Request) {
		q, at := eng.Quota()
		if refresh := r.URL.Query().Get("refresh"); q == nil || refresh == "1" || refresh == "true" {
			ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
			defer cancel()
			if _, err := eng.RefreshQuota(ctx); err != nil {
				if q == nil {
					http.Error(w, "storage quota unavailable", http.StatusServiceUnavailable)
					return
				}
			} else {
				q, at = eng.Quota()
			}
		}
		writeJSON(w, 200, quotaPayload{Quota: *q, FetchedAt: at})
	}))
	mux.HandleFunc("/v1/pause", post(func(w http.ResponseWriter, r *http.Request) {
		// Blocks until any in-flight pass finishes: a 200 guarantees no
		// reconcile is running or will start until resume (runSelectiveSync
		// relies on this).
		eng.PauseAndWait()
		writeJSON(w, 200, eng.Status())
	}))
	mux.HandleFunc("/v1/resume", post(func(w http.ResponseWriter, r *http.Request) {
		eng.SetPaused(false)
		writeJSON(w, 200, eng.Status())
	}))
	mux.HandleFunc("/v1/quit", post(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"ok": true})
		if h.Quit != nil {
			go h.Quit()
		}
	}))
	return mux
}
