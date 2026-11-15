// Package reconcile implements the reconciliation engine.
// It orchestrates Docker discovery, rule lifecycle, Cloudflare ingress,
// DNS records, and state persistence per §6.7 of the plan.
package reconcile

import (
	"context"
	"log/slog"
	"maps"
	"time"

	"github.com/rm-hull/cf-tunnel-sync/internal/cf"
	"github.com/rm-hull/cf-tunnel-sync/internal/config"
	"github.com/rm-hull/cf-tunnel-sync/internal/docker"
	"github.com/rm-hull/cf-tunnel-sync/internal/labels"
	"github.com/rm-hull/cf-tunnel-sync/internal/rules"
	"github.com/rm-hull/cf-tunnel-sync/internal/state"
)

// Result holds the outcome of a reconciliation pass.
type Result struct {
	Tunnel         cf.Tunnel
	RulesAdded     int
	RulesUpdated   int
	RulesPending   int
	RulesExpired   int
	DNSCreated     int
	DNSDeleted     int
	IngressUpdated bool
	DryRun         bool
	LastError      error
	Duration       time.Duration
	PlannedActions []string // recorded would_* actions in dry-run mode
}

// Engine runs one reconciliation pass.
type Engine struct {
	log        *slog.Logger
	dockerAPI  docker.API
	cfAPI      cf.API
	statePath  string
	tunnelName string
	prefixes   []string
	grace      time.Duration
	dryRun     bool

	// Cloudflared container lifecycle (S8)
	cloudflaredCfg   config.CloudflaredConfig
	externalTunnelID string
}

// Option configures the Engine.
type Option func(*Engine)

// WithDryRun enables dry-run mode.
func WithDryRun(dryRun bool) Option {
	return func(e *Engine) {
		e.dryRun = dryRun
	}
}

// WithCloudflared configures the cloudflared container lifecycle.
func WithCloudflared(cfg config.CloudflaredConfig, externalID string) Option {
	return func(e *Engine) {
		e.cloudflaredCfg = cfg
		e.externalTunnelID = externalID
	}
}

// New creates a new Engine.
func New(
	log *slog.Logger,
	dockerAPI docker.API,
	cfAPI cf.API,
	statePath string,
	tunnelName string,
	prefixes []string,
	grace time.Duration,
	opts ...Option,
) *Engine {
	e := &Engine{
		log:        log,
		dockerAPI:  dockerAPI,
		cfAPI:      cfAPI,
		statePath:  statePath,
		tunnelName: tunnelName,
		prefixes:   prefixes,
		grace:      grace,
	}
	for _, opt := range opts {
		opt(e)
	}
	return e
}

// Run executes one reconciliation pass per §6.7.
func (e *Engine) Run(ctx context.Context) Result {
	start := time.Now()
	res := Result{DryRun: e.dryRun}
	defer func() {
		res.Duration = time.Since(start)
	}()

	// 1. Load state
	st, err := state.Load(e.statePath)
	if err != nil {
		res.LastError = err
		e.log.Error("load state", "error", err)
		return res
	}

	// 2. Find or create tunnel
	tunnel, err := e.ensureTunnel(ctx)
	if err != nil {
		res.LastError = err
		e.log.Error("ensure tunnel", "error", err)
		return res
	}
	res.Tunnel = tunnel

	// 2b. Ensure cloudflared container is running
	cfCfg := docker.CloudflaredConfig{
		ContainerName: e.cloudflaredCfg.ContainerName,
		Image:         e.cloudflaredCfg.Image,
		Network:       e.cloudflaredCfg.Network,
		MetricsPort:   e.cloudflaredCfg.MetricsPort,
	}
	if cfErr := docker.EnsureCloudflared(ctx, e.dockerAPI, e.cfAPI, tunnel.ID, cfCfg, e.externalTunnelID, e.dryRun, e.log); cfErr != nil {
		e.log.Error("ensure cloudflared", "error", cfErr)
		// Don't abort the pass; continue with ingress/DNS reconciliation
	}

	// 3. Discover desired rules from Docker
	desiredLabels, issues, err := e.dockerAPI.DesiredRules(ctx, e.prefixes, false)
	if err != nil {
		res.LastError = err
		e.log.Error("discover rules", "error", err)
		return res
	}
	for _, iss := range issues {
		e.log.Warn("label issue", "kind", iss.Kind, "container", iss.Container, "detail", iss.Detail)
	}

	// Convert labels.Rule to rules.Rule
	desired := e.convertDesired(desiredLabels)

	// Count added vs updated before Apply() modifies state
	for key := range desired {
		if _, exists := st.Rules[key]; !exists {
			res.RulesAdded++
		} else {
			res.RulesUpdated++
		}
	}

	// 4. Apply lifecycle
	now := time.Now()
	st, active, pending, expired := rules.Apply(st, desired, now, e.grace)
	res.RulesPending = len(pending)
	res.RulesExpired = len(expired)

	// 5. Render ingress from active + pending
	merged := make(map[string]rules.Rule)
	maps.Copy(merged, active)
	maps.Copy(merged, pending)
	ingress := rules.RenderIngress(merged)

	// 6. Put ingress (before DNS)
	ingressChanged, err := e.putIngressIfChanged(ctx, tunnel.ID, ingress)
	if err != nil {
		res.LastError = err
		e.log.Error("put ingress", "error", err)
		// Per §6.7: on ingress failure, skip DNS work this pass
		return res
	}
	res.IngressUpdated = ingressChanged

	// 7. Resolve zones and handle DNS for active rules
	for key, rule := range active {
		created, dnsErr := e.ensureDNS(ctx, tunnel.ID, rule)
		if dnsErr != nil {
			e.log.Warn("DNS error for rule", "rule", key, "error", dnsErr)
			// Per-rule failure isolation: continue with other rules
			if rule, ok := st.Rules[key]; ok {
				rule.Status = "dns_conflict"
				st.Rules[key] = rule
			}
			continue
		}
		if created {
			res.DNSCreated++
		}
	}

	// 8. Handle expired rules: delete DNS and remove from state
	for key, rule := range expired {
		e.deleteDNSIfOrphaned(ctx, tunnel.ID, rule)
		delete(st.Rules, key)
	}

	// 9. Persist state
	st.Tunnel = rules.TunnelInfo{
		ID:   tunnel.ID,
		Name: tunnel.Name,
	}
	if err := state.Save(e.statePath, st); err != nil {
		res.LastError = err
		e.log.Error("save state", "error", err)
		return res
	}

	duration := time.Since(start)
	res.Duration = duration

	// 10. Log summary
	e.log.Info("reconcile done",
		"added", res.RulesAdded,
		"updated", res.RulesUpdated,
		"pending", res.RulesPending,
		"expired", res.RulesExpired,
		"dns_created", res.DNSCreated,
		"ingress_updated", res.IngressUpdated,
		"dry_run", res.DryRun,
		"duration_ms", float64(duration.Nanoseconds())/1e6,
	)

	return res
}

// ensureTunnel finds or creates the tunnel.
func (e *Engine) ensureTunnel(ctx context.Context) (cf.Tunnel, error) {
	tunnel, err := e.cfAPI.FindTunnel(ctx, e.tunnelName)
	if err == nil {
		return tunnel, nil
	}
	if !e.dryRun {
		// Would create tunnel here in full implementation
		// For now, return the error
		return cf.Tunnel{}, err
	}
	e.log.Info("would_create_tunnel", "name", e.tunnelName)
	return cf.Tunnel{}, err
}

// putIngressIfChanged compares remote ingress with local and PUTs only if different.
func (e *Engine) putIngressIfChanged(ctx context.Context, tunnelID string, localIngress []rules.IngressEntry) (bool, error) {
	remote, err := e.cfAPI.GetIngress(ctx, tunnelID)
	if err != nil {
		return false, err
	}

	// Normalize: drop version for comparison
	remoteRules := remote.Rules

	if ingressEqual(localIngress, remoteRules) {
		e.log.Debug("ingress unchanged, skipping PUT")
		return false, nil
	}

	if e.dryRun {
		e.log.Info("would_update_ingress", "rules", len(localIngress))
		return false, nil
	}

	_, err = e.cfAPI.PutIngress(ctx, tunnelID, cf.IngressConfig{
		Rules: localIngress,
	})
	if err != nil {
		return false, err
	}
	e.log.Info("ingress updated", "rules", len(localIngress))
	return true, nil
}

// ensureDNS ensures a CNAME record exists for the rule.
// Returns (created, error) where created is true if a new record was created.
func (e *Engine) ensureDNS(ctx context.Context, tunnelID string, rule rules.Rule) (bool, error) {
	zoneID, err := e.cfAPI.ResolveZone(ctx, rule.Hostname, rule.ZoneName, "")
	if err != nil {
		e.log.Debug("no zone for rule", "hostname", rule.Hostname, "error", err)
		return false, nil // no zone → rule error, but still in ingress; DNS untouched
	}

	target := tunnelID + ".cfargotunnel.com"
	records, err := e.cfAPI.ListDNSRecords(ctx, zoneID, rule.Hostname, "CNAME")
	if err != nil {
		return false, err
	}

	// Check existing records
	hasConflict := false
	for _, rec := range records {
		if rec.Content == target {
			// Record exists and is correct
			e.log.Debug("dns record ok", "hostname", rule.Hostname)
			return false, nil
		}
		// Different content → conflict; don't modify
		e.log.Warn("dns_conflict", "hostname", rule.Hostname, "content", rec.Content)
		hasConflict = true
	}
	if hasConflict {
		return false, nil
	}

	// No record → create
	if e.dryRun {
		e.log.Info("would_create_dns", "hostname", rule.Hostname, "target", target)
		return false, nil
	}

	_, err = e.cfAPI.CreateDNSRecord(ctx, cf.DNSRecord{
		Name:    rule.Hostname,
		Type:    "CNAME",
		Content: target,
		Proxied: true,
		ZoneID:  zoneID,
	})
	if err != nil {
		return false, err
	}
	e.log.Info("dns created", "hostname", rule.Hostname, "target", target)
	return true, nil
}

// deleteDNSIfOrphaned deletes DNS for expired rules if safe.
func (e *Engine) deleteDNSIfOrphaned(ctx context.Context, tunnelID string, rule rules.Rule) {
	zoneID, err := e.cfAPI.ResolveZone(ctx, rule.Hostname, rule.ZoneName, "")
	if err != nil {
		return
	}

	target := tunnelID + ".cfargotunnel.com"
	records, err := e.cfAPI.ListDNSRecords(ctx, zoneID, rule.Hostname, "CNAME")
	if err != nil {
		return
	}

	for _, rec := range records {
		if rec.Content == target {
			if e.dryRun {
				e.log.Info("would_delete_dns", "hostname", rule.Hostname)
				return
			}
			if err := e.cfAPI.DeleteDNSRecord(ctx, rec.ID); err != nil {
				e.log.Warn("dns delete failed", "hostname", rule.Hostname, "error", err)
			} else {
				e.log.Info("dns deleted", "hostname", rule.Hostname)
			}
		}
	}
}

// convertDesired converts labels.Rule map to rules.Rule map.
func (e *Engine) convertDesired(desiredLabels map[string]labels.Rule) map[string]rules.Rule {
	desired := make(map[string]rules.Rule)
	for key, r := range desiredLabels {
		desired[key] = rules.Rule{
			Hostname:         r.Hostname,
			Path:             r.Path,
			Service:          r.Service,
			NoTLSVerify:      r.NoTLSVerify,
			OriginServerName: r.OriginServerName,
			ZoneName:         r.ZoneName,
			ContainerName:    r.ContainerName,
			SourcePrefix:     r.SourcePrefix,
			SourceIndex:      r.SourceIndex,
			Status:           "active",
		}
	}
	return desired
}

// ingressEqual compares two ingress configs (ignoring version).
func ingressEqual(a, b []rules.IngressEntry) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		ae := a[i]
		be := b[i]
		if ae.Hostname != be.Hostname || ae.Path != be.Path || ae.Service != be.Service {
			return false
		}
		ao := ae.OriginRequest
		bo := be.OriginRequest
		if (ao == nil) != (bo == nil) {
			return false
		}
		if ao != nil && bo != nil {
			if ao.NoTLSVerify != bo.NoTLSVerify || ao.OriginServerName != bo.OriginServerName {
				return false
			}
		}
	}
	return true
}
