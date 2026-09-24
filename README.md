# Webbite Brick — desktop app

A native system-tray app that keeps a local folder in two-way sync with
Webbite Brick, similar to the Dropbox tray app. Built with
[Wails v3](https://v3.wails.io/) (currently alpha).

The app runs the sync engine itself. It does **not** need the
[`brick`](https://github.com/webbite-io/brick-cli) CLI, but it is fully
compatible with it: both use the same config file, the same sync state and the
same OAuth client, and only one of them syncs at a time.

## How sync works

The sync engine is a port of brick-cli's (`cmd/brick/sync.go` @ `f3ef7bd`),
kept semantically identical — see [`internal/PARITY.md`](internal/PARITY.md):

- A full two-way reconcile of the remote tree against the local folder, with a
  per-file index (`sync-state-<accountId>.json`) that tells a new file apart
  from a deleted one. Deletions propagate both ways (to Brick's trash);
  server-side moves/renames are mirrored as local renames; when both sides
  changed a file, Brick's copy wins.
- Driven by a filesystem watcher (debounced), a `check-updates` poll every 20s,
  and a forced full reconcile every ~30 min (catches purged files).
- `excludeDirs` (selective sync) are never created, uploaded or downloaded.
- The first sync into a folder that already has files applies the conflict
  mode chosen during onboarding (overwrite this device / overwrite Brick / keep
  both copies).
- The remote-file agent registers the device with Brick Online and, only if
  remote access is enabled, serves the chosen folders to the same user.

## Onboarding

The setup window (`frontend/startup.html` + `src/startup.ts`) is the graphical
version of brick-cli's interactive setup. On launch it routes:

| Situation | Screen |
|---|---|
| The Brick CLI is already syncing (holds the lock) | "Brick CLI is running" + Retry |
| Not logged in | Welcome → **Log in** (opens the browser; OIDC + PKCE with a loopback callback) |
| Session expired | "Authentication failed" → Log in again |
| Several accounts, none chosen | Account picker |
| No sync folder | Wizard: sync folder (`~/Brick` / pick existing / create) → conflict mode (only if the folder has files) → which folders to sync → remote access → done |
| Can't reach the API | Error + Retry |
| All set | Starts syncing; the window never shows |

A machine already set up by the CLI goes straight to syncing. If the session
expires while syncing, the setup window opens at the login step.

## Coexisting with brick-cli

- **Shared files**: `config.yaml`, `sync-state-*.json` and `brick.lock` in
  `~/.config/brick` (`%AppData%\brick` on Windows). Config writes are
  read-modify-write and keep keys this app doesn't know about, so neither app
  erases the other's settings.
- **One engine at a time**: both take the same instance lock. If the CLI is
  syncing, the app says so and waits for Retry.
- **Shared tokens**: the app must use the CLI's OAuth client
  (`OAUTH_CLIENT_ID`), since a refresh token can only be refreshed by the
  client it was issued to. Token rotation re-reads the config first, so a
  refresh done by the CLI is adopted rather than replayed.
- **CLI commands that touch a running instance**: the CLI no longer has a
  control API, so `brick sync -s`, `brick switch-accounts` and `brick restart`
  can't reach into the app. They detect the held lock and ask the user to quit
  the app first.

## Configuration

Settings use the same keys as brick-cli — see [`.env.example`](.env.example).

- **Development**: copy `.env.example` to `.env.local` (gitignored). Dev builds
  load `.env.local` (then `.env.dev`) at startup.
- **Production**: values are baked in at compile time via `-ldflags` (see
  `BRICK_LDFLAGS` in `Taskfile.yml`). `make build-prod` reads them from
  `.env.prod` (gitignored) or the environment, and refuses to build if
  `ACC_API_URL`, `STORAGE_API_URL` or `OAUTH_CLIENT_ID` is missing. Production
  builds never read `.env` files.
- `BRICK_CONFIG_DIR` (dev/test only) points the app at a separate config
  directory, which also moves its runtime files — useful to try the app
  without touching a real brick setup.

Logs go to `<config dir>/brick-ui.log` (plus stderr with `DEBUG=true`).

## Project layout

- `main.go` — tray icon and menu, the status popover and setup windows, event
  wiring, compile-time defaults.
- `services_sync.go` — `SyncService` (status, activity, pause/resume) for the
  popover.
- `services_onboarding.go` — `OnboardingService`: a thin Wails adapter
  (native folder dialogs, browser, window control) over `internal/onboarding`.
- `internal/` — everything else, with no Wails dependency (so it tests without
  GTK/WebKit):
  - `brickcfg` — config file + env resolution
  - `auth` — OIDC login, token refresh/rotation, authenticated requests
  - `storage` — Storage API client
  - `syncengine` — the sync engine
  - `lock` — the per-user instance lock (shared with brick-cli)
  - `agent` — the remote-file agent
  - `runner` — lifecycle: lock + engine + agent, app-level state
  - `onboarding` — startup routing and wizard steps
  - `testutil` — fake accounts/OIDC and Storage APIs, test helpers, and
    `cmd/brick-fakes` to run the fakes as real servers
- `integration/` — end-to-end tests (build tag `integration`).
- `frontend/` — Vanilla TypeScript + Vite: `index.html`/`src/main.ts` (popover),
  `startup.html`/`src/startup.ts` (setup window), `src/wizard.ts` and
  `src/status.ts` (pure view logic, unit tested).

## Development

```bash
make doctor           # check build prerequisites
make setup            # install wails3 + frontend deps
make dev              # hot reload (uses .env.local)
make build-dev        # dev build → bin/brick-ui
wails3 generate bindings -clean=true -ts -i   # after changing a service's methods/types
```

Trying the app without the real backend:

```bash
go run ./internal/testutil/cmd/brick-fakes -seed      # fake API on :18080/:18081; login auto-approves
BRICK_CONFIG_DIR=/tmp/brick-dev ACC_API_URL=http://127.0.0.1:18080 \
  STORAGE_API_URL=http://127.0.0.1:18081 OAUTH_CLIENT_ID=test-client ./bin/brick-ui
```

## Tests

```bash
make test               # Go unit tests (-race) + frontend typecheck and vitest
make test-integration   # end-to-end sync/onboarding tests
make test-all
```

- **Unit** (`internal/...`): config round-trips (unknown keys, read-modify-write,
  Windows path), login/PKCE/refresh (including a single refresh under concurrent
  401s, and adopting a CLI-rotated token), the Storage API client, a reconcile
  matrix covering every create/update/delete/move/conflict/exclude case, pause
  semantics, cursor and state-file compatibility, a cross-process lock test,
  the agent's path sandboxing and tunnel, the runner lifecycle and every
  onboarding step.
- **Integration** (`integration/`): onboarding → live two-way sync on a real
  filesystem, session expiry → re-login → resume, incremental restarts, and
  first-sync conflict handling.

## Packaging

**Linux** is wired up.

```bash
make release            # build dist/brick-ui-<version>-linux-<arch>.tar.gz + SHA256SUMS
make release-to-github  # publish dist/ as a GitHub release (prompts before replacing)
```

The tarball contains just `brick-ui.AppImage` and `brick-ui.png`. The AppImage
is self-contained: `wails3 generate appimage` runs linuxdeploy with its GTK
plugin and pulls in WebKitGTK's out-of-process helpers (`WebKitWebProcess`,
`WebKitNetworkProcess`, the injected bundle), which a plain linuxdeploy run
would miss. Expect ~150 MB — that's the GTK4 + WebKitGTK stack, not the ~13 MB
app binary.

`release-to-github` refuses to publish unless it can prove the artifacts are
production builds: it extracts the binary back out of the AppImage and greps
for `ACC_API_URL`/`STORAGE_API_URL`. (Grepping the AppImage directly doesn't
work — the payload is compressed squashfs.) It also refuses to publish version
`dev`, so tag the commit first.

### Installing

End users don't touch the tarball. `build/linux/appimage/install.sh` is served
from the repo and fetches the release itself:

```bash
curl -fsSL https://raw.githubusercontent.com/webbite-io/brick-wails/main/build/linux/appimage/install.sh | bash
```

It resolves the latest tag via the GitHub API, verifies the download against
`SHA256SUMS`, installs to `~/.local/bin/brick-ui`, writes the hicolor icons and
a `~/.local/share/applications/brick-ui.desktop` entry, then refreshes the
desktop and icon caches. No root required. Flags: `--version X`, `--prefix
PATH`, `--force`, `--uninstall`.

Re-running it upgrades in place rather than accumulating copies: every artifact
has a fixed destination, the installed version is recorded in
`~/.local/share/brick-ui/version` (so an unchanged version is a no-op), and any
stray launcher entry pointing at our binary under a different filename — the
`appimagekit-*.desktop` that AppImageLauncher writes on first launch, for
instance — is pruned before ours is written.

Because the installer lives in the repo rather than inside the tarball, fixing
it doesn't require cutting a new release.

Two caveats. The first `make release` downloads linuxdeploy and AppRun from
GitHub, caching them in `build/linux/appimage/build`. And an AppImage only runs
on glibc **at least** as new as the build host's, so release from the oldest
distro you intend to support — though the GTK4/WebKitGTK 6.0 requirement
already floors this at Ubuntu 24.04 / Debian 13.

`.deb`, `.rpm` and AUR packages are still defined as Task targets
(`wails3 task linux:create:deb` and friends, configured in
`build/linux/nfpm/nfpm.yaml`) but aren't part of `make release`.

**macOS and Windows** aren't set up. This is a CGO GUI app, so each needs its
own native toolchain or CI runner; `build/darwin` and `build/windows` hold the
Wails-generated scaffolding (`.dmg`, `.msi`/NSIS). See the [Wails v3 packaging
docs](https://v3.wails.io/).

The tray icon still uses Wails' placeholder logo (see `build/tray`).
