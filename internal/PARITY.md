# brick-cli parity

The sync engine, auth, storage client, lock and agent in this
directory are **ports** of brick-cli code, not shared code (the repos
deliberately don't share a Go module). When brick-cli changes any of the
functions below, mirror the change here and bump the "synced at" commit.

**Synced at:** brick-cli `09421a1` (2026-10-01)

**Deliberate divergences** from brick-cli `09421a1`, which introduced the same
delta-patching there. Each is a fix that belongs upstream too; until it lands,
do not "re-sync" these away:

- `ensureRemoteFolder` records the folder it created in `remoteTreeIDToRel`. The
  CLI leaves that index untouched there, so a folder the client created and that
  is then moved remotely before the feed reports it keeps a phantom entry at its
  old rel — see `TestOwnCreatedFolderMovedRemotelyLeavesNoPhantom`.
- A file the file pass failed on leaves `filesConverged` false, so the next pass
  falls back to the full key union and retries it. The CLI tracks only
  interruptions, so a failed transfer waits for the periodic backstop.
- The local cache is used only while `watching` is set, i.e. while a watcher is
  really feeding `notifyPath`. In poll-only mode the CLI would scope passes to a
  set nothing can ever add to.
- `Notify` (exported here, so any caller may use it) marks the next pass
  `unknown` rather than letting an unlocated change look like no change at all,
  and a remote poll walks the folder while the watcher has reports the debounce
  worker hasn't acted on. A pass that fails or is interrupted drops
  `localTreeValid`.

| brick-wails | brick-cli origin (`cmd/brick/`) | Notes |
|---|---|---|
| `brickcfg.Config`, `AccountConfig`, `Dir`, `Store.LoadOrCreate` | `config.go`: `Config`, `AccountConfig`, `configDir`, `loadOrCreateConfigQuiet`, `saveConfig` | + unknown-key preservation, read-modify-write `Update`, atomic write, `BRICK_CONFIG_DIR` |
| `brickcfg.ResolveEnv` | `config.go`: `resolveAPIURL`, `resolveStorageAPIURL`, `getEnv`, ldflags `Default*` | same precedence; + web/help URLs |
| `auth.FetchOIDCConfig`, `GeneratePKCE`, `ExchangeCode`, `RefreshTokens` | `auth.go`: `fetchOIDCConfig`, `generatePKCE`, `exchangeCodeForToken`, `refreshAccessToken` | identical requests |
| `auth.StartLogin` / `LoginSession.Wait` | `auth.go`: `runLogin` | no stdout; listener bound up front; account selection moved to `onboarding` |
| `auth.TokenSource.Rotate`, `Client.Do` | `sync.go`: `tokenMu`, `rotateAccessToken`, `authedRequest`; `auth.go`: `authedGet/Post` | + re-reads config before refreshing (cross-process) |
| `storage.Client` | `sync.go`: `storageClient` and its methods | identical endpoints |
| `syncengine.SyncState`, `LoadState`, `StatePath` | `sync.go`: `SyncEntry`, `SyncState`, `loadSyncState`, `syncStatePath` | identical JSON; atomic save |
| `syncengine.Engine.ReconcileAll` and helpers (`reconcile.go`) | `sync.go`: `reconcileAll`, `buildRemoteTree`, `buildLocalTree`, `applyRemoteFolderMoves`, `applyRemoteFileMoves`, `rewritePrefix`, `pruneRemoteSubtree`, `reconcileFile`, `reconcileExcludedFile`, `applyFirstSyncConflict`, `keepBothFile`, `downloadFile`, `deleteRemoteFile`, `ensureRemoteFolder`, `uploadNewFile`, `replaceFile`, `verifyUnsyncedFileMatches`, `hashFileMD5` | **semantics must stay identical** — the reconcile matrix in `syncengine/engine_test.go` pins them |
| `syncengine.Engine.DryRun`, `dryRunClassify` (`dryrun.go`) | `sync.go`: `runSyncDryRun`, `dryRunClassify` | classification identical; returns `[]Change` instead of printing, so the app can render it. The CLI's spinners and instance-lock-free framing have no counterpart here |
| `syncengine.Engine.PollRemoteChanges`, `ForceReconcile`, `setCursor` | `sync.go`: `pollRemoteChanges`, `forceReconcile`, `setCursor` | |
| `syncengine.Engine.fetchRemoteTree`, `applyRemoteDelta`, `rewriteRemotePrefix`, `buildRemoteIDIndex`, the `remoteTree*` cache fields | `sync.go`: `reconcileAll`'s `fetchRemote`, `applyRemoteDelta`, `rewriteRemotePrefix`, `buildRemoteIDIndex`, `remoteTreeCache` fields | reuses the last walk when a check-updates probe says nothing changed, and patches it from the delta when something did; `ForceReconcile` always bypasses it |
| `syncengine.Engine.fetchLocalTree`, `applyLocalChanges`, `localScope`, the `localTree*` fields, `filesConverged` | `sync.go`: `reconcileAllImpl`'s `compareLocal`, `applyLocalChanges`, `markLocalChange`/`drainLocalChanges`, `localTree*`, `filesConverged` | patches the cached local tree from watcher-reported paths; see the divergences above |
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
