package desktopentry

import (
	"os"
	"path/filepath"
	"strings"
)

// autostartKeys are set on the copy of the entry that starts the app at
// login. A tray app has nothing to attach to until the panel is up, so it
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
// users expect to find and turn it off, and Hidden would disable it outright.
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

// EnableAutostart copies the application entry at entryPath to path, with
// the autostart keys set, so the desktop runs it at login.
func EnableAutostart(entryPath, path string) error {
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
	return writeFile(path, data)
}

// DisableAutostart removes the login entry at path; one already gone is
// fine.
func DisableAutostart(path string) error {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
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
