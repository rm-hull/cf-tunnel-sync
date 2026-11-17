package rules

import (
	"sort"
	"strings"
	"time"
)

// Rule represents a tunnel routing rule with lifecycle state.
type Rule struct {
	Hostname         string `json:"hostname"`
	Path             string `json:"path"`
	Service          string `json:"service"`
	NoTLSVerify      bool   `json:"noTLSVerify"`
	OriginServerName string `json:"originServerName"`
	ZoneName         string `json:"zoneName"`
	ContainerName    string `json:"containerName"`
	SourcePrefix     string `json:"sourcePrefix"`
	SourceIndex      *int   `json:"sourceIndex"` // nil for non-indexed rules

	// Lifecycle fields
	Status       string     `json:"status"` // "active", "pending", "expired"
	FirstSeen    time.Time  `json:"firstSeen"`
	LastSeen     time.Time  `json:"lastSeen"`
	PendingSince *time.Time `json:"pendingSince"` // nil when not pending
	DeleteAfter  *time.Time `json:"deleteAfter"`  // nil when not pending
}

// Key returns the deterministic rule key: lower(hostname) + "|" + path.
// If path is empty, the key is just the lowercased hostname.
func (r Rule) Key() string {
	if r.Path == "" {
		return strings.ToLower(r.Hostname)
	}
	return strings.ToLower(r.Hostname) + "|" + r.Path
}

// IsExpired returns true if the rule has a DeleteAfter time and now has passed it.
func (r Rule) IsExpired(now time.Time) bool {
	return r.DeleteAfter != nil && now.Compare(*r.DeleteAfter) >= 0
}

// applyDesired updates the rule's fields from the desired rule and sets LastSeen.
// It always resets the status to "active" and clears pending fields, regardless of
// the previous status (e.g., dns_error, dns_conflict). Those statuses are informational
// and recomputed from scratch each pass.
func (r Rule) applyDesired(desired Rule, now time.Time) Rule {
	r.Status = "active"
	r.PendingSince = nil
	r.DeleteAfter = nil
	r.Hostname = desired.Hostname
	r.Path = desired.Path
	r.Service = desired.Service
	r.NoTLSVerify = desired.NoTLSVerify
	r.OriginServerName = desired.OriginServerName
	r.ZoneName = desired.ZoneName
	r.ContainerName = desired.ContainerName
	r.SourcePrefix = desired.SourcePrefix
	r.SourceIndex = desired.SourceIndex
	r.LastSeen = now
	return r
}

// transitionNotDesired handles the lifecycle transition when a rule is no longer desired.
// It returns the (possibly updated) rule and a transition string:
//   - "expired" if the rule should be removed
//   - "pending" if it newly transitioned to pending (needs persist)
//   - "keep" if it's still pending but not yet expired (no state change needed)
func (r Rule) transitionNotDesired(now time.Time, grace time.Duration) (Rule, string) {
	if r.Status == "pending" {
		if r.IsExpired(now) {
			return r, "expired"
		}
		// Still pending, not expired — keep as is
		return r, "keep"
	}
	// Active → pending transition
	r.Status = "pending"
	pendingSince := now
	deleteAfter := now.Add(grace)
	r.PendingSince = &pendingSince
	r.DeleteAfter = &deleteAfter
	return r, "pending"
}

// State holds the persisted rule state.
type State struct {
	Version int             `json:"version"`
	Tunnel  TunnelInfo      `json:"tunnel"`
	Rules   map[string]Rule `json:"rules"`
}

// TunnelInfo stores tunnel identification.
type TunnelInfo struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// NewState creates a new empty state with the current version.
func NewState() *State {
	return &State{
		Version: 1,
		Rules:   make(map[string]Rule),
	}
}

// Apply computes the new state and categorizes rules into active, pending, and expired.
// It implements the lifecycle from §6.2:
//
//   - in desired, not in state → new active; firstSeen=now
//   - in desired and state (pending) → back to active; clear pending fields
//   - in state (active), not in desired → pending; pendingSince=now; deleteAfter=now+grace
//   - pending and now >= deleteAfter → expired
//   - in desired, fields changed → updated in place
func Apply(state *State, desired map[string]Rule, now time.Time, grace time.Duration) (*State, map[string]Rule, map[string]Rule, map[string]Rule) {
	if state == nil {
		state = NewState()
	}
	if state.Rules == nil {
		state.Rules = make(map[string]Rule)
	}

	active := make(map[string]Rule)
	pending := make(map[string]Rule)
	expired := make(map[string]Rule)

	// Track which desired keys we've processed
	processed := make(map[string]bool)

	// Process existing state rules
	for key, rule := range state.Rules {
		desiredRule, isDesired := desired[key]

		if isDesired {
			processed[key] = true
			rule = rule.applyDesired(desiredRule, now)
			state.Rules[key] = rule
			active[key] = rule
			continue
		}

		// Rule not in desired — handle lifecycle transition
		rule, transition := rule.transitionNotDesired(now, grace)
		switch transition {
		case "expired":
			expired[key] = rule
			delete(state.Rules, key)
		case "pending":
			pending[key] = rule
			state.Rules[key] = rule
		case "keep":
			// Still pending (not expired) — keep in pending map, no state change
			pending[key] = rule
		}
	}

	// Process new desired rules not in state
	for key, desiredRule := range desired {
		if !processed[key] {
			// New rule
			rule := desiredRule
			rule.Status = "active"
			rule.FirstSeen = now
			rule.LastSeen = now
			state.Rules[key] = rule
			active[key] = rule
		}
	}

	return state, active, pending, expired
}

// IngressEntry represents a single entry in the cloudflared ingress config.
type IngressEntry struct {
	Hostname      string         `json:"hostname"`
	Path          string         `json:"path,omitempty"`
	Service       string         `json:"service"`
	OriginRequest *OriginRequest `json:"originRequest,omitempty"`
}

// OriginRequest holds optional origin request settings.
type OriginRequest struct {
	NoTLSVerify      bool   `json:"noTLSVerify,omitempty"`
	OriginServerName string `json:"originServerName,omitempty"`
}

// RenderIngress produces the ordered ingress entries from active and pending rules.
// Only active and pending rules are included; expired rules are excluded.
//
// Ordering (first match wins in cloudflared):
// 1. Non-wildcard before wildcard
// 2. Within a hostname, rules with path before without
// 3. Longer path first
// 4. Hostname ascending
// 5. Service ascending
//
// Always ends with the catch-all {service: "http_status:404"}.
func RenderIngress(rules map[string]Rule) []IngressEntry {
	var entries []IngressEntry

	for _, rule := range rules {
		// Only include active and pending rules
		if rule.Status != "active" && rule.Status != "pending" {
			continue
		}

		entry := IngressEntry{
			Hostname: rule.Hostname,
			Path:     rule.Path,
			Service:  rule.Service,
		}

		// Only include originRequest when needed
		if rule.NoTLSVerify || rule.OriginServerName != "" {
			entry.OriginRequest = &OriginRequest{
				NoTLSVerify:      rule.NoTLSVerify,
				OriginServerName: rule.OriginServerName,
			}
		}

		entries = append(entries, entry)
	}

	// Sort entries
	sort.SliceStable(entries, func(i, j int) bool {
		a, b := entries[i], entries[j]

		// Non-wildcard before wildcard
		aWildcard := strings.HasPrefix(a.Hostname, "*.")
		bWildcard := strings.HasPrefix(b.Hostname, "*.")
		if aWildcard != bWildcard {
			return !aWildcard
		}

		// Hostname ascending
		if a.Hostname != b.Hostname {
			return a.Hostname < b.Hostname
		}

		// Rules with path before without
		aHasPath := a.Path != ""
		bHasPath := b.Path != ""
		if aHasPath != bHasPath {
			return aHasPath
		}

		// Longer path first (when both have paths)
		if aHasPath && bHasPath {
			if len(a.Path) != len(b.Path) {
				return len(a.Path) > len(b.Path)
			}
			// Same length: lexicographic
			if a.Path != b.Path {
				return a.Path < b.Path
			}
		}

		// Service ascending
		return a.Service < b.Service
	})

	// Append catch-all
	entries = append(entries, IngressEntry{
		Service: "http_status:404",
	})

	return entries
}
