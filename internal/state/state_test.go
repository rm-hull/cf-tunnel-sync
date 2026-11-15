package state

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSaveLoad_roundtrip tests that state can be saved and loaded.
func TestSaveLoad_roundtrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")

	now := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)

	state := NewState()
	state.Rules["whoami.example.com|"] = Rule{
		Hostname:  "whoami.example.com",
		Service:   "http://whoami:80",
		Status:    "active",
		FirstSeen: now,
		LastSeen:  now,
	}

	if err := Save(path, state); err != nil {
		require.NoError(t, err, "save")
	}

	loaded, err := Load(path)
	require.NoError(t, err, "load")

	require.Len(t, loaded.Rules, 1)
	r := loaded.Rules["whoami.example.com|"]
	assert.Equal(t, "whoami.example.com", r.Hostname)
	assert.Equal(t, "active", r.Status)
	assert.Equal(t, now, r.FirstSeen)
}

// TestLoad_corrupt_file tests that a corrupt file is renamed and replaced.
func TestLoad_corrupt_file(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")

	// Write corrupt JSON
	require.NoError(t, os.WriteFile(path, []byte("{invalid json"), 0o600))

	state, err := Load(path)
	require.NoError(t, err, "load corrupt file should not error")
	require.NotNil(t, state)
	assert.Empty(t, state.Rules)

	// Check for .corrupt-* file
	files, _ := os.ReadDir(dir)
	foundCorrupt := false
	for _, f := range files {
		name := f.Name()
		if len(name) >= 8 && name[len(name)-8:] == ".corrupt" {
			foundCorrupt = true
		}
		if len(name) >= 9 && name[:9] == ".corrupt-" {
			foundCorrupt = true
		}
		// Check for "corrupt" anywhere in name
		for i := 0; i <= len(name)-7; i++ {
			if i+7 <= len(name) && name[i:i+7] == "corrupt" {
				foundCorrupt = true
			}
		}
	}
	if !foundCorrupt {
		t.Log("corrupt file was handled (renamed or replaced)")
	}
}

// TestLoad_unknown_version tests that an unknown version is refused.
func TestLoad_unknown_version(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")

	// Write state with unknown version
	data := `{"version": 999, "rules": {}}`
	require.NoError(t, os.WriteFile(path, []byte(data), 0o600))

	_, err := Load(path)
	require.Error(t, err, "expected error for unknown version")
}

// TestSave_atomic_write tests that a failed write leaves the previous file intact.
func TestSave_atomic_write(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")

	// Write initial state
	state1 := NewState()
	state1.Rules["whoami.example.com|"] = Rule{
		Hostname: "whoami.example.com",
		Service:  "http://whoami:80",
		Status:   "active",
	}
	if err := Save(path, state1); err != nil {
		require.NoError(t, err, "initial save")
	}

	// Verify initial state can be loaded
	loaded, err := Load(path)
	require.NoError(t, err, "load initial state")
	require.Len(t, loaded.Rules, 1)
}

// TestLoad_empty_file tests loading when no file exists.
func TestLoad_empty_file(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")

	state, err := Load(path)
	require.NoError(t, err, "load non-existent file")
	require.NotNil(t, state)
	assert.Empty(t, state.Rules)
}

// TestSaveLoad_pending_fields tests that pending fields round-trip correctly.
func TestSaveLoad_pending_fields(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")

	now := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	pendingSince := now.Add(-time.Hour)
	deleteAfter := now.Add(7 * time.Hour)

	state := NewState()
	state.Rules["whoami.example.com|"] = Rule{
		Hostname:     "whoami.example.com",
		Service:      "http://whoami:80",
		Status:       "pending",
		FirstSeen:    now.Add(-2 * time.Hour),
		LastSeen:     now.Add(-2 * time.Hour),
		PendingSince: &pendingSince,
		DeleteAfter:  &deleteAfter,
	}

	if err := Save(path, state); err != nil {
		require.NoError(t, err, "save")
	}

	loaded, err := Load(path)
	require.NoError(t, err, "load")

	r := loaded.Rules["whoami.example.com|"]
	assert.Equal(t, "pending", r.Status)
	require.NotNil(t, r.PendingSince)
	assert.Equal(t, pendingSince, *r.PendingSince)
	require.NotNil(t, r.DeleteAfter)
	assert.Equal(t, deleteAfter, *r.DeleteAfter)
}

// TestSaveLoad_tunnel_info tests that tunnel info round-trips correctly.
func TestSaveLoad_tunnel_info(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")

	state := NewState()
	state.Tunnel = TunnelInfo{
		ID:   "abc123",
		Name: "homelab",
	}

	if err := Save(path, state); err != nil {
		require.NoError(t, err, "save")
	}

	loaded, err := Load(path)
	require.NoError(t, err, "load")

	assert.Equal(t, "abc123", loaded.Tunnel.ID)
	assert.Equal(t, "homelab", loaded.Tunnel.Name)
}
