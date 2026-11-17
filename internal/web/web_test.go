package web

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rm-hull/cf-tunnel-sync/internal/cf"
	"github.com/rm-hull/cf-tunnel-sync/internal/config"
	"github.com/rm-hull/cf-tunnel-sync/internal/logx"
	"github.com/rm-hull/cf-tunnel-sync/internal/reconcile"
	"github.com/rm-hull/cf-tunnel-sync/internal/rules"
	"github.com/rm-hull/cf-tunnel-sync/internal/state"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTestServer creates a Server with a fake daemon for testing.
func newTestServer(t *testing.T, opts ...func(*testServerOpts)) *Server {
	t.Helper()

	// Create a temp state file
	tmpDir := t.TempDir()
	statePath := filepath.Join(tmpDir, "state.json")

	// Save state with test rules
	st := rules.NewState()
	st.Rules["whoami.example.com|"] = rules.Rule{
		Hostname:      "whoami.example.com",
		Service:       "http://whoami:80",
		ContainerName: "whoami",
		SourcePrefix:  "cloudflare.tunnel",
		Status:        "active",
		FirstSeen:     time.Now().Add(-1 * time.Hour),
		LastSeen:      time.Now(),
	}
	require.NoError(t, state.Save(statePath, st))

	// Create logger and ring buffer
	log := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	_, ring := logx.NewJSONLogger(slog.LevelInfo, 100)

	// Create fake CF API
	cfAPI := cf.NewFake()

	// Create engine and daemon
	eng := reconcile.New(
		log,
		nil, // docker API not needed for web tests
		cfAPI,
		statePath,
		"test-tunnel",
		[]string{"cloudflare.tunnel"},
		8*time.Hour,
		reconcile.WithDryRun(true),
	)

	daemon := reconcile.NewDaemon(
		log,
		eng,
		nil, // docker API not needed
		cfAPI,
		reconcile.DaemonConfig{
			Debounce:       2 * time.Second,
			ResyncInterval: 5 * time.Minute,
		},
		config.CloudflaredConfig{},
		"",
		true,
	)

	optCfg := testServerOpts{
		metricsEnabled: false,
		dryRun:         true,
	}
	for _, opt := range opts {
		opt(&optCfg)
	}

	cfg := Config{
		Listen:         ":0",
		Metrics:        optCfg.metricsEnabled,
		StatusRefresh:  15 * time.Second,
		DryRun:         optCfg.dryRun,
		ResyncInterval: 5 * time.Minute,
	}

	return NewServer(log, daemon, ring, cfAPI, statePath, cfg)
}

type testServerOpts struct {
	metricsEnabled bool
	dryRun         bool
}

func withMetrics() func(*testServerOpts) {
	return func(o *testServerOpts) {
		o.metricsEnabled = true
	}
}

// newTestRequest creates a test HTTP request.
func newTestRequest(path string) *http.Request {
	req := httptest.NewRequest(http.MethodGet, path, nil) //nolint:noctx // test request
	return req
}

// Test_index_healthy verifies the HTML status page returns 200 with correct content.
func Test_index_healthy(t *testing.T) {
	server := newTestServer(t)

	req := newTestRequest("/")
	rec := httptest.NewRecorder()

	server.engine.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Header().Get("Content-Type"), "text/html")
	assert.Contains(t, rec.Body.String(), "cf-tunnel-sync")
	assert.Contains(t, rec.Body.String(), "healthy")
}

// Test_index_no_script verifies the HTML contains no script tags.
// External URLs in <a href> are allowed for route links that open in new tabs.
func Test_index_no_script(t *testing.T) {
	server := newTestServer(t)

	req := newTestRequest("/")
	rec := httptest.NewRecorder()

	server.engine.ServeHTTP(rec, req)

	body := rec.Body.String()
	assert.NotContains(t, body, "<script", "HTML should not contain script tags")

	// Check that src= attributes don't point to external URLs (href= is allowed for route links)
	lines := strings.Split(body, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.Contains(line, "src=") {
			assert.NotContains(t, line, "http://", "src should not use external URLs: %s", line)
			assert.NotContains(t, line, "https://", "src should not use external URLs: %s", line)
		}
	}
}

// Test_index_csp_header verifies the CSP header is present.
func Test_index_csp_header(t *testing.T) {
	server := newTestServer(t)

	req := newTestRequest("/")
	rec := httptest.NewRecorder()

	server.engine.ServeHTTP(rec, req)

	csp := rec.Header().Get("Content-Security-Policy")
	assert.NotEmpty(t, csp, "CSP header should be present")
	assert.Contains(t, csp, "default-src 'self'")
}

// Test_index_rules_table verifies the rules table shows active rules.
func Test_index_rules_table(t *testing.T) {
	server := newTestServer(t)

	req := newTestRequest("/")
	rec := httptest.NewRecorder()

	server.engine.ServeHTTP(rec, req)

	body := rec.Body.String()
	assert.Contains(t, body, "whoami.example.com")
	assert.Contains(t, body, "Active")
}

// Test_index_orphan_dns_hidden verifies orphan DNS section is hidden when empty.
func Test_index_orphan_dns_hidden(t *testing.T) {
	server := newTestServer(t)

	req := newTestRequest("/")
	rec := httptest.NewRecorder()

	server.engine.ServeHTTP(rec, req)

	body := rec.Body.String()
	assert.NotContains(t, body, "Orphan DNS")
}

// Test_index_label_warnings_hidden verifies label warnings section is hidden when empty.
func Test_index_label_warnings_hidden(t *testing.T) {
	server := newTestServer(t)

	req := newTestRequest("/")
	rec := httptest.NewRecorder()

	server.engine.ServeHTTP(rec, req)

	body := rec.Body.String()
	assert.NotContains(t, body, "Label warnings")
}

// Test_api_status_json verifies the JSON API returns correct structure.
func Test_api_status_json(t *testing.T) {
	server := newTestServer(t)

	req := newTestRequest("/api/status")
	rec := httptest.NewRecorder()

	server.engine.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Header().Get("Content-Type"), "application/json")

	var data StatusData
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &data))

	assert.Equal(t, true, data.DryRun)
	assert.Equal(t, 1, len(data.Rules))
	assert.Equal(t, "whoami.example.com", data.Rules[0].Hostname)
	assert.Equal(t, "active", data.Rules[0].Status)
}

// Test_api_status_counts verifies the rule counts are correct.
func Test_api_status_counts(t *testing.T) {
	server := newTestServer(t)

	req := newTestRequest("/api/status")
	rec := httptest.NewRecorder()

	server.engine.ServeHTTP(rec, req)

	var data StatusData
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &data))

	assert.Equal(t, 1, data.Counts.Active)
	assert.Equal(t, 0, data.Counts.Pending)
}

// Test_healthz verifies the health endpoint returns 200.
func Test_healthz(t *testing.T) {
	server := newTestServer(t)

	req := newTestRequest("/healthz")
	rec := httptest.NewRecorder()

	server.engine.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "ok", rec.Body.String())
}

// Test_readyz_not_ready verifies readiness returns 503 before first pass.
func Test_readyz_not_ready(t *testing.T) {
	server := newTestServer(t)

	req := newTestRequest("/readyz")
	rec := httptest.NewRecorder()

	server.engine.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
	assert.Contains(t, rec.Body.String(), "not ready")
}

// Test_metrics_disabled verifies metrics returns 404 when disabled.
func Test_metrics_disabled(t *testing.T) {
	server := newTestServer(t)

	req := newTestRequest("/metrics")
	rec := httptest.NewRecorder()

	server.engine.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusNotFound, rec.Code)
}

// Test_metrics_enabled verifies metrics returns 200 with custom metrics when enabled.
func Test_metrics_enabled(t *testing.T) {
	server := newTestServer(t, withMetrics())

	req := newTestRequest("/metrics")
	rec := httptest.NewRecorder()

	server.engine.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()

	// Verify custom metrics are present
	assert.Contains(t, body, "cftunnelsync_reconciler_pass_count_total")
	assert.Contains(t, body, "cftunnelsync_reconciler_last_pass_duration_seconds")
	assert.Contains(t, body, "cftunnelsync_reconciler_last_error")
	assert.Contains(t, body, "cftunnelsync_rules_active")
	assert.Contains(t, body, "cftunnelsync_rules_pending")
	assert.Contains(t, body, "cftunnelsync_rules_expired")
	assert.Contains(t, body, "cftunnelsync_rules_conflict")
	assert.Contains(t, body, "cftunnelsync_cf_api_calls_total")
	assert.Contains(t, body, "cftunnelsync_cf_api_errors_total")
	assert.Contains(t, body, "cftunnelsync_docker_events_total")

	// Verify default Go/runtime metrics are also present
	assert.Contains(t, body, "go_goroutines")
}

// Test_css_served verifies the CSS file is served with correct headers.
func Test_css_served(t *testing.T) {
	server := newTestServer(t)

	req := newTestRequest("/static/app.css")
	rec := httptest.NewRecorder()

	server.engine.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Header().Get("Content-Type"), "text/css")
	assert.Contains(t, rec.Header().Get("Cache-Control"), "immutable")
}

// Test_healthySentence verifies the status sentence generation.
func Test_healthySentence(t *testing.T) {
	tests := []struct {
		name     string
		data     StatusData
		expected string
	}{
		{
			name: "healthy with active rules",
			data: StatusData{
				Tunnel: TunnelStatus{Name: "homelab"},
				Counts: RuleCounts{Active: 3},
			},
			expected: "Tunnel <strong>homelab</strong> is healthy, 3 routes live.",
		},
		{
			name: "healthy with pending rules",
			data: StatusData{
				Tunnel: TunnelStatus{Name: "homelab"},
				Counts: RuleCounts{Active: 2, Pending: 1},
			},
			expected: "Tunnel <strong>homelab</strong> is healthy, 2 routes live, 1 pending removal.",
		},
		{
			name: "error state",
			data: StatusData{
				Tunnel:    TunnelStatus{Name: "homelab"},
				Reconcile: ReconcileStatus{LastError: "connection refused"},
			},
			expected: "Tunnel <strong>homelab</strong> has errors: connection refused.",
		},
		{
			name: "no rules",
			data: StatusData{
				Tunnel: TunnelStatus{Name: "homelab"},
			},
			expected: "Tunnel <strong>homelab</strong> is running with no active routes.",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := healthySentence(tt.data)
			assert.Equal(t, tt.expected, string(result))
		})
	}
}

// Test_statusClass verifies the CSS class mapping for statuses.
func Test_statusClass(t *testing.T) {
	assert.Equal(t, "ok", statusClass("active"))
	assert.Equal(t, "warn", statusClass("pending"))
	assert.Equal(t, "bad", statusClass("dns_conflict"))
	assert.Equal(t, "bad", statusClass("dns_error"))
	assert.Equal(t, "bad", statusClass("invalid"))
	assert.Equal(t, "bad", statusClass("conflict"))
	assert.Equal(t, "warn", statusClass("blocked"))
	assert.Equal(t, "", statusClass("unknown"))
}

// Test_statusLabel verifies the human-readable label mapping.
func Test_statusLabel(t *testing.T) {
	assert.Equal(t, "Active", statusLabel("active"))
	assert.Equal(t, "Pending removal", statusLabel("pending"))
	assert.Equal(t, "DNS conflict", statusLabel("dns_conflict"))
	assert.Equal(t, "DNS error", statusLabel("dns_error"))
	assert.Equal(t, "Invalid", statusLabel("invalid"))
	assert.Equal(t, "Conflict", statusLabel("conflict"))
	assert.Equal(t, "Blocked", statusLabel("blocked"))
}

// Test_csp_on_all_routes verifies CSP header is present on all routes.
func Test_csp_on_all_routes(t *testing.T) {
	server := newTestServer(t)

	routes := []string{"/", "/api/status", "/healthz", "/readyz", "/static/app.css"}

	for _, route := range routes {
		t.Run(route, func(t *testing.T) {
			req := newTestRequest(route)
			rec := httptest.NewRecorder()

			server.engine.ServeHTTP(rec, req)

			csp := rec.Header().Get("Content-Security-Policy")
			assert.NotEmpty(t, csp, "CSP header should be present on %s", route)
		})
	}
}

// Test_pending_rule_countdown verifies pending rules show removal countdown.
func Test_pending_rule_countdown(t *testing.T) {
	// Create state with a pending rule
	tmpDir := t.TempDir()
	statePath := filepath.Join(tmpDir, "state.json")

	st := rules.NewState()
	deleteAfter := time.Now().Add(7 * time.Hour)
	st.Rules["pending.example.com|"] = rules.Rule{
		Hostname:      "pending.example.com",
		Service:       "http://pending:80",
		ContainerName: "pending",
		Status:        "pending",
		DeleteAfter:   &deleteAfter,
		FirstSeen:     time.Now().Add(-24 * time.Hour),
	}
	require.NoError(t, state.Save(statePath, st))

	log := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	_, ring := logx.NewJSONLogger(slog.LevelInfo, 100)
	cfAPI := cf.NewFake()

	eng := reconcile.New(
		log, nil, cfAPI, statePath, "test", []string{"cloudflare.tunnel"}, 8*time.Hour,
	)
	daemon := reconcile.NewDaemon(
		log, eng, nil, cfAPI,
		reconcile.DaemonConfig{Debounce: 2 * time.Second, ResyncInterval: 5 * time.Minute},
		config.CloudflaredConfig{}, "", true,
	)

	server := NewServer(log, daemon, ring, cfAPI, statePath, Config{
		Listen: ":0", DryRun: true, ResyncInterval: 5 * time.Minute,
	})

	req := newTestRequest("/api/status")
	rec := httptest.NewRecorder()
	server.engine.ServeHTTP(rec, req)

	var data StatusData
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &data))

	assert.Equal(t, 1, data.Counts.Pending)
	assert.Equal(t, "pending", data.Rules[0].Status)
	assert.NotNil(t, data.Rules[0].PendingRemoval)
}

// Test_dry_run_banner verifies the dry-run banner appears in HTML.
func Test_dry_run_banner(t *testing.T) {
	server := newTestServer(t)

	req := newTestRequest("/")
	rec := httptest.NewRecorder()

	server.engine.ServeHTTP(rec, req)

	body := rec.Body.String()
	assert.Contains(t, body, "dry-run", "Dry-run banner should be visible")
}

// Test_json_api_schema verifies the JSON API matches the expected schema fields.
func Test_json_api_schema(t *testing.T) {
	server := newTestServer(t)

	req := newTestRequest("/api/status")
	rec := httptest.NewRecorder()

	server.engine.ServeHTTP(rec, req)

	body := rec.Body.Bytes()

	// Check all required top-level fields exist
	var raw map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(body, &raw))

	requiredFields := []string{
		"startedAt", "uptimeSeconds", "dryRun", "tunnel", "cloudflared",
		"counts", "reconcile", "cloudflareApi", "docker", "rules",
		"orphanDns", "labelWarnings", "recentEvents",
	}

	for _, field := range requiredFields {
		assert.NotNil(t, raw[field], "Field %s should be present in JSON response", field)
	}
}

// Test_no_external_urls_in_html verifies no external resource URLs in the HTML output.
// Route links (<a href="https://...">) are allowed as they intentionally open services in new tabs.
func Test_no_external_urls_in_html(t *testing.T) {
	server := newTestServer(t)

	req := newTestRequest("/")
	rec := httptest.NewRecorder()

	server.engine.ServeHTTP(rec, req)

	body := rec.Body.String()

	// Check src= attributes don't load external resources (href= is allowed for route links)
	lines := strings.Split(body, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.Contains(line, "src=") {
			if strings.Contains(line, "http://") || strings.Contains(line, "https://") {
				t.Errorf("Found external resource URL in HTML: %s", line)
			}
		}
	}
}
