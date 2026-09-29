// Package update checks GitHub for a newer brick-wails release than the one
// currently running, and hands off to the platform's install script. Mirrors
// brick-cli's own update check (cmd/brick/main.go), with a longer timeout
// since this one runs silently on every launch rather than at a moment the
// user is already waiting on the CLI.
package update

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// checkTimeout bounds how long Check waits for GitHub before giving up, so a
// slow or unreachable network never delays startup noticeably.
const checkTimeout = 3 * time.Second

// CheckInterval is how often the app re-checks after the one at launch. A
// tray app can stay running for weeks, so the launch check alone would let a
// release go unnoticed for as long as the machine stays up.
const CheckInterval = 6 * time.Hour

// releaseURL is a var (not a const) so tests can point it at a fake server.
var releaseURL = "https://api.github.com/repos/webbite-io/brick-wails/releases/latest"

// Info describes an available update.
type Info struct {
	Current string `json:"current"`
	Latest  string `json:"latest"`
}

// Check asks GitHub for the latest release and compares it to current. It
// returns a nil Info (with a nil error) when already up to date; the caller
// treats that the same as an error — either way, say nothing to the user.
func Check(ctx context.Context, current string) (*Info, error) {
	ctx, cancel := context.WithTimeout(ctx, checkTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, releaseURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	var release struct {
		TagName string `json:"tag_name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return nil, err
	}

	latest := strings.TrimPrefix(release.TagName, "v")
	if latest == "" || latest == current {
		return nil, nil
	}
	return &Info{Current: current, Latest: latest}, nil
}
