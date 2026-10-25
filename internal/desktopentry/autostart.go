package desktopentry

import (
	"os"
	"path/filepath"
	"strings"
)

// autostartKeys are set on the copy of the entry that starts the app at
// login (X-GNOME-Autostart-enabled is flipped to false on one that's off). A
// tray app has nothing to attach to until the panel is up, so it
// asks to wait for it: GNOME (whose tray is an extension) honours the delay,
// Plasma 5 the after-panel phase, and Plasma 6 starts autostart entries once
// the shell is up anyway. X-GNOME-Autostart-enabled is the switch GNOME's
// Startup Applications flips; systemd's XDG autostart generator, which Plasma
// 6 runs these entries through, honours it too.
var autostartKeys = [][2]string{
	{"X-GNOME-Autostart-enabled", "true"},
	{"X-GNOME-Autostart-Delay", "5"},
	{"X-KDE-autostart-after", "panel"},
}

// autostartDropKeys don't carry over from the application entry: NoDisplay
// would hide the copy from the desktop's own list of startup apps, where
// users expect to find and turn it off, and Hidden disables it outright —
// which only DisableAutostart means to do.
var autostartDropKeys = []string{"NoDisplay", "Hidden", generatedKey}

// AutostartPath returns where the login entry for id lives, given
// $XDG_CONFIG_HOME (~/.config by default).
func AutostartPath(configHome, id string) string {
	return filepath.Join(configHome, "autostart", id+".desktop")
}

// AutostartEnabled reports whether the entry at path starts the app at
// login: it exists and hasn't been switched off by Hidden or by GNOME's
// X-GNOME-Autostart-enabled, which some settings panels set rather than
// deleting the file.
func AutostartEnabled(path string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	if v, ok := getKey(data, "Hidden"); ok && v == "true" {
		return false
	}
	if v, ok := getKey(data, "X-GNOME-Autostart-enabled"); ok && v == "false" {
		return false
	}
	return true
}

// AutostartPreference reports whether starting at login should be offered
// as on: what the entry at path says, or on when there's no entry, as on a
// fresh install — turning it off leaves a disabled entry behind, so a missing
// one means nobody has chosen yet.
func AutostartPreference(path string) bool {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return true
	}
	return AutostartEnabled(path)
}

// EnableAutostart copies the application entry at entryPath to path, with
// the autostart keys set, so the desktop runs it at login.
func EnableAutostart(entryPath, path string) error {
	return writeAutostart(entryPath, path, true)
}

// DisableAutostart writes the same copy as EnableAutostart switched off with
// Hidden=true, the XDG autostart spec's way of disabling an entry, rather
// than removing it: the entry left behind records that the user said no (see
// AutostartPreference).
func DisableAutostart(entryPath, path string) error {
	return writeAutostart(entryPath, path, false)
}

func writeAutostart(entryPath, path string, on bool) error {
	data, err := os.ReadFile(entryPath)
	if err != nil {
		return err
	}
	for _, key := range autostartDropKeys {
		data = removeKey(data, key)
	}
	for _, kv := range autostartKeys {
		data, _ = setKey(data, kv[0], kv[1])
	}
	if !on {
		data, _ = setKey(data, "X-GNOME-Autostart-enabled", "false")
		data, _ = setKey(data, "Hidden", "true")
	}
	return writeFile(path, data)
}

// removeKey drops key from the [Desktop Entry] group, keeping every other
// line as it was.
func removeKey(data []byte, key string) []byte {
	if _, ok := getKey(data, key); !ok {
		return data
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	out := make([]string, 0, len(lines))
	group := ""
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "[") {
			group = trimmed
		} else if group == "[Desktop Entry]" {
			if k, _, ok := strings.Cut(trimmed, "="); ok && strings.TrimSpace(k) == key {
				continue
			}
		}
		out = append(out, line)
	}
	return []byte(strings.Join(out, "\n") + "\n")
}
