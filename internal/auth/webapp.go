package auth

import (
	"net/url"
	"strings"
)

// Destinations inside the web app. Always paths, never absolute URLs: the web
// app validates the one it is handed and silently drops anything that would
// navigate off-origin back to "/".
const (
	// WebTargetFiles is the file browser — the web app's home.
	WebTargetFiles = "/"
)

// WebCallbackURL is the web app's OIDC redirect URI. It has to be exactly
// what is registered for the web client on the auth server, or the authorize
// call is rejected, which is why it is derived here rather than at each call
// site.
func WebCallbackURL(webURL string) string {
	return strings.TrimRight(webURL, "/") + "/oauth2/callback"
}

// HandoffURL is the URL that opens target in the web app already signed in:
// the minted code's redirect, with the verifier, the account to select and
// where to land appended as a fragment.
//
// The verifier goes in the fragment and never the query, which is the entire
// security argument for the design: a fragment is not sent to the server, so
// it stays out of access logs, CDN logs and Referer headers — and the code in
// the query, which does travel, cannot be redeemed without it.
func HandoffURL(code *HandoffCode, accountID, target string) string {
	u := *code.Redirect
	u.Fragment, u.RawFragment = "", ""
	return u.String() + "#v=" + url.QueryEscape(code.Verifier) +
		"&acct=" + url.QueryEscape(accountID) +
		"&to=" + url.QueryEscape(target)
}

// PlainWebURL is where to send the browser when there is no hand-off to be
// had — no session to hand over, no consent for the web client yet, or the
// authorize call simply failed.
//
// The web app runs its own sign-in from here, which is a worse experience
// exactly once: after consenting, every later hand-off goes through silently.
// accountID preselects the same account this machine is syncing, so the user
// does not land in a different one after signing in.
func PlainWebURL(webURL, target, accountID string) string {
	u := strings.TrimRight(webURL, "/") + target
	if accountID == "" {
		return u
	}
	return u + "?hq_account=" + url.QueryEscape(accountID)
}
