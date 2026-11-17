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
	RulesActive    int
	RulesConflict  int
	DNSCreated     int
	DNSUpdated     int
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

	// DNS conflict handling
	overwriteConflicts bool

	// Allow unprotected access (R7)
	allowUnprotectedAccess bool
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

// WithOverwriteConflicts enables overwriting conflicting DNS records.
func WithOverwriteConflicts(overwrite bool) Option {
	return func(e *Engine) {
		e.overwriteConflicts = overwrite
	}
}

// WithAllowUnprotectedAccess allows rules that reference Access-protected zones.
func WithAllowUnprotectedAccess(allow bool) Option {
	return func(e *Engine) {
		e.allowUnprotectedAccess = allow
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

// Prefixes returns the configured label prefixes.
func (e *Engine) Prefixes() []string {
	return e.prefixes
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
		if e.dryRun {
			// R15: dry-run doesn't error when tunnel is missing
			res.PlannedActions = append(res.PlannedActions, "would_create_tunnel")
			e.log.Info("would_create_tunnel", "name", e.tunnelName)
			// Continue with empty tunnel ID; ingress/DNS will be no-ops in dry-run
		} else {
			res.LastError = err
			e.log.Error("ensure tunnel", "error", err)
			return res
		}
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
	desiredLabels, issues, err := e.dockerAPI.DesiredRules(ctx, e.prefixes, e.allowUnprotectedAccess)
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
	for key, d := range desired {
		if existing, exists := st.Rules[key]; !exists {
			res.RulesAdded++
		} else if rulesChanged(existing, d) {
			res.RulesUpdated++
		}
	}

	// 4. Apply lifecycle
	now := time.Now()
	st, active, pending, expired := rules.Apply(st, desired, now, e.grace)
	res.RulesActive = len(active)
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
		created, updated, hasConflict, dnsErr := e.ensureDNS(ctx, tunnel.ID, rule)
		if dnsErr != nil {
			e.log.Warn("DNS error for rule", "rule", key, "error", dnsErr)
			// Per-rule failure isolation: continue with other rules
			if stRule, ok := st.Rules[key]; ok {
				stRule.Status = "dns_error"
				st.Rules[key] = stRule
			}
			continue
		}
		if hasConflict {
			res.RulesConflict++
			if stRule, ok := st.Rules[key]; ok {
				stRule.Status = "dns_conflict"
				st.Rules[key] = stRule
			}
		}
		if created {
			res.DNSCreated++
		}
		if updated {
			res.DNSUpdated++
		}
	}

	// 8. Handle expired rules: delete DNS and remove from state
	for key, rule := range expired {
		e.deleteDNSIfOrphaned(ctx, tunnel.ID, rule, active)
		delete(st.Rules, key)
	}

	// 9. Persist state (skip in dry-run mode, R5c)
	if !e.dryRun {
		st.Tunnel = rules.TunnelInfo{
			ID:   tunnel.ID,
			Name: tunnel.Name,
		}
		if err := state.Save(e.statePath, st); err != nil {
			res.LastError = err
			e.log.Error("save state", "error", err)
			return res
		}
	}

	duration := time.Since(start)
	res.Duration = duration

	// 10. Log summary — INFO only when something changed, DEBUG otherwise
	hasChanges := res.RulesAdded > 0 || res.RulesUpdated > 0 || res.DNSCreated > 0 ||
		res.DNSUpdated > 0 || res.IngressUpdated
	if hasChanges {
		e.log.Info("reconcile done",
			"active", res.RulesActive,
			"added", res.RulesAdded,
			"updated", res.RulesUpdated,
			"pending", res.RulesPending,
			"expired", res.RulesExpired,
			"conflict", res.RulesConflict,
			"dns_created", res.DNSCreated,
			"dns_updated", res.DNSUpdated,
			"ingress_updated", res.IngressUpdated,
			"dry_run", res.DryRun,
			"duration", duration,
		)
	} else {
		e.log.Debug("reconcile done (no changes)",
			"active", res.RulesActive,
			"duration", duration,
		)
	}

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
// Returns (created, updated, hasConflict, error).
func (e *Engine) ensureDNS(ctx context.Context, tunnelID string, rule rules.Rule) (created, updated, hasConflict bool, err error) {
	zoneID, zoneErr := e.cfAPI.ResolveZone(ctx, rule.Hostname, rule.ZoneName, "")
	if zoneErr != nil {
		e.log.Debug("no zone for rule", "hostname", rule.Hostname, "error", zoneErr)
		return false, false, false, nil // no zone → rule error, but still in ingress; DNS untouched
	}

	target := tunnelID + ".cfargotunnel.com"
	records, listErr := e.cfAPI.ListDNSRecords(ctx, zoneID, rule.Hostname, "CNAME")
	if listErr != nil {
		return false, false, false, listErr
	}

	// Check existing records — only one CNAME per hostname is expected.
	if len(records) > 0 {
		rec := records[0]
		if rec.Content == target {
			// Record exists and is correct
			e.log.Debug("dns record ok", "hostname", rule.Hostname)
			return false, false, false, nil
		}
		// Different content → conflict
		if e.overwriteConflicts {
			if e.dryRun {
				e.log.Info("would_update_dns", "hostname", rule.Hostname, "from", rec.Content, "to", target)
				return false, false, true, nil
			}
			updateErr := e.cfAPI.UpdateDNSRecord(ctx, cf.DNSRecord{
				ID:      rec.ID,
				Name:    rule.Hostname,
				Type:    "CNAME",
				Content: target,
				Proxied: true,
				ZoneID:  zoneID,
			})
			if updateErr != nil {
				return false, false, true, updateErr
			}
			e.log.Info("dns updated", "hostname", rule.Hostname, "from", rec.Content, "to", target)
			return false, true, false, nil
		}
		e.log.Warn("dns_conflict", "hostname", rule.Hostname, "content", rec.Content)
		return false, false, true, nil
	}

	// No record → create
	if e.dryRun {
		e.log.Info("would_create_dns", "hostname", rule.Hostname, "target", target)
		return false, false, false, nil
	}

	_, createErr := e.cfAPI.CreateDNSRecord(ctx, cf.DNSRecord{
		Name:    rule.Hostname,
		Type:    "CNAME",
		Content: target,
		Proxied: true,
		ZoneID:  zoneID,
	})
	if createErr != nil {
		return false, false, false, createErr
	}
	e.log.Info("dns created", "hostname", rule.Hostname, "target", target)
	return true, false, false, nil
}

// deleteDNSIfOrphaned deletes DNS for expired rules if safe.
// It keeps DNS if another active rule shares the same hostname (R5a)
// and retries failed deletes (R5b).
func (e *Engine) deleteDNSIfOrphaned(ctx context.Context, tunnelID string, rule rules.Rule, active map[string]rules.Rule) {
	// R5a: keep DNS if another active rule uses the same hostname
	for _, a := range active {
		if a.Hostname == rule.Hostname {
			e.log.Debug("keeping dns — hostname shared with active rule",
				"hostname", rule.Hostname, "active_container", a.ContainerName)
			return
		}
	}

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
			// R5b: retry failed delete once
			if err := e.cfAPI.DeleteDNSRecord(ctx, zoneID, rec.ID); err != nil {
				e.log.Debug("dns delete failed, retrying", "hostname", rule.Hostname, "error", err)
				if err2 := e.cfAPI.DeleteDNSRecord(ctx, zoneID, rec.ID); err2 != nil {
					e.log.Warn("dns delete failed after retry", "hostname", rule.Hostname, "error", err2)
				} else {
					e.log.Info("dns deleted (after retry)", "hostname", rule.Hostname)
				}
			} else {
				e.log.Info("dns deleted", "hostname", rule.Hostname)
			}
		}
	}
}

// rulesChanged returns true if the desired rule differs from the existing rule
// in any field that affects the tunnel configuration (excluding lifecycle fields).
func rulesChanged(existing, desired rules.Rule) bool {
	return existing.Service != desired.Service ||
		existing.NoTLSVerify != desired.NoTLSVerify ||
		existing.OriginServerName != desired.OriginServerName ||
		existing.ZoneName != desired.ZoneName
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
// It normalizes nil vs empty OriginRequest so they are treated as equal (R11).
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
		// R11: normalize nil vs empty OriginRequest
		aoEmpty := ao == nil || (!ao.NoTLSVerify && ao.OriginServerName == "")
		boEmpty := bo == nil || (!bo.NoTLSVerify && bo.OriginServerName == "")
		if aoEmpty && boEmpty {
			continue
		}
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
