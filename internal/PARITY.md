# brick-cli parity

The sync engine, auth, storage client, lock and agent in this
directory are **ports** of brick-cli code, not shared code (the repos
deliberately don't share a Go module). When brick-cli changes any of the
functions below, mirror the change here and bump the "synced at" commit.

**Synced at:** brick-cli `f3ef7bd` (2026-09-19)

| brick-wails | brick-cli origin (`cmd/brick/`) | Notes |
|---|---|---|
| `brickcfg.Config`, `AccountConfig`, `Dir`, `Store.LoadOrCreate` | `config.go`: `Config`, `AccountConfig`, `configDir`, `loadOrCreateConfigQuiet`, `saveConfig` | + unknown-key preservation, read-modify-write `Update`, atomic write, `BRICK_CONFIG_DIR` |
| `brickcfg.ResolveEnv` | `config.go`: `resolveAPIURL`, `resolveStorageAPIURL`, `getEnv`, ldflags `Default*` | same precedence; + web/help URLs |
| `auth.FetchOIDCConfig`, `GeneratePKCE`, `ExchangeCode`, `RefreshTokens` | `auth.go`: `fetchOIDCConfig`, `generatePKCE`, `exchangeCodeForToken`, `refreshAccessToken` | identical requests |
| `auth.StartLogin` / `LoginSession.Wait` | `auth.go`: `runLogin` | no stdout; listener bound up front; account selection moved to `onboarding` |
| `auth.TokenSource.Rotate`, `Client.Do` | `sync.go`: `tokenMu`, `rotateAccessToken`, `authedRequest`; `auth.go`: `authedGet/Post` | + re-reads config before refreshing (cross-process) |
| `storage.Client` | `sync.go`: `storageClient` and its methods | identical endpoints |
| `syncengine.SyncState`, `LoadState`, `StatePath` | `sync.go`: `SyncEntry`, `SyncState`, `loadSyncState`, `syncStatePath` | identical JSON; atomic save |
| `syncengine.Engine.ReconcileAll` and helpers (`reconcile.go`) | `sync.go`: `reconcileAll`, `buildRemoteTree`, `buildLocalTree`, `applyRemoteFolderMoves`, `applyRemoteFileMoves`, `rewritePrefix`, `pruneRemoteSubtree`, `reconcileFile`, `reconcileExcludedFile`, `applyFirstSyncConflict`, `keepBothFile`, `downloadFile`, `deleteRemoteFile`, `ensureRemoteFolder`, `uploadNewFile`, `replaceFile` | **semantics must stay identical** — the reconcile matrix in `syncengine/engine_test.go` pins them |
| `syncengine.Engine.PollRemoteChanges`, `ForceReconcile`, `setCursor` | `sync.go`: `pollRemoteChanges`, `forceReconcile`, `setCursor` | |
| `syncengine.Engine.Run` (`loop.go`) | `sync.go`: `runSyncLoop` | no TUI/signals/detach; timings via `Options` (same defaults) |
| `syncengine` status/activity/pause | `sync.go`: `setState` … `statusSnapshot`, `setPaused`, `checkInterrupted` | logging via `Sink` |
| `lock` | `lock.go`, `lock_unix.go`, `lock_windows.go` | same file path → mutual exclusion with the CLI |
| `agent` | `agent.go` | local API + tunnel verbatim (`localapi.go`) |
| `onboarding.Flow` | `sync.go`: `prepareSync`, `ensureStorageSyncFolder`, `promptForSyncFolder`, `promptCreateFolder`, `promptConflictMode`, `runSyncScopeOnboarding`, `promptForRemoteControl`; `auth.go`: `ensureAuthenticated`, `selectAccount` | wizard copy mirrors the CLI prompts |

Not ported (CLI-only): `tui.go`, `live.go`, `daemon_*.go`, `transfer.go`
upload/download commands, update check, uninstall, restart, switch-accounts,
selective-sync editing after onboarding.

Gone from both: the local status/control API (server here, client + server in
brick-cli) and the CLI's `--self-test`. `onboarding.Flow.Route` still performs
the readiness checks `--self-test` used to report, in the same order.
