package onboarding

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/webbite-io/brick-wails/internal/auth"
	"github.com/webbite-io/brick-wails/internal/brickcfg"
	"github.com/webbite-io/brick-wails/internal/lock"
	"github.com/webbite-io/brick-wails/internal/testutil"
	"github.com/webbite-io/brick-wails/internal/testutil/fakeoidc"
	"github.com/webbite-io/brick-wails/internal/testutil/fakestorage"
)

type env struct {
	t     *testing.T
	home  string
	idp   *fakeoidc.Server
	fs    *fakestorage.Server
	store *brickcfg.Store
	flow  *Flow
}

func freeCallback(t *testing.T) string {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	defer ln.Close()
	return "http://" + ln.Addr().String() + "/auth/callback"
}

func newEnv(t *testing.T) *env {
	t.Helper()
	home, _ := testutil.IsolateHome(t)
	idp := fakeoidc.New()
	t.Cleanup(idp.Close)
	fs := fakestorage.New("acct-1")
	t.Cleanup(fs.Close)
	fs.Authorize = idp.ValidAccess
	store, err := brickcfg.NewStore()
	if err != nil {
		t.Fatal(err)
	}
	e := brickcfg.Env{APIURL: idp.URL, StorageAPIURL: fs.URL, OAuthClientID: idp.ClientID, OAuthScopes: "openid", OAuthCallbackURL: freeCallback(t)}
	ts, err := auth.NewTokenSource(store, idp.URL, idp.ClientID)
	if err != nil {
		t.Fatal(err)
	}
	return &env{t: t, home: home, idp: idp, fs: fs, store: store, flow: New(e, store, ts)}
}

func (e *env) route() Route {
	e.t.Helper()
	return e.flow.Route(context.Background())
}

func (e *env) login() *LoginResult {
	e.t.Helper()
	url, err := e.flow.BeginLogin(context.Background())
	if err != nil {
		e.t.Fatal(err)
	}
	go func() {
		if err := e.idp.CompleteLogin(url); err != nil {
			e.t.Errorf("CompleteLogin: %v", err)
		}
	}()
	res, err := e.flow.AwaitLogin(context.Background())
	if err != nil {
		e.t.Fatal(err)
	}
	return res
}

func (e *env) cfg() *brickcfg.Config {
	c, err := e.store.Load()
	if err != nil {
		e.t.Fatal(err)
	}
	return c
}

func TestRouteFreshInstallIsWelcome(t *testing.T) {
	e := newEnv(t)
	r := e.route()
	if r.Step != StepWelcome || !r.FirstRun || !strings.Contains(r.Message, "welcome to Brick") {
		t.Errorf("fresh route %+v", r)
	}
	if e.cfg().ClientID == "" {
		t.Error("config not created with a clientId")
	}
	if r := e.route(); r.Step != StepWelcome || r.FirstRun {
		t.Errorf("second route %+v (config exists, no creds)", r)
	}
}

// The whole first-run wizard, step by step, as the UI drives it.
func TestFullOnboardingHappyPath(t *testing.T) {
	e := newEnv(t)
	e.fs.PutFile("Photos/a.jpg", "12345")
	e.fs.PutFile("Work/b.txt", "123")

	res := e.login()
	if res.Greeting != "Hello Ada Lovelace 👋" || !res.AccountSelected || len(res.Accounts) != 1 {
		t.Fatalf("login %+v", res)
	}
	if e.cfg().ActiveAccountID != "acct-1" || e.cfg().AccessToken == "" {
		t.Fatalf("login not persisted: %+v", e.cfg())
	}

	if r := e.route(); r.Step != StepFolder {
		t.Fatalf("route after login %+v", r)
	}
	def, _ := e.flow.DefaultSyncFolder()
	if def != filepath.Join(e.home, "Brick") {
		t.Errorf("default folder %s", def)
	}
	choice, err := e.flow.ChooseSyncFolder(def)
	if err != nil || choice.HasFiles || choice.Display != filepath.Join("~", "Brick") {
		t.Fatalf("choose %+v %v", choice, err)
	}
	if _, err := e.flow.ConfirmSyncFolder(""); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(def); err != nil || !info.IsDir() {
		t.Error("sync folder not created")
	}

	scope, err := e.flow.Connect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !scope.ShowScope || !scope.ShowRemote || scope.TotalHuman != "8 B" || !reflect.DeepEqual(scope.Folders, []string{"Photos", "Work"}) {
		t.Errorf("scope %+v", scope)
	}
	if err := e.flow.SetSyncScope(false, []string{"Photos"}); err != nil {
		t.Fatal(err)
	}
	if err := e.flow.SetRemoteAccess(true, ""); err != nil {
		t.Fatal(err)
	}
	p := e.flow.Finish()
	if !p.FirstSync || p.ConflictMode != "" {
		t.Errorf("start params %+v", p)
	}

	c := e.cfg()
	ac := c.ActiveAccount()
	if ac.StorageSyncFolder != def || !reflect.DeepEqual(ac.ExcludeDirs, []string{"Photos"}) || !c.RemoteControl || !reflect.DeepEqual(c.AgentRoots, []string{e.home}) {
		t.Errorf("config %+v / %+v", c, ac)
	}
	want := []string{
		"Logged in to account: Acme",
		"Sync folder selected (" + filepath.Join("~", "Brick") + ")",
		"Folders selected (1 of 2)",
		"Remote file access enabled (root folder: ~)",
		"Done and ready to go!",
	}
	if got := e.flow.Checklist(); !reflect.DeepEqual(got, want) {
		t.Errorf("checklist\n got %q\nwant %q", got, want)
	}
	if r := e.route(); r.Step != StepReady {
		t.Errorf("final route %+v", r)
	}
}

func TestMultipleAccountsNeedPicker(t *testing.T) {
	e := newEnv(t)
	e.idp.SetAccounts([]fakeoidc.Account{{ID: "acct-1", Name: "Acme"}, {ID: "acct-2", Name: "Beta"}})
	res := e.login()
	if res.AccountSelected || len(res.Accounts) != 2 {
		t.Fatalf("login %+v", res)
	}
	r := e.route()
	if r.Step != StepAccount || len(e.flow.Accounts()) != 2 {
		t.Fatalf("route %+v", r)
	}
	if err := e.flow.SelectAccount("nope"); err == nil {
		t.Error("unknown account accepted")
	}
	if err := e.flow.SelectAccount("acct-2"); err != nil {
		t.Fatal(err)
	}
	if e.cfg().ActiveAccountID != "acct-2" || e.flow.Checklist()[0] != "Logged in to account: Beta" {
		t.Errorf("selection not saved: %q %v", e.cfg().ActiveAccountID, e.flow.Checklist())
	}
}

func TestNonEmptyFolderRequiresConflictMode(t *testing.T) {
	e := newEnv(t)
	e.login()
	e.route()
	dir := filepath.Join(e.home, "Existing")
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "f.txt"), []byte("x"), 0o644)

	choice, err := e.flow.ChooseSyncFolder(dir)
	if err != nil || !choice.HasFiles {
		t.Fatalf("%+v %v", choice, err)
	}
	for _, bad := range []string{"", "nonsense"} {
		if _, err := e.flow.ConfirmSyncFolder(bad); err == nil {
			t.Errorf("conflict mode %q accepted", bad)
		}
	}
	if e.cfg().ActiveAccount() != nil && e.cfg().ActiveAccount().StorageSyncFolder != "" {
		t.Error("folder saved before a conflict mode was chosen")
	}
	if _, err := e.flow.ConfirmSyncFolder("copy"); err != nil {
		t.Fatal(err)
	}
	scope, err := e.flow.Connect(context.Background())
	if err != nil || scope.ShowScope {
		t.Errorf("no remote folders → no scope step: %+v %v", scope, err)
	}
	if p := e.flow.Finish(); !p.FirstSync || p.ConflictMode != "copy" {
		t.Errorf("params %+v", p)
	}
}

func TestSessionExpiredRoutesToLogin(t *testing.T) {
	e := newEnv(t)
	e.login()
	e.idp.ExpireAccess()
	e.idp.SetFailRefreshes(true)
	if r := e.route(); r.Step != StepLogin || !strings.Contains(r.Message, "expired") {
		t.Errorf("route %+v", r)
	}
}

// Re-login for a configured account keeps the account and its folder, and
// lands straight on "ready".
func TestReloginKeepsExistingSetup(t *testing.T) {
	e := newEnv(t)
	e.login()
	e.route()
	e.flow.ChooseSyncFolder(filepath.Join(e.home, "Brick"))
	e.flow.ConfirmSyncFolder("")
	e.flow.Reset()

	e.idp.SetAccounts([]fakeoidc.Account{{ID: "acct-0", Name: "Other"}, {ID: "acct-1", Name: "Acme"}})
	res := e.login()
	if !res.AccountSelected || e.cfg().ActiveAccountID != "acct-1" {
		t.Fatalf("relogin %+v active=%s", res, e.cfg().ActiveAccountID)
	}
	if r := e.route(); r.Step != StepReady {
		t.Errorf("route %+v", r)
	}
}

func TestRouteLockedByCLI(t *testing.T) {
	e := newEnv(t)
	lp, _ := lock.PathIn(e.store.Dir())
	lk, err := lock.Acquire(lp)
	if err != nil {
		t.Fatal(err)
	}
	defer lk.Release()
	if r := e.route(); r.Step != StepLocked {
		t.Errorf("route %+v", r)
	}
}

func TestRouteStorageUnreachableAndMissingFolder(t *testing.T) {
	e := newEnv(t)
	e.login()
	e.route()
	dir := filepath.Join(e.home, "Brick")
	e.flow.ChooseSyncFolder(dir)
	e.flow.ConfirmSyncFolder("")

	os.RemoveAll(dir)
	if r := e.route(); r.Step != StepReady {
		t.Fatalf("route %+v", r)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Error("missing sync folder not recreated")
	}

	e.fs.FailNext("GET /resolve", 1)
	if r := e.route(); r.Step != StepConnectError || r.Detail == "" {
		t.Errorf("route %+v", r)
	}
}

func TestCreateFolderInHome(t *testing.T) {
	e := newEnv(t)
	got, err := e.flow.CreateFolderInHome("a/b")
	if err != nil || got != filepath.Join(e.home, "a", "b") {
		t.Fatalf("%q %v", got, err)
	}
	if _, err := os.Stat(got); err != nil {
		t.Error("not created")
	}
	if _, err := e.flow.CreateFolderInHome("a/b"); err != nil {
		t.Error("existing folder should be fine")
	}
	for _, bad := range []string{"", "  ", "../escape", "/abs", "~/x"} {
		if _, err := e.flow.CreateFolderInHome(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestSetRemoteAccessAndScopeValidation(t *testing.T) {
	e := newEnv(t)
	e.login()
	e.route()
	custom := filepath.Join(e.home, "shared")
	os.MkdirAll(custom, 0o755)
	e.flow.SetRemoteAccess(true, custom)
	e.flow.SetRemoteAccess(true, custom)
	if c := e.cfg(); !reflect.DeepEqual(c.AgentRoots, []string{custom}) || !c.RemoteControl {
		t.Errorf("agentRoots %v", c.AgentRoots)
	}
	if err := e.flow.SetRemoteAccess(true, filepath.Join(e.home, "missing")); err == nil {
		t.Error("missing root accepted")
	}
	if err := e.flow.SetRemoteAccess(false, ""); err != nil || len(e.flow.Checklist()) != 3 {
		t.Errorf("declining should be a no-op: %v %v", err, e.flow.Checklist())
	}
	if err := e.flow.SetSyncScope(false, []string{"NotAFolder"}); err == nil {
		t.Error("unknown folder accepted")
	}
}

func TestLoginCancelAndBeginErrors(t *testing.T) {
	e := newEnv(t)
	if _, err := e.flow.AwaitLogin(context.Background()); err == nil {
		t.Error("await without begin should fail")
	}
	if _, err := e.flow.BeginLogin(context.Background()); err != nil {
		t.Fatal(err)
	}
	go func() { time.Sleep(20 * time.Millisecond); e.flow.CancelLogin() }()
	if _, err := e.flow.AwaitLogin(context.Background()); err == nil {
		t.Error("cancelled login succeeded")
	}
	// A new login can start right away on the same port.
	if _, err := e.flow.BeginLogin(context.Background()); err != nil {
		t.Errorf("begin after cancel: %v", err)
	}
	e.flow.CancelLogin()
}
