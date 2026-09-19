// Package testutil holds shared helpers for brick-wails tests.
package testutil

import (
	"path/filepath"
	"testing"

	"github.com/webbite-io/brick-wails/internal/auth"
	"github.com/webbite-io/brick-wails/internal/brickcfg"
	"github.com/webbite-io/brick-wails/internal/storage"
	"github.com/webbite-io/brick-wails/internal/testutil/fakestorage"
)

// IsolateHome points HOME/USERPROFILE/APPDATA/XDG_RUNTIME_DIR/LOCALAPPDATA and
// BRICK_CONFIG_DIR at temp dirs so a test can never touch the real brick
// config. Returns the fake home and config dir.
func IsolateHome(t testing.TB) (home, configDir string) {
	t.Helper()
	home = t.TempDir()
	configDir = filepath.Join(home, ".config", "brick")
	for _, k := range []string{"HOME", "USERPROFILE", "APPDATA", "LOCALAPPDATA", "XDG_RUNTIME_DIR"} {
		t.Setenv(k, home)
	}
	t.Setenv(brickcfg.ConfigDirEnv, configDir)
	return home, configDir
}

// NewAuthClient returns an auth.Client presenting a static access token, backed
// by a throwaway config store.
func NewAuthClient(t testing.TB, access string) *auth.Client {
	t.Helper()
	store := brickcfg.NewStoreAt(filepath.Join(t.TempDir(), "config.yaml"))
	if _, err := store.Update(func(c *brickcfg.Config) error { c.AccessToken = access; return nil }); err != nil {
		t.Fatal(err)
	}
	ts, err := auth.NewTokenSource(store, "http://127.0.0.1:1", "test-client")
	if err != nil {
		t.Fatal(err)
	}
	return auth.NewClient(ts)
}

// NewStorage starts a fakestorage server for "acct-1" and a client for it.
func NewStorage(t testing.TB) (*storage.Client, *fakestorage.Server) {
	t.Helper()
	fs := fakestorage.New("acct-1")
	t.Cleanup(fs.Close)
	return &storage.Client{BaseURL: fs.URL, AccountID: "acct-1", Auth: NewAuthClient(t, "test-token")}, fs
}
