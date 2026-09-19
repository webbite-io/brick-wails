package auth

import "github.com/webbite-io/brick-wails/internal/brickcfg"

// Clear forgets all tokens (in memory and on disk). Test-only.
func (ts *TokenSource) Clear() error {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	ts.access, ts.refresh, ts.id = "", "", ""
	_, err := ts.store.Update(func(c *brickcfg.Config) error {
		c.AccessToken, c.RefreshToken, c.IDToken = "", "", ""
		return nil
	})
	return err
}
