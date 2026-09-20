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
- **CLI commands still work**: while syncing, the app serves brick's local
  control API (the server side only — the app never calls it), so `brick sync
  -s` pauses the app's engine before deleting newly excluded folders, and
  `brick switch-accounts` / `brick restart` stop it. The app then shows
  "Not syncing" with a *Start Syncing* button.

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
  - `controlapi` — brick's local control API (server side)
  - `runner` — lifecycle: lock + engine + agent + control API, app-level state
  - `onboarding` — startup routing and wizard steps
  - `testutil` — fake accounts/OIDC and Storage APIs, test helpers, and
    `cmd/brick-fakes` to run the fakes as real servers
- `integration/` — end-to-end tests (build tag `integration`).
- `frontend/` — Vanilla TypeScript + Vite: `index.html`/`src/main.ts` (popover),
  `startup.html`/`src/startup.ts` (setup window), `src/wizard.ts` and
  `src/status.ts` (pure view logic, unit tested).

## Development

Prerequisites: Go, Node (Vite needs **^20.19 or >=22.12** — a system Node from
an old installer is a common trap), and the native GUI toolchain for your OS:

| OS | Needs |
|---|---|
| Linux | `build-essential pkg-config libgtk-4-dev libwebkitgtk-6.0-dev libayatana-appindicator3-dev` (GTK4 + WebKitGTK 6.0 + AppIndicator) |
| macOS | Only the Xcode command line tools (`xcode-select --install`) — WKWebView and the status-bar item are system frameworks. Builds target macOS 12+. |

`make doctor` checks all of this for the OS you're on and says what's missing.

```bash
make doctor           # check build prerequisites
make setup            # install wails3 + frontend deps
make dev              # hot reload (uses .env.local)
make build-dev        # dev build → bin/brick-ui
make run              # run the last build (on macOS: wrapped in a .dev.app bundle)
wails3 generate bindings -clean=true -ts -i   # after changing a service's methods/types
```

`make setup` installs the wails3 CLI with `go install`, i.e. into
`$(go env GOPATH)/bin`. The Makefile calls it by full path, so the targets work
whether or not that directory is on your PATH; add it if you want to run
`wails3` yourself.

On macOS the app runs as a menu bar (accessory) app: no Dock icon, no menu bar
of its own — look for the tray icon. `make run` and `make install` build a
`.app` bundle for it, since that is what carries the bundle identifier, the
icon and the activation policy; the bare `bin/brick-ui` binary works too, but
macOS treats it as an unbundled process.

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
  the agent's path sandboxing and tunnel, the control API, the runner lifecycle
  and every onboarding step.
- **Integration** (`integration/`): onboarding → live two-way sync on a real
  filesystem, session expiry → re-login → resume, incremental restarts, and
  first-sync conflict handling. When a brick-cli checkout is available
  (`BRICK_CLI_DIR`, default `../brick-cli`) it also builds the real `brick` and
  checks `--self-test` accepts an app-onboarded config, the instance lock is
  shared both ways, `brick switch-accounts` stops the app's engine, and the app
  reuses sync state written by the CLI.

## Packaging

Installers are not set up yet. `make package` (= `wails3 task package`) builds
what the OS you're on can build natively: an ad-hoc signed `bin/brick-ui.app`
bundle on macOS, AppImage/`.deb`/`.rpm` on Linux. `make install` then puts it
where you can run it for testing — `~/Applications` on macOS, `~/.local/bin`
on Linux.

Still missing for real distribution: a `.dmg` and Developer ID signing +
notarization on macOS (`wails3 task darwin:sign:notarize` after
`wails3 setup`), `.msi`/NSIS on Windows, and CI to build each OS on its own
runner — this is a CGO app with per-OS webview/tray libraries, so it does not
cross-compile. See `build/darwin`, `build/windows`, `build/linux` and the
[Wails v3 packaging docs](https://v3.wails.io/). The tray icon still uses
Wails' placeholder logo (see `build/tray`).
