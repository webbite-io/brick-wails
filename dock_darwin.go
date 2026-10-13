//go:build darwin

package main

/*
#cgo CFLAGS: -x objective-c
#cgo LDFLAGS: -framework Cocoa
#import <Cocoa/Cocoa.h>

// Whether the window is ordered in, covered or not. Wails' IsVisible reports
// the occlusion state instead, which turns false whenever another window
// covers this one.
static bool isOrderedIn(void *window) {
	return [(NSWindow *)window isVisible];
}

static void setInAppSwitcher(bool on) {
	NSApplicationActivationPolicy want = on ? NSApplicationActivationPolicyRegular
	                                        : NSApplicationActivationPolicyAccessory;
	if ([NSApp activationPolicy] == want) {
		return;
	}
	[NSApp setActivationPolicy:want];
	// A newly regular app isn't frontmost until it's activated, so the window
	// that just opened would sit behind whatever had focus.
	if (on) {
		[NSApp activateIgnoringOtherApps:YES];
	}
}

static void raiseWindow(void *window, bool makeKey) {
	if (makeKey) {
		[(NSWindow *)window makeKeyAndOrderFront:nil];
	} else {
		[(NSWindow *)window orderFront:nil];
	}
}

static bool hasKeyWindow(void) {
	return [NSApp keyWindow] != nil;
}

static bool runningFromBundle(void) {
	return [[[NSBundle mainBundle] bundlePath] hasSuffix:@".app"];
}

static void setAppIcon(void *data, int length) {
	NSImage *image = [[NSImage alloc] initWithData:[NSData dataWithBytes:data length:length]];
	[NSApp setApplicationIconImage:image];
	[image release];
}
*/
import "C"

import (
	_ "embed"
	"unsafe"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
)

// The icon for a bare binary run outside its .app bundle (from a terminal,
// say), which macOS would otherwise show with the generic executable icon.
//
//go:embed build/appicon.png
var bareBinaryIcon []byte

// The state syncAppSwitcher works from. Main thread only.
var (
	// switcherWindows are the windows trackAppSwitcher watches.
	switcherWindows []*application.WebviewWindow
	// updaterUIOpen is set while Sparkle may have a window up: its windows
	// aren't Wails windows, so they can't be found among switcherWindows.
	updaterUIOpen bool
)

// syncAppSwitcher puts Brick in the Dock and Cmd-Tab if any of its windows is
// open, and takes it out otherwise. Main thread only.
func syncAppSwitcher() {
	open := updaterUIOpen
	for _, w := range switcherWindows {
		if p := w.NativeWindow(); p != nil && bool(C.isOrderedIn(p)) {
			open = true
		}
	}
	C.setInAppSwitcher(C.bool(open))
}

// setUpdaterUIOpen tells the app switcher whether Sparkle may have a window
// up. An accessory app made regular is activated too (see setInAppSwitcher),
// so the window Sparkle is about to show isn't left behind the app that had
// focus. Not on the main thread.
func setUpdaterUIOpen(open bool) {
	application.InvokeSync(func() {
		updaterUIOpen = open
		syncAppSwitcher()
	})
}

// trackAppSwitcher keeps Brick in the Dock and Cmd-Tab while any of windows
// is open, and a menu bar-only (accessory) app otherwise: an open window has
// to be reachable with Cmd-Tab like any other, which an accessory app never
// is.
//
// macOS reports WindowShow/WindowHide on occlusion changes, so they fire when
// a window is merely covered too; the handler re-checks what is actually
// open rather than trusting the event. Losing key status is watched as well,
// to catch a window hidden while it was fully covered, which changes no
// occlusion state and so sends no WindowHide.
func trackAppSwitcher(app *application.App, windows ...*application.WebviewWindow) {
	// Set directly: this runs before the app does, so nothing reads it yet
	// (and InvokeSync would wait forever for a run loop that hasn't started).
	switcherWindows = windows
	sync := func(*application.WindowEvent) { application.InvokeSync(syncAppSwitcher) }

	// The window that last had focus, to give it back on activation.
	var lastKey *application.WebviewWindow
	for _, w := range windows {
		w.OnWindowEvent(events.Common.WindowShow, sync)
		w.OnWindowEvent(events.Common.WindowHide, sync)
		w.OnWindowEvent(events.Mac.WindowDidResignKey, sync)
		w.OnWindowEvent(events.Mac.WindowDidBecomeKey, func(*application.WindowEvent) {
			application.InvokeSync(func() { lastKey = w })
		})
	}

	// Switching to Brick with Cmd-Tab or its Dock icon activates the app but
	// leaves its windows where they are: an app that was an accessory when its
	// windows were created doesn't get them raised for it. Raise the open ones
	// and give focus back to the one that had it.
	raise := func(*application.ApplicationEvent) {
		application.InvokeSync(func() {
			// Activation that already focused a window (opening one from the
			// tray menu does) needs no help, and must not have focus moved.
			if bool(C.hasKeyWindow()) {
				return
			}
			var key unsafe.Pointer
			for _, w := range windows {
				p := w.NativeWindow()
				if p == nil || !bool(C.isOrderedIn(p)) {
					continue
				}
				C.raiseWindow(p, false)
				if key == nil || w == lastKey {
					key = p
				}
			}
			if key != nil {
				C.raiseWindow(key, true)
			}
		})
	}
	app.Event.OnApplicationEvent(events.Mac.ApplicationDidBecomeActive, raise)
	app.Event.OnApplicationEvent(events.Mac.ApplicationShouldHandleReopen, raise)

	app.Event.OnApplicationEvent(events.Common.ApplicationStarted, func(*application.ApplicationEvent) {
		application.InvokeSync(func() {
			// A bundle has its own icon, which is the better one: Assets.car
			// carries the macOS-styled rendition the plain PNG lacks.
			if !bool(C.runningFromBundle()) {
				C.setAppIcon(unsafe.Pointer(&bareBinaryIcon[0]), C.int(len(bareBinaryIcon)))
			}
		})
	})
}
