//go:build integration

// Package integration exercises the app's sync end to end: the real
// onboarding flow and runner (lock, engine with a real fsnotify watcher,
// control API) against in-process fake auth and Storage APIs, and — when a
// brick-cli checkout is available — against the real `brick` binary to prove
// the two apps share config, state and the instance lock correctly.
//
// Run with: make test-integration   (or go test -tags integration ./integration/...)
package integration

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/webbite-io/brick-wails/internal/auth"
	"github.com/webbite-io/brick-wails/internal/brickcfg"
	"github.com/webbite-io/brick-wails/internal/onboarding"
	"github.com/webbite-io/brick-wails/internal/runner"
	"github.com/webbite-io/brick-wails/internal/syncengine"
	"github.com/webbite-io/brick-wails/internal/testutil"
	"github.com/webbite-io/brick-wails/internal/testutil/fakeoidc"
	"github.com/webbite-io/brick-wails/internal/testutil/fakestorage"
)

type recEvents struct{ activity atomic.Int32 }

func (e *recEvents) Status(runner.Status)              {}
func (e *recEvents) Activity(syncengine.ActivityEvent) { e.activity.Add(1) }
func (e *recEvents) Logf(string, ...any)               {}

type world struct {
	t      *testing.T
	home   string
	cfgDir string
	idp    *fakeoidc.Server
	fs     *fakestorage.Server
	env    brickcfg.Env
	store  *brickcfg.Store
	tokens *auth.TokenSource
	flow   *onboarding.Flow
	run    *runner.Runner
}

func freeCallback(t *testing.T) string {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return "http://" + ln.Addr().String() + "/auth/callback"
}

func newWorld(t *testing.T) *world {
	t.Helper()
	home, cfgDir := testutil.IsolateHome(t)
	idp := fakeoidc.New()
	t.Cleanup(idp.Close)
	fs := fakestorage.New("acct-1")
	t.Cleanup(fs.Close)
	fs.Authorize = idp.ValidAccess
	w := &world{t: t, home: home, cfgDir: cfgDir, idp: idp, fs: fs}
	w.env = brickcfg.Env{APIURL: idp.URL, StorageAPIURL: fs.URL, OAuthClientID: idp.ClientID, OAuthScopes: "openid", OAuthCallbackURL: freeCallback(t)}
	w.reopen()
	return w
}

// reopen simulates an app restart: fresh store, token source, flow, runner.
func (w *world) reopen() {
	w.t.Helper()
	if w.run != nil {
		w.run.Stop()
	}
	var err error
	if w.store, err = brickcfg.NewStore(); err != nil {
		w.t.Fatal(err)
	}
	if w.tokens, err = auth.NewTokenSource(w.store, w.env.APIURL, w.env.OAuthClientID); err != nil {
		w.t.Fatal(err)
	}
	w.flow = onboarding.New(w.env, w.store, w.tokens)
	w.run = runner.New(runner.Config{
		Env: w.env, Store: w.store, Tokens: w.tokens, Version: "it", Events: &recEvents{},
		EngineOptions: syncengine.Options{PollInterval: 50 * time.Millisecond, Debounce: 30 * time.Millisecond, RecentWindow: 500 * time.Millisecond},
		DisableAgent:  true, // the fake Storage API has no WebSocket endpoint
	})
	w.t.Cleanup(w.run.Stop)
}

func (w *world) login() *onboarding.LoginResult {
	w.t.Helper()
	url, err := w.flow.BeginLogin(context.Background())
	if err != nil {
		w.t.Fatal(err)
	}
	go func() {
		if err := w.idp.CompleteLogin(url); err != nil {
			w.t.Errorf("browser: %v", err)
		}
	}()
	res, err := w.flow.AwaitLogin(context.Background())
	if err != nil {
		w.t.Fatal(err)
	}
	return res
}

func (w *world) route() string {
	w.t.Helper()
	return w.flow.Route(context.Background()).Step
}

// onboard runs the whole wizard: login, ~/Brick, exclude "Excluded", remote
// access on the home folder, and starts syncing.
func (w *world) onboard() string {
	w.t.Helper()
	w.login()
	if s := w.route(); s != onboarding.StepFolder {
		w.t.Fatalf("route after login = %s", s)
	}
	folder, _ := w.flow.DefaultSyncFolder()
	if _, err := w.flow.ChooseSyncFolder(folder); err != nil {
		w.t.Fatal(err)
	}
	if _, err := w.flow.ConfirmSyncFolder(""); err != nil {
		w.t.Fatal(err)
	}
	scope, err := w.flow.Connect(context.Background())
	if err != nil {
		w.t.Fatal(err)
	}
	all := len(scope.Folders) == 0
	var excl []string
	for _, f := range scope.Folders {
		if f == "Excluded" {
			excl = append(excl, f)
		}
	}
	if scope.ShowScope {
		if err := w.flow.SetSyncScope(all, excl); err != nil {
			w.t.Fatal(err)
		}
	}
	if err := w.flow.SetRemoteAccess(true, ""); err != nil {
		w.t.Fatal(err)
	}
	if err := w.run.Start(w.flow.Finish()); err != nil {
		w.t.Fatal(err)
	}
	return folder
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func fileIs(path, content string) func() bool {
	return func() bool {
		b, err := os.ReadFile(path)
		return err == nil && string(b) == content
	}
}

func gone(path string) func() bool {
	return func() bool { _, err := os.Stat(path); return os.IsNotExist(err) }
}

// 1. Fresh machine → onboarding → live two-way sync, with the config the
// wizard wrote honoured by the engine.
func TestFreshOnboardingToLiveSync(t *testing.T) {
	w := newWorld(t)
	w.fs.PutFile("Docs/readme.txt", "hello")
	w.fs.PutFile("Excluded/big.bin", "nope")

	if s := w.route(); s != onboarding.StepWelcome {
		t.Fatalf("fresh route = %s", s)
	}
	folder := w.onboard()

	eventually(t, "initial download", fileIs(filepath.Join(folder, "Docs", "readme.txt"), "hello"))
	if _, err := os.Stat(filepath.Join(folder, "Excluded")); !os.IsNotExist(err) {
		t.Error("excluded folder synced")
	}
	cfg, _ := w.store.Load()
	if ac := cfg.ActiveAccount(); ac.StorageSyncFolder != folder || !reflect.DeepEqual(ac.ExcludeDirs, []string{"Excluded"}) || !cfg.RemoteControl {
		t.Errorf("config %+v %+v", cfg, ac)
	}

	// Local → remote: create, edit, rename, delete.
	os.WriteFile(filepath.Join(folder, "notes.txt"), []byte("v1"), 0o644)
	eventually(t, "upload", func() bool { c, ok := w.fs.Read("notes.txt"); return ok && c == "v1" })
	os.WriteFile(filepath.Join(folder, "notes.txt"), []byte("v2"), 0o644)
	eventually(t, "update", func() bool { c, _ := w.fs.Read("notes.txt"); return c == "v2" })
	os.MkdirAll(filepath.Join(folder, "New", "Deep"), 0o755)
	eventually(t, "new folder", func() bool { return w.fs.Exists("New/Deep") })
	os.Remove(filepath.Join(folder, "notes.txt"))
	eventually(t, "remote trash", func() bool { return !w.fs.Exists("notes.txt") })

	// Remote → local: create, edit, move, trash.
	w.fs.PutFile("from-web.txt", "web")
	eventually(t, "remote create", fileIs(filepath.Join(folder, "from-web.txt"), "web"))
	w.fs.PutFile("from-web.txt", "web2")
	eventually(t, "remote edit", fileIs(filepath.Join(folder, "from-web.txt"), "web2"))
	w.fs.Move("Docs", "Documents")
	eventually(t, "remote folder rename", fileIs(filepath.Join(folder, "Documents", "readme.txt"), "hello"))
	w.fs.Trash("from-web.txt")
	eventually(t, "remote trash mirrored", gone(filepath.Join(folder, "from-web.txt")))

	// Pause / resume under live changes.
	w.run.SetPaused(true)
	w.fs.PutFile("paused.txt", "p")
	os.WriteFile(filepath.Join(folder, "paused-local.txt"), []byte("l"), 0o644)
	time.Sleep(300 * time.Millisecond)
	if _, err := os.Stat(filepath.Join(folder, "paused.txt")); err == nil || w.fs.Exists("paused-local.txt") {
		t.Error("synced while paused")
	}
	w.run.SetPaused(false)
	eventually(t, "catch-up down", fileIs(filepath.Join(folder, "paused.txt"), "p"))
	eventually(t, "catch-up up", func() bool { return w.fs.Exists("paused-local.txt") })
}

// 2. Session loss mid-run → auth-required → re-login through the wizard →
// syncing resumes with no re-download (state survives).
func TestSessionExpiryReloginResumes(t *testing.T) {
	w := newWorld(t)
	w.fs.PutFile("a.txt", "A")
	folder := w.onboard()
	eventually(t, "download", fileIs(filepath.Join(folder, "a.txt"), "A"))

	w.idp.ExpireAccess()
	w.idp.SetFailRefreshes(true)
	eventually(t, "auth-required", func() bool { return w.run.Status().State == runner.StateAuthRequired })
	if s := w.route(); s != onboarding.StepLogin {
		t.Fatalf("route = %s, want login", s)
	}

	w.idp.SetFailRefreshes(false)
	w.flow.Reset()
	if res := w.login(); !res.AccountSelected {
		t.Fatal("re-login should keep the account")
	}
	if s := w.route(); s != onboarding.StepReady {
		t.Fatalf("route after re-login = %s", s)
	}
	w.fs.ResetRequests()
	if err := w.run.Start(w.flow.Finish()); err != nil {
		t.Fatal(err)
	}
	w.fs.PutFile("b.txt", "B")
	eventually(t, "sync resumed", fileIs(filepath.Join(folder, "b.txt"), "B"))
	if n := w.fs.Requests("GET /files/"); n != 1 {
		t.Errorf("downloads after resume = %d, want 1 (only b.txt)", n)
	}
}

// 3. App restart: configured machine goes straight to "ready", changes made
// while it was closed sync on start, nothing else is transferred.
func TestRestartIsIncremental(t *testing.T) {
	w := newWorld(t)
	for _, n := range []string{"1", "2", "3"} {
		w.fs.PutFile("f"+n+".txt", n)
	}
	folder := w.onboard()
	eventually(t, "initial", fileIs(filepath.Join(folder, "f3.txt"), "3"))

	w.run.Stop()
	w.fs.PutFile("while-closed.txt", "x")
	os.WriteFile(filepath.Join(folder, "local-while-closed.txt"), []byte("y"), 0o644)

	w.reopen()
	if s := w.route(); s != onboarding.StepReady {
		t.Fatalf("route after restart = %s", s)
	}
	w.fs.ResetRequests()
	if err := w.run.Start(runner.StartParams{}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "remote change", fileIs(filepath.Join(folder, "while-closed.txt"), "x"))
	eventually(t, "local change", func() bool { return w.fs.Exists("local-while-closed.txt") })
	time.Sleep(200 * time.Millisecond)
	if n := w.fs.Requests("GET /files/"); n != 1 {
		t.Errorf("downloads = %d, want 1", n)
	}
	if n := w.fs.Requests("POST /files"); n != 1 {
		t.Errorf("uploads = %d, want 1", n)
	}
}

// 4. First sync into a pre-populated folder honours the chosen conflict mode.
func TestFirstSyncConflictModeFromWizard(t *testing.T) {
	w := newWorld(t)
	w.fs.PutFile("same.txt", "remote")
	folder := filepath.Join(w.home, "Existing")
	os.MkdirAll(folder, 0o755)
	os.WriteFile(filepath.Join(folder, "same.txt"), []byte("local"), 0o644)

	w.login()
	w.route()
	choice, err := w.flow.ChooseSyncFolder(folder)
	if err != nil || !choice.HasFiles {
		t.Fatalf("%+v %v", choice, err)
	}
	if _, err := w.flow.ConfirmSyncFolder("copy"); err != nil {
		t.Fatal(err)
	}
	if _, err := w.flow.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	w.flow.SetRemoteAccess(false, "")
	if err := w.run.Start(w.flow.Finish()); err != nil {
		t.Fatal(err)
	}
	eventually(t, "copy kept", fileIs(filepath.Join(folder, "same (copy).txt"), "local"))
	eventually(t, "remote wins at original path", fileIs(filepath.Join(folder, "same.txt"), "remote"))
	eventually(t, "copy uploaded", func() bool { c, _ := w.fs.Read("same (copy).txt"); return c == "local" })
}
