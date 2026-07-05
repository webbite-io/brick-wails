package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"time"
)

// This file talks to brick's local control API (see
// requestbite-brick-cli/cmd/brick/controlapi.go and that repo's
// openapi.yaml). The two repos are deliberately not sharing a Go module for
// this — the coupling is the small, versioned HTTP/JSON protocol below, not
// shared code, so brick-wails and brick-cli can ship independently. If this
// protocol changes, mirror the change on both sides and bump
// controlProtocolVersion in brick-cli's controlapi.go.

const controlSecretHeader = "X-Brick-Control-Secret"

// errBrickNotRunning distinguishes "brick isn't running" from a genuine
// control-API error, since the former is an expected, common state the UI
// needs to render (not a failure to log/alert on).
var errBrickNotRunning = errors.New("brick is not running")

// brickDiscovery mirrors controlDiscovery in brick-cli's controlapi.go.
type brickDiscovery struct {
	PID             int       `json:"pid"`
	Version         string    `json:"version"`
	ProtocolVersion int       `json:"protocolVersion"`
	Transport       string    `json:"transport"`
	Address         string    `json:"address"`
	Token           string    `json:"token"`
	StartedAt       time.Time `json:"startedAt"`
}

// BrickInFlight is the file transfer currently in progress, if any.
type BrickInFlight struct {
	RelPath   string `json:"relPath"`
	Direction string `json:"direction"`
}

// BrickCounters are cumulative counts for the running brick process.
type BrickCounters struct {
	Uploaded   int64 `json:"uploaded"`
	Downloaded int64 `json:"downloaded"`
	Deleted    int64 `json:"deleted"`
}

// BrickStatus is brick's /v1/status response, plus Running (set locally:
// false whenever brick couldn't be reached at all, rather than an HTTP
// error).
type BrickStatus struct {
	State               string         `json:"state"`
	Folder              string         `json:"folder"`
	LastError           string         `json:"lastError,omitempty"`
	LastSyncCompletedAt time.Time      `json:"lastSyncCompletedAt,omitempty"`
	Counters            BrickCounters  `json:"counters"`
	InFlight            *BrickInFlight `json:"inFlight"`
	Running             bool           `json:"running"`
}

// BrickActivityEvent is one entry from brick's /v1/activity feed.
type BrickActivityEvent struct {
	Kind    string    `json:"kind"`
	RelPath string    `json:"relPath"`
	At      time.Time `json:"at"`
}

// BrickAccount is brick's /v1/account response.
type BrickAccount struct {
	AccountID string `json:"accountId"`
	ClientID  string `json:"clientId"`
}

// brickDiscoveryPath returns the per-OS path brick writes its discovery file
// to. Must stay in sync with controlRuntimeDir in brick-cli's controlapi.go.
func brickDiscoveryPath() (string, error) {
	var base string
	switch runtime.GOOS {
	case "linux":
		if v := os.Getenv("XDG_RUNTIME_DIR"); v != "" {
			base = filepath.Join(v, "brick")
		}
	case "darwin":
		if home, err := os.UserHomeDir(); err == nil {
			base = filepath.Join(home, "Library", "Application Support", "brick", "run")
		}
	case "windows":
		if v := os.Getenv("LOCALAPPDATA"); v != "" {
			base = filepath.Join(v, "brick", "run")
		}
	}
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		base = filepath.Join(home, ".config", "brick", "run")
	}
	return filepath.Join(base, "agent.json"), nil
}

// readBrickDiscovery reads and validates the discovery file, treating a
// dead-pid file as absent rather than trusting a leftover from a crash.
func readBrickDiscovery() (*brickDiscovery, error) {
	path, err := brickDiscoveryPath()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, errBrickNotRunning
	}
	var disc brickDiscovery
	if err := json.Unmarshal(data, &disc); err != nil {
		return nil, errBrickNotRunning
	}
	if !processAlive(disc.PID) {
		return nil, errBrickNotRunning
	}
	return &disc, nil
}

// brickRequest makes one request against brick's control socket and decodes
// the JSON response into out (if non-nil). errBrickNotRunning is returned
// whenever brick can't be reached at all, so callers can tell that apart
// from a real error the running process returned.
func brickRequest(method, path string, out any) error {
	disc, err := readBrickDiscovery()
	if err != nil {
		return err
	}

	client := &http.Client{
		Transport: &http.Transport{
			DialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
				return net.Dial("unix", disc.Address)
			},
		},
		Timeout: 3 * time.Second,
	}

	req, err := http.NewRequest(method, "http://unix"+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set(controlSecretHeader, disc.Token)

	resp, err := client.Do(req)
	if err != nil {
		return errBrickNotRunning
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("brick control API %s %s: %s", method, path, resp.Status)
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}

// BrickService is bound to the frontend via application.NewService in
// main.go; every exported method here is callable from TypeScript through
// the generated bindings.
type BrickService struct{}

// Status returns brick's current sync status. When brick isn't running, it
// returns {State: "not-running", Running: false} rather than an error,
// since that's an expected state the tray UI needs to render normally.
func (b *BrickService) Status() (BrickStatus, error) {
	var st BrickStatus
	if err := brickRequest(http.MethodGet, "/v1/status", &st); err != nil {
		if errors.Is(err, errBrickNotRunning) {
			return BrickStatus{State: "not-running"}, nil
		}
		return BrickStatus{}, err
	}
	st.Running = true
	return st, nil
}

// Activity returns the most recent sync events, newest first. Returns an
// empty list (not an error) when brick isn't running.
func (b *BrickService) Activity(limit int) ([]BrickActivityEvent, error) {
	if limit <= 0 {
		limit = 50
	}
	var events []BrickActivityEvent
	if err := brickRequest(http.MethodGet, fmt.Sprintf("/v1/activity?limit=%d", limit), &events); err != nil {
		if errors.Is(err, errBrickNotRunning) {
			return []BrickActivityEvent{}, nil
		}
		return nil, err
	}
	return events, nil
}

// Account returns the logged-in account/client IDs, or a zero value (not an
// error) when brick isn't running.
func (b *BrickService) Account() (BrickAccount, error) {
	var acc BrickAccount
	if err := brickRequest(http.MethodGet, "/v1/account", &acc); err != nil {
		if errors.Is(err, errBrickNotRunning) {
			return BrickAccount{}, nil
		}
		return BrickAccount{}, err
	}
	return acc, nil
}

// Pause stops brick's reconcile loop until Resume is called.
func (b *BrickService) Pause() error {
	return brickRequest(http.MethodPost, "/v1/pause", nil)
}

// Resume clears a pause and immediately wakes brick's reconcile loop.
func (b *BrickService) Resume() error {
	return brickRequest(http.MethodPost, "/v1/resume", nil)
}

// QuitBrick gracefully shuts down the brick sync process itself — distinct
// from quitting this tray app, which just hides the UI and leaves brick's
// sync running.
func (b *BrickService) QuitBrick() error {
	return brickRequest(http.MethodPost, "/v1/quit", nil)
}
