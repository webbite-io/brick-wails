//go:build darwin

package main

/*
#cgo CFLAGS: -x objective-c
#cgo LDFLAGS: -framework Cocoa
#import <Cocoa/Cocoa.h>
#include <stdlib.h>

// The About window's Website button sends its click here.
@interface BrickAboutTarget : NSObject {
	NSURL *url;
}
- (instancetype)initWithURL:(NSURL *)u;
- (void)openWebsite:(id)sender;
@end

@implementation BrickAboutTarget
- (instancetype)initWithURL:(NSURL *)u {
	if ((self = [super init])) {
		url = [u retain];
	}
	return self;
}
- (void)dealloc {
	[url release];
	[super dealloc];
}
- (void)openWebsite:(id)sender {
	[[NSWorkspace sharedWorkspace] openURL:url];
}
@end

// Wails' own About box (showAboutBox in its dialogs_darwin.go), with a
// Website button between the text and OK. The alert centres its accessory
// view and leaves 16pt either side of it; the view's own height around the
// button adds to that, so the button stands apart from both.
static void showAbout(const char *name, const char *description, const char *website) {
	NSAlert *alert = [[NSAlert alloc] init];
	[alert setMessageText:[NSString stringWithUTF8String:name]];
	[alert setInformativeText:[NSString stringWithUTF8String:description]];
	[alert setAlertStyle:NSAlertStyleInformational];

	BrickAboutTarget *target = [[BrickAboutTarget alloc]
		initWithURL:[NSURL URLWithString:[NSString stringWithUTF8String:website]]];
	NSButton *button = [NSButton buttonWithTitle:@"Website" target:target action:@selector(openWebsite:)];
	NSView *view = [[NSView alloc] initWithFrame:NSMakeRect(0, 0, 160, 60)];
	CGFloat height = button.intrinsicContentSize.height;
	[button setFrame:NSMakeRect(20, (60 - height) / 2, 120, height)];
	[view addSubview:button];
	[alert setAccessoryView:view];
	[view release];

	[alert runModal];
	[alert release];
	[target release];
}
*/
import "C"

import (
	"unsafe"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// websiteURL is where the About window's Website button goes.
const websiteURL = "https://webbite.io"

// setupAboutWindow points the app menu's About item, which macOS shows while
// one of Brick's windows is open, at showAbout rather than Wails' own About
// box.
func setupAboutWindow(app *application.App) {
	menu := application.DefaultApplicationMenu()
	about := menu.FindByLabel("About " + appName)
	if about == nil {
		return
	}
	about.OnClick(func(*application.Context) {
		application.InvokeAsync(showAbout)
	})
	app.Menu.Set(menu)
}

// showAbout runs the About window until it's closed. Main thread only.
func showAbout() {
	name, description, website := C.CString(appName), C.CString(appDescription), C.CString(websiteURL)
	defer C.free(unsafe.Pointer(name))
	defer C.free(unsafe.Pointer(description))
	defer C.free(unsafe.Pointer(website))
	C.showAbout(name, description, website)
}
