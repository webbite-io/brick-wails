package brickcfg

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/google/uuid"
	"gopkg.in/yaml.v3"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	return NewStoreAt(filepath.Join(t.TempDir(), "nested", "config.yaml"))
}

func TestLoadOrCreateCreatesFileWithClientID(t *testing.T) {
	s := newTestStore(t)
	cfg, created, err := s.LoadOrCreate()
	if err != nil {
		t.Fatal(err)
	}
	if !created {
		t.Error("created = false for a fresh store")
	}
	if _, err := uuid.Parse(cfg.ClientID); err != nil {
		t.Errorf("clientId %q is not a UUID: %v", cfg.ClientID, err)
	}
	info, err := os.Stat(s.Path())
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want 0600", info.Mode().Perm())
	}

	again, created, err := s.LoadOrCreate()
	if err != nil {
		t.Fatal(err)
	}
	if created || again.ClientID != cfg.ClientID {
		t.Errorf("second load: created=%v clientId=%q, want false/%q", created, again.ClientID, cfg.ClientID)
	}
}

func TestLoadOrCreateBackfillsClientID(t *testing.T) {
	s := newTestStore(t)
	if err := os.MkdirAll(s.Dir(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(s.Path(), []byte("activeAccountId: acct\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, _, err := s.LoadOrCreate()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ClientID == "" || cfg.ActiveAccountID != "acct" {
		t.Fatalf("got %+v", cfg)
	}
	onDisk, _ := s.Load()
	if onDisk.ClientID != cfg.ClientID {
		t.Error("backfilled clientId not persisted")
	}
}

func TestLoadMissingReturnsErrNotExist(t *testing.T) {
	if _, err := newTestStore(t).Load(); err != ErrNotExist {
		t.Fatalf("err = %v, want ErrNotExist", err)
	}
}

// A field only brick-cli knows about (top-level or per-account) must survive
// this app loading and saving the config.
func TestUnknownKeysSurviveRoundTrip(t *testing.T) {
	s := newTestStore(t)
	if err := os.MkdirAll(s.Dir(), 0o755); err != nil {
		t.Fatal(err)
	}
	in := `clientId: c1
activeAccountId: a1
futureTopLevel:
  nested: true
accounts:
  a1:
    storageSyncFolder: /tmp/Brick
    futureAccountField: 42
`
	if err := os.WriteFile(s.Path(), []byte(in), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Update(func(c *Config) error {
		c.AccessToken = "tok"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(s.Path())
	var raw map[string]any
	if err := yaml.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	if _, ok := raw["futureTopLevel"]; !ok {
		t.Errorf("futureTopLevel dropped:\n%s", data)
	}
	acct := raw["accounts"].(map[string]any)["a1"].(map[string]any)
	if acct["futureAccountField"] != 42 {
		t.Errorf("futureAccountField dropped:\n%s", data)
	}
	if raw["accessToken"] != "tok" || acct["storageSyncFolder"] != "/tmp/Brick" {
		t.Errorf("known fields wrong:\n%s", data)
	}
}

// Update must apply to what is on disk *now*, not to a copy loaded earlier —
// otherwise a concurrent brick-cli edit to another field would be clobbered.
func TestUpdateIsReadModifyWrite(t *testing.T) {
	s := newTestStore(t)
	if _, _, err := s.LoadOrCreate(); err != nil {
		t.Fatal(err)
	}
	stale, _ := s.Load()

	// Simulate brick-cli rewriting the file behind our back.
	external := *stale
	external.RefreshToken = "from-cli"
	data, _ := yaml.Marshal(&external)
	if err := os.WriteFile(s.Path(), data, 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := s.Update(func(c *Config) error {
		c.ActiveAccountID = "from-gui"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Load()
	if got.RefreshToken != "from-cli" || got.ActiveAccountID != "from-gui" {
		t.Fatalf("got refresh=%q active=%q; want both edits kept", got.RefreshToken, got.ActiveAccountID)
	}
}

func TestSchemaMatchesCLIKeys(t *testing.T) {
	c := &Config{
		ClientID: "c", AccessToken: "a", RefreshToken: "r", IDToken: "i",
		ActiveAccountID: "acct", RemoteControl: true, AgentRoots: []string{"/x"},
		Accounts: map[string]*AccountConfig{"acct": {StorageSyncFolder: "/f", ExcludeDirs: []string{"d"}}},
	}
	out, err := yaml.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"clientId:", "accessToken:", "refreshToken:", "idToken:", "activeAccountId:", "accounts:", "agentRoots:", "remoteControl:", "storageSyncFolder:", "excludeDirs:"} {
		if !strings.Contains(string(out), key) {
			t.Errorf("marshalled config missing %s\n%s", key, out)
		}
	}
}

func TestEnsureActiveAccount(t *testing.T) {
	c := &Config{}
	if c.EnsureActiveAccount() != nil {
		t.Error("EnsureActiveAccount with no active account should be nil")
	}
	c.ActiveAccountID = "a"
	ac := c.EnsureActiveAccount()
	if ac == nil || c.Accounts["a"] != ac || c.ActiveAccount() != ac {
		t.Fatal("EnsureActiveAccount did not create/return the entry")
	}
}

func TestDirOverrideAndDefaults(t *testing.T) {
	t.Setenv(ConfigDirEnv, "/custom/dir")
	if d, _ := Dir(); d != "/custom/dir" {
		t.Errorf("Dir() = %q with override", d)
	}
	t.Setenv(ConfigDirEnv, "")
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	if d, _ := defaultDir("linux"); d != filepath.Join(home, ".config", "brick") {
		t.Errorf("linux dir = %q", d)
	}
	if d, _ := defaultDir("darwin"); d != filepath.Join(home, ".config", "brick") {
		t.Errorf("darwin dir = %q (CLI uses ~/.config on macOS too)", d)
	}
	// Windows uses os.UserConfigDir (%AppData%), never ~/.config.
	if d, err := defaultDir("windows"); err == nil && strings.Contains(d, ".config"+string(filepath.Separator)+"brick") && runtime.GOOS == "windows" {
		t.Errorf("windows dir = %q", d)
	}
}

func TestResolveEnvPrecedence(t *testing.T) {
	for _, k := range []string{"ACC_API_URL", "STORAGE_API_URL", "OAUTH_CLIENT_ID", "OAUTH_SCOPES", "OAUTH_CALLBACK_URL", "STORAGE_WEB_URL", "STORAGE_HELP_URL", "DEBUG"} {
		t.Setenv(k, "")
	}
	env := ResolveEnv(Defaults{})
	if env.APIURL != FallbackAPIURL || env.StorageAPIURL != FallbackStorageAPIURL || env.OAuthScopes != FallbackOAuthScopes || env.OAuthCallbackURL != FallbackOAuthCallbackURL || env.WebURL != FallbackWebURL {
		t.Errorf("fallbacks wrong: %+v", env)
	}
	env = ResolveEnv(Defaults{APIURL: "https://acc", OAuthClientID: "baked"})
	if env.APIURL != "https://acc" || env.OAuthClientID != "baked" {
		t.Errorf("defaults not applied: %+v", env)
	}
	t.Setenv("ACC_API_URL", "https://env")
	t.Setenv("DEBUG", "TRUE")
	env = ResolveEnv(Defaults{APIURL: "https://acc"})
	if env.APIURL != "https://env" || !env.Debug {
		t.Errorf("env should win: %+v", env)
	}
	if ShouldLoadDevEnv(Defaults{APIURL: "x"}) || !ShouldLoadDevEnv(Defaults{}) {
		t.Error("ShouldLoadDevEnv wrong")
	}
}

func TestDisplayPathAndExpandHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	if got := DisplayPath(filepath.Join(home, "Brick")); got != filepath.Join("~", "Brick") {
		t.Errorf("DisplayPath = %q", got)
	}
	if got := DisplayPath("/elsewhere"); got != "/elsewhere" {
		t.Errorf("DisplayPath outside home = %q", got)
	}
	if got := ExpandHome("~/Brick"); got != filepath.Join(home, "Brick") {
		t.Errorf("ExpandHome = %q", got)
	}
}

func TestIsolated(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv(ConfigDirEnv, "")
	if Isolated() {
		t.Error("no override: not isolated")
	}
	def, _ := defaultDir(runtime.GOOS)
	t.Setenv(ConfigDirEnv, def+string(filepath.Separator))
	if Isolated() {
		t.Error("override equal to the CLI's dir: shared, not isolated")
	}
	t.Setenv(ConfigDirEnv, filepath.Join(home, "elsewhere"))
	if !Isolated() {
		t.Error("override elsewhere: isolated")
	}
}
