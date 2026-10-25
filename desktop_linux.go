//go:build linux

package main

import (
	_ "embed"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/wailsapp/wails/v3/pkg/application"

	"github.com/webbite-io/brick-wails/internal/desktopentry"
)

// linuxAppID is the app id Wails gives the GTK application and so every
// window: "org.wails." plus the app Name from main(), lowercased with spaces
// turned into underscores. build/linux/Taskfile.yml and install.sh write the
// same value into their entries' StartupWMClass.
const linuxAppID = "org.wails.webbite_brick"

//go:embed build/appicon.png
var desktopIcon []byte

// The tray item's label while starting at login is off and on. The check is
// part of the label rather than the item's icon or a checkbox item, because
// tray hosts don't agree on where those go: GNOME's AppIndicator extension
// puts both after the label, while the Status Tray extension puts them before
// it and, for a checkbox, keeps that space even while it's off, which
// indents the item past the rest of the menu. Text lands the same everywhere.
const (
	startAtLoginLabel   = "Start at Login"
	startAtLoginOnLabel = "Start at Login ✓"
)

// ensureDesktopEntry makes sure the desktop can match Brick's windows to a
// .desktop entry, so the window switcher shows "Webbite Brick" and its icon
// rather than the bare app id — including when Brick runs as an AppImage
// straight from where it was built, or from an install that predates the
// right StartupWMClass. See the desktopentry package. Returns the path of the
// entry in effect, or "" when there isn't one to point at.
func ensureDesktopEntry(logger *log.Logger) string {
	home, err := os.UserHomeDir()
	if err != nil {
		logger.Printf("desktop entry: %v", err)
		return ""
	}
	dataHome := os.Getenv("XDG_DATA_HOME")
	if dataHome == "" {
		dataHome = filepath.Join(home, ".local", "share")
	}
	dataDirs := os.Getenv("XDG_DATA_DIRS")
	if dataDirs == "" {
		dataDirs = "/usr/local/share:/usr/share"
	}

	// Inside an AppImage the executable is a path in its temporary mount;
	// $APPIMAGE is the file that was actually launched.
	exec := os.Getenv("APPIMAGE")
	if exec == "" {
		if exec, err = os.Executable(); err != nil {
			logger.Printf("desktop entry: %v", err)
			return ""
		}
	}

	path, action, err := desktopentry.Ensure(desktopentry.Options{
		DataHome: dataHome,
		DataDirs: strings.Split(dataDirs, ":"),
		ID:       "brick-ui",
		WMClass:  linuxAppID,
		Name:     "Webbite Brick",
		Comment:  "Tray companion for the Webbite Brick CLI",
		Exec:     exec,
		Icon:     desktopIcon,
		// install.sh's state dir, which --uninstall removes.
		StateDir: filepath.Join(dataHome, "brick-ui"),
	})
	if err != nil {
		logger.Printf("desktop entry: %v", err)
		return ""
	}
	if action != desktopentry.Unchanged {
		logger.Printf("desktop entry %s: %s", action, path)
	}
	return path
}

// startAtLogin starts Brick at login: on, it copies the desktop entry at
// entryPath into ~/.config/autostart, where every XDG desktop looks at login;
// off, it leaves the copy switched off, which records the choice. The state
// is read from that file rather than kept here, so turning it off in the
// desktop's own settings shows too. The tray menu and the setup wizard both
// switch it, and both go through Set so the tray's check follows either.
type startAtLogin struct {
	logger    *log.Logger
	entryPath string
	path      string

	// mu makes a switch and the check that follows it one step, so two
	// switches can't leave the check showing the earlier one.
	mu   sync.Mutex
	item *application.MenuItem
}

// newStartAtLogin returns nil when there's no entry to copy, which leaves
// both the tray item and the wizard's checkbox out.
func newStartAtLogin(entryPath string, logger *log.Logger) *startAtLogin {
	if entryPath == "" {
		return nil
	}
	configHome := os.Getenv("XDG_CONFIG_HOME")
	if configHome == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			logger.Printf("start at login: %v", err)
			return nil
		}
		configHome = filepath.Join(home, ".config")
	}
	return &startAtLogin{
		logger:    logger,
		entryPath: entryPath,
		path:      desktopentry.AutostartPath(configHome, "brick-ui"),
	}
}

// Supported reports whether Brick can start itself at login here.
func (s *startAtLogin) Supported() bool { return s != nil }

// Preference is what the setup wizard's checkbox starts as: the current
// setting, or on when it has never been set.
func (s *startAtLogin) Preference() bool {
	return s != nil && desktopentry.AutostartPreference(s.path)
}

// Set turns starting at login on or off and updates the tray's check.
func (s *startAtLogin) Set(on bool) error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.setLocked(on)
}

func (s *startAtLogin) setLocked(on bool) error {
	var err error
	if on {
		err = desktopentry.EnableAutostart(s.entryPath, s.path)
	} else {
		err = desktopentry.DisableAutostart(s.entryPath, s.path)
	}
	if err != nil {
		s.logger.Printf("start at login: %v", err)
	}
	s.applyCheckLocked()
	return err
}

// addMenuItem adds the "Start at Login" toggle to the tray menu.
func (s *startAtLogin) addMenuItem(menu *application.Menu) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.item = menu.Add(startAtLoginLabel)
	s.applyCheckLocked()
	s.mu.Unlock()

	s.item.OnClick(func(*application.Context) {
		s.mu.Lock()
		defer s.mu.Unlock()
		_ = s.setLocked(!desktopentry.AutostartEnabled(s.path))
	})
}

// applyCheckLocked shows the check while starting at login is on.
func (s *startAtLogin) applyCheckLocked() {
	if s.item == nil {
		return
	}
	if desktopentry.AutostartEnabled(s.path) {
		s.item.SetLabel(startAtLoginOnLabel)
	} else {
		s.item.SetLabel(startAtLoginLabel)
	}
}
