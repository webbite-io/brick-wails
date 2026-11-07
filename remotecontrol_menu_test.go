//go:build !windows

package main

import (
	"io"
	"log"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/webbite-io/brick-wails/internal/brickcfg"
)

func TestRootLabel(t *testing.T) {
	t.Setenv("HOME", "/home/blund")
	for _, tc := range []struct {
		path string
		keep int
		want string
	}{
		{"/home/blund/Documents", 20, "~/Documents"},
		{"~/Documents", 20, "~/Documents"},
		// The home folder itself is written out rather than a bare ~.
		{"/home/blund", 20, "/home/blund"},
		{"~", 20, "/home/blund"},
		{"/home/blund", 7, ".../blund"},
		{"/srv/a/b/c/d/e/f/g/h", 20, "/srv/a/b/c/d/e/f/g/h"},
		// Up to keep plus the ellipsis's width is shown whole.
		{"/srv/a/b/c/d/e/f/g/h/i/", 20, "/srv/a/b/c/d/e/f/g/h/i/"},
		{"/srv/a/b/c/d/e/f/g/h/i/j", 20, ".../a/b/c/d/e/f/g/h/i/j"},
		{"/very/long/path/to/some/blund", 10, ".../blund"},
		{"/very/long/path/to/some/blund", 11, ".../some/blund"},
		{"/a/averyveryverylongfoldername", 8, "...ldername"},
	} {
		got := rootLabel(tc.path, tc.keep)
		if got != tc.want {
			t.Errorf("rootLabel(%q, %d) = %q, want %q", tc.path, tc.keep, got, tc.want)
		}
		if n := len([]rune(got)); n > tc.keep+3 {
			t.Errorf("rootLabel(%q, %d) is %d characters", tc.path, tc.keep, n)
		}
	}
}

func TestRemoveRoot(t *testing.T) {
	store := brickcfg.NewStoreAt(filepath.Join(t.TempDir(), "config.yaml"))
	rw := false
	if _, err := store.Update(func(c *brickcfg.Config) error {
		c.RemoteControl = true
		c.AgentRoots = []brickcfg.AgentRoot{{Path: "/srv/a"}, {Path: "/srv/b", ReadOnly: &rw}, {Path: "/srv/a"}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	applied := 0
	m := newRemoteControlMenu(store, log.New(io.Discard, "", 0), func() error { applied++; return nil })
	relaid := 0
	m.relayout = func() { relaid++ }

	if err := m.removeRoot("/srv/a"); err != nil {
		t.Fatal(err)
	}
	c, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	// Every entry for the path goes; remote control itself stays on.
	if !reflect.DeepEqual(c.AgentRoots, []brickcfg.AgentRoot{{Path: "/srv/b", ReadOnly: &rw}}) || !c.RemoteControl {
		t.Errorf("config after removal: %v %+v", c.RemoteControl, c.AgentRoots)
	}
	if applied != 1 || relaid != 1 || len(m.roots) != 1 || m.roots[0].path != "/srv/b" {
		t.Errorf("applied %d, relaid out %d, tray roots %d", applied, relaid, len(m.roots))
	}
	if err := m.removeRoot("/srv/a"); err == nil {
		t.Error("removing a root that is already gone should say so")
	}
	if applied != 1 {
		t.Errorf("a no-op removal reloaded the agent")
	}
}
