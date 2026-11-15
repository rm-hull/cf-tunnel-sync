package state

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/rm-hull/cf-tunnel-sync/internal/rules"
)

const currentVersion = 1

// State is an alias for rules.State to avoid circular imports.
// We re-export the types from rules package.
type (
	State      = rules.State
	Rule       = rules.Rule
	TunnelInfo = rules.TunnelInfo
)

// NewState creates a new empty state.
var NewState = rules.NewState

// Save writes state to path atomically. It writes to a temp file first,
// then renames it to the target path. If the write fails, the previous
// file is left intact.
func Save(path string, state *State) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create state dir: %w", err)
	}

	// Write to temp file first
	tmpPath := path + ".tmp"
	f, err := os.OpenFile(tmpPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return fmt.Errorf("open temp file: %w", err)
	}

	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	if err := enc.Encode(state); err != nil {
		f.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("encode state: %w", err)
	}

	if err := f.Close(); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("close temp file: %w", err)
	}

	// Atomic rename
	if err := os.Rename(tmpPath, path); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("rename temp file: %w", err)
	}

	return nil
}

// Load reads state from path. If the file doesn't exist, it returns an empty state.
// If the file is corrupt, it renames it to .corrupt-<timestamp> and returns an empty state.
// If the version is unknown, it returns an error.
func Load(path string) (*State, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return rules.NewState(), nil
		}
		return nil, fmt.Errorf("read state file: %w", err)
	}

	// Check version first (minimal parse)
	var raw struct {
		Version int `json:"version"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		// Corrupt file
		renameCorrupt(path)
		return rules.NewState(), nil
	}

	if raw.Version != currentVersion {
		return nil, fmt.Errorf("unknown state version %d (expected %d)", raw.Version, currentVersion)
	}

	var state State
	if err := json.Unmarshal(data, &state); err != nil {
		// Corrupt file
		renameCorrupt(path)
		return rules.NewState(), nil
	}

	if state.Rules == nil {
		state.Rules = make(map[string]Rule)
	}

	return &state, nil
}

// renameCorrupt renames a corrupt state file to preserve it for debugging.
func renameCorrupt(path string) {
	ts := time.Now().Format("20060102T150405")
	corruptPath := path + ".corrupt-" + ts
	if err := os.Rename(path, corruptPath); err != nil {
		// If rename fails, try to remove
		os.Remove(path)
	}
}

// ListCorruptFiles returns any .corrupt-* files in the same directory as path.
func ListCorruptFiles(path string) []string {
	dir := filepath.Dir(path)
	base := filepath.Base(path)

	files, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}

	var corrupt []string
	prefix := base + ".corrupt-"
	for _, f := range files {
		if strings.HasPrefix(f.Name(), prefix) {
			corrupt = append(corrupt, filepath.Join(dir, f.Name()))
		}
	}

	return corrupt
}
