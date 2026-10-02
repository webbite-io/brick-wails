//go:build darwin

package main

/*
#cgo CFLAGS: -x objective-c -F${SRCDIR}/build/darwin/sparkle
#cgo LDFLAGS: -framework Foundation
#include <stdlib.h>
#include "updater_darwin.h"
*/
import "C"

import (
	"log"
	"unsafe"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
)

// updateMenuLabel is the tray update item's resting label, spelled the way
// every Sparkle app spells it.
const updateMenuLabel = "Check for Updates…"

// appUpdater is Sparkle on macOS: it checks the appcast (SUFeedURL in
// Info.plist) at launch and every SUScheduledCheckInterval, and owns the
// whole update UI — prompt, release notes, download, install and relaunch.
// The app's own GitHub check and update window are for the other platforms
// (updates_other.go).
type appUpdater struct {
	logger *log.Logger
}

// newAppUpdater ignores the UpdateService, which is only bound on macOS so
// the generated frontend bindings stay the same whichever OS generates them.
func newAppUpdater(logger *log.Logger, _ *UpdateService) *appUpdater {
	return &appUpdater{logger: logger}
}

// updaterEvents carries Sparkle's user-driver callbacks (updater_darwin.h's
// BRICK_UPDATER_* values) off the main thread, in order. They arrive on the
// main thread, where the tray and app-switcher updates they lead to can't run:
// those go through application.InvokeSync, which would deadlock there.
var updaterEvents = make(chan C.int, 64)

//export brickUpdaterEvent
func brickUpdaterEvent(kind C.int) {
	// Never block the main thread. A full buffer means nobody is draining it
	// (the updater never started), so dropping is harmless.
	select {
	case updaterEvents <- kind:
	default:
	}
}

// start wires the tray's update item to Sparkle and starts the updater once
// the app is running. setPending moves the tray icon onto (or off) its
// update face.
func (u *appUpdater) start(app *application.App, item *application.MenuItem, setPending func(bool)) {
	// Greyed out until Sparkle is running, which a dev build (Version ==
	// "dev") never starts — matching brick-cli's isRunningInDevelopment gate.
	item.SetEnabled(false)
	if Version == "dev" {
		return
	}

	// The same item both runs a check and, while the tray is announcing an
	// update, brings that update into focus: Sparkle does whichever is due.
	item.OnClick(func(*application.Context) {
		go func() {
			setUpdaterUIOpen(true)
			application.InvokeSync(func() { C.brickUpdaterCheck() })
		}()
	})

	clearReminder := func() {
		item.SetLabel(updateMenuLabel)
		setPending(false)
	}

	app.Event.OnApplicationEvent(events.Common.ApplicationStarted, func(*application.ApplicationEvent) {
		go func() {
			var msg string
			application.InvokeSync(func() {
				if cmsg := C.brickUpdaterStart(); cmsg != nil {
					msg = C.GoString(cmsg)
					C.free(unsafe.Pointer(cmsg))
				}
			})
			if msg != "" {
				u.logger.Printf("updater not started: %s", msg)
				return
			}
			item.SetEnabled(true)

			for {
				select {
				case kind := <-updaterEvents:
					switch kind {
					case C.BRICK_UPDATER_SHOWING:
						setUpdaterUIOpen(true)
					case C.BRICK_UPDATER_REMINDER:
						// The same blue dot the other platforms show, on the
						// tray icon; the item says what clicking it does.
						u.logger.Printf("update available; announcing it in the tray")
						item.SetLabel("Install Update…")
						setPending(true)
					case C.BRICK_UPDATER_ATTENTION:
						clearReminder()
					case C.BRICK_UPDATER_FINISHED:
						clearReminder()
						setUpdaterUIOpen(false)
					}
				case <-app.Context().Done():
					return
				}
			}
		}()
	})
}
