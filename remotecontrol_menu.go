package main

import (
	"errors"
	"log"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/wailsapp/wails/v3/pkg/application"

	"github.com/webbite-io/brick-wails/internal/brickcfg"
)

// How much of a root's path its label in the tray keeps, in characters, not
// counting the "..." that marks a cut. Tray menus are as wide as their widest
// item, so a deep path would stretch the whole menu; past this it's cut from
// the left (see rootLabel).
const rootLabelKeep = 20

const (
	remoteControlLabel = "Remote Control"
	readOnlyLabel      = "Read-Only"
	readWriteLabel     = "Read/Write"
)

// remoteControlMenu is the tray's remote control section: a toggle for
// config.yaml's remoteControl, then one submenu per agentRoots entry to
// choose between read-only and read/write access to it. The roots stay
// listed while remote control is off, dimmed. The state is read from
// config.yaml rather than kept here, so a change made by the setup wizard or
// brick-cli shows on the next refresh.
type remoteControlMenu struct {
	store  *brickcfg.Store
	logger *log.Logger
	// apply hands a change to the running remote-file agent.
	apply func() error
	// relayout re-sets the tray menu once the section's items have changed,
	// which happens when roots are added or removed.
	relayout func()

	mu     sync.Mutex
	toggle *application.MenuItem
	roots  []*rootMenuItem
	shown  *remoteControlState
}

type rootMenuItem struct {
	path                string
	item                *application.MenuItem
	readOnly, readWrite *application.MenuItem
}

// remoteControlState is the slice of config.yaml the section shows.
type remoteControlState struct {
	enabled bool
	roots   []rootState
}

type rootState struct {
	path     string
	readOnly bool
}

func newRemoteControlMenu(store *brickcfg.Store, logger *log.Logger, apply func() error) *remoteControlMenu {
	m := &remoteControlMenu{store: store, logger: logger, apply: apply}
	m.toggle = newCheckMenuItem(remoteControlLabel)
	m.toggle.OnClick(func(*application.Context) {
		m.update(func(c *brickcfg.Config) string {
			c.RemoteControl = !c.RemoteControl
			if c.RemoteControl {
				return "Remote control turned on from the tray"
			}
			return "Remote control turned off from the tray"
		})
	})
	// The first call builds the items the tray menu starts with; there is
	// nothing to replace yet.
	m.refreshLocked(m.load(), false)
	return m
}

// menu returns the section's items, in order, for the tray menu.
func (m *remoteControlMenu) menu() *application.Menu {
	m.mu.Lock()
	defer m.mu.Unlock()
	var roots []*application.MenuItem
	for _, r := range m.roots {
		roots = append(roots, r.item)
	}
	return application.NewMenuFromItems(m.toggle, roots...)
}

// refresh brings the section in step with config.yaml. force redraws every
// item rather than only those whose state changed.
func (m *remoteControlMenu) refresh(force bool) {
	st := m.load()
	m.mu.Lock()
	stale, relayout := m.refreshLocked(st, force)
	m.mu.Unlock()
	if !relayout {
		return
	}
	if m.relayout != nil {
		m.relayout()
	}
	// Only once the tray has let go of them: on macOS this releases the
	// native item.
	for _, r := range stale {
		r.item.Destroy()
	}
}

func (m *remoteControlMenu) load() remoteControlState {
	cfg, err := m.store.Load()
	if err != nil {
		if !errors.Is(err, brickcfg.ErrNotExist) {
			m.logger.Printf("remote control menu: %v", err)
		}
		return remoteControlState{}
	}
	st := remoteControlState{enabled: cfg.RemoteControl}
	for _, r := range cfg.AgentRoots {
		if i := slices.IndexFunc(st.roots, func(s rootState) bool { return s.path == r.Path }); i >= 0 {
			// Listed twice: read-only wins, as in agent.ResolveRoots.
			st.roots[i].readOnly = st.roots[i].readOnly || r.IsReadOnly()
			continue
		}
		st.roots = append(st.roots, rootState{path: r.Path, readOnly: r.IsReadOnly()})
	}
	return st
}

// refreshLocked shows st. When the roots changed it reports that the tray
// menu needs re-setting, and returns the items it replaced for the caller to
// destroy once it has been. Only what changed is touched: this runs every couple of
// seconds, and each change costs a tray refresh.
func (m *remoteControlMenu) refreshLocked(st remoteControlState, force bool) (stale []*rootMenuItem, relayout bool) {
	prev := m.shown
	m.shown = &st
	samePaths := prev != nil && slices.EqualFunc(prev.roots, st.roots, func(a, b rootState) bool { return a.path == b.path })
	if !samePaths {
		stale, relayout = m.roots, prev != nil
		m.roots = nil
		for _, r := range st.roots {
			m.roots = append(m.roots, m.newRootItem(r.path))
		}
	}
	redraw := force || !samePaths
	if redraw || prev.enabled != st.enabled {
		setMenuItemCheck(m.toggle, remoteControlLabel, st.enabled)
		for _, r := range m.roots {
			r.item.SetEnabled(st.enabled)
		}
	}
	for i, r := range st.roots {
		if !redraw && prev.roots[i].readOnly == r.readOnly {
			continue
		}
		setMenuItemCheck(m.roots[i].readOnly, readOnlyLabel, r.readOnly)
		setMenuItemCheck(m.roots[i].readWrite, readWriteLabel, !r.readOnly)
	}
	return stale, relayout
}

func (m *remoteControlMenu) newRootItem(path string) *rootMenuItem {
	sub := application.NewMenu()
	r := &rootMenuItem{
		path:      path,
		item:      application.NewSubmenu(rootLabel(path, rootLabelKeep), sub),
		readOnly:  newCheckMenuItem(readOnlyLabel),
		readWrite: newCheckMenuItem(readWriteLabel),
	}
	sub.Append(application.NewMenuFromItems(r.readOnly, r.readWrite))
	r.readOnly.OnClick(func(*application.Context) { m.setReadOnly(path, true) })
	r.readWrite.OnClick(func(*application.Context) { m.setReadOnly(path, false) })
	return r
}

func (m *remoteControlMenu) setReadOnly(path string, readOnly bool) {
	m.update(func(c *brickcfg.Config) string {
		found := false
		for i := range c.AgentRoots {
			if c.AgentRoots[i].Path != path {
				continue
			}
			found = true
			// Read-only is the default, left unset so the entry keeps the
			// plain-path form.
			if readOnly {
				c.AgentRoots[i].ReadOnly = nil
			} else {
				c.AgentRoots[i].ReadOnly = new(bool)
			}
		}
		if !found {
			return ""
		}
		if readOnly {
			return "Remote control access to " + brickcfg.DisplayPath(path) + " set to read-only from the tray"
		}
		return "Remote control access to " + brickcfg.DisplayPath(path) + " set to read/write from the tray"
	})
}

// update saves fn's change to config.yaml, hands it to the running agent and
// refreshes the section. fn returns the line to log, or "" when it changed
// nothing.
func (m *remoteControlMenu) update(fn func(c *brickcfg.Config) string) {
	var msg string
	_, err := m.store.Update(func(c *brickcfg.Config) error {
		msg = fn(c)
		return nil
	})
	switch {
	case err != nil:
		m.logger.Printf("remote control menu: %v", err)
	case msg != "":
		m.logger.Print(msg)
		if err := m.apply(); err != nil {
			m.logger.Printf("remote control: %v", err)
		}
	}
	// Forced, to put back a check the platform flipped by itself (macOS's
	// checkbox items) when that isn't what was saved.
	m.refresh(true)
}

// rootLabel is how the tray names the root at path: under the home folder it
// starts with ~, but the home folder itself is written out in full, since a
// lone "~" says little. Past keep characters it's cut from the left, keeping
// whole folders where it can and marking the cut with "...":
// "~/a/b/c/blund" becomes ".../c/blund". A last folder too long on its own is
// cut mid-name.
func rootLabel(path string, keep int) string {
	abs := brickcfg.ExpandHome(path)
	if path = brickcfg.DisplayPath(abs); path == "~" {
		path = abs
	}
	const ellipsis = "..."
	r := []rune(path)
	if len(r) <= keep+len(ellipsis) {
		return path
	}
	sep := string(filepath.Separator)
	parts := strings.Split(path, sep)
	tail := ""
	for i := len(parts) - 1; i > 0; i-- {
		next := sep + parts[i] + tail
		if len([]rune(next)) > keep {
			break
		}
		tail = next
	}
	if tail == "" {
		tail = string(r[len(r)-keep:])
	}
	return ellipsis + tail
}
