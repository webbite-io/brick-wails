package desktopentry

import (
	"path/filepath"
	"strings"
	"testing"
)

// The login entry is the application entry with the autostart keys added
// and anything that would hide or disable it dropped.
func TestEnableAutostartCopiesEntry(t *testing.T) {
	root := t.TempDir()
	entry := filepath.Join(root, "applications", "brick-ui.desktop")
	write(t, entry, "[Desktop Entry]\nName=Webbite Brick\nExec=/home/u/.local/bin/brick-ui\nNoDisplay=true\n"+
		generatedKey+"=true\nStartupWMClass="+wmClass+"\n\n[Desktop Action x]\nNoDisplay=true\n")
	path := AutostartPath(filepath.Join(root, "config"), "brick-ui")

	if AutostartEnabled(path) {
		t.Fatal("enabled before the entry was written")
	}
	if !AutostartPreference(path) {
		t.Fatal("a fresh install should offer it as on")
	}
	if err := EnableAutostart(entry, path); err != nil {
		t.Fatal(err)
	}
	if !AutostartEnabled(path) {
		t.Fatal("not enabled after EnableAutostart")
	}

	got := read(t, path)
	want := "[Desktop Entry]\nName=Webbite Brick\nExec=/home/u/.local/bin/brick-ui\nStartupWMClass=" + wmClass + "\n" +
		"X-GNOME-Autostart-enabled=true\nX-GNOME-Autostart-Delay=5\nX-KDE-autostart-after=panel\n\n" +
		"[Desktop Action x]\nNoDisplay=true\n"
	if got != want {
		t.Fatalf("autostart entry:\n%s\nwant:\n%s", got, want)
	}
}

// Settings panels that switch an entry off rather than deleting it leave it
// disabled, and re-enabling rewrites it. Disabling leaves an entry behind,
// so a no is remembered.
func TestAutostartSwitchedOff(t *testing.T) {
	root := t.TempDir()
	path := AutostartPath(root, "brick-ui")
	for _, off := range []string{"X-GNOME-Autostart-enabled=false", "Hidden=true"} {
		write(t, path, "[Desktop Entry]\nExec=brick-ui\n"+off+"\n")
		if AutostartEnabled(path) {
			t.Errorf("%s: reported enabled", off)
		}
	}

	entry := filepath.Join(root, "brick-ui.desktop")
	write(t, entry, "[Desktop Entry]\nExec=brick-ui\n")
	if err := EnableAutostart(entry, path); err != nil {
		t.Fatal(err)
	}
	if got := read(t, path); strings.Contains(got, "Hidden") || !AutostartEnabled(path) {
		t.Fatalf("not re-enabled:\n%s", got)
	}

	if err := DisableAutostart(entry, path); err != nil {
		t.Fatal(err)
	}
	if AutostartEnabled(path) || AutostartPreference(path) {
		t.Fatalf("enabled after DisableAutostart:\n%s", read(t, path))
	}
	got := read(t, path)
	if !strings.Contains(got, "Hidden=true\n") || !strings.Contains(got, "X-GNOME-Autostart-enabled=false\n") {
		t.Fatalf("disabled entry:\n%s", got)
	}
	if err := DisableAutostart(entry, path); err != nil {
		t.Fatalf("disabling twice: %v", err)
	}
}
