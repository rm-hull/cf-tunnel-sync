// Package cf provides a wrapper around the Cloudflare API for tunnel,
// DNS, and zone operations.
//
// The SDK implementation in this file is a read-only stub for S6.
// Full write support will be added in S7.
package cf

import (
	"context"
	"log/slog"
	"sync"

	"github.com/rm-hull/cf-tunnel-sync/internal/rules"
)

type sdkAPI struct {
	log        *slog.Logger
	accountID  string
	fallbackID string

	mu    sync.RWMutex
	stats Stats

	zoneResolver *ZoneResolver
}

// New creates a new Cloudflare API client.
// Note: The SDK implementation is read-only in S6. Full write support comes in S7.
func New(_, accountID, fallbackZoneID string, log *slog.Logger) API {
	return &sdkAPI{
		accountID:    accountID,
		fallbackID:   fallbackZoneID,
		log:          log,
		zoneResolver: NewZoneResolver(fallbackZoneID),
		stats: Stats{
			ByResource: make(map[string]int),
		},
	}
}

func (s *sdkAPI) inc(resource string) {
	s.mu.Lock()
	s.stats.Calls++
	s.stats.ByResource[resource]++
	s.mu.Unlock()
}

// FindTunnel finds a tunnel by name.
func (s *sdkAPI) FindTunnel(_ context.Context, _ string) (Tunnel, error) {
	s.inc("tunnels")
	return Tunnel{}, ErrReadOnly
}

// GetTunnel gets a tunnel by ID.
func (s *sdkAPI) GetTunnel(_ context.Context, _ string) (Tunnel, error) {
	s.inc("tunnels")
	return Tunnel{}, ErrReadOnly
}

// GetConnections returns the number of active connections.
func (s *sdkAPI) GetConnections(_ context.Context, _ string) (int, error) {
	s.inc("connections")
	return 0, ErrReadOnly
}

// GetTunnelToken returns the tunnel token for running cloudflared.
func (s *sdkAPI) GetTunnelToken(_ context.Context, _ string) (string, error) {
	s.inc("tunnels")
	return "", ErrReadOnly
}

// ListZones returns all zones for the account.
func (s *sdkAPI) ListZones(_ context.Context) ([]Zone, error) {
	s.inc("zones")
	return nil, ErrReadOnly
}

// GetZone looks up a zone by ID.
func (s *sdkAPI) GetZone(_ context.Context, _ string) (Zone, error) {
	s.inc("zones")
	return Zone{}, ErrReadOnly
}

// ResolveZone resolves a hostname to a zone ID.
func (s *sdkAPI) ResolveZone(_ context.Context, hostname, zonename, _ string) (string, error) {
	s.mu.RLock()
	resolver := s.zoneResolver
	s.mu.RUnlock()
	if resolver == nil {
		return "", ErrNotFound
	}
	return resolver.Resolve(hostname, zonename)
}

// GetIngress returns the current remote ingress configuration.
func (s *sdkAPI) GetIngress(_ context.Context, _ string) (IngressConfig, error) {
	s.inc("ingress")
	return IngressConfig{}, ErrReadOnly
}

// PutIngress updates the remote ingress configuration.
func (s *sdkAPI) PutIngress(_ context.Context, _ string, _ IngressConfig) (int64, error) {
	s.inc("ingress")
	return 0, ErrReadOnly
}

// ListDNSRecords lists DNS records for a zone.
func (s *sdkAPI) ListDNSRecords(_ context.Context, _, _, _ string) ([]DNSRecord, error) {
	s.inc("dns")
	return nil, ErrReadOnly
}

// CreateDNSRecord creates a new DNS record.
func (s *sdkAPI) CreateDNSRecord(_ context.Context, _ DNSRecord) (string, error) {
	s.inc("dns")
	return "", ErrReadOnly
}

// DeleteDNSRecord deletes a DNS record.
func (s *sdkAPI) DeleteDNSRecord(_ context.Context, _ string) error {
	s.inc("dns")
	return ErrReadOnly
}

// Plan performs a dry-run reconciliation.
func (s *sdkAPI) Plan(_ context.Context, _ string, _ map[string]rules.Rule,
	_ *rules.State, _ bool,
) (PlanResult, error) {
	return PlanResult{}, ErrReadOnly
}

// GetStats returns API call statistics.
func (s *sdkAPI) GetStats() Stats {
	s.mu.RLock()
	defer s.mu.RUnlock()
	stats := s.stats
	stats.ByResource = make(map[string]int, len(s.stats.ByResource))
	for k, v := range s.stats.ByResource {
		stats.ByResource[k] = v
	}
	return stats
}

// Ensure sdkAPI implements API
var _ API = (*sdkAPI)(nil)
