//go:build !windows

package runner

// Who else could be holding the instance lock, and how the user gets rid of
// them. On Linux and macOS the lock file lives in the config directory this
// app shares with brick-cli (see the README's "Coexisting with brick-cli"), so
// the other holder is most likely the CLI.
const (
	lockedBy      = "the Brick CLI is already syncing on this computer"
	LockedHint    = "Stop it (Ctrl+C in its terminal, or quit its background daemon) and press Retry."
	LockedTooltip = "Brick — the Brick CLI is syncing"
)
