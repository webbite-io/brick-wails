// Package webapp opens the Brick web app with this machine's session handed
// across, so the browser lands signed in rather than at a login page.
package webapp

import (
	"context"
	"errors"
	"log"
	"time"

	"github.com/webbite-io/brick-wails/internal/auth"
	"github.com/webbite-io/brick-wails/internal/brickcfg"
)

// handoffTimeout caps the round trip to the auth server. The user has already
// clicked a tray item, so the browser has to open reasonably soon whatever
// happens; past this we stop waiting and open the plain URL instead.
const handoffTimeout = 15 * time.Second

// Opener opens the web app from the tray. Every field is required.
type Opener struct {
	Env    brickcfg.Env
	Store  *brickcfg.Store
	Tokens *auth.TokenSource
	Logger *log.Logger

	// OpenURL hands a URL to the user's browser (application.Browser.OpenURL).
	OpenURL func(string) error
}

// Open sends the browser to target in the web app. Call it off the UI thread:
// minting the hand-off is a network round trip.
func (o *Opener) Open(target string) {
	if err := o.OpenURL(o.url(target)); err != nil {
		o.Logger.Printf("could not open the browser: %v", err)
	}
}

// url is the URL to open: a hand-off when this machine has a session to hand
// over, and the plain web app URL whenever anything at all stands in the way.
// Every failure here is recoverable by the user signing in on the web, so
// none of them stops the browser from opening.
func (o *Opener) url(target string) string {
	accountID := ""
	if cfg, err := o.Store.Load(); err == nil {
		accountID = cfg.ActiveAccountID
	}
	plain := auth.PlainWebURL(o.Env.WebURL, target, accountID)

	// Not logged in at all: there is nothing to hand over, and no reason to
	// log a failure for it.
	if o.Tokens.Current() == "" && !o.Tokens.HasRefresh() {
		return plain
	}

	ctx, cancel := context.WithTimeout(context.Background(), handoffTimeout)
	defer cancel()
	code, err := auth.MintHandoffCode(ctx, o.Tokens, o.Env.WebOAuthClientID,
		auth.WebCallbackURL(o.Env.WebURL), o.Env.OAuthScopes)
	if err != nil {
		// Said out loud, and with the reason: a misconfiguration on the auth
		// server — an unregistered redirect_uri, a web client id that is not
		// the one the web app redeems as — otherwise looks exactly like a
		// user who simply needs to sign in on the web.
		if errors.Is(err, auth.ErrNoConsent) {
			o.Logger.Printf("no consent for the web client yet; opening the plain URL")
		} else {
			o.Logger.Printf("web hand-off failed, opening the plain URL: %v", err)
		}
		return plain
	}
	return auth.HandoffURL(code, accountID, target)
}
