// Package update checks GitHub for a newer brick-wails release than the one
// currently running, and hands off to the platform's install script. Mirrors
// brick-cli's own update check (cmd/brick/main.go), with a longer timeout
// since this one runs silently on every launch rather than at a moment the
// user is already waiting on the CLI.
//
// macOS doesn't use it: there Sparkle checks an appcast and installs the
// update itself (updates_darwin.go in the main package).
package update

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"runtime"
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

// releasesURL lists the newest releases first. Check reads the list rather
// than GitHub's latest release because macOS and Linux are released from
// different machines, so the latest release can hold only a DMG for a while,
// or for good. It's a var (not a const) so tests can point it at a fake
// server.
var releasesURL = "https://api.github.com/repos/webbite-io/brick-wails/releases?per_page=20"

// Info describes an available update.
type Info struct {
	Current string `json:"current"`
	Latest  string `json:"latest"`
}

// Check finds the newest release with a tarball for this platform and
// compares it to current, skipping drafts, pre-releases and releases made for
// other platforms only. It returns a nil Info (with a nil error) when already
// up to date or when no release has this platform's tarball; the caller
// treats that the same as an error — either way, say nothing to the user.
func Check(ctx context.Context, current string) (*Info, error) {
	ctx, cancel := context.WithTimeout(ctx, checkTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, releasesURL, nil)
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

	var releases []struct {
		TagName    string `json:"tag_name"`
		Draft      bool   `json:"draft"`
		Prerelease bool   `json:"prerelease"`
		Assets     []struct {
			Name string `json:"name"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&releases); err != nil {
		return nil, err
	}

	for _, release := range releases {
		version := strings.TrimPrefix(release.TagName, "v")
		if version == "" || release.Draft || release.Prerelease {
			continue
		}
		for _, asset := range release.Assets {
			if asset.Name == tarballName(version) {
				if version == current {
					return nil, nil
				}
				return &Info{Current: current, Latest: version}, nil
			}
		}
	}
	return nil, nil
}

// tarballName is the release artifact install.sh downloads for this
// platform, named as `make release` names it:
// brick-ui-<version>-linux-amd64.tar.gz.
func tarballName(version string) string {
	return fmt.Sprintf("brick-ui-%s-%s-%s.tar.gz", version, runtime.GOOS, runtime.GOARCH)
}
