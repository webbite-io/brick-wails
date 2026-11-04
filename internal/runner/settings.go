package runner

import (
	"errors"
	"slices"
	"strings"

	"github.com/webbite-io/brick-wails/internal/brickcfg"
	"github.com/webbite-io/brick-wails/internal/syncengine"
)

// excludeDirsSettings is remote control's view of the active account's
// excludeDirs during a sync session (brick-cli's syncExcludeDirsSettings): a
// change is saved to config.yaml, then handed to the engine and a reconcile
// pass triggered.
type excludeDirsSettings struct {
	store     *brickcfg.Store
	accountID string
	eng       *syncengine.Engine
	logf      func(format string, args ...any)
}

func (s *excludeDirsSettings) ExcludeDirs() []string {
	cfg, err := s.store.Load()
	if err != nil {
		return nil
	}
	if ac := cfg.Accounts[s.accountID]; ac != nil {
		return slices.Clone(ac.ExcludeDirs)
	}
	return nil
}

func (s *excludeDirsSettings) SetExcludeDirs(dirs []string) error {
	// Saved for the account this session syncs, even if another one has been
	// made active since.
	if _, err := s.store.Update(func(c *brickcfg.Config) error {
		if c.Accounts == nil {
			c.Accounts = map[string]*brickcfg.AccountConfig{}
		}
		ac := c.Accounts[s.accountID]
		if ac == nil {
			return errors.New("the account being synced is no longer configured")
		}
		ac.ExcludeDirs = dirs
		return nil
	}); err != nil {
		return err
	}

	s.eng.QueueExcludeDirs(dirs)
	s.eng.Notify()
	if len(dirs) == 0 {
		s.logf("Excluded folders updated remotely: none")
	} else {
		s.logf("Excluded folders updated remotely: %s", strings.Join(dirs, ", "))
	}
	return nil
}
