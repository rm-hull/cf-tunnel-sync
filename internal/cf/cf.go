// Package cf provides a wrapper around the Cloudflare API for tunnel,
// DNS, and zone operations.
package cf

import (
	"context"
	"errors"
	"time"

	"github.com/rm-hull/cf-tunnel-sync/internal/rules"
)

var (
	// ErrNotFound is returned when a resource is not found.
	ErrNotFound = errors.New("not found")

	// ErrMultipleMatches is returned when multiple tunnels match the query.
	ErrMultipleMatches = errors.New("multiple tunnels match")

	// ErrConfigSrcLocal is returned when a tunnel's config_src is "local".
	ErrConfigSrcLocal = errors.New("tunnel config_src is local; expected cloudflare")

	// ErrReadOnly is returned when a mutating method is called in read-only mode.
	ErrReadOnly = errors.New("read-only mode; mutating operations not allowed")
)

// Tunnel represents a Cloudflare Tunnel.
type Tunnel struct {
	ID          string
	Name        string
	ConfigSrc   string // "cloudflare" or "local"
	Status      string
	Connections int
}

// Zone represents a Cloudflare DNS zone.
type Zone struct {
	ID   string
	Name string
}

// DNSRecord represents a Cloudflare DNS record.
type DNSRecord struct {
	ID       string
	Name     string
	Type     string
	Content  string
	Proxied  bool
	ZoneID   string
	ZoneName string
}

// IngressConfig represents the remote tunnel ingress configuration.
type IngressConfig struct {
	Version int64
	Rules   []rules.IngressEntry
}

// PlanAction represents a planned change.
type PlanAction struct {
	Kind    string // "add", "update", "remove", "dns_create", "dns_adopt", "dns_conflict", "dns_orphan"
	RuleKey string
	Detail  string
}

// PlanResult holds the result of a plan (dry-run) reconciliation.
type PlanResult struct {
	Tunnel         Tunnel
	IngressActions []PlanAction
	DNSActions     []PlanAction
	OrphanDNS      []DNSRecord
	RemoteIngress  []rules.IngressEntry
	LocalIngress   []rules.IngressEntry
}

// Stats holds Cloudflare API call statistics.
type Stats struct {
	Calls      int
	Errors     int
	ByResource map[string]int
}

// API is the interface for Cloudflare operations.
type API interface {
	// FindTunnel finds a tunnel by name. Returns ErrNotFound if none,
	// ErrMultipleMatches if more than one, ErrConfigSrcLocal if config_src != "cloudflare".
	FindTunnel(ctx context.Context, name string) (Tunnel, error)

	// GetTunnel gets a tunnel by ID.
	GetTunnel(ctx context.Context, id string) (Tunnel, error)

	// GetConnections returns the number of active connections for a tunnel.
	GetConnections(ctx context.Context, tunnelID string) (int, error)

	// GetTunnelToken returns the tunnel token/secret for running cloudflared.
	GetTunnelToken(ctx context.Context, tunnelID string) (string, error)

	// ListZones returns all zones for the account.
	ListZones(ctx context.Context) ([]Zone, error)

	// GetZone looks up a zone by ID.
	GetZone(ctx context.Context, zoneID string) (Zone, error)

	// ResolveZone resolves a hostname to a zone ID.
	// It tries: zonename label → longest-suffix match → fallback zone ID.
	// Returns ("", ErrNotFound) if no zone can be resolved.
	ResolveZone(ctx context.Context, hostname, zonename, fallbackZoneID string) (string, error)

	// GetIngress returns the current remote ingress configuration.
	GetIngress(ctx context.Context, tunnelID string) (IngressConfig, error)

	// PutIngress updates the remote ingress configuration.
	// Returns the new version. In read-only mode, returns ErrReadOnly.
	PutIngress(ctx context.Context, tunnelID string, config IngressConfig) (int64, error)

	// ListDNSRecords lists DNS records for a zone, filtered by name and type.
	ListDNSRecords(ctx context.Context, zoneID, name, recordType string) ([]DNSRecord, error)

	// CreateDNSRecord creates a new DNS record.
	// In read-only mode, returns ErrReadOnly.
	CreateDNSRecord(ctx context.Context, record DNSRecord) (string, error)

	// UpdateDNSRecord updates an existing DNS record.
	// In read-only mode, returns ErrReadOnly.
	UpdateDNSRecord(ctx context.Context, record DNSRecord) error

	// DeleteDNSRecord deletes a DNS record.
	// In read-only mode, returns ErrReadOnly.
	DeleteDNSRecord(ctx context.Context, recordID string) error

	// Plan performs a dry-run reconciliation and returns planned actions.
	Plan(ctx context.Context, tunnelID string, desiredRules map[string]rules.Rule,
		state *rules.State, overwriteConflicts bool) (PlanResult, error)

	// GetStats returns API call statistics.
	GetStats() Stats
}

// ZoneResolver resolves hostnames to zone IDs using a cached zone list.
type ZoneResolver struct {
	zones          []Zone
	fallbackZoneID string
	cacheExpiry    time.Time
}

// NewZoneResolver creates a new zone resolver.
func NewZoneResolver(fallbackZoneID string) *ZoneResolver {
	return &ZoneResolver{
		fallbackZoneID: fallbackZoneID,
	}
}

// SetZones updates the cached zone list.
func (r *ZoneResolver) SetZones(zones []Zone) {
	r.zones = zones
	r.cacheExpiry = time.Now().Add(10 * time.Minute)
}

// IsCacheValid returns true if the zone cache is still valid.
func (r *ZoneResolver) IsCacheValid() bool {
	return time.Now().Before(r.cacheExpiry)
}

// Resolve finds the zone ID for a hostname.
// Priority: zonename label → longest-suffix match → fallback zone ID.
func (r *ZoneResolver) Resolve(hostname, zonename string) (string, error) {
	// 1. zonename label override
	if zonename != "" {
		for _, z := range r.zones {
			if z.Name == zonename {
				return z.ID, nil
			}
		}
		return "", ErrNotFound
	}

	// 2. Longest-suffix match
	bestZone := ""
	bestLen := 0
	h := hostname
	for _, z := range r.zones {
		zName := z.Name
		if h == zName || (len(h) > len(zName) && h[len(h)-len(zName)-1:] == "."+zName) {
			if len(zName) > bestLen {
				bestLen = len(zName)
				bestZone = z.ID
			}
		}
	}
	if bestZone != "" {
		return bestZone, nil
	}

	// 3. Fallback zone ID
	if r.fallbackZoneID != "" {
		return r.fallbackZoneID, nil
	}

	return "", ErrNotFound
}
