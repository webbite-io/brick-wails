//go:build darwin

package main

/*
#cgo CFLAGS: -x objective-c
#cgo LDFLAGS: -framework Cocoa
#import <Cocoa/Cocoa.h>
#import <objc/runtime.h>

static IMP brickNSMenuItemSetSubmenu;

// Wails gives every NSMenuItem it makes, a submenu's parent item included,
// the action that reports a click (handleClick on its MenuItem class). AppKit
// treats a parent item with an action as clickable: a click runs the action
// and closes the whole menu, where a parent item should only open its
// submenu. Wails always sets the submenu after the action, so dropping the
// action there makes every parent item behave like a native one, rebuilt
// menus included. With no handleClick action, Wails' enable/disable leaves
// the target nil too.
static void brickSetSubmenu(id self, SEL _cmd, NSMenu *submenu) {
	((void (*)(id, SEL, NSMenu *))brickNSMenuItemSetSubmenu)(self, _cmd, submenu);
	if (submenu != nil) {
		[(NSMenuItem *)self setAction:NULL];
		[(NSMenuItem *)self setTarget:nil];
	}
}

static void installSubmenuFix(void) {
	Class cls = objc_getClass("MenuItem");
	if (cls == Nil) {
		return;
	}
	SEL sel = @selector(setSubmenu:);
	brickNSMenuItemSetSubmenu = class_getMethodImplementation([NSMenuItem class], sel);
	Method m = class_getInstanceMethod([NSMenuItem class], sel);
	// Added to Wails' subclass only, so NSMenuItems made elsewhere are
	// untouched.
	class_addMethod(cls, sel, (IMP)brickSetSubmenu, method_getTypeEncoding(m));
}
*/
import "C"

func init() {
	C.installSubmenuFix()
}
