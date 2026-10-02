//go:build !darwin

package main

import (
	"log"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"

	"github.com/webbite-io/brick-wails/internal/trayicon"
	"github.com/webbite-io/brick-wails/internal/update"
)

// updateMenuLabel is the tray update item's resting label.
const updateMenuLabel = "Check for Updates"

// menuDotSize is the pixel size of the blue dot drawn beside the tray menu's
// "Install Update" item — menu item icons are rendered at roughly the text
// height, so it's sized for that rather than for the tray icon.
const menuDotSize = 16

// appUpdater is the app's own update mechanism outside macOS: a check against
// GitHub at launch and every update.CheckInterval, the update window, and a
// hand-off to the install script in a terminal. macOS has Sparkle instead
// (updates_darwin.go).
type appUpdater struct {
	logger *log.Logger
	svc    *UpdateService
	window *application.WebviewWindow
}

// newAppUpdater drives svc, the service the update window is bound to.
func newAppUpdater(logger *log.Logger, svc *UpdateService) *appUpdater {
	u := &appUpdater{logger: logger, svc: svc}
	svc.window = func() *application.WebviewWindow { return u.window }
	return u
}

// start creates the update window, wires the tray's update item and starts
// the checks. setPending moves the tray icon onto (or off) its update face.
func (u *appUpdater) start(app *application.App, item *application.MenuItem, setPending func(bool)) {
	// The update window is a small, fixed-size prompt: it starts hidden, and
	// UpdateService shows it when the launch check finds a newer release or
	// when the user picks the tray's update item themselves.
	u.window = app.Window.NewWithOptions(application.WebviewWindowOptions{
		Name:             "Update",
		Title:            "Webbite Brick",
		Width:            380,
		Height:           210,
		MinWidth:         380,
		MinHeight:        210,
		MaxWidth:         380,
		MaxHeight:        210,
		Hidden:           true,
		AlwaysOnTop:      true,
		Windows:          application.WindowsWindow{HiddenOnTaskbar: true},
		BackgroundColour: application.NewRGB(24, 24, 27),
		URL:              "/update.html",
	})
	u.window.RegisterHook(events.Common.WindowClosing, func(e *application.WindowEvent) {
		u.window.Hide()
		e.Cancel()
	})

	// Offers whatever a check has already turned up, and otherwise runs one on
	// the spot — see onPending below for the "Install Update" face of this
	// item.
	item.OnClick(func(*application.Context) { go u.svc.offerOrCheck() })

	// One place where a found update changes the tray: the icon gains its blue
	// dot and the menu item becomes the offer to install it, carrying the same
	// dot. Both stay until the app is actually replaced, so dismissing the
	// window doesn't hide that an update is still waiting.
	updateDot, err := trayicon.Dot(menuDotSize, trayicon.UpdateBlue)
	if err != nil {
		u.logger.Printf("menu dot: %v", err)
	}
	u.svc.onPending = func(*update.Info) {
		item.SetLabel("Install Update")
		if updateDot != nil {
			item.SetBitmap(updateDot)
		}
		setPending(true)
	}

	// The launch check runs immediately, then every CheckInterval — a tray app
	// can stay running for weeks. A dev build (Version == "dev") never checks,
	// matching brick-cli's own isRunningInDevelopment gate.
	if Version != "dev" {
		app.Event.OnApplicationEvent(events.Common.ApplicationStarted, func(*application.ApplicationEvent) {
			go func() {
				u.svc.checkOnStartup()
				t := time.NewTicker(update.CheckInterval)
				defer t.Stop()
				for {
					select {
					case <-t.C:
						u.svc.checkInBackground()
					case <-app.Context().Done():
						return
					}
				}
			}()
		})
	}
}
