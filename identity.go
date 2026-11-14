package main

import (
	"context"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/webbite-io/brick-wails/internal/auth"
	"github.com/webbite-io/brick-wails/internal/brickcfg"
)

// Identity is who the status popover says is logged in: the active account's
// name and the user's, fetched the way brick-cli's whoami does
// (/oauth2/userinfo and /v1/accounts/{id}). Either is "" when unknown.
type Identity struct {
	Account string `json:"account"`
	User    string `json:"user"`
}

// identityRetry is how long a failed fetch waits before the next try.
const identityRetry = 30 * time.Second

// identityWatcher keeps the Identity for the login in config.yaml. refresh is
// cheap while that login is unchanged, so it runs on the popover's status
// tick; it fetches again once the active account or the tokens change (a new
// login, from here or brick-cli).
type identityWatcher struct {
	store  *brickcfg.Store
	client *auth.Client
	logger *log.Logger
	// onChange hears the Identity each time it changes.
	onChange func(Identity)

	mu       sync.Mutex
	key      string // the login shown (or being fetched); "" when logged out
	fetched  bool   // whether shown is the result for key
	fetching bool
	retryAt  time.Time
	shown    Identity
}

func (w *identityWatcher) current() Identity {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.shown
}

func (w *identityWatcher) refresh() {
	var key, accountID string
	if cfg, err := w.store.Load(); err == nil && cfg.ActiveAccountID != "" && (cfg.AccessToken != "" || cfg.RefreshToken != "") {
		accountID = cfg.ActiveAccountID
		key = accountID + "\x00" + cfg.RefreshToken
	}
	w.mu.Lock()
	if key == "" {
		w.key, w.fetched = "", false
		w.setLocked(Identity{})
		w.mu.Unlock()
		return
	}
	if key != w.key {
		// A new login: fetch now, even if the last one failed a moment ago.
		w.key, w.fetched, w.retryAt = key, false, time.Time{}
	}
	if w.fetched || w.fetching || time.Now().Before(w.retryAt) {
		w.mu.Unlock()
		return
	}
	w.fetching = true
	w.mu.Unlock()
	go w.fetch(key, accountID)
}

func (w *identityWatcher) fetch(key, accountID string) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	var id Identity
	u, err := w.client.UserInfo(ctx)
	if err == nil {
		if id.User = strings.TrimSpace(u.GivenName + " " + u.FamilyName); id.User == "" {
			id.User = u.Email
		}
		var a *auth.Account
		if a, err = w.client.Account(ctx, accountID); err == nil {
			id.Account = a.Name
		}
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.fetching = false
	if key != w.key {
		// The login changed meanwhile; the next refresh fetches for it.
		return
	}
	if err != nil {
		w.logger.Printf("account details: %v", err)
		w.retryAt = time.Now().Add(identityRetry)
		return
	}
	w.fetched = true
	w.setLocked(id)
}

func (w *identityWatcher) setLocked(id Identity) {
	if id == w.shown {
		return
	}
	w.shown = id
	if w.onChange != nil {
		w.onChange(id)
	}
}
