// The Sparkle bridge behind updates_darwin.go; implemented in updater_darwin.m.

#ifndef BRICK_UPDATER_DARWIN_H
#define BRICK_UPDATER_DARWIN_H

// What Sparkle's user driver reports, passed to the Go side's
// brickUpdaterEvent in the order Sparkle reports it.
enum {
	// Sparkle is about to put a window up (an update found at launch, or any
	// part of a check the user asked for).
	BRICK_UPDATER_SHOWING = 1,
	// A scheduled check found an update and left it to us to announce gently,
	// rather than putting a window up in the middle of whatever the user is
	// doing.
	BRICK_UPDATER_REMINDER = 2,
	// The user has brought an announced update into focus.
	BRICK_UPDATER_ATTENTION = 3,
	// The update session is over: installed, skipped, deferred or failed.
	BRICK_UPDATER_FINISHED = 4,
};

// brickUpdaterStart creates and starts the updater. It returns NULL once
// Sparkle is running, or a malloc'd message saying why it isn't, for the
// caller to free. Main thread only.
char *brickUpdaterStart(void);

// brickUpdaterCheck runs the check behind the tray's "Check for Updates…"
// item, which also brings an announced update into focus. Main thread only.
void brickUpdaterCheck(void);

#endif
