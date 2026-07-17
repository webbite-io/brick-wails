package main

import (
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// brickConfig mirrors the relevant fields of brick-cli's Config (see
// webbite-brick-cli/cmd/brick/config.go) — brick-wails only reads the
// storage sync folder, not the whole schema.
type brickConfig struct {
	StorageSyncFolder string `yaml:"storageSyncFolder"`
}

// brickConfigPath returns ~/.config/brick/config.yaml, which brick-cli writes
// to on every OS (unlike its runtime discovery file, this path isn't
// per-platform — see configPath in brick-cli's config.go).
func brickConfigPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "brick", "config.yaml"), nil
}

// storageSyncFolder returns brick's configured sync folder, or "" if the
// config file doesn't exist or doesn't set storageSyncFolder.
func storageSyncFolder() string {
	path, err := brickConfigPath()
	if err != nil {
		return ""
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var cfg brickConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return ""
	}
	return cfg.StorageSyncFolder
}
