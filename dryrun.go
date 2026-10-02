package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/webbite-io/brick-wails/internal/auth"
	"github.com/webbite-io/brick-wails/internal/brickcfg"
	"github.com/webbite-io/brick-wails/internal/storage"
	"github.com/webbite-io/brick-wails/internal/syncengine"
)

// runDryRun is the app's headless counterpart of `brick sync --dry-run`: it
// reports what the sync engine would do right now — every file it would
// upload, download or otherwise change — and exits, without ever starting the
// GUI. Nothing is transferred, deleted or written: no instance lock is taken
// (so it is safe to run while the app or brick-cli is syncing) and the
// sync-state file is read but never saved.
//
// It is the way to exercise the reconcile logic — in particular the
// content-MD5 verification that keeps a missing sync-state file from causing
// a full re-transfer — without a display.
func runDryRun(env brickcfg.Env, store *brickcfg.Store) error {
	cfg, _, err := store.LoadOrCreate()
	if err != nil {
		return err
	}
	ac := cfg.ActiveAccount()
	if cfg.ActiveAccountID == "" || ac == nil || strings.TrimSpace(ac.StorageSyncFolder) == "" {
		return errors.New("brick is not set up yet: run the app once to choose an account and sync folder")
	}

	tokens, err := auth.NewTokenSource(store, env.APIURL, env.OAuthClientID)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	if err := tokens.EnsureAccess(ctx); err != nil {
		return fmt.Errorf("not logged in: %w", err)
	}

	sc := &storage.Client{BaseURL: env.StorageAPIURL, AccountID: cfg.ActiveAccountID, Auth: auth.NewClient(tokens)}
	root, err := sc.ResolveRoot(ctx)
	if err != nil {
		return fmt.Errorf("could not reach the Brick storage API at %s: %w", env.StorageAPIURL, err)
	}

	// FirstSync is false and ConflictMode empty: both are onboarding
	// decisions carried in memory from the wizard, and a dry run on an
	// already-configured machine is by definition a later pass — the same
	// plain remote-wins resolution a running app would apply.
	eng := syncengine.New(syncengine.Config{
		Storage:     sc,
		Folder:      ac.StorageSyncFolder,
		AccountID:   cfg.ActiveAccountID,
		RootID:      root.ID,
		ExcludeDirs: ac.ExcludeDirs,
		StatePath:   syncengine.StatePath(store.Dir(), cfg.ActiveAccountID),
	})

	fmt.Printf("Comparing %s with Brick...\n\n", ac.StorageSyncFolder)
	changes, err := eng.DryRun(ctx)
	if err != nil {
		return err
	}
	for _, c := range changes {
		fmt.Println(c.RelPath)
		fmt.Printf("  - %s\n", c.Label)
	}
	if len(changes) == 0 {
		fmt.Println("Nothing to sync — the local folder and the server already match.")
		return nil
	}
	fmt.Printf("\n%d file(s) would be synced.\n", len(changes))
	return nil
}

// dryRunRequested reports whether --dry-run (or -dry-run) was passed. The app
// takes no other flags, so this is a plain argv scan rather than a flag set —
// Wails and the platform webview helpers pass their own arguments through,
// and a FlagSet would reject those.
func dryRunRequested(args []string) bool {
	for _, a := range args {
		if a == "--dry-run" || a == "-dry-run" {
			return true
		}
	}
	return false
}

// maybeRunDryRun runs the dry run and exits when --dry-run was passed;
// otherwise it returns and the app starts normally.
func maybeRunDryRun(env brickcfg.Env, store *brickcfg.Store) {
	if !dryRunRequested(os.Args[1:]) {
		return
	}
	if err := runDryRun(env, store); err != nil {
		fmt.Fprintf(os.Stderr, "Dry run failed: %v\n", err)
		os.Exit(1)
	}
	os.Exit(0)
}
