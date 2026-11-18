//go:build unix

package runner

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/webbite-io/brick-wails/internal/brickcfg"
	"github.com/webbite-io/brick-wails/internal/lock"
)

func (f *fixture) excludeDirs() []string {
	f.t.Helper()
	cfg, err := f.store.Load()
	if err != nil {
		f.t.Fatal(err)
	}
	return cfg.ActiveAccount().ExcludeDirs
}

func (f *fixture) setExcludeDirs(dirs ...string) {
	f.store.Update(func(c *brickcfg.Config) error {
		c.EnsureActiveAccount().ExcludeDirs = dirs
		return nil
	})
}

func (f *fixture) localExists(rel string) bool {
	_, err := os.Stat(filepath.Join(f.folder, filepath.FromSlash(rel)))
	return err == nil
}

// The window lists the top-level folders and ticks every one not excluded.
func TestSelectiveSyncFoldersTicksTheNonExcluded(t *testing.T) {
	f := newFixture(t, true)
	f.fs.PutFile("Music/a.mp3", "a")
	f.fs.PutFile("Work/b.txt", "b")
	f.fs.PutFile("loose.txt", "c")
	f.setExcludeDirs("/Music/", "Camera Uploads")

	got, err := f.r.SelectiveSyncFolders(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got.Folders, []string{"Music", "Work"}) || !slices.Equal(got.Synced, []string{"Work"}) {
		t.Errorf("got %+v", got)
	}
}

// Saving while syncing applies at once, without a restart: the unticked
// folder's local copy is removed and it stays on the server, and exclusions
// that weren't listed (Camera Uploads before it exists) survive.
func TestSetSyncedFoldersWhileSyncing(t *testing.T) {
	f := newFixture(t, true)
	f.fs.PutFile("Music/a.mp3", "a")
	f.fs.PutFile("Work/b.txt", "b")
	f.setExcludeDirs("Camera Uploads")
	if err := f.r.Start(StartParams{FirstSync: true, ConflictMode: "device"}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "first pass", func() bool { return f.localExists("Music/a.mp3") && f.localExists("Work/b.txt") })

	if err := f.r.SetSyncedFolders([]string{"Music", "Work"}, []string{"Work"}); err != nil {
		t.Fatal(err)
	}
	if got, want := f.excludeDirs(), []string{"Camera Uploads", "Music"}; !slices.Equal(got, want) {
		t.Errorf("excludeDirs = %v, want %v", got, want)
	}
	if f.localExists("Music") {
		t.Error("local copy of the excluded folder not removed")
	}
	// Let a few passes run on the new list: none may trash it remotely, or
	// bring it back locally.
	time.Sleep(200 * time.Millisecond)
	if !f.fs.Exists("Music/a.mp3") {
		t.Error("excluded folder was trashed on the server")
	}
	if f.localExists("Music") {
		t.Error("excluded folder downloaded again")
	}

	// Ticking it again downloads it.
	if err := f.r.SetSyncedFolders([]string{"Music", "Work"}, []string{"Music", "Work"}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the re-ticked folder to download", func() bool { return f.localExists("Music/a.mp3") })
	if got, want := f.excludeDirs(), []string{"Camera Uploads"}; !slices.Equal(got, want) {
		t.Errorf("excludeDirs = %v, want %v", got, want)
	}
}

// Not syncing: saved and removed directly, under the sync lock — refused
// while brick-cli holds it, since it may be syncing the folder.
func TestSetSyncedFoldersWhileStopped(t *testing.T) {
	f := newFixture(t, true)
	if err := os.MkdirAll(filepath.Join(f.folder, "Music"), 0o755); err != nil {
		t.Fatal(err)
	}

	lp, _ := lock.PathIn(f.store.Dir())
	lk, err := lock.Acquire(lp)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.r.SetSyncedFolders([]string{"Music"}, nil); err == nil || !strings.Contains(err.Error(), "CLI") {
		t.Errorf("err = %v, want refusal while the CLI holds the lock", err)
	}
	if len(f.excludeDirs()) != 0 || !f.localExists("Music") {
		t.Error("changed anything while refused")
	}
	lk.Release()

	if err := f.r.SetSyncedFolders([]string{"Music"}, nil); err != nil {
		t.Fatal(err)
	}
	if got := f.excludeDirs(); !slices.Equal(got, []string{"Music"}) {
		t.Errorf("excludeDirs = %v", got)
	}
	if f.localExists("Music") {
		t.Error("local copy not removed")
	}
	if lk, err := lock.Acquire(lp); err != nil {
		t.Errorf("lock not released: %v", err)
	} else {
		lk.Release()
	}
}

func TestSetSyncedFoldersNotConfigured(t *testing.T) {
	f := newFixture(t, false)
	if err := f.r.SetSyncedFolders(nil, nil); !errors.Is(err, ErrNotConfigured) {
		t.Errorf("err = %v", err)
	}
}

func TestSelectiveSyncExcludes(t *testing.T) {
	for _, tc := range []struct {
		name                 string
		prev, listed, synced []string
		wantExcl, wantNewly  []string
	}{
		{"untick one; unlisted entries kept", []string{"Camera Uploads", "Photos/2019"}, []string{"Music", "Work"}, []string{"Work"},
			[]string{"Camera Uploads", "Photos/2019", "Music"}, []string{"Music"}},
		{"re-tick a previously excluded folder", []string{"Music", "Camera Uploads"}, []string{"Music", "Work"}, []string{"Music", "Work"},
			[]string{"Camera Uploads"}, nil},
		{"keep an existing exclusion: not newly excluded", []string{"Music"}, []string{"Music", "Work"}, []string{"Work"},
			[]string{"Music"}, nil},
		{"everything ticked", nil, []string{"Music", "Work"}, []string{"Music", "Work"}, nil, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			excl, newly := SelectiveSyncExcludes(tc.prev, tc.listed, tc.synced)
			if !slices.Equal(excl, tc.wantExcl) || !slices.Equal(newly, tc.wantNewly) {
				t.Errorf("got exclude=%v newly=%v, want exclude=%v newly=%v", excl, newly, tc.wantExcl, tc.wantNewly)
			}
		})
	}
	if got, want := NormalizeExcludeDirs([]string{"/Music/", "", "Music", "Photos/2019/", "/"}), []string{"Music", "Photos/2019"}; !slices.Equal(got, want) {
		t.Errorf("NormalizeExcludeDirs = %v, want %v", got, want)
	}
}
