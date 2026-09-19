// Package brickcfg reads and writes brick's config.yaml — the same file (same
// location, same schema) brick-cli uses, so a machine set up by either app
// works in the other.
//
// Ported from brick-cli cmd/brick/config.go @ f3ef7bd. Differences from the
// CLI, all deliberate:
//   - Unknown keys (top-level and per-account) are preserved across a
//     load/save round trip, so a field only one of the two apps knows about
//     is never silently erased by the other.
//   - Store.Update re-reads the file before every save (read-modify-write),
//     so a concurrent edit by the CLI to an unrelated field isn't clobbered
//     by a stale in-memory copy.
//   - BRICK_CONFIG_DIR overrides the directory (dev/test isolation only; the
//     CLI ignores it).
package brickcfg

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"github.com/google/uuid"
	"gopkg.in/yaml.v3"
)

// ConfigDirEnv overrides the config directory. Never set in normal use: an
// overridden directory is not shared with brick-cli.
const ConfigDirEnv = "BRICK_CONFIG_DIR"

// Config mirrors brick-cli's Config. Field names and yaml tags must stay
// identical to the CLI's.
type Config struct {
	ClientID     string `yaml:"clientId"`
	AccessToken  string `yaml:"accessToken,omitempty"`
	RefreshToken string `yaml:"refreshToken,omitempty"`
	IDToken      string `yaml:"idToken,omitempty"`

	// ActiveAccountID is the account currently in effect; it always keys into
	// Accounts.
	ActiveAccountID string `yaml:"activeAccountId,omitempty"`

	// Accounts holds one entry per account ever synced on this device, keyed
	// by account ID.
	Accounts map[string]*AccountConfig `yaml:"accounts,omitempty"`

	// AgentRoots are the directories remote control exposes (global, not
	// per-account).
	AgentRoots []string `yaml:"agentRoots,omitempty"`

	// RemoteControl enables remote file access by default.
	RemoteControl bool `yaml:"remoteControl,omitempty"`

	// Extra holds keys this app doesn't know about, written back verbatim.
	Extra map[string]any `yaml:",inline"`
}

// AccountConfig mirrors brick-cli's AccountConfig.
type AccountConfig struct {
	StorageSyncFolder string `yaml:"storageSyncFolder,omitempty"`

	// ExcludeDirs lists slash-separated folder paths, relative to
	// StorageSyncFolder, that are never uploaded or downloaded.
	ExcludeDirs []string `yaml:"excludeDirs,omitempty"`

	Extra map[string]any `yaml:",inline"`
}

// ActiveAccount returns the AccountConfig for ActiveAccountID, or nil.
func (c *Config) ActiveAccount() *AccountConfig {
	if c.ActiveAccountID == "" {
		return nil
	}
	return c.Accounts[c.ActiveAccountID]
}

// EnsureActiveAccount returns the active account's entry, creating it (and the
// Accounts map) if needed. Returns nil if no account is active.
func (c *Config) EnsureActiveAccount() *AccountConfig {
	if c.ActiveAccountID == "" {
		return nil
	}
	if c.Accounts == nil {
		c.Accounts = map[string]*AccountConfig{}
	}
	ac, ok := c.Accounts[c.ActiveAccountID]
	if !ok || ac == nil {
		ac = &AccountConfig{}
		c.Accounts[c.ActiveAccountID] = ac
	}
	return ac
}

// HasCredentials reports whether any token is stored.
func (c *Config) HasCredentials() bool {
	return c.AccessToken != "" || c.RefreshToken != ""
}

// Dir returns brick's config directory: %AppData%\brick on Windows,
// ~/.config/brick elsewhere (matching brick-cli's configDir), unless
// BRICK_CONFIG_DIR is set.
func Dir() (string, error) {
	if v := strings.TrimSpace(os.Getenv(ConfigDirEnv)); v != "" {
		return v, nil
	}
	return defaultDir(runtime.GOOS)
}

// Isolated reports whether BRICK_CONFIG_DIR points somewhere other than the
// directory brick-cli uses — i.e. this app is deliberately not sharing state
// with the CLI (dev/testing). Per-user runtime files (the control API socket
// and discovery file) then move under the config dir too, so an isolated app
// never touches a real brick-cli's runtime files.
func Isolated() bool {
	v := strings.TrimSpace(os.Getenv(ConfigDirEnv))
	if v == "" {
		return false
	}
	def, err := defaultDir(runtime.GOOS)
	if err != nil {
		return true
	}
	return filepath.Clean(v) != filepath.Clean(def)
}

func defaultDir(goos string) (string, error) {
	if goos == "windows" {
		dir, err := os.UserConfigDir()
		if err != nil {
			return "", fmt.Errorf("could not determine config directory: %w", err)
		}
		return filepath.Join(dir, "brick"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("could not determine home directory: %w", err)
	}
	return filepath.Join(home, ".config", "brick"), nil
}

// Path returns the absolute path of config.yaml.
func Path() (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "config.yaml"), nil
}

// Store serializes access to one config.yaml within this process.
type Store struct {
	path string
	mu   sync.Mutex
}

// NewStore returns a Store for the default config path.
func NewStore() (*Store, error) {
	p, err := Path()
	if err != nil {
		return nil, err
	}
	return &Store{path: p}, nil
}

// NewStoreAt returns a Store for an explicit path (tests).
func NewStoreAt(path string) *Store { return &Store{path: path} }

// Path returns the file this store reads and writes.
func (s *Store) Path() string { return s.path }

// Dir returns the directory containing the config file.
func (s *Store) Dir() string { return filepath.Dir(s.path) }

// ErrNotExist is returned by Load when config.yaml doesn't exist yet.
var ErrNotExist = errors.New("config file does not exist")

// Load reads the config without creating it. Returns ErrNotExist if absent.
func (s *Store) Load() (*Config, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.readLocked()
}

func (s *Store) readLocked() (*Config, error) {
	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrNotExist
		}
		return nil, fmt.Errorf("could not read config file: %w", err)
	}
	var c Config
	if err := yaml.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("could not parse config file: %w", err)
	}
	return &c, nil
}

// LoadOrCreate reads the config, creating it (and its directory) with a fresh
// UUIDv4 clientId if it doesn't exist, and backfilling a missing clientId.
// created reports whether the file was newly created.
func (s *Store) LoadOrCreate() (cfg *Config, created bool, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, err := s.readLocked()
	if errors.Is(err, ErrNotExist) {
		if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
			return nil, false, fmt.Errorf("could not create config directory: %w", err)
		}
		c = &Config{ClientID: uuid.New().String()}
		if err := s.writeLocked(c); err != nil {
			return nil, false, err
		}
		return c, true, nil
	}
	if err != nil {
		return nil, false, err
	}
	if c.ClientID == "" {
		c.ClientID = uuid.New().String()
		if err := s.writeLocked(c); err != nil {
			return nil, false, err
		}
	}
	return c, false, nil
}

// Update re-reads the config from disk (creating it if absent), applies fn and
// saves the result. fn must only change the fields it means to change: every
// other field keeps whatever is on disk right now.
func (s *Store) Update(fn func(c *Config) error) (*Config, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, err := s.readLocked()
	if errors.Is(err, ErrNotExist) {
		if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
			return nil, fmt.Errorf("could not create config directory: %w", err)
		}
		c = &Config{ClientID: uuid.New().String()}
	} else if err != nil {
		return nil, err
	}
	if err := fn(c); err != nil {
		return nil, err
	}
	if err := s.writeLocked(c); err != nil {
		return nil, err
	}
	return c, nil
}

// writeLocked writes c with mode 0600 via a temp file + rename, so a reader
// (possibly brick-cli) never sees a half-written file.
func (s *Store) writeLocked(c *Config) error {
	out, err := yaml.Marshal(c)
	if err != nil {
		return fmt.Errorf("could not marshal config: %w", err)
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, out, 0o600); err != nil {
		return fmt.Errorf("could not write config file: %w", err)
	}
	if err := os.Chmod(tmp, 0o600); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, s.path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("could not write config file: %w", err)
	}
	return nil
}

// ExpandHome expands a leading ~ to the user's home directory.
func ExpandHome(p string) string {
	if strings.HasPrefix(p, "~") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, strings.TrimPrefix(p, "~"))
		}
	}
	return p
}

// DisplayPath renders abs relative to home (prefixed with ~) when under it.
func DisplayPath(abs string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return abs
	}
	if abs == home {
		return "~"
	}
	if rel, relErr := filepath.Rel(home, abs); relErr == nil && !strings.HasPrefix(rel, "..") {
		return filepath.Join("~", rel)
	}
	return abs
}
