package brickcfg

import (
	"os"
	"strings"
)

// Fallbacks used when neither the environment nor compile-time defaults set a
// value — identical to brick-cli's (local dev backend).
const (
	FallbackAPIURL           = "http://localhost:8080"
	FallbackStorageAPIURL    = "http://localhost:8081"
	FallbackOAuthScopes      = "openid email profile accounts offline_access brick:manage"
	FallbackOAuthCallbackURL = "http://localhost:7332/auth/callback"
	FallbackWebURL           = "https://brick.webbite.io"
)

// Defaults are the compile-time values baked in via ldflags (see main.go and
// the Taskfiles). Empty in dev builds.
type Defaults struct {
	APIURL           string
	StorageAPIURL    string
	OAuthClientID    string
	OAuthScopes      string
	OAuthCallbackURL string
	WebURL           string
	HelpURL          string
}

// Env is the fully resolved runtime configuration.
type Env struct {
	APIURL           string
	StorageAPIURL    string
	OAuthClientID    string
	OAuthScopes      string
	OAuthCallbackURL string
	WebURL           string
	HelpURL          string
	Debug            bool
}

// ResolveEnv resolves every setting with brick-cli's precedence: a non-empty
// runtime env var, then the compile-time default, then the dev fallback.
func ResolveEnv(d Defaults) Env {
	pick := func(key, def, fallback string) string {
		if v := strings.TrimSpace(os.Getenv(key)); v != "" {
			return v
		}
		if def != "" {
			return def
		}
		return fallback
	}
	return Env{
		APIURL:           pick("ACC_API_URL", d.APIURL, FallbackAPIURL),
		StorageAPIURL:    pick("STORAGE_API_URL", d.StorageAPIURL, FallbackStorageAPIURL),
		OAuthClientID:    pick("OAUTH_CLIENT_ID", d.OAuthClientID, ""),
		OAuthScopes:      pick("OAUTH_SCOPES", d.OAuthScopes, FallbackOAuthScopes),
		OAuthCallbackURL: pick("OAUTH_CALLBACK_URL", d.OAuthCallbackURL, FallbackOAuthCallbackURL),
		WebURL:           pick("STORAGE_WEB_URL", d.WebURL, FallbackWebURL),
		HelpURL:          pick("STORAGE_HELP_URL", d.HelpURL, ""),
		Debug:            strings.EqualFold(strings.TrimSpace(os.Getenv("DEBUG")), "true"),
	}
}

// DevEnvFiles are loaded (in order, first value wins) only in builds without
// compile-time defaults, mirroring brick-cli, so a stray file can never
// override a production build.
var DevEnvFiles = []string{".env.local", ".env.dev"}

// ShouldLoadDevEnv reports whether dev env files should be loaded.
func ShouldLoadDevEnv(d Defaults) bool { return d.APIURL == "" }
