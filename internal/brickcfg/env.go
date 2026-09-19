package brickcfg

import (
	"os"
	"strings"

	"github.com/joho/godotenv"
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

// LoadDevEnv loads files (in order; an earlier file wins) into the process
// environment. A variable already set to a non-empty value is kept, but an
// *empty* one is treated as unset — plain godotenv.Load skips any existing
// key, so e.g. `export ACC_API_URL=` (as a Makefile can do) would otherwise
// silently hide the value in .env.local. Missing files are ignored.
func LoadDevEnv(files ...string) {
	for _, f := range files {
		vals, err := godotenv.Read(f)
		if err != nil {
			continue
		}
		for k, v := range vals {
			if strings.TrimSpace(os.Getenv(k)) == "" {
				_ = os.Setenv(k, v)
			}
		}
	}
}
