//go:build !windows

package main

import "testing"

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
