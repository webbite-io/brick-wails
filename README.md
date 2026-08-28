# Webbite Brick — tray companion

A native system-tray app that shows live sync status for
[`brick`](https://github.com/webbite-io/brick-cli) — Webbite's Brick CLI — and
lets you pause/resume/quit it without a terminal, similar to the Dropbox tray
icon. Built with [Wails v3](https://v3.wails.io/) (currently alpha).

## How it talks to brick

This app does not run the sync engine itself. It's a thin client for the
local control API a running `brick` process exposes over a Unix domain
socket (see `brickclient.go`, and
[`openapi.yaml`](../webbite-brick-cli/openapi.yaml) /the "Local
Status/Control API" section of brick-cli's README for the authoritative
protocol). The two repos deliberately don't share a Go module — only the
small HTTP/JSON protocol is duplicated between them, versioned via
`protocolVersion` in brick's discovery file, so each can ship on its own
release cadence with its own (heavier, multi-OS) native-packaging pipeline.

If `brick` isn't running, `BrickService.Status()` returns
`{state: "not-running"}` rather than an error — the UI is expected to render
that as a normal state, not a fault.

## Getting brick running

On every launch, a second "Startup" window (see below) checks whether a
`brick` process is already answering the control API and, if not, drives it
to a running state:

1. Locate the `brick` binary (`PATH`, falling back to `~/.local/bin/brick`).
   If it can't be found, offer to install it — `curl -fsSL
   https://webbite.io/cli/install.sh | bash` on Linux/macOS, `winget install
   --id Webbite.Brick -e` on Windows — then retry.
2. Run `brick --self-test --no-upgrade-check` and branch on its JSON report:
   - all checks `ok` → just start it (`brick --no-upgrade-check`).
   - `instance_lock` failing → another `brick` is already running with its
     IPC API disabled; this is reported as an error, not auto-resolved.
   - any other check failing → run `brick --setup-and-exit` in a new native
     terminal window (it's interactive — it can prompt for login — so it
     can't just be captured and mirrored into the app's own UI) and, once
     it exits 0, start `brick` as above. A non-zero exit offers to run
     setup again.

Once brick is confirmed running, the Startup window closes itself and the
tray popover behaves as normal.

## Project layout

- `main.go` — creates the tray icon, an attached popover window (hidden
  until the tray icon is clicked), the Startup window (shown on launch),
  and the tray menu (Open, Pause/Resume, Quit Brick, Quit). Polls brick's
  `/v1/status` every 2s and both updates the tray tooltip and emits a
  `brick:status` event the frontend listens for.
- `brickclient.go` — the `BrickService`, bound to the frontend. Finds
  brick's discovery file, dials its control socket, and exposes
  `Status`/`Activity`/`Account`/`Pause`/`Resume`/`QuitBrick` — each callable
  from TypeScript via the generated bindings in `frontend/bindings/`.
- `startup.go` — the `StartupService`, bound to the frontend. Locates the
  `brick` binary, runs `--self-test` and the platform install command
  (streaming the installer's output as `brick:install-output` events), and
  starts brick's sync daemon.
- `terminal.go` — opens a command in a new native terminal window (used for
  `brick --setup-and-exit`, which is interactive) and waits for it to
  finish. Works by writing a small wrapper script that records the
  command's exit code to a file, then polling for that file rather than
  relying on the terminal-launching process's own exit — several terminal
  emulators (`gnome-terminal` chief among them) hand off to an
  already-running server process and return immediately, so there'd be
  nothing meaningful to wait on otherwise.
- `process_unix.go` / `process_windows.go` — per-OS "is this pid alive"
  check (used to treat a discovery file left behind by a crashed `brick` as
  stale) and `detachProcess`, which starts a subprocess detached from this
  app's process/console so it outlives it.
- `frontend/` — Vanilla + TypeScript + Vite, with two windows/entry points:
  `index.html` + `src/main.ts` render the status popover (state dot,
  folder, in-flight transfer, counters, recent activity, pause/resume and
  quit buttons); `startup.html` + `src/startup.ts` render the startup flow
  described above.

## Development

```bash
wails3 dev            # hot-reload, both frontend and backend
wails3 generate bindings -clean=true -ts -i   # regenerate frontend/bindings after changing BrickService's methods/types
wails3 build           # production build for the current platform
```

The tray icon currently uses Wails' placeholder logo assets
(`pkg/icons.SystrayLight`/`SystrayDark`/`SystrayMacTemplate` — see the TODO
in `main.go`); swap these for a Brick-branded icon (with idle/syncing/error/
paused variants) before shipping.

## Packaging

Not yet set up. `wails3 package` targets native installers per OS (`.dmg` on
macOS, `.msi`/NSIS on Windows, AppImage/`.deb` on Linux) — see
`build/darwin`, `build/windows`, `build/linux` for the per-platform Taskfiles
Wails generated, and the [Wails v3 packaging
docs](https://v3.wails.io/) for what each needs (e.g. an Apple Developer ID
for macOS signing/notarization) before a real release can be cut.
