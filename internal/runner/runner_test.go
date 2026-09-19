//go:build unix

package runner

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/webbite-io/brick-wails/internal/auth"
	"github.com/webbite-io/brick-wails/internal/brickcfg"
	"github.com/webbite-io/brick-wails/internal/controlapi"
	"github.com/webbite-io/brick-wails/internal/lock"
	"github.com/webbite-io/brick-wails/internal/syncengine"
	"github.com/webbite-io/brick-wails/internal/testutil"
	"github.com/webbite-io/brick-wails/internal/testutil/fakeoidc"
	"github.com/webbite-io/brick-wails/internal/testutil/fakestorage"
)

type events struct {
	mu       sync.Mutex
	statuses []Status
}

func (e *events) Status(s Status)                   { e.mu.Lock(); e.statuses = append(e.statuses, s); e.mu.Unlock() }
func (e *events) Activity(syncengine.ActivityEvent) {}
func (e *events) Logf(string, ...any)               {}

type fixture struct {
	t      *testing.T
	home   string
	idp    *fakeoidc.Server
	fs     *fakestorage.Server
	store  *brickcfg.Store
	r      *Runner
	folder string
	ev     *events
}

func newFixture(t *testing.T, configured bool) *fixture {
	t.Helper()
	home, _ := testutil.IsolateHome(t)
	idp := fakeoidc.New()
	t.Cleanup(idp.Close)
	fs := fakestorage.New("acct-1")
	t.Cleanup(fs.Close)
	fs.Authorize = idp.ValidAccess
	store, _ := brickcfg.NewStore()
	folder := filepath.Join(home, "Brick")
	if configured {
		a, r := idp.IssueTokens()
		store.Update(func(c *brickcfg.Config) error {
			c.AccessToken, c.RefreshToken, c.ActiveAccountID = a, r, "acct-1"
			c.EnsureActiveAccount().StorageSyncFolder = folder
			return nil
		})
	} else {
		store.LoadOrCreate()
	}
	ts, _ := auth.NewTokenSource(store, idp.URL, idp.ClientID)
	ev := &events{}
	r := New(Config{
		Env:           brickcfg.Env{APIURL: idp.URL, StorageAPIURL: fs.URL, OAuthClientID: idp.ClientID},
		Store:         store,
		Tokens:        ts,
		Version:       "test",
		Events:        ev,
		EngineOptions: syncengine.Options{PollInterval: 30 * time.Millisecond, Debounce: 10 * time.Millisecond},
		DisableAgent:  true,
	})
	t.Cleanup(r.Stop)
	return &fixture{t: t, home: home, idp: idp, fs: fs, store: store, r: r, folder: folder, ev: ev}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(15 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestStartNotConfigured(t *testing.T) {
	f := newFixture(t, false)
	if err := f.r.Start(StartParams{}); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("err = %v", err)
	}
	if st := f.r.Status(); st.State != StateNotConfigured || st.Running {
		t.Errorf("status %+v", st)
	}
}

func TestStartLockedByCLI(t *testing.T) {
	f := newFixture(t, true)
	lp, _ := lock.PathIn(f.store.Dir())
	lk, _ := lock.Acquire(lp)
	defer lk.Release()
	if err := f.r.Start(StartParams{}); !errors.Is(err, ErrLocked) {
		t.Fatalf("err = %v", err)
	}
	if st := f.r.Status(); st.State != StateLocked {
		t.Errorf("status %+v", st)
	}
}

func TestStartSyncStop(t *testing.T) {
	f := newFixture(t, true)
	f.fs.PutFile("hello.txt", "hi")
	if err := f.r.Start(StartParams{FirstSync: true, ConflictMode: "device"}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "download", func() bool {
		b, err := os.ReadFile(filepath.Join(f.folder, "hello.txt"))
		return err == nil && string(b) == "hi"
	})
	st := f.r.Status()
	if !st.Running || st.Folder != f.folder || st.Counters.Downloaded != 1 {
		t.Errorf("status %+v", st)
	}
	if acct, client := f.r.Account(); acct != "acct-1" || client == "" {
		t.Errorf("account %q %q", acct, client)
	}
	if len(f.r.Activity(10)) != 1 {
		t.Errorf("activity %v", f.r.Activity(10))
	}
	f.r.SetPaused(true)
	if f.r.Status().State != "paused" {
		t.Error("pause not reflected")
	}
	f.r.SetPaused(false)

	// While running, the lock is held (brick-cli would be refused)...
	lp, _ := lock.PathIn(f.store.Dir())
	if _, err := lock.Acquire(lp); !errors.Is(err, lock.ErrLocked) {
		t.Errorf("lock not held while syncing: %v", err)
	}
	disc, _ := controlapi.RuntimeDir(f.store.Dir())
	if _, err := os.Stat(filepath.Join(disc, "agent.json")); err != nil {
		t.Errorf("control API discovery file missing: %v", err)
	}

	f.r.Stop()
	if st := f.r.Status(); st.Running || st.State != StateStopped {
		t.Errorf("after stop %+v", st)
	}
	// ...and released afterwards, with the discovery file gone.
	lk, err := lock.Acquire(lp)
	if err != nil {
		t.Fatalf("lock not released: %v", err)
	}
	lk.Release()
	if _, err := os.Stat(filepath.Join(disc, "agent.json")); !os.IsNotExist(err) {
		t.Error("discovery file left behind")
	}
	if _, err := os.Stat(syncengine.StatePath(f.store.Dir(), "acct-1")); err != nil {
		t.Errorf("state file not in config dir: %v", err)
	}
	f.ev.mu.Lock()
	n := len(f.ev.statuses)
	f.ev.mu.Unlock()
	if n == 0 {
		t.Error("no status events emitted")
	}
}

func TestSessionExpiryStopsWithAuthRequired(t *testing.T) {
	f := newFixture(t, true)
	if err := f.r.Start(StartParams{}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "idle", func() bool { return f.r.Status().State == "idle" })
	f.idp.ExpireAccess()
	f.idp.SetFailRefreshes(true)
	waitFor(t, "auth-required", func() bool { return f.r.Status().State == StateAuthRequired })
	if f.r.Running() {
		t.Error("still running")
	}
	// A new Start without re-login reports the same.
	if err := f.r.Start(StartParams{}); !errors.Is(err, auth.ErrSessionExpired) || f.r.Status().State != StateAuthRequired {
		t.Errorf("restart: %v %+v", err, f.r.Status())
	}
}

func TestStorageUnreachableIsStopped(t *testing.T) {
	f := newFixture(t, true)
	f.fs.FailNext("GET /resolve", 1)
	if err := f.r.Start(StartParams{}); err == nil {
		t.Fatal("expected error")
	}
	if st := f.r.Status(); st.State != StateStopped || st.LastError == "" {
		t.Errorf("status %+v", st)
	}
}

// `brick switch-accounts` / `brick restart` stop "the running instance" via
// POST /v1/quit — which must stop this app's engine (not the app).
func TestCLIQuitViaControlAPIStopsEngine(t *testing.T) {
	f := newFixture(t, true)
	if err := f.r.Start(StartParams{}); err != nil {
		t.Fatal(err)
	}
	dir, _ := controlapi.RuntimeDir(f.store.Dir())
	data, err := os.ReadFile(filepath.Join(dir, "agent.json"))
	if err != nil {
		t.Fatal(err)
	}
	var d controlapi.Discovery
	json.Unmarshal(data, &d)
	c := &http.Client{Transport: &http.Transport{DialContext: func(context.Context, string, string) (net.Conn, error) { return net.Dial("unix", d.Address) }}}
	req, _ := http.NewRequest("POST", "http://unix/v1/quit", nil)
	req.Header.Set(controlapi.SecretHeader, d.Token)
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	waitFor(t, "stopped", func() bool { return !f.r.Running() })
	if st := f.r.Status(); st.State != StateStopped || st.LastError == "" {
		t.Errorf("status %+v", st)
	}
}
