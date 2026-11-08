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

func TestNewRoot(t *testing.T) {
	store := brickcfg.NewStoreAt(filepath.Join(t.TempDir(), "config.yaml"))
	applied := 0
	m := newRemoteControlMenu(store, log.New(io.Discard, "", 0), func() error { applied++; return nil })
	m.relayout = func() {}
	ro, rw := t.TempDir(), t.TempDir()

	if err := m.newRoot(ro, true); err != nil {
		t.Fatal(err)
	}
	if err := m.newRoot(rw, false); err != nil {
		t.Fatal(err)
	}
	c, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	// Read-only keeps the plain-path form; read/write is saved explicitly.
	off := false
	if !reflect.DeepEqual(c.AgentRoots, []brickcfg.AgentRoot{{Path: ro}, {Path: rw, ReadOnly: &off}}) {
		t.Errorf("agentRoots %+v", c.AgentRoots)
	}
	if applied != 2 || len(m.roots) != 2 {
		t.Errorf("applied %d, tray roots %d", applied, len(m.roots))
	}
	// Sharing a folder again is a no-op that succeeds, closing the window.
	if err := m.newRoot(ro, false); err != nil {
		t.Errorf("sharing a folder twice: %v", err)
	}
	if c, _ := store.Load(); len(c.AgentRoots) != 2 || c.AgentRoots[0].ReadOnly != nil {
		t.Errorf("sharing a folder twice changed agentRoots: %+v", c.AgentRoots)
	}
	if err := m.newRoot(filepath.Join(ro, "missing"), true); err == nil {
		t.Error("a missing folder was shared")
	}
	if applied != 2 {
		t.Errorf("a share that changed nothing reloaded the agent")
	}
}
