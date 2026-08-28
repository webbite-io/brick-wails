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
that as a normal state, not a fault. This scaffold does not yet launch
`brick` itself when it's missing; that's a natural next step once there's a
real installer to locate the binary from.

## Project layout

- `main.go` — creates the tray icon, an attached popover window (hidden
  until the tray icon is clicked), and the tray menu (Open, Pause/Resume,
  Quit Brick, Quit). Polls brick's `/v1/status` every 2s and both updates the
  tray tooltip and emits a `brick:status` event the frontend listens for.
- `brickclient.go` — the `BrickService`, bound to the frontend. Finds
  brick's discovery file, dials its control socket, and exposes
  `Status`/`Activity`/`Account`/`Pause`/`Resume`/`QuitBrick` — each callable
  from TypeScript via the generated bindings in `frontend/bindings/`.
- `process_unix.go` / `process_windows.go` — per-OS "is this pid alive"
  check, used to treat a discovery file left behind by a crashed `brick` as
  stale rather than trusting it.
- `frontend/` — Vanilla + TypeScript + Vite. `src/main.ts` renders the
  status popover (state dot, folder, in-flight transfer, counters, recent
  activity, pause/resume and quit buttons).

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
