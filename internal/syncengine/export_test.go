package syncengine

// Test-only accessors.

// FirstSyncPending reports whether the first full pass hasn't completed yet.
func (e *Engine) FirstSyncPending() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.firstSync
}

// State returns a copy of the sync index.
func (e *Engine) State() SyncState {
	e.mu.Lock()
	defer e.mu.Unlock()
	cp := SyncState{Folder: e.state.Folder, ServerTime: e.state.ServerTime,
		Entries: map[string]SyncEntry{}, Folders: map[string]bool{}, FolderIDs: map[string]string{}}
	for k, v := range e.state.Entries {
		cp.Entries[k] = v
	}
	for k, v := range e.state.Folders {
		cp.Folders[k] = v
	}
	for k, v := range e.state.FolderIDs {
		cp.FolderIDs[k] = v
	}
	return cp
}
