//go:build darwin

package main

/*
#cgo CFLAGS: -x objective-c
#cgo LDFLAGS: -framework Foundation -framework ServiceManagement
#import <Foundation/Foundation.h>
#import <ServiceManagement/ServiceManagement.h>
#include <stdlib.h>
#include <string.h>

// Set once the user has turned starting at login on or off, from the tray or
// the setup wizard. The login item's status alone can't say whether nobody
// has chosen yet: an app that was never registered and one that was turned
// off both read as not registered.
static NSString *const chosenKey = @"StartAtLoginChosen";

// A login item can only point at an app bundle; a bare binary run from a
// terminal has nothing for macOS to launch.
static bool loginItemSupported(void) {
	return [[[NSBundle mainBundle] bundlePath] hasSuffix:@".app"];
}

// Whether macOS will start Brick at login. A login item the user switched
// off in System Settings reads as requiring approval, which is off too.
static bool loginItemEnabled(void) {
	return SMAppService.mainAppService.status == SMAppServiceStatusEnabled;
}

static bool loginItemChosen(void) {
	return [[NSUserDefaults standardUserDefaults] objectForKey:chosenKey] != nil;
}

// setLoginItem registers or unregisters Brick as a login item and records
// that the user has chosen. It returns NULL on success, or a malloc'd message
// for the caller to free. Registering one the user once switched off in
// System Settings needs their approval there, so that pane is opened for
// them.
static char *setLoginItem(bool on) {
	[[NSUserDefaults standardUserDefaults] setBool:on forKey:chosenKey];
	SMAppService *service = SMAppService.mainAppService;
	NSError *error = nil;
	if (on) {
		if (service.status != SMAppServiceStatusEnabled && ![service registerAndReturnError:&error]) {
			if (service.status != SMAppServiceStatusRequiresApproval) {
				return strdup(error.localizedDescription.UTF8String);
			}
		}
		if (service.status == SMAppServiceStatusRequiresApproval) {
			[SMAppService openSystemSettingsLoginItems];
		}
	} else if (service.status != SMAppServiceStatusNotRegistered && ![service unregisterAndReturnError:&error]) {
		return strdup(error.localizedDescription.UTF8String);
	}
	return NULL;
}
*/
import "C"

import (
	"errors"
	"log"
	"sync"
	"unsafe"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// startAtLogin starts Brick at login by registering the app as a login item
// (SMAppService), which macOS lists under System Settings → General → Login
// Items, where the user can turn it off too. The state is read from macOS
// rather than kept here, so that shows on the next switch. The tray menu and
// the setup wizard both switch it, and both go through Set so the tray's
// check follows either.
type startAtLogin struct {
	logger *log.Logger

	// mu makes a switch and the check that follows it one step, so two
	// switches can't leave the check showing the earlier one.
	mu   sync.Mutex
	item *application.MenuItem
}

// newStartAtLogin returns nil outside an app bundle, which leaves both the
// tray item and the wizard's checkbox out. The entry path is Linux's
// (ensureDesktopEntry), always "" here.
func newStartAtLogin(_ string, logger *log.Logger) *startAtLogin {
	if !bool(C.loginItemSupported()) {
		return nil
	}
	return &startAtLogin{logger: logger}
}

// Supported reports whether Brick can start itself at login here.
func (s *startAtLogin) Supported() bool { return s != nil }

// Preference is what the setup wizard's checkbox starts as: the current
// setting, or on when it has never been set.
func (s *startAtLogin) Preference() bool {
	if s == nil {
		return false
	}
	return !bool(C.loginItemChosen()) || bool(C.loginItemEnabled())
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
	if msg := C.setLoginItem(C.bool(on)); msg != nil {
		err = errors.New(C.GoString(msg))
		C.free(unsafe.Pointer(msg))
		s.logger.Printf("start at login: %v", err)
	}
	s.applyCheckLocked()
	return err
}

// addMenuItem adds the "Start at Login" toggle to the tray menu: a plain
// checkbox item, since macOS menus draw the check in the same place for
// every item.
func (s *startAtLogin) addMenuItem(menu *application.Menu) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.item = menu.AddCheckbox("Start at Login", false)
	s.applyCheckLocked()
	s.mu.Unlock()

	// Wails has already flipped the check by the time this runs; the switch
	// goes by what macOS says instead, and the check is put right after.
	s.item.OnClick(func(*application.Context) {
		s.mu.Lock()
		defer s.mu.Unlock()
		_ = s.setLocked(!bool(C.loginItemEnabled()))
	})
}

// applyCheckLocked shows the check while starting at login is on.
func (s *startAtLogin) applyCheckLocked() {
	if s.item != nil {
		s.item.SetChecked(bool(C.loginItemEnabled()))
	}
}
