package desktopentry

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const wmClass = "org.wails.webbite_brick"

func testOptions(t *testing.T) Options {
	t.Helper()
	root := t.TempDir()
	return Options{
		DataHome: filepath.Join(root, "home"),
		DataDirs: []string{filepath.Join(root, "usr-local"), filepath.Join(root, "usr")},
		ID:       "brick-ui",
		WMClass:  wmClass,
		Name:     "Webbite Brick",
		Comment:  "Sync",
		Exec:     "/opt/brick/brick-ui",
		Icon:     []byte("png"),
		StateDir: filepath.Join(root, "home", "brick-ui"),
	}
}

func userPath(o Options) string {
	return filepath.Join(o.DataHome, "applications", o.ID+".desktop")
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func ensure(t *testing.T, o Options) (string, Action) {
	t.Helper()
	path, action, err := Ensure(o)
	if err != nil {
		t.Fatal(err)
	}
	return path, action
}

// With no entry anywhere — an AppImage run from where it was built, a dev
// build — a hidden entry is written, with the icon beside the app's state.
func TestCreatesHiddenEntryWhenNoneExists(t *testing.T) {
	o := testOptions(t)
	path, action := ensure(t, o)
	if action != Created || path != userPath(o) {
		t.Fatalf("got %v at %s, want created at %s", action, path, userPath(o))
	}
	got := read(t, path)
	iconPath := filepath.Join(o.StateDir, "brick-ui.png")
	for _, want := range []string{
		"Name=Webbite Brick\n",
		"Exec=/opt/brick/brick-ui\n",
		"Icon=" + iconPath + "\n",
		"NoDisplay=true\n",
		"StartupWMClass=" + wmClass + "\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("entry lacks %q:\n%s", want, got)
		}
	}
	if read(t, iconPath) != "png" {
		t.Errorf("icon not written")
	}

	// A second start finds it as it should be.
	if _, action := ensure(t, o); action != Unchanged {
		t.Errorf("second run: got %v, want unchanged", action)
	}
	// A moved binary refreshes it.
	o.Exec = "/elsewhere/brick-ui"
	if _, action := ensure(t, o); action != Created {
		t.Errorf("after move: got %v, want created", action)
	}
	if !strings.Contains(read(t, path), "Exec=/elsewhere/brick-ui\n") {
		t.Errorf("Exec not updated:\n%s", read(t, path))
	}
}

// An entry install.sh wrote with the right class is left exactly as it is.
func TestLeavesCorrectInstalledEntryAlone(t *testing.T) {
	o := testOptions(t)
	entry := "[Desktop Entry]\nName=Webbite Brick\nExec=/home/u/.local/bin/brick-ui\nStartupWMClass=" + wmClass + "\n"
	write(t, userPath(o), entry)

	if _, action := ensure(t, o); action != Unchanged {
		t.Errorf("got %v, want unchanged", action)
	}
	if got := read(t, userPath(o)); got != entry {
		t.Errorf("entry changed:\n%s", got)
	}
}

// An install from before the fix carries StartupWMClass=brick-ui: only that
// line changes, in place, and everything else — other groups included —
// stays.
func TestFixesStaleClassInInstalledEntry(t *testing.T) {
	o := testOptions(t)
	write(t, userPath(o), "[Desktop Entry]\nName=Webbite Brick\nStartupWMClass=brick-ui\nX-AppImage-Version=0.1.0\n\n[Desktop Action quit]\nName=Quit\nStartupWMClass=other\n")

	path, action := ensure(t, o)
	if action != Fixed || path != userPath(o) {
		t.Fatalf("got %v at %s, want fixed at %s", action, path, userPath(o))
	}
	want := "[Desktop Entry]\nName=Webbite Brick\nStartupWMClass=" + wmClass + "\nX-AppImage-Version=0.1.0\n\n[Desktop Action quit]\nName=Quit\nStartupWMClass=other\n"
	if got := read(t, path); got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

// A missing StartupWMClass is added at the end of [Desktop Entry], not after
// a later group.
func TestAddsMissingClassToDesktopEntryGroup(t *testing.T) {
	o := testOptions(t)
	write(t, userPath(o), "[Desktop Entry]\nName=Webbite Brick\n\n[Desktop Action quit]\nName=Quit\n")

	ensure(t, o)
	want := "[Desktop Entry]\nName=Webbite Brick\nStartupWMClass=" + wmClass + "\n\n[Desktop Action quit]\nName=Quit\n"
	if got := read(t, userPath(o)); got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

// A package's correct system entry needs nothing, and writes nothing.
func TestLeavesCorrectSystemEntryAlone(t *testing.T) {
	o := testOptions(t)
	sys := filepath.Join(o.DataDirs[1], "applications", "brick-ui.desktop")
	write(t, sys, "[Desktop Entry]\nName=Webbite Brick\nStartupWMClass="+wmClass+"\n")

	path, action := ensure(t, o)
	if action != Unchanged || path != sys {
		t.Errorf("got %v at %s, want unchanged at %s", action, path, sys)
	}
	if _, err := os.Stat(userPath(o)); !os.IsNotExist(err) {
		t.Errorf("user entry written: %v", err)
	}
}

// A system entry can't be written, so a stale one is copied into the user's
// data dir, which shadows it.
func TestShadowsStaleSystemEntry(t *testing.T) {
	o := testOptions(t)
	sys := filepath.Join(o.DataDirs[0], "applications", "brick-ui.desktop")
	write(t, sys, "[Desktop Entry]\nName=Webbite Brick\nExec=brick-ui\n")

	path, action := ensure(t, o)
	if action != Fixed || path != userPath(o) {
		t.Fatalf("got %v at %s, want fixed at %s", action, path, userPath(o))
	}
	want := "[Desktop Entry]\nName=Webbite Brick\nExec=brick-ui\nStartupWMClass=" + wmClass + "\n"
	if got := read(t, path); got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

// A hidden entry from an earlier run would hide a package installed since
// (same id, and the user's wins), so it makes way for the package's.
func TestHiddenEntryMakesWayForSystemEntry(t *testing.T) {
	o := testOptions(t)
	ensure(t, o)
	sys := filepath.Join(o.DataDirs[1], "applications", "brick-ui.desktop")
	write(t, sys, "[Desktop Entry]\nName=Webbite Brick\nStartupWMClass="+wmClass+"\n")

	path, action := ensure(t, o)
	if action != Fixed || path != sys {
		t.Errorf("got %v at %s, want fixed at %s", action, path, sys)
	}
	if _, err := os.Stat(userPath(o)); !os.IsNotExist(err) {
		t.Errorf("hidden user entry still there: %v", err)
	}
}

func TestQuoteExec(t *testing.T) {
	for in, want := range map[string]string{
		"/opt/brick/brick-ui":      "/opt/brick/brick-ui",
		"/home/u/My Apps/brick-ui": `"/home/u/My Apps/brick-ui"`,
		"/a/100%/brick-ui":         "/a/100%%/brick-ui",
		`/a/$x "y"/b`:              `"/a/\\$x \\"y\\"/b"`,
		`/a/back\slash/b`:          `"/a/back\\\\slash/b"`,
	} {
		if got := quoteExec(in); got != want {
			t.Errorf("quoteExec(%q) = %s, want %s", in, got, want)
		}
	}
}
