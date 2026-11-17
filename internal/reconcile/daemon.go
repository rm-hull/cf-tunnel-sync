package reconcile

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/rm-hull/cf-tunnel-sync/internal/cf"
	"github.com/rm-hull/cf-tunnel-sync/internal/config"
	"github.com/rm-hull/cf-tunnel-sync/internal/docker"
)

// DaemonConfig holds the configuration for the daemon loop.
type DaemonConfig struct {
	Debounce        time.Duration
	ResyncInterval  time.Duration
	BackoffInitial  time.Duration
	BackoffMax      time.Duration
	MinimumInterval time.Duration
}

// Snapshot holds the current state of the daemon for status reporting.
type Snapshot struct {
	TunnelID      string
	TunnelName    string
	Connections   int
	PassCount     int
	LastDuration  time.Duration
	LastError     error
	RulesActive   int
	RulesPending  int
	RulesExpired  int
	RulesConflict int
	CFStats       cf.Stats
	EventCount    int // total accumulated events (kept for backward compatibility)
	// EventCounts tracks accumulated Docker events per container and action type.
	// Key: container name, value: map of action -> count.
	EventCounts  map[string]map[string]int
	LastPassTime time.Time
}

// Daemon orchestrates the event-driven reconciliation loop.
// It watches Docker events, debounces them, runs periodic resyncs,
// and retries failed passes with exponential backoff.
type Daemon struct {
	log       *slog.Logger
	eng       *Engine
	dockerAPI docker.API
	cfAPI     cf.API
	cfg       DaemonConfig

	// Cloudflared container lifecycle (for shutdown cleanup)
	cloudflaredCfg   config.CloudflaredConfig
	externalTunnelID string
	dryRun           bool

	// Trigger channel for internal signals from the event loop.
	trigger chan struct{}

	// External trigger for SIGHUP or programmatic triggers.
	externalTrigger chan struct{}

	// Snapshot state, protected by mutex.
	mu     sync.Mutex
	snap   Snapshot
	passMu sync.Mutex // ensures only one pass runs at a time
}

// NewDaemon creates a new Daemon.
func NewDaemon(
	log *slog.Logger,
	eng *Engine,
	dockerAPI docker.API,
	cfAPI cf.API,
	cfg DaemonConfig,
	cloudflaredCfg config.CloudflaredConfig,
	externalTunnelID string,
	dryRun bool,
) *Daemon {
	if cfg.Debounce <= 0 {
		cfg.Debounce = 30 * time.Second
	}
	if cfg.ResyncInterval <= 0 {
		cfg.ResyncInterval = 5 * time.Minute
	}
	if cfg.BackoffInitial <= 0 {
		cfg.BackoffInitial = 30 * time.Second
	}
	if cfg.BackoffMax <= 0 {
		cfg.BackoffMax = 5 * time.Minute
	}
	if cfg.MinimumInterval < 0 {
		cfg.MinimumInterval = 1 * time.Minute
	}
	return &Daemon{
		log:              log,
		eng:              eng,
		dockerAPI:        dockerAPI,
		cfAPI:            cfAPI,
		cfg:              cfg,
		cloudflaredCfg:   cloudflaredCfg,
		externalTunnelID: externalTunnelID,
		dryRun:           dryRun,
		trigger:          make(chan struct{}, 1),
		externalTrigger:  make(chan struct{}, 1),
		snap:             Snapshot{},
	}
}

// Trigger signals the daemon to run a reconciliation pass.
// It is non-blocking and safe to call from multiple goroutines.
func (d *Daemon) Trigger() {
	select {
	case d.externalTrigger <- struct{}{}:
	default:
		// Already pending; don't block
	}
}

// Snapshot returns the current daemon snapshot.
func (d *Daemon) Snapshot() Snapshot {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.snap
}

// SetEventCounts injects event counts into the snapshot for testing.
func (d *Daemon) SetEventCounts(counts map[string]map[string]int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.snap.EventCounts = counts
	d.snap.EventCount = 0
	for _, actions := range counts {
		for _, c := range actions {
			d.snap.EventCount += c
		}
	}
}

// Run starts the daemon loop. It blocks until ctx is cancelled.
// On cancellation, it waits for any in-progress pass to complete before returning.
func (d *Daemon) Run(ctx context.Context) {
	// Start Docker event stream with label filters so only containers
	// with our label prefixes generate events.
	prefixes := d.eng.Prefixes()
	stream := d.dockerAPI.Events(ctx, prefixes)

	// Debounce timer
	debounceTimer := time.NewTimer(d.cfg.Debounce)

	// Resync timer
	resyncTimer := time.NewTimer(d.cfg.ResyncInterval)

	defer func() {
		debounceTimer.Stop()
		resyncTimer.Stop()
		d.log.Info("daemon stopped")
	}()

	for {
		select {
		case <-ctx.Done():
			d.log.Info("daemon shutting down")
			d.stopCloudflared(ctx)
			return

		case evt, ok := <-stream.Events:
			if !ok {
				// Event stream closed; restart
				d.log.Warn("docker event stream closed, restarting")
				stream = d.dockerAPI.Events(ctx, prefixes)
				d.Trigger()
				continue
			}
			d.mu.Lock()
			d.snap.EventCount++
			containerName := evt.Actor["name"]
			if containerName == "" && len(evt.ID) >= 12 {
				containerName = evt.ID[:12]
			} else if containerName == "" {
				containerName = evt.ID
			}
			if d.snap.EventCounts == nil {
				d.snap.EventCounts = make(map[string]map[string]int)
			}
			if d.snap.EventCounts[containerName] == nil {
				d.snap.EventCounts[containerName] = make(map[string]int)
			}
			d.snap.EventCounts[containerName][evt.Action]++
			d.mu.Unlock()
			if !debounceTimer.Stop() {
				// Timer already fired; drain the channel
				select {
				case <-debounceTimer.C:
				default:
				}
			}
			debounceTimer.Reset(d.cfg.Debounce)

		case <-stream.Reconnected:
			d.log.Info("docker event stream reconnected, triggering pass")
			select {
			case d.trigger <- struct{}{}:
			default:
			}

		case err := <-stream.Err:
			d.log.Error("docker event stream error", "error", err)

		case <-debounceTimer.C:
			d.log.Info("debounce timer fired")
			select {
			case d.trigger <- struct{}{}:
			default:
			}

		case <-resyncTimer.C:
			d.log.Info("resync timer fired")
			select {
			case d.trigger <- struct{}{}:
			default:
			}

		case <-d.trigger:
			d.log.Info("reconciliation trigger received, starting pass")
			// Reset resync timer immediately so the timer keeps ticking
			// even if a pass is already running.
			if !resyncTimer.Stop() {
				select {
				case <-resyncTimer.C:
				default:
				}
			}
			resyncTimer.Reset(d.cfg.ResyncInterval)

			// Run pass asynchronously so the main loop stays responsive
			// to Docker events, debounce, and SIGHUP.
			go d.runPass(ctx)

		case <-d.externalTrigger:
			// Reset resync timer immediately
			if !resyncTimer.Stop() {
				select {
				case <-resyncTimer.C:
				default:
				}
			}
			resyncTimer.Reset(d.cfg.ResyncInterval)

			// Run pass asynchronously
			go d.runPass(ctx)
		}
	}
}

// runPass executes a single reconciliation pass with backoff retry.
// It enforces a minimum interval between passes to cap API call rates.
func (d *Daemon) runPass(ctx context.Context) {
	d.passMu.Lock()
	defer d.passMu.Unlock()

	// Enforce minimum interval between passes
	now := time.Now()
	d.mu.Lock()
	sinceLast := now.Sub(d.snap.LastPassTime)
	d.mu.Unlock()
	if d.cfg.MinimumInterval > 0 && sinceLast < d.cfg.MinimumInterval {
		remaining := d.cfg.MinimumInterval - sinceLast
		d.log.Debug("skipping pass — minimum interval not reached",
			"since_last", sinceLast,
			"remaining", remaining,
		)
		return
	}

	start := time.Now()

	res := d.eng.Run(ctx)

	duration := time.Since(start)

	// Fetch active connections from Cloudflare
	connections := 0
	if res.Tunnel.ID != "" {
		var err error
		connections, err = d.cfAPI.GetConnections(ctx, res.Tunnel.ID)
		if err != nil {
			d.log.Debug("get connections failed", "error", err)
		}
	}

	d.mu.Lock()
	d.snap.PassCount++
	d.snap.LastDuration = duration
	d.snap.LastError = res.LastError
	d.snap.RulesActive = res.RulesActive
	d.snap.RulesPending = res.RulesPending
	d.snap.RulesExpired = res.RulesExpired
	d.snap.RulesConflict = res.RulesConflict
	d.snap.Connections = connections
	d.snap.LastPassTime = start
	if res.Tunnel.ID != "" {
		d.snap.TunnelID = res.Tunnel.ID
		d.snap.TunnelName = res.Tunnel.Name
	}
	d.snap.CFStats = d.cfAPI.GetStats()
	d.mu.Unlock()

	if res.LastError != nil {
		d.log.Warn("reconcile pass failed",
			"error", res.LastError,
			"duration", duration,
			"backoff", backoffFor(d.cfg, d.snap.PassCount),
		)
		// Schedule retry with backoff
		retryAfter := backoffFor(d.cfg, d.snap.PassCount)
		d.log.Info("retrying in", "after", retryAfter)
		time.AfterFunc(retryAfter, func() {
			d.Trigger()
		})
	} else {
		// Log at INFO only when something changed, DEBUG otherwise
		hasChanges := res.RulesAdded > 0 || res.RulesUpdated > 0 || res.DNSCreated > 0 ||
			res.DNSUpdated > 0 || res.IngressUpdated
		if hasChanges {
			d.log.Info("reconcile pass complete",
				"active", res.RulesActive,
				"added", res.RulesAdded,
				"updated", res.RulesUpdated,
				"pending", res.RulesPending,
				"expired", res.RulesExpired,
				"conflict", res.RulesConflict,
				"dns_created", res.DNSCreated,
				"dns_updated", res.DNSUpdated,
				"ingress_updated", res.IngressUpdated,
				"duration", duration,
			)
		} else {
			d.log.Debug("reconcile pass complete (no changes)",
				"active", res.RulesActive,
				"duration", duration,
			)
		}
	}
}

// stopCloudflared stops the cloudflared container on graceful shutdown.
// It uses a fresh context with a timeout so cleanup can complete even after
// the main context has been cancelled by the shutdown signal.
func (d *Daemon) stopCloudflared(ctx context.Context) {
	ctx, cancel := context.WithTimeout(
		context.WithoutCancel(ctx),
		10*time.Second,
	)
	defer cancel()
	cfCfg := docker.CloudflaredConfig{
		ContainerName: d.cloudflaredCfg.ContainerName,
		Image:         d.cloudflaredCfg.Image,
		Network:       d.cloudflaredCfg.Network,
		MetricsPort:   d.cloudflaredCfg.MetricsPort,
	}
	if err := docker.StopCloudflared(ctx, d.dockerAPI, cfCfg, d.externalTunnelID, d.dryRun, d.log); err != nil {
		d.log.Error("stop cloudflared on shutdown", "error", err)
	}
}

// backoffFor calculates the backoff duration based on the config.
// It uses exponential backoff: initial * 2^(passCount-1), capped at max.
func backoffFor(cfg DaemonConfig, passCount int) time.Duration {
	if passCount <= 1 {
		return cfg.BackoffInitial
	}
	backoff := cfg.BackoffInitial
	for i := 1; i < passCount && backoff < cfg.BackoffMax; i++ {
		backoff *= 2
	}
	if backoff > cfg.BackoffMax {
		backoff = cfg.BackoffMax
	}
	return backoff
}
