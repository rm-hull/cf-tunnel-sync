package web

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus"
	sloggin "github.com/samber/slog-gin"

	"github.com/rm-hull/cf-tunnel-sync/internal/cf"
	"github.com/rm-hull/cf-tunnel-sync/internal/logx"
	"github.com/rm-hull/cf-tunnel-sync/internal/reconcile"
	"github.com/rm-hull/cf-tunnel-sync/internal/rules"
	"github.com/rm-hull/cf-tunnel-sync/internal/state"
)

//go:embed templates/*.html
var templateFS embed.FS

//go:embed static/*
var staticFS embed.FS

// Server holds the web server and its dependencies.
type Server struct {
	log             *slog.Logger
	engine          *gin.Engine
	daemon          *reconcile.Daemon
	ring            *logx.RingBufferHandler
	cfAPI           cf.API
	statePath       string
	resyncInterval  time.Duration
	metricsEnabled  bool
	dryRun          bool
	listenAddr      string
	statusRefresh   time.Duration
	metricsGatherer prometheus.Gatherer
	server          *http.Server
	startedAt       time.Time
}

// Config holds the web server configuration.
type Config struct {
	Listen         string
	Metrics        bool
	StatusRefresh  time.Duration
	DryRun         bool
	ResyncInterval time.Duration
}

// NewServer creates a new web server.
func NewServer(
	log *slog.Logger,
	daemon *reconcile.Daemon,
	ring *logx.RingBufferHandler,
	cfAPI cf.API,
	statePath string,
	cfg Config,
) *Server {
	gin.SetMode(gin.ReleaseMode)
	engine := gin.New()

	s := &Server{
		log:            log,
		engine:         engine,
		daemon:         daemon,
		ring:           ring,
		cfAPI:          cfAPI,
		statePath:      statePath,
		resyncInterval: cfg.ResyncInterval,
		metricsEnabled: cfg.Metrics,
		dryRun:         cfg.DryRun,
		listenAddr:     cfg.Listen,
		statusRefresh:  cfg.StatusRefresh,
		startedAt:      time.Now(),
	}

	if cfg.Metrics {
		s.metricsGatherer = newMetricsGatherer(daemon)
	}

	s.setupRoutes()
	return s
}

func (s *Server) setupRoutes() {
	// Middleware
	s.engine.Use(sloggin.NewWithFilters(s.log,
		sloggin.Ignore(sloggin.AcceptPath("/healthz", "/readyz")),
	))
	s.engine.Use(gin.Recovery())
	s.engine.Use(cspMiddleware())

	// Routes
	s.engine.GET("/", s.handleIndex)
	s.engine.GET("/api/status", s.handleStatusJSON)
	s.engine.GET("/healthz", s.handleHealthz)
	s.engine.GET("/readyz", s.handleReadyz)
	s.engine.GET("/static/*filepath", s.handleStatic)

	// Metrics — only if enabled
	if s.metricsEnabled {
		s.engine.GET("/metrics", s.handleMetrics)
	}
}

// Start begins the HTTP server. It blocks until ctx is cancelled.
func (s *Server) Start(ctx context.Context) error {
	listener, err := (&net.ListenConfig{}).Listen(ctx, "tcp", s.listenAddr)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}

	s.server = &http.Server{
		Handler:           s.engine,
		ReadHeaderTimeout: 5 * time.Second,
	}

	addr := listener.Addr().String()
	url := fmt.Sprintf("http://%s", addr)
	s.log.Info("web server ready", "url", url)

	// Start serving in background
	go func() {
		if err := s.server.Serve(listener); err != nil && err != http.ErrServerClosed {
			s.log.Error("web server error", "error", err)
		}
	}()

	// Wait for context cancellation
	<-ctx.Done()
	s.log.Info("shutting down web server")

	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()

	if err := s.server.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shutdown web server: %w", err)
	}

	return nil
}

// StatusData holds the data for the status page and JSON API.
type StatusData struct {
	Version       string            `json:"version"`
	Commit        string            `json:"commit"`
	StartedAt     time.Time         `json:"startedAt"`
	UptimeSeconds float64           `json:"uptimeSeconds"`
	DryRun        bool              `json:"dryRun"`
	Tunnel        TunnelStatus      `json:"tunnel"`
	Cloudflared   CloudflaredStatus `json:"cloudflared"`
	Counts        RuleCounts        `json:"counts"`
	Reconcile     ReconcileStatus   `json:"reconcile"`
	CloudflareAPI CFAPISnapshot     `json:"cloudflareApi"`
	Docker        DockerSnapshot    `json:"docker"`
	Rules         []RuleSnapshot    `json:"rules"`
	OrphanDNS     []string          `json:"orphanDns"`
	LabelWarnings []LabelWarning    `json:"labelWarnings"`
	RecentEvents  []LogEvent        `json:"recentEvents"`
}

// TunnelStatus holds tunnel information for the status page.
type TunnelStatus struct {
	Name        string `json:"name"`
	ID          string `json:"id"`
	ConfigSrc   string `json:"configSrc"`
	Connections int    `json:"connections"`
	Status      string `json:"status"`
}

// CloudflaredStatus holds cloudflared container information.
type CloudflaredStatus struct {
	Mode          string  `json:"mode"`
	Container     string  `json:"container"`
	State         string  `json:"state"`
	UptimeSeconds float64 `json:"uptimeSeconds"`
	Network       string  `json:"network"`
	Ready         bool    `json:"ready"`
}

// RuleCounts holds the count of rules by status.
type RuleCounts struct {
	Active      int `json:"active"`
	Pending     int `json:"pending"`
	Invalid     int `json:"invalid"`
	Conflict    int `json:"conflict"`
	Blocked     int `json:"blocked"`
	DNSConflict int `json:"dnsConflict"`
	Error       int `json:"error"`
}

// ReconcileStatus holds reconciliation statistics.
type ReconcileStatus struct {
	Count          int       `json:"count"`
	LastAt         time.Time `json:"lastAt"`
	LastDurationMs float64   `json:"lastDurationMs"`
	AvgDurationMs  float64   `json:"avgDurationMs"`
	MaxDurationMs  float64   `json:"maxDurationMs"`
	LastError      string    `json:"lastError"`
	NextResyncAt   time.Time `json:"nextResyncAt"`
	Changes        Changes   `json:"changes"`
}

// Changes holds the last reconciliation changes.
type Changes struct {
	IngressUpdates int `json:"ingressUpdates"`
	DNSCreated     int `json:"dnsCreated"`
	DNSDeleted     int `json:"dnsDeleted"`
	RulesExpired   int `json:"rulesExpired"`
}

// CFAPISnapshot holds Cloudflare API statistics.
type CFAPISnapshot struct {
	Calls       int            `json:"calls"`
	Errors      int            `json:"errors"`
	ByResource  map[string]int `json:"byResource"`
	LastErrorAt *time.Time     `json:"lastErrorAt"`
}

// DockerSnapshot holds Docker event statistics.
type DockerSnapshot struct {
	Connected      bool      `json:"connected"`
	EventsReceived int       `json:"eventsReceived"`
	LastEventAt    time.Time `json:"lastEventAt"`
	Reconnects     int       `json:"reconnects"`
}

// RuleSnapshot holds a single rule's status information.
type RuleSnapshot struct {
	Hostname           string         `json:"hostname"`
	Path               string         `json:"path"`
	Service            string         `json:"service"`
	Container          string         `json:"container"`
	Source             string         `json:"source"`
	Status             string         `json:"status"`
	DNS                DNSStatus      `json:"dns"`
	PendingRemoval     *time.Duration `json:"pendingRemoval"`
	PendingRemovalText string         `json:"-"`
	FirstSeen          time.Time      `json:"firstSeen"`
	Error              string         `json:"error"`
}

// DNSStatus holds DNS record status for a rule.
type DNSStatus struct {
	RecordID  string `json:"recordID"`
	OK        bool   `json:"ok"`
	CheckedAt string `json:"checkedAt"`
}

// LabelWarning holds a label parsing warning.
type LabelWarning struct {
	Container string `json:"container"`
	Field     string `json:"field"`
	Kind      string `json:"kind"`
	Detail    string `json:"detail"`
}

// LogEvent holds a recent log entry.
type LogEvent struct {
	Time  time.Time      `json:"time"`
	Level string         `json:"level"`
	Msg   string         `json:"msg"`
	Attrs map[string]any `json:"attrs"`
}

// buildStatusData constructs the full status data from all sources.
func (s *Server) buildStatusData() StatusData {
	snap := s.daemon.Snapshot()
	now := time.Now()

	data := StatusData{
		StartedAt:     s.startedAt,
		UptimeSeconds: now.Sub(s.startedAt).Seconds(),
		DryRun:        s.dryRun,
		Tunnel: TunnelStatus{
			Name:        snap.TunnelName,
			ID:          snap.TunnelID,
			Connections: snap.Connections,
		},
		Reconcile: ReconcileStatus{
			Count:          snap.PassCount,
			LastDurationMs: float64(snap.LastDuration.Milliseconds()),
			LastError:      "",
		},
		CloudflareAPI: CFAPISnapshot{
			Calls:      snap.CFStats.Calls,
			Errors:     snap.CFStats.Errors,
			ByResource: snap.CFStats.ByResource,
		},
		Docker: DockerSnapshot{
			Connected:      true,
			EventsReceived: snap.EventCount,
		},
	}

	if snap.LastError != nil {
		data.Reconcile.LastError = snap.LastError.Error()
	}

	// Load state for rule details
	st, err := state.Load(s.statePath)
	if err == nil && st != nil {
		data.Counts = countRules(st.Rules)
		data.Rules = buildRuleSnapshots(st.Rules, now)
	}

	// Recent log events
	data.RecentEvents = buildRecentEvents(s.ring.Records())

	return data
}

func countRules(rules map[string]rules.Rule) RuleCounts {
	counts := RuleCounts{}
	for _, r := range rules {
		switch r.Status {
		case "active":
			counts.Active++
		case "pending":
			counts.Pending++
		case "dns_conflict":
			counts.DNSConflict++
		}
	}
	return counts
}

func buildRuleSnapshots(rules map[string]rules.Rule, now time.Time) []RuleSnapshot {
	snapshots := make([]RuleSnapshot, 0, len(rules))
	for _, r := range rules {
		snap := RuleSnapshot{
			Hostname:  r.Hostname,
			Path:      r.Path,
			Service:   r.Service,
			Container: r.ContainerName,
			Source:    r.SourcePrefix,
			Status:    r.Status,
			FirstSeen: r.FirstSeen,
			Error:     "",
		}

		if r.Status == "pending" && r.DeleteAfter != nil {
			remaining := r.DeleteAfter.Sub(now)
			if remaining > 0 {
				snap.PendingRemoval = &remaining
				seconds := remaining.Seconds()
				hours := int(seconds) / 3600
				minutes := (int(seconds) % 3600) / 60
				if hours > 0 {
					snap.PendingRemovalText = fmt.Sprintf("in %dh %dm", hours, minutes)
				} else {
					snap.PendingRemovalText = fmt.Sprintf("in %dm", minutes)
				}
			}
		}

		snapshots = append(snapshots, snap)
	}

	// Sort by hostname for consistent output
	sortRuleSnapshots(snapshots)
	return snapshots
}

func sortRuleSnapshots(snapshots []RuleSnapshot) {
	// Simple sort by hostname
	for i := 0; i < len(snapshots); i++ {
		for j := i + 1; j < len(snapshots); j++ {
			if snapshots[i].Hostname > snapshots[j].Hostname {
				snapshots[i], snapshots[j] = snapshots[j], snapshots[i]
			}
		}
	}
}

func buildRecentEvents(records []json.RawMessage) []LogEvent {
	events := make([]LogEvent, 0, len(records))
	for _, rec := range records {
		var raw map[string]any
		if err := json.Unmarshal(rec, &raw); err != nil {
			continue
		}

		event := LogEvent{
			Attrs: make(map[string]any),
		}

		if t, ok := raw["time"]; ok {
			if tt, err := time.Parse(time.RFC3339Nano, fmt.Sprintf("%v", t)); err == nil {
				event.Time = tt
			}
		}
		if l, ok := raw["level"]; ok {
			event.Level = fmt.Sprintf("%v", l)
		}
		if m, ok := raw["msg"]; ok {
			event.Msg = fmt.Sprintf("%v", m)
		}

		// Copy remaining attrs
		for k, v := range raw {
			if k != "time" && k != "level" && k != "msg" {
				event.Attrs[k] = v
			}
		}

		events = append(events, event)
	}
	return events
}

// healthySentence generates the status sentence for the page.
func healthySentence(data StatusData) string {
	if data.Reconcile.LastError != "" {
		return fmt.Sprintf("Tunnel %s has errors: %s.", data.Tunnel.Name, data.Reconcile.LastError)
	}

	active := data.Counts.Active
	pending := data.Counts.Pending

	if active == 0 && pending == 0 {
		return fmt.Sprintf("Tunnel %s is running with no active routes.", data.Tunnel.Name)
	}

	parts := []string{fmt.Sprintf("Tunnel %s is healthy", data.Tunnel.Name)}
	if active > 0 {
		parts = append(parts, fmt.Sprintf("%d routes live", active))
	}
	if pending > 0 {
		// Find the shortest pending time
		minSeconds := float64(0)
		first := true
		for _, r := range data.Rules {
			if r.Status == "pending" && r.PendingRemoval != nil {
				seconds := r.PendingRemoval.Seconds()
				if first || seconds < minSeconds {
					minSeconds = seconds
					first = false
				}
			}
		}
		if !first {
			hours := int(minSeconds) / 3600
			minutes := (int(minSeconds) % 3600) / 60
			if hours > 0 {
				parts = append(parts, fmt.Sprintf("%d pending removal in %dh %dm", pending, hours, minutes))
			} else {
				parts = append(parts, fmt.Sprintf("%d pending removal in %dm", pending, minutes))
			}
		} else if pending > 0 {
			parts = append(parts, fmt.Sprintf("%d pending removal", pending))
		}
	}

	return strings.Join(parts, ", ") + "."
}
