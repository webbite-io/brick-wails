package agent

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// connectWithReconnect closes its connected channel once a connection
// actually succeeds, and still returns promptly once ctx is cancelled
// afterward.
func TestConnectWithReconnectSignalsConnectedOnce(t *testing.T) {
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		// connectOnce only needs a successful WS upgrade to get past
		// yamux.Server() and log "connected" — it doesn't need this fake
		// peer to speak yamux at all, so just idle until the client hangs up.
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}))
	defer server.Close()

	o := testOptions(t, server.URL)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	connected := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		connectWithReconnect(ctx, o, "secret", http.NotFoundHandler(), func(string, ...any) {}, connected)
	}()

	select {
	case <-connected:
	case <-time.After(2 * time.Second):
		t.Fatal("connected channel was never closed after a successful connection")
	}

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("connectWithReconnect did not return promptly after ctx cancellation")
	}
}

// A nil connected channel (Run passes none) must never be sent to or closed:
// connectWithReconnect must guard every use of it.
func TestConnectWithReconnectToleratesNilConnectedChannel(t *testing.T) {
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}))
	defer server.Close()

	o := testOptions(t, server.URL)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		connectWithReconnect(ctx, o, "secret", http.NotFoundHandler(), func(string, ...any) {}, nil)
	}()

	// No connected channel to wait on — just give it a moment to actually
	// connect (proving the nil channel didn't panic), then shut down.
	time.Sleep(200 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("connectWithReconnect did not return promptly after ctx cancellation")
	}
}

// A connection that came up and then dropped must start the backoff over:
// otherwise every Storage API restart over a sync's lifetime adds a step,
// and before long each reconnect waits the longest delay.
func TestConnectWithReconnectResetsBackoffAfterConnecting(t *testing.T) {
	saved := reconnectBackoff
	reconnectBackoff = []time.Duration{10 * time.Millisecond, 5 * time.Second}
	t.Cleanup(func() { reconnectBackoff = saved })

	upgrader := websocket.Upgrader{}
	var connects atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		connects.Add(1)
		conn.Close() // drop every connection right away, like a server restarting
	}))
	defer server.Close()

	o := testOptions(t, server.URL)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		connectWithReconnect(ctx, o, "secret", http.NotFoundHandler(), func(string, ...any) {}, nil)
	}()

	// Without the reset, the second drop would wait 5s.
	deadline := time.Now().Add(2 * time.Second)
	for connects.Load() < 4 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	<-done
	if n := connects.Load(); n < 4 {
		t.Fatalf("connected %d times in 2s, want at least 4: the backoff didn't start over after a connection", n)
	}
}

// The connect request identifies this client, its version and its type.
func TestConnectSendsClientInfo(t *testing.T) {
	got := make(chan url.Values, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/accounts/acct-1/agent/connect" || r.Header.Get("Authorization") != "Bearer test-token" || r.Header.Get(agentSecretHeader) != "secret" {
			http.Error(w, "bad handshake", http.StatusBadRequest)
			return
		}
		select {
		case got <- r.URL.Query():
		default:
		}
		http.Error(w, "done", http.StatusBadRequest)
	}))
	defer server.Close()

	o := testOptions(t, server.URL)
	o.RemoteControl = true
	o.Version = VersionInfo{Version: "1.2.3"}
	_ = connectOnce(context.Background(), o, "secret", http.NotFoundHandler(), func(string, ...any) {}, nil)
	var q url.Values
	select {
	case q = <-got:
	case <-time.After(2 * time.Second):
		t.Fatal("the agent never dialed")
	}
	host, _ := os.Hostname()
	for k, want := range map[string]string{
		"clientId": "client-1", "hostname": host, "os": runtime.GOOS, "arch": runtime.GOARCH,
		"remoteControl": "true", "version": "1.2.3", "clientType": "brick-desktop",
	} {
		if q.Get(k) != want {
			t.Errorf("%s = %q, want %q", k, q.Get(k), want)
		}
	}
}
