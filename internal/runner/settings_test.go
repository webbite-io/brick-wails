//go:build unix

package runner

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/webbite-io/brick-wails/internal/brickcfg"
)

// A remote control settings change is saved to config.yaml and applied to
// the running sync: a folder no longer excluded is downloaded.
func TestExcludeDirsSettingsSavesAndApplies(t *testing.T) {
	f := newFixture(t, true)
	f.store.Update(func(c *brickcfg.Config) error {
		c.EnsureActiveAccount().ExcludeDirs = []string{"Photos"}
		return nil
	})
	f.fs.PutFile("Photos/a.jpg", "jpeg")
	f.fs.PutFile("hello.txt", "hi")
	if err := f.r.Start(StartParams{FirstSync: true, ConflictMode: "device"}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "first pass", func() bool {
		_, err := os.Stat(filepath.Join(f.folder, "hello.txt"))
		return err == nil
	})
	if _, err := os.Stat(filepath.Join(f.folder, "Photos")); err == nil {
		t.Fatal("excluded folder was downloaded")
	}

	f.r.mu.Lock()
	s := &excludeDirsSettings{store: f.store, accountID: "acct-1", eng: f.r.eng, logf: f.r.logf}
	f.r.mu.Unlock()
	if got := s.ExcludeDirs(); !slices.Equal(got, []string{"Photos"}) {
		t.Errorf("ExcludeDirs() = %v, want [Photos]", got)
	}
	if err := s.SetExcludeDirs([]string{"Music"}); err != nil {
		t.Fatal(err)
	}
	cfg, _ := f.store.Load()
	if got := cfg.ActiveAccount().ExcludeDirs; !slices.Equal(got, []string{"Music"}) {
		t.Errorf("config excludeDirs = %v, want [Music]", got)
	}
	waitFor(t, "the un-excluded folder to download", func() bool {
		b, err := os.ReadFile(filepath.Join(f.folder, "Photos", "a.jpg"))
		return err == nil && string(b) == "jpeg"
	})
	if !f.fs.Exists("Photos/a.jpg") {
		t.Error("the un-excluded folder was trashed on the server")
	}
}
