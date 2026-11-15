package rules

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestApply_new_rule tests that a rule in desired but not in state becomes active.
func TestApply_new_rule(t *testing.T) {
	now := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	grace := 8 * time.Hour

	desired := map[string]Rule{
		"whoami.example.com|": {
			Hostname: "whoami.example.com",
			Service:  "http://whoami:80",
		},
	}

	state := NewState()
	_, active, pending, expired := Apply(state, desired, now, grace)

	require.Len(t, active, 1)
	r := active["whoami.example.com|"]
	assert.Equal(t, "active", r.Status)
	assert.Equal(t, now, r.FirstSeen)
	assert.Nil(t, r.PendingSince)
	assert.Nil(t, r.DeleteAfter)
	assert.Empty(t, pending)
	assert.Empty(t, expired)
}

// TestApply_disappear_pending tests that a rule in state but not in desired becomes pending.
func TestApply_disappear_pending(t *testing.T) {
	now := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	grace := 8 * time.Hour

	state := NewState()
	state.Rules["whoami.example.com|"] = Rule{
		Hostname:  "whoami.example.com",
		Service:   "http://whoami:80",
		Status:    "active",
		FirstSeen: now.Add(-time.Hour),
		LastSeen:  now.Add(-time.Hour),
	}

	desired := map[string]Rule{}

	_, active, pending, expired := Apply(state, desired, now, grace)

	assert.Empty(t, active)
	require.Len(t, pending, 1)
	r := pending["whoami.example.com|"]
	assert.Equal(t, "pending", r.Status)
	require.NotNil(t, r.PendingSince)
	assert.Equal(t, now, *r.PendingSince)
	require.NotNil(t, r.DeleteAfter)
	assert.Equal(t, now.Add(grace), *r.DeleteAfter)
	assert.Empty(t, expired)
}

// TestApply_pending_returned_to_active tests that a rule that was pending returns to active when it reappears.
func TestApply_pending_returned_to_active(t *testing.T) {
	now := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	grace := 8 * time.Hour

	pendingSince := now.Add(-time.Hour)
	deleteAfter := pendingSince.Add(grace)

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

	desired := map[string]Rule{
		"whoami.example.com|": {
			Hostname: "whoami.example.com",
			Service:  "http://whoami:80",
		},
	}

	_, active, pending, _ := Apply(state, desired, now, grace)

	require.Len(t, active, 1)
	r := active["whoami.example.com|"]
	assert.Equal(t, "active", r.Status)
	assert.Nil(t, r.PendingSince)
	assert.Nil(t, r.DeleteAfter)
	assert.Empty(t, pending)
}

// TestApply_expiry tests that a pending rule expires when now >= deleteAfter.
func TestApply_expiry(t *testing.T) {
	now := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	grace := 8 * time.Hour

	pendingSince := now.Add(-9 * time.Hour)
	deleteAfter := pendingSince.Add(grace)

	state := NewState()
	state.Rules["whoami.example.com|"] = Rule{
		Hostname:     "whoami.example.com",
		Service:      "http://whoami:80",
		Status:       "pending",
		FirstSeen:    now.Add(-24 * time.Hour),
		LastSeen:     now.Add(-24 * time.Hour),
		PendingSince: &pendingSince,
		DeleteAfter:  &deleteAfter,
	}

	desired := map[string]Rule{}

	_, active, pending, expired := Apply(state, desired, now, grace)

	assert.Empty(t, active)
	assert.Empty(t, pending)
	require.Len(t, expired, 1)
	assert.Contains(t, expired, "whoami.example.com|")
	// Rule should be removed from state
	assert.NotContains(t, state.Rules, "whoami.example.com|")
}

// TestApply_field_change tests that a rule with changed fields is updated in place.
func TestApply_field_change(t *testing.T) {
	now := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	grace := 8 * time.Hour

	state := NewState()
	state.Rules["whoami.example.com|"] = Rule{
		Hostname:  "whoami.example.com",
		Service:   "http://whoami:80",
		Status:    "active",
		FirstSeen: now.Add(-time.Hour),
		LastSeen:  now.Add(-time.Hour),
	}

	desired := map[string]Rule{
		"whoami.example.com|": {
			Hostname: "whoami.example.com",
			Service:  "http://whoami:8080",
			Path:     "/api",
		},
	}

	_, active, _, _ := Apply(state, desired, now, grace)

	require.Len(t, active, 1)
	r := active["whoami.example.com|"]
	assert.Equal(t, "http://whoami:8080", r.Service)
	assert.Equal(t, "/api", r.Path)
	assert.Equal(t, now.Add(-time.Hour), r.FirstSeen)
}

// TestApply_state_roundtrip tests that state survives a simulated restart.
func TestApply_state_roundtrip(t *testing.T) {
	now := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	grace := 8 * time.Hour

	// First pass: add rule
	state := NewState()
	desired1 := map[string]Rule{
		"whoami.example.com|": {
			Hostname: "whoami.example.com",
			Service:  "http://whoami:80",
		},
	}
	state, _, _, _ = Apply(state, desired1, now, grace)

	// Simulate restart: state is preserved
	// Second pass: rule still desired
	state2, active, _, _ := Apply(state, desired1, now.Add(time.Minute), grace)

	require.Len(t, active, 1)
	r := active["whoami.example.com|"]
	assert.Equal(t, "active", r.Status)
	assert.Equal(t, now, r.FirstSeen)
	_ = state2
}

// TestApply_no_expiry_before_delete_after tests that pending rules don't expire before deleteAfter.
func TestApply_no_expiry_before_delete_after(t *testing.T) {
	now := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	grace := 8 * time.Hour

	pendingSince := now.Add(-1 * time.Hour)
	deleteAfter := pendingSince.Add(grace)

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

	desired := map[string]Rule{}

	// Check before expiry
	beforeExpiry := now.Add(-1 * time.Hour)
	_, active, pending, expired := Apply(state, desired, beforeExpiry, grace)

	require.Len(t, pending, 1)
	assert.Empty(t, expired)
	assert.Empty(t, active)
}
