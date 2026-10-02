//go:build linux

package main

import (
	_ "embed"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/webbite-io/brick-wails/internal/desktopentry"
)

// linuxAppID is the app id Wails gives the GTK application and so every
// window: "org.wails." plus the app Name from main(), lowercased with spaces
// turned into underscores. build/linux/Taskfile.yml and install.sh write the
// same value into their entries' StartupWMClass.
const linuxAppID = "org.wails.webbite_brick"

//go:embed build/appicon.png
var desktopIcon []byte

// ensureDesktopEntry makes sure the desktop can match Brick's windows to a
// .desktop entry, so the window switcher shows "Webbite Brick" and its icon
// rather than the bare app id — including when Brick runs as an AppImage
// straight from where it was built, or from an install that predates the
// right StartupWMClass. See the desktopentry package.
func ensureDesktopEntry(logger *log.Logger) {
	home, err := os.UserHomeDir()
	if err != nil {
		logger.Printf("desktop entry: %v", err)
		return
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
			return
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
		return
	}
	if action != desktopentry.Unchanged {
		logger.Printf("desktop entry %s: %s", action, path)
	}
}
