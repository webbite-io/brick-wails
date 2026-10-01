//go:build windows

package runner

// Windows deliberately does not share state with brick-cli. The config
// directory is %AppData%\brick, which MSIX redirects into the package's own
// per-package store, so brick-cli could not see this app's lock file even if
// it were installed. Whoever holds the lock is therefore another copy of this
// app — naming the CLI here would send the user hunting for a terminal that
// does not exist.
const (
	lockedBy      = "Brick is already syncing on this computer"
	LockedHint    = "Quit the other Brick and press Retry."
	LockedTooltip = "Brick — already syncing"
)
