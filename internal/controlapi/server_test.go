//go:build unix

package controlapi

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/webbite-io/brick-wails/internal/syncengine"
	"github.com/webbite-io/brick-wails/internal/testutil"
)

func start(t *testing.T) (*Server, *syncengine.Engine, chan struct{}) {
	t.Helper()
	_, cfgDir := testutil.IsolateHome(t)
	sc, fs := testutil.NewStorage(t)
	fs.PutFile("a.txt", "A")
	eng := syncengine.New(syncengine.Config{Storage: sc, Folder: t.TempDir(), AccountID: "acct-1", RootID: "root"})
	quit := make(chan struct{}, 1)
	s, err := Start(Options{ConfigDir: cfgDir, Version: "test"}, Hooks{
		Engine:  eng,
		Account: func() (string, string) { return "acct-1", "client-1" },
		Quit:    func() { quit <- struct{}{} },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return s, eng, quit
}

func client(s *Server) *http.Client {
	return &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{
		DialContext: func(context.Context, string, string) (net.Conn, error) { return net.Dial("unix", s.SocketPath()) },
	}}
}

func call(t *testing.T, s *Server, method, path string, token string, out any) int {
	t.Helper()
	req, _ := http.NewRequest(method, "http://unix"+path, nil)
	if token != "" {
		req.Header.Set(SecretHeader, token)
	}
	resp, err := client(s).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if out != nil {
		_ = json.NewDecoder(resp.Body).Decode(out)
	}
	return resp.StatusCode
}

// The discovery file must be exactly what brick-cli's stopRunningInstance /
// pauseRunningInstance read, at the path they look in.
func TestDiscoveryFileCompatibleWithCLI(t *testing.T) {
	s, _, _ := start(t)
	data, err := os.ReadFile(s.DiscoveryPath())
	if err != nil {
		t.Fatal(err)
	}
	var d Discovery
	if err := json.Unmarshal(data, &d); err != nil {
		t.Fatal(err)
	}
	if d.PID != os.Getpid() || d.ProtocolVersion != 1 || d.Transport != "unix" || d.Address != s.SocketPath() || d.Token != s.Token() || d.Background {
		t.Errorf("discovery %+v", d)
	}
	dir, _ := RuntimeDir("")
	if s.DiscoveryPath() != dir+"/agent.json" {
		t.Errorf("discovery path %s not in runtime dir %s", s.DiscoveryPath(), dir)
	}
	info, _ := os.Stat(s.SocketPath())
	if info.Mode().Perm() != 0o600 {
		t.Errorf("socket mode %v", info.Mode().Perm())
	}
	s.Close()
	if _, err := os.Stat(s.DiscoveryPath()); !os.IsNotExist(err) {
		t.Error("discovery file not removed on Close")
	}
}

func TestAuthAndEndpoints(t *testing.T) {
	s, eng, quit := start(t)
	if code := call(t, s, "GET", "/v1/health", "", nil); code != 200 {
		t.Errorf("health %d", code)
	}
	if code := call(t, s, "GET", "/v1/status", "", nil); code != 403 {
		t.Errorf("status without token %d", code)
	}
	if code := call(t, s, "GET", "/v1/status", "wrong", nil); code != 403 {
		t.Errorf("status with wrong token %d", code)
	}

	if err := eng.ReconcileAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	var st syncengine.Status
	call(t, s, "GET", "/v1/status", s.Token(), &st)
	if st.State != "idle" || st.Counters.Downloaded != 1 {
		t.Errorf("status %+v", st)
	}
	var act []syncengine.ActivityEvent
	call(t, s, "GET", "/v1/activity?limit=5", s.Token(), &act)
	if len(act) != 1 || act[0].Kind != "download" {
		t.Errorf("activity %+v", act)
	}
	var acct map[string]string
	call(t, s, "GET", "/v1/account", s.Token(), &acct)
	if acct["accountId"] != "acct-1" || acct["clientId"] != "client-1" {
		t.Errorf("account %+v", acct)
	}
	var q map[string]any
	if code := call(t, s, "GET", "/v1/quota", s.Token(), &q); code != 200 || q["usedBytes"] == nil || q["fetchedAt"] == nil {
		t.Errorf("quota %d %+v", code, q)
	}

	if code := call(t, s, "GET", "/v1/pause", s.Token(), nil); code != 405 {
		t.Errorf("GET pause %d", code)
	}
	call(t, s, "POST", "/v1/pause", s.Token(), &st)
	if st.State != "paused" || !eng.Paused() {
		t.Errorf("after pause %+v", st)
	}
	call(t, s, "POST", "/v1/resume", s.Token(), &st)
	if eng.Paused() {
		t.Error("still paused after resume")
	}
	call(t, s, "POST", "/v1/quit", s.Token(), nil)
	select {
	case <-quit:
	case <-time.After(2 * time.Second):
		t.Fatal("quit hook not called")
	}
}

// An app pointed at a private config dir must not put its socket/discovery
// file where a real brick-cli looks.
func TestRuntimeDirIsolated(t *testing.T) {
	home, _ := testutil.IsolateHome(t)
	private := home + "/private-brick"
	t.Setenv("BRICK_CONFIG_DIR", private)
	dir, err := RuntimeDir(private)
	if err != nil || dir != private+"/run" {
		t.Errorf("isolated runtime dir = %q %v", dir, err)
	}
}
