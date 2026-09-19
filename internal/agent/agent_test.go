package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/hashicorp/yamux"

	"github.com/webbite-io/brick-wails/internal/auth"
	"github.com/webbite-io/brick-wails/internal/brickcfg"
	"github.com/webbite-io/brick-wails/internal/storage"
	"github.com/webbite-io/brick-wails/internal/testutil"
)

func localAPI(t *testing.T, remoteControl bool) (*agentServer, string, string) {
	t.Helper()
	root := t.TempDir()
	sc, _ := testutil.NewStorage(t)
	a, ln, err := startAgentServer([]string{root}, "s3cret", sc, remoteControl)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	return a, "http://" + ln.Addr().String(), root
}

func do(t *testing.T, method, url, secret string, body string) (int, map[string]any) {
	t.Helper()
	req, _ := http.NewRequest(method, url, strings.NewReader(body))
	if secret != "" {
		req.Header.Set(agentSecretHeader, secret)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	data, _ := io.ReadAll(resp.Body)
	_ = json.Unmarshal(data, &out)
	return resp.StatusCode, out
}

func TestLocalAPIRequiresRemoteControlAndSecret(t *testing.T) {
	_, url, _ := localAPI(t, false)
	if code, out := do(t, "GET", url+"/fs/roots", "s3cret", ""); code != 403 || !strings.Contains(toJSON(out), "remote_control_disabled") {
		t.Errorf("disabled: %d %v", code, out)
	}
	_, url, _ = localAPI(t, true)
	if code, _ := do(t, "GET", url+"/fs/roots", "wrong", ""); code != 403 {
		t.Errorf("bad secret: %d", code)
	}
	if code, out := do(t, "GET", url+"/fs/roots", "s3cret", ""); code != 200 || out["roots"] == nil {
		t.Errorf("roots: %d %v", code, out)
	}
}

func toJSON(v any) string { b, _ := json.Marshal(v); return string(b) }

func TestLocalAPIPathContainment(t *testing.T) {
	_, url, root := localAPI(t, true)
	os.WriteFile(filepath.Join(root, "in.txt"), []byte("hi"), 0o644)
	outside := t.TempDir()
	os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("no"), 0o644)
	os.Symlink(outside, filepath.Join(root, "escape"))

	if code, _ := do(t, "GET", url+"/fs/read?path="+filepath.Join(root, "in.txt"), "s3cret", ""); code != 200 {
		t.Errorf("read inside: %d", code)
	}
	for _, p := range []string{filepath.Join(outside, "secret.txt"), filepath.Join(root, "..", filepath.Base(outside), "secret.txt"), filepath.Join(root, "escape", "secret.txt")} {
		if code, out := do(t, "GET", url+"/fs/read?path="+p, "s3cret", ""); code != 403 {
			t.Errorf("read %s: %d %v, want 403", p, code, out)
		}
	}
	if code, _ := do(t, "DELETE", url+"/fs/delete?path="+root, "s3cret", ""); code != 403 {
		t.Errorf("deleting the root must be refused: %d", code)
	}
	if code, _ := do(t, "POST", url+"/fs/write?path="+filepath.Join(root, "sub", "new.txt"), "s3cret", "body"); code != 200 {
		t.Errorf("write: %d", code)
	}
	if data, _ := os.ReadFile(filepath.Join(root, "sub", "new.txt")); string(data) != "body" {
		t.Errorf("written content %q", data)
	}
	if code, out := do(t, "GET", url+"/fs/list?path="+root, "s3cret", ""); code != 200 || len(out["entries"].([]any)) < 2 {
		t.Errorf("list: %d %v", code, out)
	}
}

func TestLocalAPITransfer(t *testing.T) {
	root := t.TempDir()
	sc, fs := testutil.NewStorage(t)
	_, ln, _ := startAgentServer([]string{root}, "s", sc, true)
	defer ln.Close()
	url := "http://" + ln.Addr().String()
	os.WriteFile(filepath.Join(root, "up.txt"), []byte("UP"), 0o644)

	code, out := do(t, "POST", url+"/transfer/upload", "s", `{"localPath":"`+filepath.Join(root, "up.txt")+`","parentId":"root"}`)
	if code != 200 || out["node"] == nil {
		t.Fatalf("upload %d %v", code, out)
	}
	if got, _ := fs.Read("up.txt"); got != "UP" {
		t.Errorf("remote %q", got)
	}
	id := fs.PutFile("down.txt", "DOWN")
	code, _ = do(t, "POST", url+"/transfer/download", "s", `{"nodeId":"`+id+`","localPath":"`+filepath.Join(root, "d", "down.txt")+`"}`)
	if data, _ := os.ReadFile(filepath.Join(root, "d", "down.txt")); code != 200 || string(data) != "DOWN" {
		t.Errorf("download %d %q", code, data)
	}
}

func TestResolveRoots(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	got := ResolveRoots([]string{"~/a", home + "/a/", "/tmp/../tmp"})
	if len(got) != 2 || got[0] != filepath.Join(home, "a") || got[1] != "/tmp" {
		t.Errorf("got %v", got)
	}
}

// End-to-end tunnel: a fake Storage API accepts the WebSocket, takes the
// yamux client role and proxies an HTTP request through to the local agent,
// the way the real Storage API does for the webapp. On cancel the device is
// deregistered.
func TestTunnelProxiesRequestsAndDeregisters(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "hello.txt"), []byte("hello"), 0o644)

	var deregistered atomic.Int32
	gotBody := make(chan string, 1)
	upgrader := websocket.Upgrader{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/agent/connect"):
			if r.Header.Get("Authorization") != "Bearer tok" || r.URL.Query().Get("clientId") != "cid" || r.URL.Query().Get("remoteControl") != "true" {
				http.Error(w, "bad handshake", 400)
				return
			}
			secret := r.Header.Get(agentSecretHeader)
			ws, err := upgrader.Upgrade(w, r, nil)
			if err != nil {
				return
			}
			sess, err := yamux.Client(newWSConn(ws), yamuxConfig())
			if err != nil {
				return
			}
			stream, err := sess.Open()
			if err != nil {
				return
			}
			req, _ := http.NewRequest("GET", "http://agent/fs/read?path="+filepath.Join(root, "hello.txt"), nil)
			req.Header.Set(agentSecretHeader, secret)
			req.Write(stream)
			resp, err := http.ReadResponse(bufio.NewReader(stream), req)
			if err == nil {
				b, _ := io.ReadAll(resp.Body)
				gotBody <- string(b)
			}
			<-r.Context().Done()
		case r.Method == "DELETE" && strings.HasSuffix(r.URL.Path, "/clients/cid"):
			deregistered.Add(1)
			w.WriteHeader(204)
		}
	}))
	defer srv.Close()

	store := brickcfg.NewStoreAt(filepath.Join(t.TempDir(), "c.yaml"))
	store.Update(func(c *brickcfg.Config) error { c.AccessToken = "tok"; return nil })
	ts, _ := auth.NewTokenSource(store, srv.URL, "x")
	sc := &storage.Client{BaseURL: srv.URL, AccountID: "acct-1", Auth: auth.NewClient(ts)}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		Run(ctx, Options{Storage: sc, Tokens: ts, ClientID: "cid", Roots: []string{root}, RemoteControl: true})
		close(done)
	}()
	select {
	case body := <-gotBody:
		if body != "hello" {
			t.Errorf("proxied body %q", body)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no response through tunnel")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return")
	}
	if deregistered.Load() != 1 {
		t.Error("device not deregistered on shutdown")
	}
}
