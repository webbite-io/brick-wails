// Package desktopentry makes sure a freedesktop .desktop entry ties the
// running app's windows to its name and icon.
//
// GNOME (and other shells) only learn an app's name and icon from a
// .desktop entry, matched to a window through the app id — on Wayland the
// window's app_id, which StartupWMClass is compared against. Without a
// matching entry the window switcher shows the bare id
// ("org.wails.webbite_brick") and a generic icon. install.sh and the
// packages write a proper entry, but an AppImage run straight from where it
// was built, a dev build, and an install from before the entry carried the
// right StartupWMClass all lack one. Ensure covers those at startup.
package desktopentry

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// generatedKey marks an entry this package wrote from scratch, as opposed to
// one written by install.sh or a package (or fixed up from one): only those
// are rewritten when, say, the binary has moved.
const generatedKey = "X-Brick-Generated"

// Options describes the app and where to look.
type Options struct {
	// DataHome is $XDG_DATA_HOME (~/.local/share by default): where entries
	// are written.
	DataHome string
	// DataDirs is $XDG_DATA_DIRS: system locations searched after DataHome,
	// as the desktop does, for an entry a package installed.
	DataDirs []string

	// ID is the entry's file name without ".desktop" — the same name
	// install.sh and the packages use, so whichever writes last owns it and
	// --uninstall removes it either way.
	ID string
	// WMClass is the app id the window carries, written as StartupWMClass.
	WMClass string

	// The rest only go into an entry written from scratch.
	Name    string
	Comment string
	// Exec is the path of the binary to run.
	Exec string
	// Icon is a PNG, saved to StateDir and referenced by absolute path, so
	// the entry doesn't depend on icons install.sh may not have written.
	Icon     []byte
	StateDir string
}

// Action says what Ensure did.
type Action int

const (
	// Unchanged means a matching entry was already in place.
	Unchanged Action = iota
	// Fixed means an existing entry was copied or rewritten with the right
	// StartupWMClass, keeping the rest of it.
	Fixed
	// Created means no entry existed, so a hidden one was written.
	Created
)

func (a Action) String() string {
	switch a {
	case Fixed:
		return "fixed"
	case Created:
		return "created"
	default:
		return "unchanged"
	}
}

// Ensure makes sure the entry the desktop resolves for o.ID carries
// StartupWMClass=o.WMClass, and returns the path of that entry and what was
// done to it.
//
//   - An entry with the right StartupWMClass is left alone.
//   - An entry with a missing or different one gets it set; a system entry
//     (which can't be written) is copied into DataHome, which shadows it.
//   - With no entry at all, one is written with NoDisplay=true: it names the
//     window in the switcher without adding a launcher for what may well be
//     a dev build to the app grid. An installed entry later replaces it, and
//     it is removed once a package installs a system entry, which it would
//     otherwise hide.
func Ensure(o Options) (string, Action, error) {
	userPath := filepath.Join(o.DataHome, "applications", o.ID+".desktop")
	user, err := readIfExists(userPath)
	if err != nil {
		return "", Unchanged, err
	}
	if user != nil && !isGenerated(user) {
		return fix(userPath, userPath, user, o.WMClass)
	}

	sysPath, sys, err := findSystem(o)
	if err != nil {
		return "", Unchanged, err
	}
	if sys == nil {
		action, err := writeGenerated(o, userPath, user)
		return userPath, action, err
	}
	if user != nil {
		if err := os.Remove(userPath); err != nil {
			return "", Unchanged, err
		}
	}
	path, action, err := fix(sysPath, userPath, sys, o.WMClass)
	if err == nil && user != nil && action == Unchanged {
		action = Fixed
	}
	return path, action, err
}

// fix returns the entry at path as is when it already carries wmClass, and
// otherwise writes it with StartupWMClass set to userPath (path itself, for
// a user entry).
func fix(path, userPath string, data []byte, wmClass string) (string, Action, error) {
	fixed, changed := setKey(data, "StartupWMClass", wmClass)
	if !changed {
		return path, Unchanged, nil
	}
	if err := writeFile(userPath, fixed); err != nil {
		return "", Unchanged, err
	}
	return userPath, Fixed, nil
}

// findSystem returns the first entry for o.ID in DataDirs, in the order the
// desktop searches them, or nil data when there is none.
func findSystem(o Options) (string, []byte, error) {
	for _, dir := range o.DataDirs {
		if dir == "" {
			continue
		}
		path := filepath.Join(dir, "applications", o.ID+".desktop")
		data, err := readIfExists(path)
		if err != nil {
			return "", nil, err
		}
		if data != nil {
			return path, data, nil
		}
	}
	return "", nil, nil
}

func readIfExists(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	return data, err
}

// writeGenerated writes (or refreshes) the hidden entry for o, along with
// its icon. old is the entry's current content, if any; nothing is written
// when it already says the same.
func writeGenerated(o Options, path string, old []byte) (Action, error) {
	iconPath := filepath.Join(o.StateDir, o.ID+".png")
	if err := os.MkdirAll(o.StateDir, 0o755); err != nil {
		return Unchanged, err
	}
	if cur, err := os.ReadFile(iconPath); err != nil || !bytes.Equal(cur, o.Icon) {
		if err := writeFile(iconPath, o.Icon); err != nil {
			return Unchanged, err
		}
	}

	var b strings.Builder
	b.WriteString("[Desktop Entry]\n")
	b.WriteString("Type=Application\n")
	b.WriteString("Version=1.0\n")
	fmt.Fprintf(&b, "Name=%s\n", o.Name)
	if o.Comment != "" {
		fmt.Fprintf(&b, "Comment=%s\n", o.Comment)
	}
	fmt.Fprintf(&b, "Exec=%s\n", quoteExec(o.Exec))
	fmt.Fprintf(&b, "Icon=%s\n", iconPath)
	b.WriteString("Terminal=false\n")
	b.WriteString("NoDisplay=true\n")
	fmt.Fprintf(&b, "StartupWMClass=%s\n", o.WMClass)
	fmt.Fprintf(&b, "%s=true\n", generatedKey)
	data := []byte(b.String())

	if old != nil && bytes.Equal(old, data) {
		return Unchanged, nil
	}
	if err := writeFile(path, data); err != nil {
		return Unchanged, err
	}
	return Created, nil
}

func isGenerated(data []byte) bool {
	v, ok := getKey(data, generatedKey)
	return ok && v == "true"
}

// getKey returns key's value in the [Desktop Entry] group.
func getKey(data []byte, key string) (string, bool) {
	group := ""
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if strings.HasPrefix(line, "[") {
			group = line
			continue
		}
		if group != "[Desktop Entry]" {
			continue
		}
		if k, v, ok := strings.Cut(line, "="); ok && strings.TrimSpace(k) == key {
			return strings.TrimSpace(v), true
		}
	}
	return "", false
}

// setKey sets key=value in the [Desktop Entry] group, replacing an existing
// line or adding one at the end of the group, and reports whether anything
// changed. Every other line, other groups included, is kept as it was.
func setKey(data []byte, key, value string) ([]byte, bool) {
	if v, ok := getKey(data, key); ok && v == value {
		return data, false
	}
	want := key + "=" + value

	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	out := make([]string, 0, len(lines)+1)
	group, done := "", false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "[") {
			if group == "[Desktop Entry]" && !done {
				out = insertBeforeBlank(out, want)
				done = true
			}
			group = trimmed
		} else if group == "[Desktop Entry]" {
			if k, _, ok := strings.Cut(trimmed, "="); ok && strings.TrimSpace(k) == key {
				if !done {
					out = append(out, want)
					done = true
				}
				continue
			}
		}
		out = append(out, line)
	}
	if !done {
		out = append(out, want)
	}
	return []byte(strings.Join(out, "\n") + "\n"), true
}

// insertBeforeBlank appends line to lines, ahead of any blank lines that
// close the group.
func insertBeforeBlank(lines []string, line string) []string {
	i := len(lines)
	for i > 0 && strings.TrimSpace(lines[i-1]) == "" {
		i--
	}
	return append(lines[:i], append([]string{line}, lines[i:]...)...)
}

// quoteExec turns a path into an Exec value. Per the Desktop Entry spec,
// "%" is doubled (it introduces field codes), and a path with reserved
// characters is double-quoted with `"`, "`", `$` and `\` backslash-escaped —
// after which every backslash is doubled again, since the value is a string,
// whose own escapes are undone before the quoting is.
func quoteExec(path string) string {
	path = strings.ReplaceAll(path, "%", "%%")
	if !strings.ContainsAny(path, " \t\n\"'\\><~|&;$*?#()`") {
		return path
	}
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range path {
		switch r {
		case '"', '`', '$':
			b.WriteString(`\\`) // \" etc., with the backslash itself escaped
			b.WriteRune(r)
		case '\\':
			b.WriteString(`\\\\`) // \\, each escaped
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// writeFile replaces path atomically, so the desktop, which watches these
// directories, never reads a half-written file.
func writeFile(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o644); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
