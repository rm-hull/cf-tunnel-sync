// Package cf provides a fake Cloudflare API for testing.
package cf

import (
	"context"
	"strings"

	"github.com/rm-hull/cf-tunnel-sync/internal/rules"
)

// Fake implements API for testing.
type Fake struct {
	Tunnels     map[string]Tunnel
	Zones       []Zone
	DNSRecords  map[string][]DNSRecord   // zoneID -> records
	Ingress     map[string]IngressConfig // tunnelID -> config
	Connections map[string]int           // tunnelID -> count
	Tokens      map[string]string        // tunnelID -> token

	// Track calls for verification
	TunnelLookups   int
	ZoneLookups     int
	DNSListCalls    int
	IngressGetCalls int
	IngressPutCalls int

	// Stats tracking
	stats Stats
}

// NewFake creates a new Fake API.
func NewFake() *Fake {
	return &Fake{
		Tunnels:     make(map[string]Tunnel),
		DNSRecords:  make(map[string][]DNSRecord),
		Ingress:     make(map[string]IngressConfig),
		Connections: make(map[string]int),
		Tokens:      make(map[string]string),
		stats: Stats{
			ByResource: make(map[string]int),
		},
	}
}

// AddTunnel adds a tunnel to the fake.
func (f *Fake) AddTunnel(t Tunnel) {
	f.Tunnels[t.ID] = t
}

// AddZone adds a zone to the fake.
func (f *Fake) AddZone(z Zone) {
	f.Zones = append(f.Zones, z)
}

// AddDNSRecord adds a DNS record to the fake.
func (f *Fake) AddDNSRecord(zoneID string, r DNSRecord) {
	f.DNSRecords[zoneID] = append(f.DNSRecords[zoneID], r)
}

// SetIngress sets the ingress config for a tunnel.
func (f *Fake) SetIngress(tunnelID string, cfg IngressConfig) {
	f.Ingress[tunnelID] = cfg
}

// SetConnections sets the connection count for a tunnel.
func (f *Fake) SetConnections(tunnelID string, count int) {
	f.Connections[tunnelID] = count
}

// SetToken sets the tunnel token for a tunnel.
func (f *Fake) SetToken(tunnelID, token string) {
	f.Tokens[tunnelID] = token
}

// FindTunnel finds a tunnel by name.
func (f *Fake) FindTunnel(_ context.Context, name string) (Tunnel, error) {
	f.TunnelLookups++
	f.stats.Calls++
	f.stats.ByResource["tunnels"]++

	var matches []Tunnel
	for _, t := range f.Tunnels {
		if strings.EqualFold(t.Name, name) {
			matches = append(matches, t)
		}
	}

	if len(matches) == 0 {
		return Tunnel{}, ErrNotFound
	}
	if len(matches) > 1 {
		return Tunnel{}, ErrMultipleMatches
	}

	t := matches[0]
	if t.ConfigSrc != "cloudflare" {
		return Tunnel{}, ErrConfigSrcLocal
	}
	return t, nil
}

// GetTunnel gets a tunnel by ID.
func (f *Fake) GetTunnel(_ context.Context, id string) (Tunnel, error) {
	f.stats.Calls++
	f.stats.ByResource["tunnels"]++

	t, ok := f.Tunnels[id]
	if !ok {
		return Tunnel{}, ErrNotFound
	}
	return t, nil
}

// GetConnections returns the number of active connections.
func (f *Fake) GetConnections(_ context.Context, tunnelID string) (int, error) {
	f.stats.Calls++
	f.stats.ByResource["connections"]++

	count, ok := f.Connections[tunnelID]
	if !ok {
		return 0, nil
	}
	return count, nil
}

// GetTunnelToken returns the tunnel token for a tunnel.
func (f *Fake) GetTunnelToken(_ context.Context, tunnelID string) (string, error) {
	f.stats.Calls++
	f.stats.ByResource["tunnels"]++

	token, ok := f.Tokens[tunnelID]
	if !ok {
		return "", ErrNotFound
	}
	return token, nil
}

// ListZones returns all zones.
func (f *Fake) ListZones(_ context.Context) ([]Zone, error) {
	f.ZoneLookups++
	f.stats.Calls++
	f.stats.ByResource["zones"]++

	return f.Zones, nil
}

// GetZone looks up a zone by ID.
func (f *Fake) GetZone(_ context.Context, zoneID string) (Zone, error) {
	f.stats.Calls++
	f.stats.ByResource["zones"]++

	for _, z := range f.Zones {
		if z.ID == zoneID {
			return z, nil
		}
	}
	return Zone{}, ErrNotFound
}

// ResolveZone resolves a hostname to a zone ID.
func (f *Fake) ResolveZone(ctx context.Context, hostname, zonename, fallbackZoneID string) (string, error) {
	zones, err := f.ListZones(ctx)
	if err != nil {
		return "", err
	}

	resolver := NewZoneResolver(fallbackZoneID)
	resolver.SetZones(zones)
	return resolver.Resolve(hostname, zonename)
}

// GetIngress returns the current ingress configuration.
func (f *Fake) GetIngress(_ context.Context, tunnelID string) (IngressConfig, error) {
	f.IngressGetCalls++
	f.stats.Calls++
	f.stats.ByResource["ingress"]++

	cfg, ok := f.Ingress[tunnelID]
	if !ok {
		return IngressConfig{}, nil
	}
	return cfg, nil
}

// PutIngress updates the ingress configuration.
func (f *Fake) PutIngress(_ context.Context, tunnelID string, config IngressConfig) (int64, error) {
	f.IngressPutCalls++
	f.stats.Calls++
	f.stats.ByResource["ingress"]++

	existing := f.Ingress[tunnelID]
	config.Version = existing.Version + 1
	f.Ingress[tunnelID] = config
	return config.Version, nil
}

// ListDNSRecords lists DNS records for a zone.
func (f *Fake) ListDNSRecords(_ context.Context, zoneID, name, recordType string) ([]DNSRecord, error) {
	f.DNSListCalls++
	f.stats.Calls++
	f.stats.ByResource["dns"]++

	records, ok := f.DNSRecords[zoneID]
	if !ok {
		return nil, nil
	}

	var result []DNSRecord
	for _, r := range records {
		if name != "" && r.Name != name {
			continue
		}
		if recordType != "" && r.Type != recordType {
			continue
		}
		result = append(result, r)
	}
	return result, nil
}

// CreateDNSRecord creates a new DNS record.
func (f *Fake) CreateDNSRecord(_ context.Context, record DNSRecord) (string, error) {
	f.stats.Calls++
	f.stats.ByResource["dns"]++

	// Generate a simple ID
	id := record.Name + "." + record.Type
	f.DNSRecords[record.ZoneID] = append(f.DNSRecords[record.ZoneID], record)
	return id, nil
}

// DeleteDNSRecord deletes a DNS record.
func (f *Fake) DeleteDNSRecord(_ context.Context, recordID string) error {
	f.stats.Calls++
	f.stats.ByResource["dns"]++

	for zoneID, records := range f.DNSRecords {
		var filtered []DNSRecord
		for _, r := range records {
			if r.ID != recordID {
				filtered = append(filtered, r)
			}
		}
		if len(filtered) < len(records) {
			f.DNSRecords[zoneID] = filtered
			if len(filtered) == 0 {
				delete(f.DNSRecords, zoneID)
			}
			return nil
		}
	}
	return nil
}

// Plan performs a dry-run reconciliation.
func (f *Fake) Plan(ctx context.Context, tunnelID string, desiredRules map[string]rules.Rule,
	state *rules.State, overwriteConflicts bool,
) (PlanResult, error) {
	f.stats.Calls++
	f.stats.ByResource["plan"]++

	tunnel, err := f.GetTunnel(ctx, tunnelID)
	if err != nil {
		return PlanResult{}, err
	}

	// Get remote ingress
	remoteCfg, err := f.GetIngress(ctx, tunnelID)
	if err != nil {
		return PlanResult{}, err
	}

	// Render local ingress
	localIngress := rules.RenderIngress(desiredRules)

	// Build plan result
	result := PlanResult{
		Tunnel:        tunnel,
		RemoteIngress: remoteCfg.Rules,
		LocalIngress:  localIngress,
	}

	// ingressKey builds a comparison key for an ingress entry.
	ingressKey := func(hostname, path string) string {
		if path == "" {
			return hostname
		}
		return hostname + "|" + path
	}

	// Compare ingress rules
	remoteKeys := make(map[string]bool)
	for _, r := range remoteCfg.Rules {
		key := ingressKey(r.Hostname, r.Path)
		remoteKeys[key] = true
	}

	localKeys := make(map[string]bool)
	for _, r := range localIngress {
		key := ingressKey(r.Hostname, r.Path)
		localKeys[key] = true
	}

	// Rules to add or update
	for _, r := range localIngress {
		key := ingressKey(r.Hostname, r.Path)
		if !remoteKeys[key] {
			result.IngressActions = append(result.IngressActions, PlanAction{
				Kind:    "add",
				RuleKey: key,
				Detail:  r.Hostname + " -> " + r.Service,
			})
		}
		localKeys[key] = true
	}

	// Rules to remove (remote-only)
	for key := range remoteKeys {
		if !localKeys[key] {
			// Check if it's the catch-all (no hostname, just http_status)
			if !strings.Contains(key, ".") {
				continue
			}
			result.IngressActions = append(result.IngressActions, PlanAction{
				Kind:    "remove",
				RuleKey: key,
				Detail:  "not from labels",
			})
		}
	}

	// DNS actions
	for key, rule := range desiredRules {
		zoneID := ""
		if state != nil {
			if sr, ok := state.Rules[key]; ok && sr.ZoneName != "" {
				for _, z := range f.Zones {
					if z.Name == sr.ZoneName {
						zoneID = z.ID
						break
					}
				}
			}
		}

		// Try to resolve zone
		if zoneID == "" {
			for _, z := range f.Zones {
				if rule.ZoneName != "" && z.Name == rule.ZoneName {
					zoneID = z.ID
					break
				}
			}
		}

		if zoneID == "" {
			// Try longest suffix match
			h := rule.Hostname
			for _, z := range f.Zones {
				zName := z.Name
				if h == zName || (len(h) > len(zName) && h[len(h)-len(zName)-1:] == "."+zName) {
					zoneID = z.ID
					break
				}
			}
		}

		if zoneID == "" {
			result.DNSActions = append(result.DNSActions, PlanAction{
				Kind:    "dns_no_zone",
				RuleKey: key,
				Detail:  rule.Hostname + ": no zone found",
			})
			continue
		}

		// Check existing DNS records
		records, _ := f.ListDNSRecords(ctx, zoneID, rule.Hostname, "CNAME")
		found := false
		for _, rec := range records {
			if rec.Content == tunnel.ID+".cfargotunnel.com" {
				result.DNSActions = append(result.DNSActions, PlanAction{
					Kind:    "dns_adopt",
					RuleKey: key,
					Detail:  rule.Hostname + " -> " + rec.Content,
				})
				found = true
				break
			} else if !overwriteConflicts {
				result.DNSActions = append(result.DNSActions, PlanAction{
					Kind:    "dns_conflict",
					RuleKey: key,
					Detail:  rule.Hostname + " points to " + rec.Content,
				})
				found = true
				break
			}
		}
		if !found {
			result.DNSActions = append(result.DNSActions, PlanAction{
				Kind:    "dns_create",
				RuleKey: key,
				Detail:  rule.Hostname + " -> " + tunnel.ID + ".cfargotunnel.com",
			})
		}
	}

	// Orphan DNS records
	for _, z := range f.Zones {
		records, _ := f.ListDNSRecords(ctx, z.ID, "", "CNAME")
		for _, rec := range records {
			if strings.HasSuffix(rec.Content, ".cfargotunnel.com") {
				// Check if any rule uses this hostname
				isOrphan := true
				for _, rule := range desiredRules {
					if rule.Hostname == rec.Name {
						isOrphan = false
						break
					}
				}
				if isOrphan {
					rec.ZoneName = z.Name
					result.OrphanDNS = append(result.OrphanDNS, rec)
				}
			}
		}
	}

	return result, nil
}

// GetStats returns API call statistics.
func (f *Fake) GetStats() Stats {
	return f.stats
}
