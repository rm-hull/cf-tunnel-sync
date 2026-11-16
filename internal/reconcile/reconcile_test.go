package reconcile_test

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/rm-hull/cf-tunnel-sync/internal/cf"
	"github.com/rm-hull/cf-tunnel-sync/internal/docker"
	"github.com/rm-hull/cf-tunnel-sync/internal/reconcile"
	"github.com/rm-hull/cf-tunnel-sync/internal/rules"
	"github.com/rm-hull/cf-tunnel-sync/internal/state"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

func tempStateDir(t *testing.T) string {
	t.Helper()
	return t.TempDir()
}

func setupFakeCF(t *testing.T) *cf.Fake {
	t.Helper()
	fake := cf.NewFake()
	fake.AddTunnel(cf.Tunnel{
		ID:        "tunnel-uuid",
		Name:      "homelab",
		ConfigSrc: "cloudflare",
		Status:    "healthy",
	})
	fake.AddZone(cf.Zone{ID: "zone-1", Name: "example.com"})
	return fake
}

func setupFakeDocker(t *testing.T, containers ...docker.Container) docker.API {
	t.Helper()
	return docker.NewFake(containers...)
}

func newEngine(t *testing.T, dockerAPI docker.API, cfAPI *cf.Fake, statePath string, opts ...reconcile.Option) *reconcile.Engine {
	t.Helper()
	return reconcile.New(
		testLogger(),
		dockerAPI,
		cfAPI,
		statePath,
		"homelab",
		[]string{"cloudflare.tunnel"},
		8*time.Hour,
		opts...,
	)
}

func TestReconcile_idempotent(t *testing.T) {
	ctx := context.Background()
	stateDir := tempStateDir(t)
	statePath := filepath.Join(stateDir, "state.json")

	cfAPI := setupFakeCF(t)
	dockerAPI := setupFakeDocker(t)

	eng := newEngine(t, dockerAPI, cfAPI, statePath)
	res1 := eng.Run(ctx)
	require.NoError(t, res1.LastError)

	firstPutCalls := cfAPI.IngressPutCalls

	res2 := eng.Run(ctx)
	require.NoError(t, res2.LastError)

	assert.Equal(t, firstPutCalls, cfAPI.IngressPutCalls)
	assert.False(t, res2.IngressUpdated)
}

func TestReconcile_adds_ingress_then_dns(t *testing.T) {
	ctx := context.Background()
	stateDir := tempStateDir(t)
	statePath := filepath.Join(stateDir, "state.json")

	cfAPI := setupFakeCF(t)
	whoami := docker.Container{
		ID: "c1", Name: "whoami", State: "running",
		Labels: map[string]string{
			"cloudflare.tunnel.enable":   "true",
			"cloudflare.tunnel.hostname": "whoami.example.com",
			"cloudflare.tunnel.service":  "http://whoami:80",
		},
	}
	dockerAPI := setupFakeDocker(t, whoami)

	eng := newEngine(t, dockerAPI, cfAPI, statePath)
	res := eng.Run(ctx)
	require.NoError(t, res.LastError)

	assert.True(t, res.IngressUpdated)
	assert.Equal(t, 1, cfAPI.IngressPutCalls)

	records, _ := cfAPI.ListDNSRecords(ctx, "zone-1", "whoami.example.com", "CNAME")
	assert.Len(t, records, 1)
	assert.Equal(t, "tunnel-uuid.cfargotunnel.com", records[0].Content)
}

func TestReconcile_removal_order(t *testing.T) {
	ctx := context.Background()
	stateDir := tempStateDir(t)
	statePath := filepath.Join(stateDir, "state.json")

	cfAPI := setupFakeCF(t)
	cfAPI.AddDNSRecord("zone-1", cf.DNSRecord{
		ID: "dns-1", Name: "whoami.example.com", Type: "CNAME",
		Content: "tunnel-uuid.cfargotunnel.com", Proxied: true, ZoneID: "zone-1",
	})

	whoami := docker.Container{
		ID: "c1", Name: "whoami", State: "running",
		Labels: map[string]string{
			"cloudflare.tunnel.enable":   "true",
			"cloudflare.tunnel.hostname": "whoami.example.com",
			"cloudflare.tunnel.service":  "http://whoami:80",
		},
	}

	dockerAPI := setupFakeDocker(t, whoami)
	eng := newEngine(t, dockerAPI, cfAPI, statePath)
	res1 := eng.Run(ctx)
	require.NoError(t, res1.LastError)

	dockerAPI = setupFakeDocker(t)
	eng = newEngine(t, dockerAPI, cfAPI, statePath)
	res2 := eng.Run(ctx)
	require.NoError(t, res2.LastError)

	assert.Equal(t, 1, res2.RulesPending)
	// Ingress unchanged because pending rules stay in ingress (same as before)
	assert.False(t, res2.IngressUpdated)
}

func TestReconcile_dns_conflict_not_modified(t *testing.T) {
	ctx := context.Background()
	stateDir := tempStateDir(t)
	statePath := filepath.Join(stateDir, "state.json")

	cfAPI := setupFakeCF(t)
	cfAPI.AddDNSRecord("zone-1", cf.DNSRecord{
		ID: "dns-1", Name: "whoami.example.com", Type: "CNAME",
		Content: "other-tunnel.cfargotunnel.com", Proxied: true, ZoneID: "zone-1",
	})

	whoami := docker.Container{
		ID: "c1", Name: "whoami", State: "running",
		Labels: map[string]string{
			"cloudflare.tunnel.enable":   "true",
			"cloudflare.tunnel.hostname": "whoami.example.com",
			"cloudflare.tunnel.service":  "http://whoami:80",
		},
	}
	dockerAPI := setupFakeDocker(t, whoami)

	eng := newEngine(t, dockerAPI, cfAPI, statePath)
	res := eng.Run(ctx)
	require.NoError(t, res.LastError)

	records, _ := cfAPI.ListDNSRecords(ctx, "zone-1", "whoami.example.com", "CNAME")
	assert.Len(t, records, 1)
	assert.Equal(t, "other-tunnel.cfargotunnel.com", records[0].Content)
}

func TestReconcile_shared_hostname_dns(t *testing.T) {
	ctx := context.Background()
	stateDir := tempStateDir(t)
	statePath := filepath.Join(stateDir, "state.json")

	cfAPI := setupFakeCF(t)

	app1 := docker.Container{
		ID: "c1", Name: "app1", State: "running",
		Labels: map[string]string{
			"cloudflare.tunnel.enable":   "true",
			"cloudflare.tunnel.hostname": "app.example.com",
			"cloudflare.tunnel.service":  "http://app1:80",
			"cloudflare.tunnel.path":     "/api",
		},
	}
	app2 := docker.Container{
		ID: "c2", Name: "app2", State: "running",
		Labels: map[string]string{
			"cloudflare.tunnel.enable":   "true",
			"cloudflare.tunnel.hostname": "app.example.com",
			"cloudflare.tunnel.service":  "http://app2:80",
			"cloudflare.tunnel.path":     "/web",
		},
	}
	dockerAPI := setupFakeDocker(t, app1, app2)

	eng := newEngine(t, dockerAPI, cfAPI, statePath)
	res := eng.Run(ctx)
	require.NoError(t, res.LastError)

	records, _ := cfAPI.ListDNSRecords(ctx, "zone-1", "app.example.com", "CNAME")
	assert.Len(t, records, 1)
}

func TestReconcile_dns_delete_refused_wrong_target(t *testing.T) {
	ctx := context.Background()
	stateDir := tempStateDir(t)
	statePath := filepath.Join(stateDir, "state.json")

	cfAPI := setupFakeCF(t)
	cfAPI.AddDNSRecord("zone-1", cf.DNSRecord{
		ID: "dns-1", Name: "whoami.example.com", Type: "CNAME",
		Content: "other-tunnel.cfargotunnel.com", Proxied: true, ZoneID: "zone-1",
	})

	st := rules.NewState()
	st.Tunnel = rules.TunnelInfo{ID: "tunnel-uuid", Name: "homelab"}
	expiredTime := time.Now().Add(-time.Hour)
	deleteAfter := time.Now().Add(-time.Minute)
	st.Rules["whoami.example.com"] = rules.Rule{
		Hostname: "whoami.example.com", Service: "http://whoami:80", Status: "pending",
		FirstSeen: expiredTime, LastSeen: expiredTime,
		DeleteAfter: &deleteAfter, PendingSince: &expiredTime,
	}
	require.NoError(t, state.Save(statePath, st))

	dockerAPI := setupFakeDocker(t)
	eng := newEngine(t, dockerAPI, cfAPI, statePath)
	res := eng.Run(ctx)
	require.NoError(t, res.LastError)

	records, _ := cfAPI.ListDNSRecords(ctx, "zone-1", "whoami.example.com", "CNAME")
	assert.Len(t, records, 1)
	assert.Equal(t, "dns-1", records[0].ID)
}

func TestReconcile_dns_failure_isolation(t *testing.T) {
	ctx := context.Background()
	stateDir := tempStateDir(t)
	statePath := filepath.Join(stateDir, "state.json")

	cfAPI := setupFakeCF(t)

	whoami := docker.Container{
		ID: "c1", Name: "whoami", State: "running",
		Labels: map[string]string{
			"cloudflare.tunnel.enable":   "true",
			"cloudflare.tunnel.hostname": "whoami.example.com",
			"cloudflare.tunnel.service":  "http://whoami:80",
		},
	}
	vault := docker.Container{
		ID: "c2", Name: "vault", State: "running",
		Labels: map[string]string{
			"cloudflare.tunnel.enable":   "true",
			"cloudflare.tunnel.hostname": "vault.example.com",
			"cloudflare.tunnel.service":  "http://vault:8200",
		},
	}
	dockerAPI := setupFakeDocker(t, whoami, vault)

	eng := newEngine(t, dockerAPI, cfAPI, statePath)
	res := eng.Run(ctx)
	require.NoError(t, res.LastError)

	assert.Equal(t, 2, res.RulesAdded)
}

func TestReconcile_dry_run_no_mutations(t *testing.T) {
	ctx := context.Background()
	stateDir := tempStateDir(t)
	statePath := filepath.Join(stateDir, "state.json")

	cfAPI := setupFakeCF(t)

	whoami := docker.Container{
		ID: "c1", Name: "whoami", State: "running",
		Labels: map[string]string{
			"cloudflare.tunnel.enable":   "true",
			"cloudflare.tunnel.hostname": "whoami.example.com",
			"cloudflare.tunnel.service":  "http://whoami:80",
		},
	}
	dockerAPI := setupFakeDocker(t, whoami)

	eng := newEngine(t, dockerAPI, cfAPI, statePath, reconcile.WithDryRun(true))
	res := eng.Run(ctx)
	require.NoError(t, res.LastError)
	assert.True(t, res.DryRun)

	assert.Equal(t, 0, cfAPI.IngressPutCalls)

	records, _ := cfAPI.ListDNSRecords(ctx, "zone-1", "whoami.example.com", "CNAME")
	assert.Empty(t, records)
}

func TestReconcile_tunnel_adopted(t *testing.T) {
	ctx := context.Background()
	stateDir := tempStateDir(t)
	statePath := filepath.Join(stateDir, "state.json")

	cfAPI := setupFakeCF(t)
	dockerAPI := setupFakeDocker(t)

	eng := newEngine(t, dockerAPI, cfAPI, statePath)
	res := eng.Run(ctx)
	require.NoError(t, res.LastError)

	assert.Equal(t, "tunnel-uuid", res.Tunnel.ID)
	assert.Equal(t, "homelab", res.Tunnel.Name)
}

func TestReconcile_tunnel_created_when_absent(t *testing.T) {
	ctx := context.Background()
	stateDir := tempStateDir(t)
	statePath := filepath.Join(stateDir, "state.json")

	cfAPI := cf.NewFake()
	cfAPI.AddZone(cf.Zone{ID: "zone-1", Name: "example.com"})
	dockerAPI := setupFakeDocker(t)

	eng := newEngine(t, dockerAPI, cfAPI, statePath)
	res := eng.Run(ctx)

	assert.Error(t, res.LastError)
	assert.ErrorIs(t, res.LastError, cf.ErrNotFound)
}

func TestReconcile_remote_only_rules_removed(t *testing.T) {
	ctx := context.Background()
	stateDir := tempStateDir(t)
	statePath := filepath.Join(stateDir, "state.json")

	cfAPI := setupFakeCF(t)
	cfAPI.SetIngress("tunnel-uuid", cf.IngressConfig{
		Version: 1,
		Rules: []rules.IngressEntry{
			{Hostname: "remote.example.com", Service: "http://remote:80"},
			{Service: "http_status:404"},
		},
	})

	dockerAPI := setupFakeDocker(t)

	eng := newEngine(t, dockerAPI, cfAPI, statePath)
	res := eng.Run(ctx)
	require.NoError(t, res.LastError)

	assert.True(t, res.IngressUpdated)
	assert.Equal(t, 1, cfAPI.IngressPutCalls)

	cfg, _ := cfAPI.GetIngress(ctx, "tunnel-uuid")
	assert.Len(t, cfg.Rules, 1)
	assert.Equal(t, "http_status:404", cfg.Rules[0].Service)
}

func TestReconcile_state_persisted(t *testing.T) {
	ctx := context.Background()
	stateDir := tempStateDir(t)
	statePath := filepath.Join(stateDir, "state.json")

	cfAPI := setupFakeCF(t)

	whoami := docker.Container{
		ID: "c1", Name: "whoami", State: "running",
		Labels: map[string]string{
			"cloudflare.tunnel.enable":   "true",
			"cloudflare.tunnel.hostname": "whoami.example.com",
			"cloudflare.tunnel.service":  "http://whoami:80",
		},
	}
	dockerAPI := setupFakeDocker(t, whoami)

	eng := newEngine(t, dockerAPI, cfAPI, statePath)
	res := eng.Run(ctx)
	require.NoError(t, res.LastError)

	st, err := state.Load(statePath)
	require.NoError(t, err)
	assert.Contains(t, st.Rules, "whoami.example.com")
	assert.Equal(t, "tunnel-uuid", st.Tunnel.ID)
}

func TestReconcile_expired_rules_cleaned(t *testing.T) {
	ctx := context.Background()
	stateDir := tempStateDir(t)
	statePath := filepath.Join(stateDir, "state.json")

	cfAPI := setupFakeCF(t)
	cfAPI.AddDNSRecord("zone-1", cf.DNSRecord{
		ID: "dns-1", Name: "whoami.example.com", Type: "CNAME",
		Content: "tunnel-uuid.cfargotunnel.com", Proxied: true, ZoneID: "zone-1",
	})

	st := rules.NewState()
	st.Tunnel = rules.TunnelInfo{ID: "tunnel-uuid", Name: "homelab"}
	expiredTime := time.Now().Add(-time.Hour)
	deleteAfter := time.Now().Add(-time.Minute)
	st.Rules["whoami.example.com"] = rules.Rule{
		Hostname: "whoami.example.com", Service: "http://whoami:80", Status: "pending",
		FirstSeen: expiredTime, LastSeen: expiredTime,
		DeleteAfter: &deleteAfter, PendingSince: &expiredTime,
	}
	require.NoError(t, state.Save(statePath, st))

	dockerAPI := setupFakeDocker(t)
	eng := newEngine(t, dockerAPI, cfAPI, statePath)
	res := eng.Run(ctx)
	require.NoError(t, res.LastError)

	assert.Equal(t, 1, res.RulesExpired)

	records, _ := cfAPI.ListDNSRecords(ctx, "zone-1", "whoami.example.com", "CNAME")
	assert.Empty(t, records)

	st, err := state.Load(statePath)
	require.NoError(t, err)
	assert.NotContains(t, st.Rules, "whoami.example.com")
}

func TestReconcile_pending_in_ingress(t *testing.T) {
	ctx := context.Background()
	stateDir := tempStateDir(t)
	statePath := filepath.Join(stateDir, "state.json")

	cfAPI := setupFakeCF(t)

	whoami := docker.Container{
		ID: "c1", Name: "whoami", State: "running",
		Labels: map[string]string{
			"cloudflare.tunnel.enable":   "true",
			"cloudflare.tunnel.hostname": "whoami.example.com",
			"cloudflare.tunnel.service":  "http://whoami:80",
		},
	}

	dockerAPI := setupFakeDocker(t, whoami)
	eng := newEngine(t, dockerAPI, cfAPI, statePath)
	res1 := eng.Run(ctx)
	require.NoError(t, res1.LastError)

	dockerAPI = setupFakeDocker(t)
	eng = newEngine(t, dockerAPI, cfAPI, statePath)
	res2 := eng.Run(ctx)
	require.NoError(t, res2.LastError)

	assert.Equal(t, 1, res2.RulesPending)

	cfg, _ := cfAPI.GetIngress(ctx, "tunnel-uuid")
	found := false
	for _, r := range cfg.Rules {
		if r.Hostname == "whoami.example.com" {
			found = true
			break
		}
	}
	assert.True(t, found, "pending rule should still be in ingress")
}

func TestReconcile_no_zone_still_ingress(t *testing.T) {
	ctx := context.Background()
	stateDir := tempStateDir(t)
	statePath := filepath.Join(stateDir, "state.json")

	cfAPI := cf.NewFake()
	cfAPI.AddTunnel(cf.Tunnel{
		ID: "tunnel-uuid", Name: "homelab", ConfigSrc: "cloudflare", Status: "healthy",
	})

	whoami := docker.Container{
		ID: "c1", Name: "whoami", State: "running",
		Labels: map[string]string{
			"cloudflare.tunnel.enable":   "true",
			"cloudflare.tunnel.hostname": "whoami.unknown.net",
			"cloudflare.tunnel.service":  "http://whoami:80",
		},
	}
	dockerAPI := setupFakeDocker(t, whoami)

	eng := reconcile.New(
		testLogger(), dockerAPI, cfAPI, statePath, "homelab",
		[]string{"cloudflare.tunnel"}, 8*time.Hour,
	)
	res := eng.Run(ctx)
	require.NoError(t, res.LastError)

	assert.True(t, res.IngressUpdated)
}

func TestReconcile_duration_tracked(t *testing.T) {
	ctx := context.Background()
	stateDir := tempStateDir(t)
	statePath := filepath.Join(stateDir, "state.json")

	cfAPI := setupFakeCF(t)
	dockerAPI := setupFakeDocker(t)

	eng := newEngine(t, dockerAPI, cfAPI, statePath)
	res := eng.Run(ctx)
	require.NoError(t, res.LastError)

	// Duration may be zero for very fast runs; just verify it's non-negative
	assert.GreaterOrEqual(t, res.Duration, time.Duration(0))
}

func TestReconcile_empty_desired(t *testing.T) {
	ctx := context.Background()
	stateDir := tempStateDir(t)
	statePath := filepath.Join(stateDir, "state.json")

	cfAPI := setupFakeCF(t)
	dockerAPI := setupFakeDocker(t)

	eng := newEngine(t, dockerAPI, cfAPI, statePath)
	res := eng.Run(ctx)
	require.NoError(t, res.LastError)

	cfg, _ := cfAPI.GetIngress(ctx, "tunnel-uuid")
	assert.Len(t, cfg.Rules, 1)
	assert.Equal(t, "http_status:404", cfg.Rules[0].Service)
}

func TestReconcile_wildcard_hostname(t *testing.T) {
	ctx := context.Background()
	stateDir := tempStateDir(t)
	statePath := filepath.Join(stateDir, "state.json")

	cfAPI := setupFakeCF(t)

	wildcard := docker.Container{
		ID: "c1", Name: "wildcard", State: "running",
		Labels: map[string]string{
			"cloudflare.tunnel.enable":   "true",
			"cloudflare.tunnel.hostname": "*.example.com",
			"cloudflare.tunnel.service":  "http://wildcard:80",
		},
	}
	dockerAPI := setupFakeDocker(t, wildcard)

	eng := newEngine(t, dockerAPI, cfAPI, statePath)
	res := eng.Run(ctx)
	require.NoError(t, res.LastError)

	assert.Equal(t, 1, res.RulesAdded)
	assert.True(t, res.IngressUpdated)
}

func TestReconcile_indexed_rules(t *testing.T) {
	ctx := context.Background()
	stateDir := tempStateDir(t)
	statePath := filepath.Join(stateDir, "state.json")

	cfAPI := setupFakeCF(t)

	multi := docker.Container{
		ID: "c1", Name: "multi", State: "running",
		Labels: map[string]string{
			"cloudflare.tunnel.enable":     "true",
			"cloudflare.tunnel.0.hostname": "app0.example.com",
			"cloudflare.tunnel.0.service":  "http://app0:80",
			"cloudflare.tunnel.1.hostname": "app1.example.com",
			"cloudflare.tunnel.1.service":  "http://app1:80",
		},
	}
	dockerAPI := setupFakeDocker(t, multi)

	eng := newEngine(t, dockerAPI, cfAPI, statePath)
	res := eng.Run(ctx)
	require.NoError(t, res.LastError)

	assert.Equal(t, 2, res.RulesAdded)
}

func TestReconcile_path_rules(t *testing.T) {
	ctx := context.Background()
	stateDir := tempStateDir(t)
	statePath := filepath.Join(stateDir, "state.json")

	cfAPI := setupFakeCF(t)

	app := docker.Container{
		ID: "c1", Name: "app", State: "running",
		Labels: map[string]string{
			"cloudflare.tunnel.enable":   "true",
			"cloudflare.tunnel.hostname": "app.example.com",
			"cloudflare.tunnel.service":  "http://app:80",
			"cloudflare.tunnel.path":     "/api",
		},
	}
	dockerAPI := setupFakeDocker(t, app)

	eng := newEngine(t, dockerAPI, cfAPI, statePath)
	res := eng.Run(ctx)
	require.NoError(t, res.LastError)

	assert.Equal(t, 1, res.RulesAdded)

	cfg, _ := cfAPI.GetIngress(ctx, "tunnel-uuid")
	found := false
	for _, r := range cfg.Rules {
		if r.Hostname == "app.example.com" && r.Path == "/api" {
			found = true
			break
		}
	}
	assert.True(t, found)
}

func TestReconcile_origin_request(t *testing.T) {
	ctx := context.Background()
	stateDir := tempStateDir(t)
	statePath := filepath.Join(stateDir, "state.json")

	cfAPI := setupFakeCF(t)

	app := docker.Container{
		ID: "c1", Name: "app", State: "running",
		Labels: map[string]string{
			"cloudflare.tunnel.enable":        "true",
			"cloudflare.tunnel.hostname":      "app.example.com",
			"cloudflare.tunnel.service":       "https://app:443",
			"cloudflare.tunnel.no_tls_verify": "true",
			"cloudflare.tunnel.originsrvname": "app.internal",
		},
	}
	dockerAPI := setupFakeDocker(t, app)

	eng := newEngine(t, dockerAPI, cfAPI, statePath)
	res := eng.Run(ctx)
	require.NoError(t, res.LastError)

	cfg, _ := cfAPI.GetIngress(ctx, "tunnel-uuid")
	require.Len(t, cfg.Rules, 2)
	assert.NotNil(t, cfg.Rules[0].OriginRequest)
	assert.True(t, cfg.Rules[0].OriginRequest.NoTLSVerify)
	assert.Equal(t, "app.internal", cfg.Rules[0].OriginRequest.OriginServerName)
}

func TestReconcile_zonename_override(t *testing.T) {
	ctx := context.Background()
	stateDir := tempStateDir(t)
	statePath := filepath.Join(stateDir, "state.json")

	cfAPI := cf.NewFake()
	cfAPI.AddTunnel(cf.Tunnel{
		ID: "tunnel-uuid", Name: "homelab", ConfigSrc: "cloudflare", Status: "healthy",
	})
	cfAPI.AddZone(cf.Zone{ID: "zone-1", Name: "example.com"})
	cfAPI.AddZone(cf.Zone{ID: "zone-2", Name: "other.com"})

	app := docker.Container{
		ID: "c1", Name: "app", State: "running",
		Labels: map[string]string{
			"cloudflare.tunnel.enable":   "true",
			"cloudflare.tunnel.hostname": "app.example.com",
			"cloudflare.tunnel.service":  "http://app:80",
			"cloudflare.tunnel.zonename": "other.com",
		},
	}
	dockerAPI := setupFakeDocker(t, app)

	eng := newEngine(t, dockerAPI, cfAPI, statePath)
	res := eng.Run(ctx)
	require.NoError(t, res.LastError)

	records, _ := cfAPI.ListDNSRecords(ctx, "zone-2", "app.example.com", "CNAME")
	assert.Len(t, records, 1)
}

func TestReconcile_multiple_zones(t *testing.T) {
	ctx := context.Background()
	stateDir := tempStateDir(t)
	statePath := filepath.Join(stateDir, "state.json")

	cfAPI := cf.NewFake()
	cfAPI.AddTunnel(cf.Tunnel{
		ID: "tunnel-uuid", Name: "homelab", ConfigSrc: "cloudflare", Status: "healthy",
	})
	cfAPI.AddZone(cf.Zone{ID: "zone-1", Name: "example.com"})
	cfAPI.AddZone(cf.Zone{ID: "zone-2", Name: "other.com"})

	app1 := docker.Container{
		ID: "c1", Name: "app1", State: "running",
		Labels: map[string]string{
			"cloudflare.tunnel.enable":   "true",
			"cloudflare.tunnel.hostname": "app1.example.com",
			"cloudflare.tunnel.service":  "http://app1:80",
		},
	}
	app2 := docker.Container{
		ID: "c2", Name: "app2", State: "running",
		Labels: map[string]string{
			"cloudflare.tunnel.enable":   "true",
			"cloudflare.tunnel.hostname": "app2.other.com",
			"cloudflare.tunnel.service":  "http://app2:80",
		},
	}
	dockerAPI := setupFakeDocker(t, app1, app2)

	eng := newEngine(t, dockerAPI, cfAPI, statePath)
	res := eng.Run(ctx)
	require.NoError(t, res.LastError)

	assert.Equal(t, 2, res.RulesAdded)

	records1, _ := cfAPI.ListDNSRecords(ctx, "zone-1", "app1.example.com", "CNAME")
	assert.Len(t, records1, 1)

	records2, _ := cfAPI.ListDNSRecords(ctx, "zone-2", "app2.other.com", "CNAME")
	assert.Len(t, records2, 1)
}

func TestReconcile_access_bypass_allowed(t *testing.T) {
	ctx := context.Background()
	stateDir := tempStateDir(t)
	statePath := filepath.Join(stateDir, "state.json")

	cfAPI := setupFakeCF(t)

	app := docker.Container{
		ID: "c1", Name: "app", State: "running",
		Labels: map[string]string{
			"cloudflare.tunnel.enable":        "true",
			"cloudflare.tunnel.hostname":      "app.example.com",
			"cloudflare.tunnel.service":       "http://app:80",
			"cloudflare.tunnel.access.policy": "bypass",
		},
	}
	dockerAPI := setupFakeDocker(t, app)

	eng := newEngine(t, dockerAPI, cfAPI, statePath)
	res := eng.Run(ctx)
	require.NoError(t, res.LastError)

	assert.Equal(t, 1, res.RulesAdded)
}

func TestReconcile_access_labels_blocked(t *testing.T) {
	ctx := context.Background()
	stateDir := tempStateDir(t)
	statePath := filepath.Join(stateDir, "state.json")

	cfAPI := setupFakeCF(t)

	app := docker.Container{
		ID: "c1", Name: "app", State: "running",
		Labels: map[string]string{
			"cloudflare.tunnel.enable":        "true",
			"cloudflare.tunnel.hostname":      "app.example.com",
			"cloudflare.tunnel.service":       "http://app:80",
			"cloudflare.tunnel.access.policy": "authenticate",
		},
	}
	dockerAPI := setupFakeDocker(t, app)

	eng := newEngine(t, dockerAPI, cfAPI, statePath)
	res := eng.Run(ctx)
	require.NoError(t, res.LastError)

	assert.Equal(t, 0, res.RulesAdded)
}

func TestReconcile_rule_changes(t *testing.T) {
	ctx := context.Background()
	stateDir := tempStateDir(t)
	statePath := filepath.Join(stateDir, "state.json")

	cfAPI := setupFakeCF(t)

	whoami := docker.Container{
		ID: "c1", Name: "whoami", State: "running",
		Labels: map[string]string{
			"cloudflare.tunnel.enable":   "true",
			"cloudflare.tunnel.hostname": "whoami.example.com",
			"cloudflare.tunnel.service":  "http://whoami:80",
		},
	}
	dockerAPI := setupFakeDocker(t, whoami)
	eng := newEngine(t, dockerAPI, cfAPI, statePath)
	res1 := eng.Run(ctx)
	require.NoError(t, res1.LastError)
	assert.Equal(t, 1, res1.RulesAdded)

	vault := docker.Container{
		ID: "c2", Name: "vault", State: "running",
		Labels: map[string]string{
			"cloudflare.tunnel.enable":   "true",
			"cloudflare.tunnel.hostname": "vault.example.com",
			"cloudflare.tunnel.service":  "http://vault:8200",
		},
	}
	dockerAPI = setupFakeDocker(t, whoami, vault)
	eng = newEngine(t, dockerAPI, cfAPI, statePath)
	res2 := eng.Run(ctx)
	require.NoError(t, res2.LastError)
	assert.Equal(t, 1, res2.RulesAdded)
	assert.Equal(t, 0, res2.RulesUpdated) // whoami unchanged, only vault is new

	dockerAPI = setupFakeDocker(t, vault)
	eng = newEngine(t, dockerAPI, cfAPI, statePath)
	res3 := eng.Run(ctx)
	require.NoError(t, res3.LastError)
	assert.Equal(t, 1, res3.RulesPending)
}

func TestReconcile_restart_from_state(t *testing.T) {
	ctx := context.Background()
	stateDir := tempStateDir(t)
	statePath := filepath.Join(stateDir, "state.json")

	cfAPI := setupFakeCF(t)

	st := rules.NewState()
	st.Tunnel = rules.TunnelInfo{ID: "tunnel-uuid", Name: "homelab"}
	now := time.Now()
	st.Rules["whoami.example.com"] = rules.Rule{
		Hostname: "whoami.example.com", Service: "http://whoami:80", Status: "active",
		FirstSeen: now, LastSeen: now, ContainerName: "whoami",
	}
	require.NoError(t, state.Save(statePath, st))

	cfAPI.SetIngress("tunnel-uuid", cf.IngressConfig{
		Version: 1,
		Rules: []rules.IngressEntry{
			{Hostname: "whoami.example.com", Service: "http://whoami:80"},
			{Service: "http_status:404"},
		},
	})
	cfAPI.AddDNSRecord("zone-1", cf.DNSRecord{
		ID: "dns-1", Name: "whoami.example.com", Type: "CNAME",
		Content: "tunnel-uuid.cfargotunnel.com", Proxied: true, ZoneID: "zone-1",
	})

	whoami := docker.Container{
		ID: "c1", Name: "whoami", State: "running",
		Labels: map[string]string{
			"cloudflare.tunnel.enable":   "true",
			"cloudflare.tunnel.hostname": "whoami.example.com",
			"cloudflare.tunnel.service":  "http://whoami:80",
		},
	}
	dockerAPI := setupFakeDocker(t, whoami)

	eng := newEngine(t, dockerAPI, cfAPI, statePath)
	res := eng.Run(ctx)
	require.NoError(t, res.LastError)

	assert.False(t, res.IngressUpdated)
}

func TestReconcile_state_tunnel_info(t *testing.T) {
	ctx := context.Background()
	stateDir := tempStateDir(t)
	statePath := filepath.Join(stateDir, "state.json")

	cfAPI := setupFakeCF(t)
	dockerAPI := setupFakeDocker(t)

	eng := newEngine(t, dockerAPI, cfAPI, statePath)
	res := eng.Run(ctx)
	require.NoError(t, res.LastError)

	st, err := state.Load(statePath)
	require.NoError(t, err)
	assert.Equal(t, "tunnel-uuid", st.Tunnel.ID)
	assert.Equal(t, "homelab", st.Tunnel.Name)
	assert.Equal(t, 1, st.Version)
}

func TestReconcile_corrupt_state(t *testing.T) {
	ctx := context.Background()
	stateDir := tempStateDir(t)
	statePath := filepath.Join(stateDir, "state.json")

	require.NoError(t, os.WriteFile(statePath, []byte("{corrupt"), 0o600))

	cfAPI := setupFakeCF(t)
	dockerAPI := setupFakeDocker(t)

	eng := newEngine(t, dockerAPI, cfAPI, statePath)
	res := eng.Run(ctx)

	require.NoError(t, res.LastError)
}

func TestReconcile_state_version_error(t *testing.T) {
	ctx := context.Background()
	stateDir := tempStateDir(t)
	statePath := filepath.Join(stateDir, "state.json")

	badState := &rules.State{Version: 99, Rules: map[string]rules.Rule{}}
	require.NoError(t, state.Save(statePath, badState))

	cfAPI := setupFakeCF(t)
	dockerAPI := setupFakeDocker(t)

	eng := newEngine(t, dockerAPI, cfAPI, statePath)
	res := eng.Run(ctx)

	assert.Error(t, res.LastError)
	assert.Contains(t, res.LastError.Error(), "unknown state version")
}

func TestReconcile_cf_find_tunnel_error(t *testing.T) {
	ctx := context.Background()
	stateDir := tempStateDir(t)
	statePath := filepath.Join(stateDir, "state.json")

	cfAPI := cf.NewFake()
	dockerAPI := setupFakeDocker(t)

	eng := newEngine(t, dockerAPI, cfAPI, statePath)
	res := eng.Run(ctx)

	assert.Error(t, res.LastError)
	assert.ErrorIs(t, res.LastError, cf.ErrNotFound)
}

func TestReconcile_dry_run_with_tunnel(t *testing.T) {
	ctx := context.Background()
	stateDir := tempStateDir(t)
	statePath := filepath.Join(stateDir, "state.json")

	cfAPI := setupFakeCF(t)

	whoami := docker.Container{
		ID: "c1", Name: "whoami", State: "running",
		Labels: map[string]string{
			"cloudflare.tunnel.enable":   "true",
			"cloudflare.tunnel.hostname": "whoami.example.com",
			"cloudflare.tunnel.service":  "http://whoami:80",
		},
	}
	dockerAPI := setupFakeDocker(t, whoami)

	eng := newEngine(t, dockerAPI, cfAPI, statePath, reconcile.WithDryRun(true))
	res := eng.Run(ctx)
	require.NoError(t, res.LastError)
	assert.True(t, res.DryRun)

	assert.Equal(t, 0, cfAPI.IngressPutCalls)

	records, _ := cfAPI.ListDNSRecords(ctx, "zone-1", "whoami.example.com", "CNAME")
	assert.Empty(t, records)
}

func TestReconcile_multiple_prefixes(t *testing.T) {
	ctx := context.Background()
	stateDir := tempStateDir(t)
	statePath := filepath.Join(stateDir, "state.json")

	cfAPI := setupFakeCF(t)

	whoami := docker.Container{
		ID: "c1", Name: "whoami", State: "running",
		Labels: map[string]string{
			"cloudflare.tunnel.enable":   "true",
			"cloudflare.tunnel.hostname": "whoami.example.com",
			"cloudflare.tunnel.service":  "http://whoami:80",
		},
	}
	vault := docker.Container{
		ID: "c2", Name: "vault", State: "running",
		Labels: map[string]string{
			"dockflare.enable":   "true",
			"dockflare.hostname": "vault.example.com",
			"dockflare.service":  "http://vault:8200",
		},
	}
	dockerAPI := setupFakeDocker(t, whoami, vault)

	eng := reconcile.New(
		testLogger(), dockerAPI, cfAPI, statePath, "homelab",
		[]string{"cloudflare.tunnel", "dockflare"}, 8*time.Hour,
	)
	res := eng.Run(ctx)
	require.NoError(t, res.LastError)

	assert.Equal(t, 2, res.RulesAdded)
}

func TestReconcile_back_to_active(t *testing.T) {
	ctx := context.Background()
	stateDir := tempStateDir(t)
	statePath := filepath.Join(stateDir, "state.json")

	cfAPI := setupFakeCF(t)

	app := docker.Container{
		ID: "c1", Name: "app", State: "running",
		Labels: map[string]string{
			"cloudflare.tunnel.enable":   "true",
			"cloudflare.tunnel.hostname": "app.example.com",
			"cloudflare.tunnel.service":  "http://app:80",
		},
	}

	dockerAPI := setupFakeDocker(t, app)
	eng := newEngine(t, dockerAPI, cfAPI, statePath)
	res1 := eng.Run(ctx)
	require.NoError(t, res1.LastError)

	dockerAPI = setupFakeDocker(t)
	eng = newEngine(t, dockerAPI, cfAPI, statePath)
	res2 := eng.Run(ctx)
	require.NoError(t, res2.LastError)
	assert.Equal(t, 1, res2.RulesPending)

	dockerAPI = setupFakeDocker(t, app)
	eng = newEngine(t, dockerAPI, cfAPI, statePath)
	res3 := eng.Run(ctx)
	require.NoError(t, res3.LastError)

	st, err := state.Load(statePath)
	require.NoError(t, err)
	rule, ok := st.Rules["app.example.com"]
	require.True(t, ok)
	assert.Equal(t, "active", rule.Status)
	assert.Nil(t, rule.PendingSince)
	assert.Nil(t, rule.DeleteAfter)
}

func TestReconcile_field_change(t *testing.T) {
	ctx := context.Background()
	stateDir := tempStateDir(t)
	statePath := filepath.Join(stateDir, "state.json")

	cfAPI := setupFakeCF(t)

	app := docker.Container{
		ID: "c1", Name: "app", State: "running",
		Labels: map[string]string{
			"cloudflare.tunnel.enable":   "true",
			"cloudflare.tunnel.hostname": "app.example.com",
			"cloudflare.tunnel.service":  "http://app:80",
		},
	}

	dockerAPI := setupFakeDocker(t, app)
	eng := newEngine(t, dockerAPI, cfAPI, statePath)
	res1 := eng.Run(ctx)
	require.NoError(t, res1.LastError)

	app.Labels["cloudflare.tunnel.service"] = "http://app:8080"
	dockerAPI = setupFakeDocker(t, app)
	eng = newEngine(t, dockerAPI, cfAPI, statePath)
	res2 := eng.Run(ctx)
	require.NoError(t, res2.LastError)

	st, err := state.Load(statePath)
	require.NoError(t, err)
	rule, ok := st.Rules["app.example.com"]
	require.True(t, ok)
	assert.Equal(t, "http://app:8080", rule.Service)
}

func TestReconcile_ingress_equal_ignores_version(t *testing.T) {
	ctx := context.Background()
	stateDir := tempStateDir(t)
	statePath := filepath.Join(stateDir, "state.json")

	cfAPI := setupFakeCF(t)

	cfAPI.SetIngress("tunnel-uuid", cf.IngressConfig{
		Version: 999,
		Rules:   []rules.IngressEntry{{Service: "http_status:404"}},
	})

	dockerAPI := setupFakeDocker(t)
	eng := newEngine(t, dockerAPI, cfAPI, statePath)
	res := eng.Run(ctx)
	require.NoError(t, res.LastError)

	assert.False(t, res.IngressUpdated)
}

func TestReconcile_no_tls_verify_only(t *testing.T) {
	ctx := context.Background()
	stateDir := tempStateDir(t)
	statePath := filepath.Join(stateDir, "state.json")

	cfAPI := setupFakeCF(t)

	app := docker.Container{
		ID: "c1", Name: "app", State: "running",
		Labels: map[string]string{
			"cloudflare.tunnel.enable":        "true",
			"cloudflare.tunnel.hostname":      "app.example.com",
			"cloudflare.tunnel.service":       "https://app:443",
			"cloudflare.tunnel.no_tls_verify": "true",
		},
	}
	dockerAPI := setupFakeDocker(t, app)

	eng := newEngine(t, dockerAPI, cfAPI, statePath)
	res := eng.Run(ctx)
	require.NoError(t, res.LastError)

	cfg, _ := cfAPI.GetIngress(ctx, "tunnel-uuid")
	require.Len(t, cfg.Rules, 2)
	assert.NotNil(t, cfg.Rules[0].OriginRequest)
	assert.True(t, cfg.Rules[0].OriginRequest.NoTLSVerify)
}

func TestReconcile_no_origin_request(t *testing.T) {
	ctx := context.Background()
	stateDir := tempStateDir(t)
	statePath := filepath.Join(stateDir, "state.json")

	cfAPI := setupFakeCF(t)

	app := docker.Container{
		ID: "c1", Name: "app", State: "running",
		Labels: map[string]string{
			"cloudflare.tunnel.enable":   "true",
			"cloudflare.tunnel.hostname": "app.example.com",
			"cloudflare.tunnel.service":  "http://app:80",
		},
	}
	dockerAPI := setupFakeDocker(t, app)

	eng := newEngine(t, dockerAPI, cfAPI, statePath)
	res := eng.Run(ctx)
	require.NoError(t, res.LastError)

	cfg, _ := cfAPI.GetIngress(ctx, "tunnel-uuid")
	require.Len(t, cfg.Rules, 2)
	assert.Nil(t, cfg.Rules[0].OriginRequest)
}

func TestReconcile_dockflare_prefix(t *testing.T) {
	ctx := context.Background()
	stateDir := tempStateDir(t)
	statePath := filepath.Join(stateDir, "state.json")

	cfAPI := setupFakeCF(t)

	whoami := docker.Container{
		ID: "c1", Name: "whoami", State: "running",
		Labels: map[string]string{
			"dockflare.enable":   "true",
			"dockflare.hostname": "whoami.example.com",
			"dockflare.service":  "http://whoami:80",
		},
	}
	dockerAPI := setupFakeDocker(t, whoami)

	eng := reconcile.New(
		testLogger(), dockerAPI, cfAPI, statePath, "homelab",
		[]string{"dockflare"}, 8*time.Hour,
	)
	res := eng.Run(ctx)
	require.NoError(t, res.LastError)

	assert.Equal(t, 1, res.RulesAdded)
}

func TestReconcile_invalid_rules_not_added(t *testing.T) {
	ctx := context.Background()
	stateDir := tempStateDir(t)
	statePath := filepath.Join(stateDir, "state.json")

	cfAPI := setupFakeCF(t)

	bad := docker.Container{
		ID: "c1", Name: "bad", State: "running",
		Labels: map[string]string{
			"cloudflare.tunnel.enable":   "true",
			"cloudflare.tunnel.hostname": "not-a-valid-hostname!",
			"cloudflare.tunnel.service":  "http://bad:80",
		},
	}
	dockerAPI := setupFakeDocker(t, bad)

	eng := newEngine(t, dockerAPI, cfAPI, statePath)
	res := eng.Run(ctx)
	require.NoError(t, res.LastError)

	assert.Equal(t, 0, res.RulesAdded)
}

func TestReconcile_label_issues_logged(t *testing.T) {
	ctx := context.Background()
	stateDir := tempStateDir(t)
	statePath := filepath.Join(stateDir, "state.json")

	cfAPI := setupFakeCF(t)

	whoami := docker.Container{
		ID: "c1", Name: "whoami", State: "running",
		Labels: map[string]string{
			"cloudflare.tunnel.enable":   "true",
			"cloudflare.tunnel.hostname": "whoami.example.com",
			"cloudflare.tunnel.service":  "http://whoami:80",
			"cloudflare.tunnel.unknown":  "value",
		},
	}
	dockerAPI := setupFakeDocker(t, whoami)

	eng := newEngine(t, dockerAPI, cfAPI, statePath)
	res := eng.Run(ctx)
	require.NoError(t, res.LastError)

	assert.Equal(t, 1, res.RulesAdded)
}

func TestReconcile_labels_to_ingress(t *testing.T) {
	ctx := context.Background()
	stateDir := tempStateDir(t)
	statePath := filepath.Join(stateDir, "state.json")

	cfAPI := setupFakeCF(t)

	app := docker.Container{
		ID: "c1", Name: "app", State: "running",
		Labels: map[string]string{
			"cloudflare.tunnel.enable":        "true",
			"cloudflare.tunnel.hostname":      "app.example.com",
			"cloudflare.tunnel.service":       "https://app:443",
			"cloudflare.tunnel.no_tls_verify": "true",
			"cloudflare.tunnel.originsrvname": "app.internal",
		},
	}
	dockerAPI := setupFakeDocker(t, app)

	eng := newEngine(t, dockerAPI, cfAPI, statePath)
	res := eng.Run(ctx)
	require.NoError(t, res.LastError)

	cfg, _ := cfAPI.GetIngress(ctx, "tunnel-uuid")
	require.Len(t, cfg.Rules, 2)
	entry := cfg.Rules[0]
	assert.Equal(t, "app.example.com", entry.Hostname)
	assert.Equal(t, "https://app:443", entry.Service)
	assert.NotNil(t, entry.OriginRequest)
	assert.True(t, entry.OriginRequest.NoTLSVerify)
	assert.Equal(t, "app.internal", entry.OriginRequest.OriginServerName)
}

func TestReconcile_container_name_preserved(t *testing.T) {
	ctx := context.Background()
	stateDir := tempStateDir(t)
	statePath := filepath.Join(stateDir, "state.json")

	cfAPI := setupFakeCF(t)

	whoami := docker.Container{
		ID: "c1", Name: "my-whoami", State: "running",
		Labels: map[string]string{
			"cloudflare.tunnel.enable":   "true",
			"cloudflare.tunnel.hostname": "whoami.example.com",
			"cloudflare.tunnel.service":  "http://whoami:80",
		},
	}
	dockerAPI := setupFakeDocker(t, whoami)

	eng := newEngine(t, dockerAPI, cfAPI, statePath)
	res := eng.Run(ctx)
	require.NoError(t, res.LastError)

	st, err := state.Load(statePath)
	require.NoError(t, err)
	rule, ok := st.Rules["whoami.example.com"]
	require.True(t, ok)
	assert.Equal(t, "my-whoami", rule.ContainerName)
}

func TestReconcile_source_prefix_preserved(t *testing.T) {
	ctx := context.Background()
	stateDir := tempStateDir(t)
	statePath := filepath.Join(stateDir, "state.json")

	cfAPI := setupFakeCF(t)

	whoami := docker.Container{
		ID: "c1", Name: "whoami", State: "running",
		Labels: map[string]string{
			"cloudflare.tunnel.enable":   "true",
			"cloudflare.tunnel.hostname": "whoami.example.com",
			"cloudflare.tunnel.service":  "http://whoami:80",
		},
	}
	dockerAPI := setupFakeDocker(t, whoami)

	eng := newEngine(t, dockerAPI, cfAPI, statePath)
	res := eng.Run(ctx)
	require.NoError(t, res.LastError)

	st, err := state.Load(statePath)
	require.NoError(t, err)
	rule, ok := st.Rules["whoami.example.com"]
	require.True(t, ok)
	assert.Equal(t, "cloudflare.tunnel", rule.SourcePrefix)
}

func TestReconcile_source_index_preserved(t *testing.T) {
	ctx := context.Background()
	stateDir := tempStateDir(t)
	statePath := filepath.Join(stateDir, "state.json")

	cfAPI := setupFakeCF(t)

	multi := docker.Container{
		ID: "c1", Name: "multi", State: "running",
		Labels: map[string]string{
			"cloudflare.tunnel.enable":     "true",
			"cloudflare.tunnel.0.hostname": "app0.example.com",
			"cloudflare.tunnel.0.service":  "http://app0:80",
		},
	}
	dockerAPI := setupFakeDocker(t, multi)

	eng := newEngine(t, dockerAPI, cfAPI, statePath)
	res := eng.Run(ctx)
	require.NoError(t, res.LastError)

	st, err := state.Load(statePath)
	require.NoError(t, err)
	rule, ok := st.Rules["app0.example.com"]
	require.True(t, ok)
	require.NotNil(t, rule.SourceIndex)
	assert.Equal(t, 0, *rule.SourceIndex)
}

func TestReconcile_timestamps_set(t *testing.T) {
	ctx := context.Background()
	stateDir := tempStateDir(t)
	statePath := filepath.Join(stateDir, "state.json")

	cfAPI := setupFakeCF(t)

	app := docker.Container{
		ID: "c1", Name: "app", State: "running",
		Labels: map[string]string{
			"cloudflare.tunnel.enable":   "true",
			"cloudflare.tunnel.hostname": "app.example.com",
			"cloudflare.tunnel.service":  "http://app:80",
		},
	}
	dockerAPI := setupFakeDocker(t, app)

	eng := newEngine(t, dockerAPI, cfAPI, statePath)
	res := eng.Run(ctx)
	require.NoError(t, res.LastError)

	st, err := state.Load(statePath)
	require.NoError(t, err)
	rule, ok := st.Rules["app.example.com"]
	require.True(t, ok)
	assert.False(t, rule.FirstSeen.IsZero())
	assert.False(t, rule.LastSeen.IsZero())
}

func TestReconcile_pending_timestamps_set(t *testing.T) {
	ctx := context.Background()
	stateDir := tempStateDir(t)
	statePath := filepath.Join(stateDir, "state.json")

	cfAPI := setupFakeCF(t)

	app := docker.Container{
		ID: "c1", Name: "app", State: "running",
		Labels: map[string]string{
			"cloudflare.tunnel.enable":   "true",
			"cloudflare.tunnel.hostname": "app.example.com",
			"cloudflare.tunnel.service":  "http://app:80",
		},
	}

	dockerAPI := setupFakeDocker(t, app)
	eng := newEngine(t, dockerAPI, cfAPI, statePath)
	res1 := eng.Run(ctx)
	require.NoError(t, res1.LastError)

	dockerAPI = setupFakeDocker(t)
	eng = newEngine(t, dockerAPI, cfAPI, statePath)
	res2 := eng.Run(ctx)
	require.NoError(t, res2.LastError)

	st, err := state.Load(statePath)
	require.NoError(t, err)
	rule, ok := st.Rules["app.example.com"]
	require.True(t, ok)
	assert.Equal(t, "pending", rule.Status)
	assert.NotNil(t, rule.PendingSince)
	assert.NotNil(t, rule.DeleteAfter)
	assert.WithinDuration(t, time.Now().Add(8*time.Hour), *rule.DeleteAfter, time.Minute)
}

func TestReconcile_no_tls_verify_preserved(t *testing.T) {
	ctx := context.Background()
	stateDir := tempStateDir(t)
	statePath := filepath.Join(stateDir, "state.json")

	cfAPI := setupFakeCF(t)

	app := docker.Container{
		ID: "c1", Name: "app", State: "running",
		Labels: map[string]string{
			"cloudflare.tunnel.enable":        "true",
			"cloudflare.tunnel.hostname":      "app.example.com",
			"cloudflare.tunnel.service":       "https://app:443",
			"cloudflare.tunnel.no_tls_verify": "true",
		},
	}
	dockerAPI := setupFakeDocker(t, app)

	eng := newEngine(t, dockerAPI, cfAPI, statePath)
	res := eng.Run(ctx)
	require.NoError(t, res.LastError)

	st, err := state.Load(statePath)
	require.NoError(t, err)
	rule, ok := st.Rules["app.example.com"]
	require.True(t, ok)
	assert.True(t, rule.NoTLSVerify)
}

func TestReconcile_origin_server_name_preserved(t *testing.T) {
	ctx := context.Background()
	stateDir := tempStateDir(t)
	statePath := filepath.Join(stateDir, "state.json")

	cfAPI := setupFakeCF(t)

	app := docker.Container{
		ID: "c1", Name: "app", State: "running",
		Labels: map[string]string{
			"cloudflare.tunnel.enable":        "true",
			"cloudflare.tunnel.hostname":      "app.example.com",
			"cloudflare.tunnel.service":       "https://app:443",
			"cloudflare.tunnel.originsrvname": "app.internal",
		},
	}
	dockerAPI := setupFakeDocker(t, app)

	eng := newEngine(t, dockerAPI, cfAPI, statePath)
	res := eng.Run(ctx)
	require.NoError(t, res.LastError)

	st, err := state.Load(statePath)
	require.NoError(t, err)
	rule, ok := st.Rules["app.example.com"]
	require.True(t, ok)
	assert.Equal(t, "app.internal", rule.OriginServerName)
}

func TestReconcile_zone_name_preserved(t *testing.T) {
	ctx := context.Background()
	stateDir := tempStateDir(t)
	statePath := filepath.Join(stateDir, "state.json")

	cfAPI := setupFakeCF(t)

	app := docker.Container{
		ID: "c1", Name: "app", State: "running",
		Labels: map[string]string{
			"cloudflare.tunnel.enable":   "true",
			"cloudflare.tunnel.hostname": "app.example.com",
			"cloudflare.tunnel.service":  "http://app:80",
			"cloudflare.tunnel.zonename": "example.com",
		},
	}
	dockerAPI := setupFakeDocker(t, app)

	eng := newEngine(t, dockerAPI, cfAPI, statePath)
	res := eng.Run(ctx)
	require.NoError(t, res.LastError)

	st, err := state.Load(statePath)
	require.NoError(t, err)
	rule, ok := st.Rules["app.example.com"]
	require.True(t, ok)
	assert.Equal(t, "example.com", rule.ZoneName)
}

func TestReconcile_path_preserved(t *testing.T) {
	ctx := context.Background()
	stateDir := tempStateDir(t)
	statePath := filepath.Join(stateDir, "state.json")

	cfAPI := setupFakeCF(t)

	app := docker.Container{
		ID: "c1", Name: "app", State: "running",
		Labels: map[string]string{
			"cloudflare.tunnel.enable":   "true",
			"cloudflare.tunnel.hostname": "app.example.com",
			"cloudflare.tunnel.service":  "http://app:80",
			"cloudflare.tunnel.path":     "/api/v1",
		},
	}
	dockerAPI := setupFakeDocker(t, app)

	eng := newEngine(t, dockerAPI, cfAPI, statePath)
	res := eng.Run(ctx)
	require.NoError(t, res.LastError)

	st, err := state.Load(statePath)
	require.NoError(t, err)
	rule, ok := st.Rules["app.example.com|/api/v1"]
	require.True(t, ok)
	assert.Equal(t, "/api/v1", rule.Path)
}

func TestReconcile_service_preserved(t *testing.T) {
	ctx := context.Background()
	stateDir := tempStateDir(t)
	statePath := filepath.Join(stateDir, "state.json")

	cfAPI := setupFakeCF(t)

	app := docker.Container{
		ID: "c1", Name: "app", State: "running",
		Labels: map[string]string{
			"cloudflare.tunnel.enable":   "true",
			"cloudflare.tunnel.hostname": "app.example.com",
			"cloudflare.tunnel.service":  "http://app:8080",
		},
	}
	dockerAPI := setupFakeDocker(t, app)

	eng := newEngine(t, dockerAPI, cfAPI, statePath)
	res := eng.Run(ctx)
	require.NoError(t, res.LastError)

	st, err := state.Load(statePath)
	require.NoError(t, err)
	rule, ok := st.Rules["app.example.com"]
	require.True(t, ok)
	assert.Equal(t, "http://app:8080", rule.Service)
}

func TestReconcile_hostname_preserved(t *testing.T) {
	ctx := context.Background()
	stateDir := tempStateDir(t)
	statePath := filepath.Join(stateDir, "state.json")

	cfAPI := setupFakeCF(t)

	app := docker.Container{
		ID: "c1", Name: "app", State: "running",
		Labels: map[string]string{
			"cloudflare.tunnel.enable":   "true",
			"cloudflare.tunnel.hostname": "my-app.example.com",
			"cloudflare.tunnel.service":  "http://app:80",
		},
	}
	dockerAPI := setupFakeDocker(t, app)

	eng := newEngine(t, dockerAPI, cfAPI, statePath)
	res := eng.Run(ctx)
	require.NoError(t, res.LastError)

	st, err := state.Load(statePath)
	require.NoError(t, err)
	rule, ok := st.Rules["my-app.example.com"]
	require.True(t, ok)
	assert.Equal(t, "my-app.example.com", rule.Hostname)
}

func TestReconcile_status_active(t *testing.T) {
	ctx := context.Background()
	stateDir := tempStateDir(t)
	statePath := filepath.Join(stateDir, "state.json")

	cfAPI := setupFakeCF(t)

	app := docker.Container{
		ID: "c1", Name: "app", State: "running",
		Labels: map[string]string{
			"cloudflare.tunnel.enable":   "true",
			"cloudflare.tunnel.hostname": "app.example.com",
			"cloudflare.tunnel.service":  "http://app:80",
		},
	}
	dockerAPI := setupFakeDocker(t, app)

	eng := newEngine(t, dockerAPI, cfAPI, statePath)
	res := eng.Run(ctx)
	require.NoError(t, res.LastError)

	st, err := state.Load(statePath)
	require.NoError(t, err)
	rule, ok := st.Rules["app.example.com"]
	require.True(t, ok)
	assert.Equal(t, "active", rule.Status)
}

func TestReconcile_first_seen_not_changed_on_update(t *testing.T) {
	ctx := context.Background()
	stateDir := tempStateDir(t)
	statePath := filepath.Join(stateDir, "state.json")

	cfAPI := setupFakeCF(t)

	app := docker.Container{
		ID: "c1", Name: "app", State: "running",
		Labels: map[string]string{
			"cloudflare.tunnel.enable":   "true",
			"cloudflare.tunnel.hostname": "app.example.com",
			"cloudflare.tunnel.service":  "http://app:80",
		},
	}

	dockerAPI := setupFakeDocker(t, app)
	eng := newEngine(t, dockerAPI, cfAPI, statePath)
	res1 := eng.Run(ctx)
	require.NoError(t, res1.LastError)

	st1, err := state.Load(statePath)
	require.NoError(t, err)
	rule1, ok := st1.Rules["app.example.com"]
	require.True(t, ok)
	firstSeen := rule1.FirstSeen

	app.Labels["cloudflare.tunnel.service"] = "http://app:8080"
	dockerAPI = setupFakeDocker(t, app)
	eng = newEngine(t, dockerAPI, cfAPI, statePath)
	res2 := eng.Run(ctx)
	require.NoError(t, res2.LastError)

	st2, err := state.Load(statePath)
	require.NoError(t, err)
	rule2, ok := st2.Rules["app.example.com"]
	require.True(t, ok)
	assert.Equal(t, firstSeen, rule2.FirstSeen)
}

func TestReconcile_last_seen_updated_on_change(t *testing.T) {
	ctx := context.Background()
	stateDir := tempStateDir(t)
	statePath := filepath.Join(stateDir, "state.json")

	cfAPI := setupFakeCF(t)

	app := docker.Container{
		ID: "c1", Name: "app", State: "running",
		Labels: map[string]string{
			"cloudflare.tunnel.enable":   "true",
			"cloudflare.tunnel.hostname": "app.example.com",
			"cloudflare.tunnel.service":  "http://app:80",
		},
	}

	dockerAPI := setupFakeDocker(t, app)
	eng := newEngine(t, dockerAPI, cfAPI, statePath)
	res1 := eng.Run(ctx)
	require.NoError(t, res1.LastError)

	st1, err := state.Load(statePath)
	require.NoError(t, err)
	rule1, ok := st1.Rules["app.example.com"]
	require.True(t, ok)
	lastSeen := rule1.LastSeen

	time.Sleep(time.Millisecond)

	app.Labels["cloudflare.tunnel.service"] = "http://app:8080"
	dockerAPI = setupFakeDocker(t, app)
	eng = newEngine(t, dockerAPI, cfAPI, statePath)
	res2 := eng.Run(ctx)
	require.NoError(t, res2.LastError)

	st2, err := state.Load(statePath)
	require.NoError(t, err)
	rule2, ok := st2.Rules["app.example.com"]
	require.True(t, ok)
	assert.True(t, rule2.LastSeen.After(lastSeen))
}

func TestReconcile_pending_since_cleared_on_back_to_active(t *testing.T) {
	ctx := context.Background()
	stateDir := tempStateDir(t)
	statePath := filepath.Join(stateDir, "state.json")

	cfAPI := setupFakeCF(t)

	app := docker.Container{
		ID: "c1", Name: "app", State: "running",
		Labels: map[string]string{
			"cloudflare.tunnel.enable":   "true",
			"cloudflare.tunnel.hostname": "app.example.com",
			"cloudflare.tunnel.service":  "http://app:80",
		},
	}

	dockerAPI := setupFakeDocker(t, app)
	eng := newEngine(t, dockerAPI, cfAPI, statePath)
	res1 := eng.Run(ctx)
	require.NoError(t, res1.LastError)

	dockerAPI = setupFakeDocker(t)
	eng = newEngine(t, dockerAPI, cfAPI, statePath)
	res2 := eng.Run(ctx)
	require.NoError(t, res2.LastError)

	dockerAPI = setupFakeDocker(t, app)
	eng = newEngine(t, dockerAPI, cfAPI, statePath)
	res3 := eng.Run(ctx)
	require.NoError(t, res3.LastError)

	st, err := state.Load(statePath)
	require.NoError(t, err)
	rule, ok := st.Rules["app.example.com"]
	require.True(t, ok)
	assert.Nil(t, rule.PendingSince)
}

func TestReconcile_delete_after_cleared_on_back_to_active(t *testing.T) {
	ctx := context.Background()
	stateDir := tempStateDir(t)
	statePath := filepath.Join(stateDir, "state.json")

	cfAPI := setupFakeCF(t)

	app := docker.Container{
		ID: "c1", Name: "app", State: "running",
		Labels: map[string]string{
			"cloudflare.tunnel.enable":   "true",
			"cloudflare.tunnel.hostname": "app.example.com",
			"cloudflare.tunnel.service":  "http://app:80",
		},
	}

	dockerAPI := setupFakeDocker(t, app)
	eng := newEngine(t, dockerAPI, cfAPI, statePath)
	res1 := eng.Run(ctx)
	require.NoError(t, res1.LastError)

	dockerAPI = setupFakeDocker(t)
	eng = newEngine(t, dockerAPI, cfAPI, statePath)
	res2 := eng.Run(ctx)
	require.NoError(t, res2.LastError)

	dockerAPI = setupFakeDocker(t, app)
	eng = newEngine(t, dockerAPI, cfAPI, statePath)
	res3 := eng.Run(ctx)
	require.NoError(t, res3.LastError)

	st, err := state.Load(statePath)
	require.NoError(t, err)
	rule, ok := st.Rules["app.example.com"]
	require.True(t, ok)
	assert.Nil(t, rule.DeleteAfter)
}

func TestReconcile_pending_status_on_removal(t *testing.T) {
	ctx := context.Background()
	stateDir := tempStateDir(t)
	statePath := filepath.Join(stateDir, "state.json")

	cfAPI := setupFakeCF(t)

	app := docker.Container{
		ID: "c1", Name: "app", State: "running",
		Labels: map[string]string{
			"cloudflare.tunnel.enable":   "true",
			"cloudflare.tunnel.hostname": "app.example.com",
			"cloudflare.tunnel.service":  "http://app:80",
		},
	}

	dockerAPI := setupFakeDocker(t, app)
	eng := newEngine(t, dockerAPI, cfAPI, statePath)
	res1 := eng.Run(ctx)
	require.NoError(t, res1.LastError)

	dockerAPI = setupFakeDocker(t)
	eng = newEngine(t, dockerAPI, cfAPI, statePath)
	res2 := eng.Run(ctx)
	require.NoError(t, res2.LastError)

	st, err := state.Load(statePath)
	require.NoError(t, err)
	rule, ok := st.Rules["app.example.com"]
	require.True(t, ok)
	assert.Equal(t, "pending", rule.Status)
}

func TestReconcile_expired_status_on_expiry(t *testing.T) {
	ctx := context.Background()
	stateDir := tempStateDir(t)
	statePath := filepath.Join(stateDir, "state.json")

	cfAPI := setupFakeCF(t)

	st := rules.NewState()
	st.Tunnel = rules.TunnelInfo{ID: "tunnel-uuid", Name: "homelab"}
	expiredTime := time.Now().Add(-time.Hour)
	deleteAfter := time.Now().Add(-time.Minute)
	st.Rules["app.example.com"] = rules.Rule{
		Hostname: "app.example.com", Service: "http://app:80", Status: "pending",
		FirstSeen: expiredTime, LastSeen: expiredTime,
		DeleteAfter: &deleteAfter, PendingSince: &expiredTime,
	}
	require.NoError(t, state.Save(statePath, st))

	dockerAPI := setupFakeDocker(t)
	eng := newEngine(t, dockerAPI, cfAPI, statePath)
	res := eng.Run(ctx)
	require.NoError(t, res.LastError)

	st, err := state.Load(statePath)
	require.NoError(t, err)
	assert.NotContains(t, st.Rules, "app.example.com")
}

func TestReconcile_container_name_change(t *testing.T) {
	ctx := context.Background()
	stateDir := tempStateDir(t)
	statePath := filepath.Join(stateDir, "state.json")

	cfAPI := setupFakeCF(t)

	app := docker.Container{
		ID: "c1", Name: "app", State: "running",
		Labels: map[string]string{
			"cloudflare.tunnel.enable":   "true",
			"cloudflare.tunnel.hostname": "app.example.com",
			"cloudflare.tunnel.service":  "http://app:80",
		},
	}

	dockerAPI := setupFakeDocker(t, app)
	eng := newEngine(t, dockerAPI, cfAPI, statePath)
	res1 := eng.Run(ctx)
	require.NoError(t, res1.LastError)

	app.Name = "app-renamed"
	dockerAPI = setupFakeDocker(t, app)
	eng = newEngine(t, dockerAPI, cfAPI, statePath)
	res2 := eng.Run(ctx)
	require.NoError(t, res2.LastError)

	st, err := state.Load(statePath)
	require.NoError(t, err)
	rule, ok := st.Rules["app.example.com"]
	require.True(t, ok)
	assert.Equal(t, "app-renamed", rule.ContainerName)
}

func TestReconcile_source_prefix_change(t *testing.T) {
	ctx := context.Background()
	stateDir := tempStateDir(t)
	statePath := filepath.Join(stateDir, "state.json")

	cfAPI := setupFakeCF(t)

	app := docker.Container{
		ID: "c1", Name: "app", State: "running",
		Labels: map[string]string{
			"cloudflare.tunnel.enable":   "true",
			"cloudflare.tunnel.hostname": "app.example.com",
			"cloudflare.tunnel.service":  "http://app:80",
		},
	}

	dockerAPI := setupFakeDocker(t, app)
	eng := newEngine(t, dockerAPI, cfAPI, statePath)
	res1 := eng.Run(ctx)
	require.NoError(t, res1.LastError)

	app.Labels = map[string]string{
		"dockflare.enable":   "true",
		"dockflare.hostname": "app.example.com",
		"dockflare.service":  "http://app:80",
	}
	dockerAPI = setupFakeDocker(t, app)
	eng = reconcile.New(
		testLogger(), dockerAPI, cfAPI, statePath, "homelab",
		[]string{"cloudflare.tunnel", "dockflare"}, 8*time.Hour,
	)
	res2 := eng.Run(ctx)
	require.NoError(t, res2.LastError)

	st, err := state.Load(statePath)
	require.NoError(t, err)
	rule, ok := st.Rules["app.example.com"]
	require.True(t, ok)
	assert.Equal(t, "dockflare", rule.SourcePrefix)
}

func TestReconcile_source_index_change(t *testing.T) {
	ctx := context.Background()
	stateDir := tempStateDir(t)
	statePath := filepath.Join(stateDir, "state.json")

	cfAPI := setupFakeCF(t)

	app := docker.Container{
		ID: "c1", Name: "app", State: "running",
		Labels: map[string]string{
			"cloudflare.tunnel.enable":   "true",
			"cloudflare.tunnel.hostname": "app.example.com",
			"cloudflare.tunnel.service":  "http://app:80",
		},
	}

	dockerAPI := setupFakeDocker(t, app)
	eng := newEngine(t, dockerAPI, cfAPI, statePath)
	res1 := eng.Run(ctx)
	require.NoError(t, res1.LastError)

	app.Labels = map[string]string{
		"cloudflare.tunnel.enable":     "true",
		"cloudflare.tunnel.0.hostname": "app.example.com",
		"cloudflare.tunnel.0.service":  "http://app:80",
	}
	dockerAPI = setupFakeDocker(t, app)
	eng = newEngine(t, dockerAPI, cfAPI, statePath)
	res2 := eng.Run(ctx)
	require.NoError(t, res2.LastError)

	st, err := state.Load(statePath)
	require.NoError(t, err)
	rule, ok := st.Rules["app.example.com"]
	require.True(t, ok)
	require.NotNil(t, rule.SourceIndex)
	assert.Equal(t, 0, *rule.SourceIndex)
}

func TestReconcile_no_tls_verify_change(t *testing.T) {
	ctx := context.Background()
	stateDir := tempStateDir(t)
	statePath := filepath.Join(stateDir, "state.json")

	cfAPI := setupFakeCF(t)

	app := docker.Container{
		ID: "c1", Name: "app", State: "running",
		Labels: map[string]string{
			"cloudflare.tunnel.enable":   "true",
			"cloudflare.tunnel.hostname": "app.example.com",
			"cloudflare.tunnel.service":  "http://app:80",
		},
	}

	dockerAPI := setupFakeDocker(t, app)
	eng := newEngine(t, dockerAPI, cfAPI, statePath)
	res1 := eng.Run(ctx)
	require.NoError(t, res1.LastError)

	app.Labels["cloudflare.tunnel.no_tls_verify"] = "true"
	dockerAPI = setupFakeDocker(t, app)
	eng = newEngine(t, dockerAPI, cfAPI, statePath)
	res2 := eng.Run(ctx)
	require.NoError(t, res2.LastError)

	st, err := state.Load(statePath)
	require.NoError(t, err)
	rule, ok := st.Rules["app.example.com"]
	require.True(t, ok)
	assert.True(t, rule.NoTLSVerify)
}

func TestReconcile_origin_server_name_change(t *testing.T) {
	ctx := context.Background()
	stateDir := tempStateDir(t)
	statePath := filepath.Join(stateDir, "state.json")

	cfAPI := setupFakeCF(t)

	app := docker.Container{
		ID: "c1", Name: "app", State: "running",
		Labels: map[string]string{
			"cloudflare.tunnel.enable":   "true",
			"cloudflare.tunnel.hostname": "app.example.com",
			"cloudflare.tunnel.service":  "http://app:80",
		},
	}

	dockerAPI := setupFakeDocker(t, app)
	eng := newEngine(t, dockerAPI, cfAPI, statePath)
	res1 := eng.Run(ctx)
	require.NoError(t, res1.LastError)

	app.Labels["cloudflare.tunnel.originsrvname"] = "app.internal"
	dockerAPI = setupFakeDocker(t, app)
	eng = newEngine(t, dockerAPI, cfAPI, statePath)
	res2 := eng.Run(ctx)
	require.NoError(t, res2.LastError)

	st, err := state.Load(statePath)
	require.NoError(t, err)
	rule, ok := st.Rules["app.example.com"]
	require.True(t, ok)
	assert.Equal(t, "app.internal", rule.OriginServerName)
}

func TestReconcile_zone_name_change(t *testing.T) {
	ctx := context.Background()
	stateDir := tempStateDir(t)
	statePath := filepath.Join(stateDir, "state.json")

	cfAPI := setupFakeCF(t)

	app := docker.Container{
		ID: "c1", Name: "app", State: "running",
		Labels: map[string]string{
			"cloudflare.tunnel.enable":   "true",
			"cloudflare.tunnel.hostname": "app.example.com",
			"cloudflare.tunnel.service":  "http://app:80",
		},
	}

	dockerAPI := setupFakeDocker(t, app)
	eng := newEngine(t, dockerAPI, cfAPI, statePath)
	res1 := eng.Run(ctx)
	require.NoError(t, res1.LastError)

	app.Labels["cloudflare.tunnel.zonename"] = "example.com"
	dockerAPI = setupFakeDocker(t, app)
	eng = newEngine(t, dockerAPI, cfAPI, statePath)
	res2 := eng.Run(ctx)
	require.NoError(t, res2.LastError)

	st, err := state.Load(statePath)
	require.NoError(t, err)
	rule, ok := st.Rules["app.example.com"]
	require.True(t, ok)
	assert.Equal(t, "example.com", rule.ZoneName)
}

func TestReconcile_path_change(t *testing.T) {
	ctx := context.Background()
	stateDir := tempStateDir(t)
	statePath := filepath.Join(stateDir, "state.json")

	cfAPI := setupFakeCF(t)

	app := docker.Container{
		ID: "c1", Name: "app", State: "running",
		Labels: map[string]string{
			"cloudflare.tunnel.enable":   "true",
			"cloudflare.tunnel.hostname": "app.example.com",
			"cloudflare.tunnel.service":  "http://app:80",
			"cloudflare.tunnel.path":     "/api",
		},
	}

	dockerAPI := setupFakeDocker(t, app)
	eng := newEngine(t, dockerAPI, cfAPI, statePath)
	res1 := eng.Run(ctx)
	require.NoError(t, res1.LastError)

	app.Labels["cloudflare.tunnel.path"] = "/api/v2"
	dockerAPI = setupFakeDocker(t, app)
	eng = newEngine(t, dockerAPI, cfAPI, statePath)
	res2 := eng.Run(ctx)
	require.NoError(t, res2.LastError)

	st, err := state.Load(statePath)
	require.NoError(t, err)
	rule, ok := st.Rules["app.example.com|/api/v2"]
	require.True(t, ok)
	assert.Equal(t, "/api/v2", rule.Path)
}

func TestReconcile_service_change(t *testing.T) {
	ctx := context.Background()
	stateDir := tempStateDir(t)
	statePath := filepath.Join(stateDir, "state.json")

	cfAPI := setupFakeCF(t)

	app := docker.Container{
		ID: "c1", Name: "app", State: "running",
		Labels: map[string]string{
			"cloudflare.tunnel.enable":   "true",
			"cloudflare.tunnel.hostname": "app.example.com",
			"cloudflare.tunnel.service":  "http://app:80",
		},
	}

	dockerAPI := setupFakeDocker(t, app)
	eng := newEngine(t, dockerAPI, cfAPI, statePath)
	res1 := eng.Run(ctx)
	require.NoError(t, res1.LastError)

	app.Labels["cloudflare.tunnel.service"] = "http://app:8080"
	dockerAPI = setupFakeDocker(t, app)
	eng = newEngine(t, dockerAPI, cfAPI, statePath)
	res2 := eng.Run(ctx)
	require.NoError(t, res2.LastError)

	st, err := state.Load(statePath)
	require.NoError(t, err)
	rule, ok := st.Rules["app.example.com"]
	require.True(t, ok)
	assert.Equal(t, "http://app:8080", rule.Service)
}

func TestReconcile_hostname_change(t *testing.T) {
	ctx := context.Background()
	stateDir := tempStateDir(t)
	statePath := filepath.Join(stateDir, "state.json")

	cfAPI := setupFakeCF(t)

	app := docker.Container{
		ID: "c1", Name: "app", State: "running",
		Labels: map[string]string{
			"cloudflare.tunnel.enable":   "true",
			"cloudflare.tunnel.hostname": "app.example.com",
			"cloudflare.tunnel.service":  "http://app:80",
		},
	}

	dockerAPI := setupFakeDocker(t, app)
	eng := newEngine(t, dockerAPI, cfAPI, statePath)
	res1 := eng.Run(ctx)
	require.NoError(t, res1.LastError)

	app.Labels["cloudflare.tunnel.hostname"] = "new-app.example.com"
	dockerAPI = setupFakeDocker(t, app)
	eng = newEngine(t, dockerAPI, cfAPI, statePath)
	res2 := eng.Run(ctx)
	require.NoError(t, res2.LastError)

	st, err := state.Load(statePath)
	require.NoError(t, err)
	rule, ok := st.Rules["new-app.example.com"]
	require.True(t, ok)
	assert.Equal(t, "new-app.example.com", rule.Hostname)
}

func TestReconcile_status_change_on_removal(t *testing.T) {
	ctx := context.Background()
	stateDir := tempStateDir(t)
	statePath := filepath.Join(stateDir, "state.json")

	cfAPI := setupFakeCF(t)

	app := docker.Container{
		ID: "c1", Name: "app", State: "running",
		Labels: map[string]string{
			"cloudflare.tunnel.enable":   "true",
			"cloudflare.tunnel.hostname": "app.example.com",
			"cloudflare.tunnel.service":  "http://app:80",
		},
	}

	dockerAPI := setupFakeDocker(t, app)
	eng := newEngine(t, dockerAPI, cfAPI, statePath)
	res1 := eng.Run(ctx)
	require.NoError(t, res1.LastError)

	dockerAPI = setupFakeDocker(t)
	eng = newEngine(t, dockerAPI, cfAPI, statePath)
	res2 := eng.Run(ctx)
	require.NoError(t, res2.LastError)

	st, err := state.Load(statePath)
	require.NoError(t, err)
	rule, ok := st.Rules["app.example.com"]
	require.True(t, ok)
	assert.Equal(t, "pending", rule.Status)
}

func TestReconcile_status_change_on_back_to_active(t *testing.T) {
	ctx := context.Background()
	stateDir := tempStateDir(t)
	statePath := filepath.Join(stateDir, "state.json")

	cfAPI := setupFakeCF(t)

	app := docker.Container{
		ID: "c1", Name: "app", State: "running",
		Labels: map[string]string{
			"cloudflare.tunnel.enable":   "true",
			"cloudflare.tunnel.hostname": "app.example.com",
			"cloudflare.tunnel.service":  "http://app:80",
		},
	}

	dockerAPI := setupFakeDocker(t, app)
	eng := newEngine(t, dockerAPI, cfAPI, statePath)
	res1 := eng.Run(ctx)
	require.NoError(t, res1.LastError)

	dockerAPI = setupFakeDocker(t)
	eng = newEngine(t, dockerAPI, cfAPI, statePath)
	res2 := eng.Run(ctx)
	require.NoError(t, res2.LastError)

	dockerAPI = setupFakeDocker(t, app)
	eng = newEngine(t, dockerAPI, cfAPI, statePath)
	res3 := eng.Run(ctx)
	require.NoError(t, res3.LastError)

	st, err := state.Load(statePath)
	require.NoError(t, err)
	rule, ok := st.Rules["app.example.com"]
	require.True(t, ok)
	assert.Equal(t, "active", rule.Status)
}

func TestReconcile_status_change_on_expiry(t *testing.T) {
	ctx := context.Background()
	stateDir := tempStateDir(t)
	statePath := filepath.Join(stateDir, "state.json")

	cfAPI := setupFakeCF(t)

	st := rules.NewState()
	st.Tunnel = rules.TunnelInfo{ID: "tunnel-uuid", Name: "homelab"}
	expiredTime := time.Now().Add(-time.Hour)
	deleteAfter := time.Now().Add(-time.Minute)
	st.Rules["app.example.com"] = rules.Rule{
		Hostname: "app.example.com", Service: "http://app:80", Status: "pending",
		FirstSeen: expiredTime, LastSeen: expiredTime,
		DeleteAfter: &deleteAfter, PendingSince: &expiredTime,
	}
	require.NoError(t, state.Save(statePath, st))

	dockerAPI := setupFakeDocker(t)
	eng := newEngine(t, dockerAPI, cfAPI, statePath)
	res := eng.Run(ctx)
	require.NoError(t, res.LastError)

	st, err := state.Load(statePath)
	require.NoError(t, err)
	assert.NotContains(t, st.Rules, "app.example.com")
}
