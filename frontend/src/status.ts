// Pure helpers for rendering sync status — kept free of DOM/Wails imports so
// they can be unit tested (see status.test.ts).

export const STATE_LABELS: Record<string, string> = {
    'not-configured': 'Brick is not set up yet',
    'auth-required': 'Login required',
    locked: 'The Brick CLI is syncing',
    stopped: 'Not syncing',
    starting: 'Starting…',
    syncing: 'Syncing…',
    idle: 'Up to date',
    paused: 'Paused',
    error: 'Error',
};

// hidesPopoverForState reports whether clicking the action button should also
// hide the popover. Setting Brick up runs in its own window, so the popover
// would only sit in front of it.
export function hidesPopoverForState(state: string): boolean {
    return state === 'not-configured';
}

// actionForState returns the popover button label offered when nothing is
// syncing (it opens the setup window), or null while syncing.
export function actionForState(state: string): string | null {
    switch (state) {
        case 'not-configured':
            return 'Set Up Brick';
        case 'auth-required':
            return 'Log In Again';
        case 'locked':
        case 'stopped':
            return 'Start Syncing';
        default:
            return null;
    }
}
