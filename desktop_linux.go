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
	"github.com/wailsapp/wails/v3/pkg/events"

	"github.com/webbite-io/brick-wails/internal/desktopentry"
	"github.com/webbite-io/brick-wails/internal/trayicon"
)

// linuxAppID is the app id Wails gives the GTK application and so every
// window: "org.wails." plus the app Name from main(), lowercased with spaces
// turned into underscores. build/linux/Taskfile.yml and install.sh write the
// same value into their entries' StartupWMClass.
const linuxAppID = "org.wails.webbite_brick"

//go:embed build/appicon.png
var desktopIcon []byte

// Lucide's "check", drawn beside "Start at Login" while it's on: zinc-800 for
// a light menu, zinc-200 for a dark one.
//
//go:embed build/tray/check-light.png
var checkIconLight []byte

//go:embed build/tray/check-dark.png
var checkIconDark []byte

// checkIconSize is the pixel size of both check PNGs.
const checkIconSize = 32

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

// addStartAtLoginItem adds the "Start at Login" toggle to the tray menu. On,
// it copies the desktop entry at entryPath into ~/.config/autostart, where
// every XDG desktop looks at login; off, it removes the copy. The state is
// read from that file rather than kept here, so turning it off in the
// desktop's own settings shows in the menu too.
func addStartAtLoginItem(app *application.App, menu *application.Menu, entryPath string, logger *log.Logger) {
	if entryPath == "" {
		return
	}
	configHome := os.Getenv("XDG_CONFIG_HOME")
	if configHome == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			logger.Printf("start at login: %v", err)
			return
		}
		configHome = filepath.Join(home, ".config")
	}
	path := desktopentry.AutostartPath(configHome, "brick-ui")

	// Unticking swaps the check for a transparent image of the same size
	// rather than clearing the icon; see trayicon.Blank.
	uncheckedIcon, err := trayicon.Blank(checkIconSize)
	if err != nil {
		logger.Printf("start at login: %v", err)
		return
	}

	item := menu.Add("Start at Login")
	// mu makes a toggle and the check that follows it one step, so a theme
	// change landing in between can't put back the previous state's icon.
	var mu sync.Mutex
	// The check is the item's icon rather than the desktop's own toggle, so
	// it's Lucide's; which side of the label it lands on is up to the
	// desktop (GNOME's AppIndicator extension puts it on the right).
	applyCheck := func() {
		switch {
		case !desktopentry.AutostartEnabled(path):
			item.SetBitmap(uncheckedIcon)
		case app.Env.IsDarkMode():
			item.SetBitmap(checkIconDark)
		default:
			item.SetBitmap(checkIconLight)
		}
	}
	refresh := func(*application.ApplicationEvent) {
		mu.Lock()
		defer mu.Unlock()
		applyCheck()
	}
	item.OnClick(func(*application.Context) {
		mu.Lock()
		defer mu.Unlock()
		var err error
		if desktopentry.AutostartEnabled(path) {
			err = desktopentry.DisableAutostart(path)
		} else {
			err = desktopentry.EnableAutostart(entryPath, path)
		}
		if err != nil {
			logger.Printf("start at login: %v", err)
		}
		applyCheck()
	})
	refresh(nil)
	// The check follows the theme the way the tray icon does; see main.
	app.Event.OnApplicationEvent(events.Common.ApplicationStarted, refresh)
	app.Event.OnApplicationEvent(events.Common.ThemeChanged, refresh)
}
