// The Sparkle side of updates_darwin.go. Only Sparkle's headers are used at
// build time: the framework is loaded from the bundle's Frameworks directory
// when the updater starts, rather than linked, so a binary run outside its
// .app bundle (or from a bundle without the framework, like the dev one)
// still launches, and brickUpdaterStart reports the framework missing.

#import <Cocoa/Cocoa.h>
#import <Sparkle/Sparkle.h>

#include <string.h>

#include "_cgo_export.h"
#include "updater_darwin.h"

@interface BrickUpdaterDelegate : NSObject <SPUUpdaterDelegate, SPUStandardUserDriverDelegate>
@end

@implementation BrickUpdaterDelegate

// Brick is a menu bar app, so a window from a scheduled check would open
// behind whatever the user is working in. Opting in to gentle reminders lets
// it badge the tray instead (see standardUserDriverWillHandleShowingUpdate).
- (BOOL)supportsGentleScheduledUpdateReminders {
	return YES;
}

// Sparkle shows the update itself only when it was found at launch, the
// moment a newly started app may put a window up; a release found later in
// the session is announced in the tray instead.
- (BOOL)standardUserDriverShouldHandleShowingScheduledUpdate:(SUAppcastItem *)update
                                         andInImmediateFocus:(BOOL)immediateFocus {
	return immediateFocus;
}

- (void)standardUserDriverWillHandleShowingUpdate:(BOOL)handleShowingUpdate
                                        forUpdate:(SUAppcastItem *)update
                                            state:(SPUUserUpdateState *)state {
	brickUpdaterEvent(handleShowingUpdate ? BRICK_UPDATER_SHOWING : BRICK_UPDATER_REMINDER);
}

- (void)standardUserDriverDidReceiveUserAttentionForUpdate:(SUAppcastItem *)update {
	brickUpdaterEvent(BRICK_UPDATER_ATTENTION);
}

- (void)standardUserDriverWillFinishUpdateSession {
	brickUpdaterEvent(BRICK_UPDATER_FINISHED);
}

@end

static SPUStandardUpdaterController *controller;
static BrickUpdaterDelegate *delegate;

char *brickUpdaterStart(void) {
	NSString *path = [[NSBundle mainBundle].privateFrameworksPath stringByAppendingPathComponent:@"Sparkle.framework"];
	NSBundle *sparkle = [NSBundle bundleWithPath:path];
	NSError *error = nil;
	if (sparkle == nil || ![sparkle loadAndReturnError:&error]) {
		NSString *msg = [NSString stringWithFormat:@"could not load %@: %@", path, error.localizedDescription ?: @"no such framework"];
		return strdup(msg.UTF8String);
	}
	Class cls = NSClassFromString(@"SPUStandardUpdaterController");
	if (cls == nil) {
		return strdup("Sparkle.framework has no SPUStandardUpdaterController");
	}
	delegate = [[BrickUpdaterDelegate alloc] init];
	// Started by hand rather than by the controller, which would put up an
	// alert of its own on failure; the reason is logged instead.
	controller = [[cls alloc] initWithStartingUpdater:NO updaterDelegate:delegate userDriverDelegate:delegate];
	if (![controller.updater startUpdater:&error]) {
		NSString *reason = error.localizedFailureReason ?: @"";
		NSString *msg = [NSString stringWithFormat:@"%@ %@", error.localizedDescription ?: @"unknown error", reason];
		[controller release];
		controller = nil;
		return strdup(msg.UTF8String);
	}
	return NULL;
}

void brickUpdaterCheck(void) {
	[controller checkForUpdates:nil];
}
