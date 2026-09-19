package syncengine

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// SyncEntry records the last-synced state of one file, keyed by its path
// relative to the sync folder. It breaks the download/upload echo loop: a
// freshly downloaded file hashes to LocalHash, and a freshly uploaded one
// carries RemoteEtag. JSON tags are shared with brick-cli's state files.
type SyncEntry struct {
	RelPath    string    `json:"relPath"`
	NodeID     string    `json:"nodeId"`
	RemoteEtag string    `json:"remoteEtag"`
	LocalHash  string    `json:"localHash"`
	LocalSize  int64     `json:"localSize"`
	SyncedAt   time.Time `json:"syncedAt"`
}

// SyncState is the persisted reconciliation index — the same
// sync-state-<accountId>.json file brick-cli reads and writes.
type SyncState struct {
	Folder  string               `json:"folder"`
	Entries map[string]SyncEntry `json:"entries"`
	// Folders is the set of folder paths that have been synced with the
	// server (tells a new local folder apart from one deleted remotely).
	Folders map[string]bool `json:"folders"`
	// FolderIDs maps a synced folder's path to its server node ID, which is
	// stable across moves/renames.
	FolderIDs map[string]string `json:"folderIds"`
	// ServerTime is the check-updates cursor (server clock of the last
	// successful poll).
	ServerTime int64 `json:"serverTime"`
}

// NewState returns an empty state for folder.
func NewState(folder string) *SyncState {
	return &SyncState{Folder: folder, Entries: map[string]SyncEntry{}, Folders: map[string]bool{}, FolderIDs: map[string]string{}}
}

// StatePath returns <configDir>/sync-state-<accountID>.json.
func StatePath(configDir, accountID string) string {
	return filepath.Join(configDir, fmt.Sprintf("sync-state-%s.json", accountID))
}

// LoadState reads the state file, returning an empty state when it is missing
// or unreadable (exactly like brick-cli: a lost index costs a re-scan, never
// data).
func LoadState(path, folder string) *SyncState {
	data, err := os.ReadFile(path)
	if err != nil {
		return NewState(folder)
	}
	var st SyncState
	if err := json.Unmarshal(data, &st); err != nil {
		return NewState(folder)
	}
	if st.Entries == nil {
		st.Entries = map[string]SyncEntry{}
	}
	if st.Folders == nil {
		st.Folders = map[string]bool{}
	}
	if st.FolderIDs == nil {
		st.FolderIDs = map[string]string{}
	}
	st.Folder = folder
	return &st
}

// Save writes the state atomically with mode 0600.
func (st *SyncState) Save(path string) error {
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}
