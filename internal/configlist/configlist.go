// Package configlist persists the desktop app's repository list.
//
// The list is the set of .airlock.toml files the dashboard and the repository
// picker watch. It lives in %APPDATA%\git-airlock\desktop.json on Windows
// (os.UserConfigDir() everywhere).
package configlist

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// state is the on-disk format. Config is the pre-0.9 single-repository field
// and is still read so an old file upgrades in place.
type state struct {
	Config  string   `json:"config,omitempty"`
	Configs []string `json:"configs,omitempty"`
}

// DefaultPath returns the state file inside the per-user config directory.
func DefaultPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "git-airlock", "desktop.json"), nil
}

// Load returns every saved config path, in the saved order.
//
// Paths are deliberately not checked here. A config can be temporarily
// unreachable - the app autostarts with Windows, often before a mapped drive
// that holds the repositories is mounted - and dropping such a path would
// silently forget the repository and then overwrite the list on the next save.
// The picker marks unreachable entries instead.
func Load(path string) []string {
	if path == "" {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var st state
	if json.Unmarshal(data, &st) != nil {
		return nil
	}
	if len(st.Configs) == 0 && st.Config != "" {
		return []string{st.Config}
	}
	return st.Configs
}

// Save writes the complete list, creating the directory if needed.
func Save(path string, paths []string) error {
	if path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(state{Configs: paths}, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}
