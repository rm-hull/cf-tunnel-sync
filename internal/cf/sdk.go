// Package cf provides a wrapper around the Cloudflare API for tunnel,
// DNS, and zone operations.
package cf

import (
	"context"
	"fmt"
	"log/slog"
	"maps"
	"sync"

	cfdns "github.com/cloudflare/cloudflare-go"
	rulespkg "github.com/rm-hull/cf-tunnel-sync/internal/rules"
)

type sdkAPI struct {
	cf         *cfdns.API
	log        *slog.Logger
	accountID  string
	fallbackID string

	mu    sync.RWMutex
	stats Stats

	zoneResolver *ZoneResolver
}

// New creates a new Cloudflare API client using the given API token.
func New(apiToken, accountID, fallbackZoneID string, log *slog.Logger) (API, error) {
	cf, err := cfdns.NewWithAPIToken(apiToken)
	if err != nil {
		return nil, fmt.Errorf("create Cloudflare client: %w", err)
	}

	log = log.With("component", "cloudflare")
	log.Debug("created Cloudflare client", "account", accountID)
	return &sdkAPI{
		cf:           cf,
		accountID:    accountID,
		fallbackID:   fallbackZoneID,
		log:          log,
		zoneResolver: NewZoneResolver(fallbackZoneID),
		stats: Stats{
			ByResource: make(map[string]int),
		},
	}, nil
}

func (s *sdkAPI) inc(resource string) {
	s.mu.Lock()
	s.stats.Calls++
	s.stats.ByResource[resource]++
	s.mu.Unlock()
}

func (s *sdkAPI) incErr(_ string) {
	s.mu.Lock()
	s.stats.Errors++
	s.mu.Unlock()
}

// FindTunnel finds a tunnel by name.
func (s *sdkAPI) FindTunnel(ctx context.Context, name string) (Tunnel, error) {
	s.inc("tunnels")
	s.log.Debug("find tunnel", "name", name)
	rc := cfdns.AccountIdentifier(s.accountID)
	tunnels, _, err := s.cf.ListTunnels(ctx, rc, cfdns.TunnelListParams{Name: name})
	if err != nil {
		s.incErr("tunnels")
		s.log.Debug("find tunnel failed", "name", name, "error", err)
		return Tunnel{}, fmt.Errorf("list tunnels: %w", err)
	}

	var matches []Tunnel
	for _, t := range tunnels {
		tunnel := Tunnel{
			ID:        t.ID,
			Name:      t.Name,
			ConfigSrc: "cloudflare",
			Status:    t.Status,
		}
		if t.RemoteConfig {
			tunnel.ConfigSrc = "cloudflare"
		} else {
			tunnel.ConfigSrc = "local"
		}
		matches = append(matches, tunnel)
	}

	if len(matches) == 0 {
		s.log.Debug("tunnel not found", "name", name)
		return Tunnel{}, ErrNotFound
	}
	if len(matches) > 1 {
		s.log.Debug("multiple tunnels found", "name", name, "count", len(matches))
		return Tunnel{}, ErrMultipleMatches
	}

	t := matches[0]
	if t.ConfigSrc != "cloudflare" {
		s.log.Debug("tunnel config is local", "name", name, "id", t.ID)
		return Tunnel{}, ErrConfigSrcLocal
	}
	s.log.Debug("found tunnel", "name", name, "id", t.ID)
	return t, nil
}

// GetTunnel gets a tunnel by ID.
func (s *sdkAPI) GetTunnel(ctx context.Context, id string) (Tunnel, error) {
	s.inc("tunnels")
	s.log.Debug("get tunnel", "id", id)
	rc := cfdns.AccountIdentifier(s.accountID)
	t, err := s.cf.GetTunnel(ctx, rc, id)
	if err != nil {
		s.incErr("tunnels")
		s.log.Debug("get tunnel failed", "id", id, "error", err)
		return Tunnel{}, fmt.Errorf("get tunnel: %w", err)
	}

	s.log.Debug("got tunnel", "id", id, "name", t.Name)
	return Tunnel{
		ID:        t.ID,
		Name:      t.Name,
		ConfigSrc: mapBoolToConfigSrc(t.RemoteConfig),
		Status:    t.Status,
	}, nil
}

// GetConnections returns the number of active connections.
func (s *sdkAPI) GetConnections(ctx context.Context, tunnelID string) (int, error) {
	s.inc("connections")
	s.log.Debug("get connections", "tunnel", tunnelID)
	rc := cfdns.AccountIdentifier(s.accountID)
	conns, err := s.cf.ListTunnelConnections(ctx, rc, tunnelID)
	if err != nil {
		s.incErr("connections")
		s.log.Debug("get connections failed", "tunnel", tunnelID, "error", err)
		return 0, fmt.Errorf("list tunnel connections: %w", err)
	}
	s.log.Debug("got connections", "tunnel", tunnelID, "count", len(conns))
	return len(conns), nil
}

// GetTunnelToken returns the tunnel token for running cloudflared.
func (s *sdkAPI) GetTunnelToken(ctx context.Context, tunnelID string) (string, error) {
	s.inc("tunnels")
	s.log.Debug("get tunnel token", "tunnel", tunnelID)
	rc := cfdns.AccountIdentifier(s.accountID)
	token, err := s.cf.GetTunnelToken(ctx, rc, tunnelID)
	if err != nil {
		s.incErr("tunnels")
		s.log.Debug("get tunnel token failed", "tunnel", tunnelID, "error", err)
		return "", fmt.Errorf("get tunnel token: %w", err)
	}
	s.log.Debug("got tunnel token", "tunnel", tunnelID)
	return token, nil
}

// ListZones returns all zones for the account.
func (s *sdkAPI) ListZones(ctx context.Context) ([]Zone, error) {
	s.inc("zones")
	s.log.Debug("list zones")
	zones, err := s.cf.ListZones(ctx)
	if err != nil {
		s.incErr("zones")
		s.log.Debug("list zones failed", "error", err)
		return nil, fmt.Errorf("list zones: %w", err)
	}

	result := make([]Zone, 0, len(zones))
	for _, z := range zones {
		result = append(result, Zone{ID: z.ID, Name: z.Name})
	}
	s.log.Debug("listed zones", "count", len(result))
	return result, nil
}

// GetZone looks up a zone by ID.
func (s *sdkAPI) GetZone(ctx context.Context, zoneID string) (Zone, error) {
	s.inc("zones")
	s.log.Debug("get zone", "zoneID", zoneID)
	rc := cfdns.ZoneIdentifier(zoneID)
	zones, err := s.cf.ListZones(ctx)
	if err != nil {
		s.incErr("zones")
		s.log.Debug("get zone failed", "zoneID", zoneID, "error", err)
		return Zone{}, fmt.Errorf("list zones: %w", err)
	}
	for _, z := range zones {
		if z.ID == zoneID {
			s.log.Debug("got zone", "zoneID", zoneID, "name", z.Name)
			return Zone{ID: z.ID, Name: z.Name}, nil
		}
	}
	_ = rc
	s.log.Debug("zone not found", "zoneID", zoneID)
	return Zone{}, ErrNotFound
}

// ResolveZone resolves a hostname to a zone ID.
func (s *sdkAPI) ResolveZone(ctx context.Context, hostname, zonename, _ string) (string, error) {
	s.log.Debug("resolve zone", "hostname", hostname, "zonename", zonename)
	s.mu.RLock()
	resolver := s.zoneResolver
	s.mu.RUnlock()

	// Refresh zone cache if expired
	if resolver == nil || !resolver.IsCacheValid() {
		zones, err := s.ListZones(ctx)
		if err == nil {
			resolver.SetZones(zones)
			s.mu.Lock()
			s.zoneResolver = resolver
			s.mu.Unlock()
		}
	}

	if resolver == nil {
		s.log.Debug("zone resolver not available", "hostname", hostname)
		return "", ErrNotFound
	}
	zoneID, err := resolver.Resolve(hostname, zonename)
	if err != nil {
		s.log.Debug("resolve zone failed", "hostname", hostname, "error", err)
		return "", err
	}
	s.log.Debug("resolved zone", "hostname", hostname, "zoneID", zoneID)
	return zoneID, nil
}

// GetIngress returns the current remote ingress configuration.
func (s *sdkAPI) GetIngress(ctx context.Context, tunnelID string) (IngressConfig, error) {
	s.inc("ingress")
	s.log.Debug("get ingress", "tunnel", tunnelID)
	rc := cfdns.AccountIdentifier(s.accountID)
	cfg, err := s.cf.GetTunnelConfiguration(ctx, rc, tunnelID)
	if err != nil {
		s.incErr("ingress")
		s.log.Debug("get ingress failed", "tunnel", tunnelID, "error", err)
		return IngressConfig{}, fmt.Errorf("get tunnel configuration: %w", err)
	}

	entries := make([]rulespkg.IngressEntry, 0, len(cfg.Config.Ingress))
	for _, r := range cfg.Config.Ingress {
		entry := rulespkg.IngressEntry{
			Hostname: r.Hostname,
			Path:     r.Path,
			Service:  r.Service,
		}
		if r.OriginRequest != nil {
			entry.OriginRequest = &rulespkg.OriginRequest{
				NoTLSVerify:      r.OriginRequest.NoTLSVerify != nil && *r.OriginRequest.NoTLSVerify,
				OriginServerName: ptrVal(r.OriginRequest.OriginServerName),
			}
		}
		entries = append(entries, entry)
	}

	s.log.Debug("got ingress", "tunnel", tunnelID, "version", cfg.Version, "rules", len(entries))
	return IngressConfig{
		Version: int64(cfg.Version),
		Rules:   entries,
	}, nil
}

// PutIngress updates the remote ingress configuration.
func (s *sdkAPI) PutIngress(ctx context.Context, tunnelID string, config IngressConfig) (int64, error) {
	s.inc("ingress")
	s.log.Debug("put ingress", "tunnel", tunnelID, "rules", len(config.Rules))
	rc := cfdns.AccountIdentifier(s.accountID)

	ingress := make([]cfdns.UnvalidatedIngressRule, 0, len(config.Rules))
	for _, r := range config.Rules {
		entry := cfdns.UnvalidatedIngressRule{
			Hostname: r.Hostname,
			Path:     r.Path,
			Service:  r.Service,
		}
		if r.OriginRequest != nil {
			noTLS := r.OriginRequest.NoTLSVerify
			entry.OriginRequest = &cfdns.OriginRequestConfig{
				NoTLSVerify:      &noTLS,
				OriginServerName: strPtr(r.OriginRequest.OriginServerName),
			}
		}
		ingress = append(ingress, entry)
	}

	cfg, err := s.cf.UpdateTunnelConfiguration(ctx, rc, cfdns.TunnelConfigurationParams{
		TunnelID: tunnelID,
		Config: cfdns.TunnelConfiguration{
			Ingress: ingress,
		},
	})
	if err != nil {
		s.incErr("ingress")
		s.log.Debug("put ingress failed", "tunnel", tunnelID, "error", err)
		return 0, fmt.Errorf("update tunnel configuration: %w", err)
	}
	s.log.Debug("put ingress", "tunnel", tunnelID, "version", cfg.Version)
	return int64(cfg.Version), nil
}

// ListDNSRecords lists DNS records for a zone.
func (s *sdkAPI) ListDNSRecords(ctx context.Context, zoneID, name, recordType string) ([]DNSRecord, error) {
	s.inc("dns")
	s.log.Debug("list DNS records", "zone", zoneID, "name", name, "type", recordType)
	rc := cfdns.ZoneIdentifier(zoneID)
	records, _, err := s.cf.ListDNSRecords(ctx, rc, cfdns.ListDNSRecordsParams{
		Name: name,
		Type: recordType,
	})
	if err != nil {
		s.incErr("dns")
		s.log.Debug("list DNS records failed", "zone", zoneID, "name", name, "error", err)
		return nil, fmt.Errorf("list DNS records: %w", err)
	}

	result := make([]DNSRecord, 0, len(records))
	for _, r := range records {
		result = append(result, DNSRecord{
			ID:      r.ID,
			Name:    r.Name,
			Type:    r.Type,
			Content: r.Content,
			Proxied: r.Proxied != nil && *r.Proxied,
			ZoneID:  zoneID,
		})
	}
	s.log.Debug("listed DNS records", "zone", zoneID, "name", name, "count", len(result))
	return result, nil
}

// CreateDNSRecord creates a new DNS record.
func (s *sdkAPI) CreateDNSRecord(ctx context.Context, record DNSRecord) (string, error) {
	s.inc("dns")
	s.log.Debug("create DNS record", "name", record.Name, "type", record.Type, "content", record.Content)
	rc := cfdns.ZoneIdentifier(record.ZoneID)
	proxied := true
	r, err := s.cf.CreateDNSRecord(ctx, rc, cfdns.CreateDNSRecordParams{
		Type:    record.Type,
		Name:    record.Name,
		Content: record.Content,
		Proxied: &proxied,
		TTL:     1, // auto
	})
	if err != nil {
		s.incErr("dns")
		s.log.Debug("create DNS record failed", "name", record.Name, "error", err)
		return "", fmt.Errorf("create DNS record: %w", err)
	}
	s.log.Debug("created DNS record", "name", record.Name, "id", r.ID)
	return r.ID, nil
}

// DeleteDNSRecord deletes a DNS record.
func (s *sdkAPI) DeleteDNSRecord(ctx context.Context, recordID string) error {
	s.inc("dns")
	s.log.Debug("delete DNS record", "id", recordID)
	// Find the record to get its zone ID
	zones, err := s.ListZones(ctx)
	if err != nil {
		s.log.Debug("delete DNS record failed", "id", recordID, "error", err)
		return fmt.Errorf("list zones for DNS delete: %w", err)
	}
	for _, z := range zones {
		rc := cfdns.ZoneIdentifier(z.ID)
		err := s.cf.DeleteDNSRecord(ctx, rc, recordID)
		if err == nil {
			s.log.Debug("deleted DNS record", "id", recordID, "zone", z.ID)
			return nil
		}
		// Try next zone
	}
	s.incErr("dns")
	s.log.Debug("delete DNS record not found in any zone", "id", recordID)
	return fmt.Errorf("delete DNS record: not found in any zone")
}

// Plan performs a dry-run reconciliation.
func (s *sdkAPI) Plan(_ context.Context, _ string, _ map[string]rulespkg.Rule,
	_ *rulespkg.State, _ bool,
) (PlanResult, error) {
	return PlanResult{}, nil
}

// GetStats returns API call statistics.
func (s *sdkAPI) GetStats() Stats {
	s.mu.RLock()
	defer s.mu.RUnlock()
	stats := s.stats
	stats.ByResource = make(map[string]int, len(s.stats.ByResource))
	maps.Copy(stats.ByResource, s.stats.ByResource)
	return stats
}

// Ensure sdkAPI implements API
var _ API = (*sdkAPI)(nil)

func mapBoolToConfigSrc(remoteConfig bool) string {
	if remoteConfig {
		return "cloudflare"
	}
	return "local"
}

func strPtr(v string) *string {
	if v == "" {
		return nil
	}
	return &v
}

func ptrVal[T any](v *T) T {
	if v == nil {
		var zero T
		return zero
	}
	return *v
}
