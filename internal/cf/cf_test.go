package cf_test

import (
	"context"
	"testing"

	"github.com/rm-hull/cf-tunnel-sync/internal/cf"
	"github.com/rm-hull/cf-tunnel-sync/internal/rules"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFindTunnel_notFound(t *testing.T) {
	fake := cf.NewFake()
	_, err := fake.FindTunnel(context.Background(), "homelab")
	assert.ErrorIs(t, err, cf.ErrNotFound)
}

func TestFindTunnel_found(t *testing.T) {
	fake := cf.NewFake()
	fake.AddTunnel(cf.Tunnel{
		ID:        "abc-123",
		Name:      "homelab",
		ConfigSrc: "cloudflare",
		Status:    "healthy",
	})

	tunnel, err := fake.FindTunnel(context.Background(), "homelab")
	require.NoError(t, err)
	assert.Equal(t, "abc-123", tunnel.ID)
	assert.Equal(t, "homelab", tunnel.Name)
	assert.Equal(t, "cloudflare", tunnel.ConfigSrc)
}

func TestFindTunnel_multipleMatches(t *testing.T) {
	fake := cf.NewFake()
	fake.AddTunnel(cf.Tunnel{ID: "a", Name: "homelab", ConfigSrc: "cloudflare"})
	fake.AddTunnel(cf.Tunnel{ID: "b", Name: "homelab", ConfigSrc: "cloudflare"})

	_, err := fake.FindTunnel(context.Background(), "homelab")
	assert.ErrorIs(t, err, cf.ErrMultipleMatches)
}

func TestFindTunnel_configSrcLocal(t *testing.T) {
	fake := cf.NewFake()
	fake.AddTunnel(cf.Tunnel{ID: "a", Name: "homelab", ConfigSrc: "local"})

	_, err := fake.FindTunnel(context.Background(), "homelab")
	assert.ErrorIs(t, err, cf.ErrConfigSrcLocal)
}

func TestFindTunnel_caseInsensitive(t *testing.T) {
	fake := cf.NewFake()
	fake.AddTunnel(cf.Tunnel{ID: "a", Name: "Homelab", ConfigSrc: "cloudflare"})

	tunnel, err := fake.FindTunnel(context.Background(), "homelab")
	require.NoError(t, err)
	assert.Equal(t, "a", tunnel.ID)
}

func TestGetTunnel(t *testing.T) {
	fake := cf.NewFake()
	fake.AddTunnel(cf.Tunnel{ID: "abc", Name: "t1", ConfigSrc: "cloudflare"})

	tunnel, err := fake.GetTunnel(context.Background(), "abc")
	require.NoError(t, err)
	assert.Equal(t, "t1", tunnel.Name)

	_, err = fake.GetTunnel(context.Background(), "missing")
	assert.ErrorIs(t, err, cf.ErrNotFound)
}

func TestGetConnections(t *testing.T) {
	fake := cf.NewFake()
	fake.SetConnections("abc", 3)

	count, err := fake.GetConnections(context.Background(), "abc")
	require.NoError(t, err)
	assert.Equal(t, 3, count)

	count, err = fake.GetConnections(context.Background(), "missing")
	require.NoError(t, err)
	assert.Equal(t, 0, count)
}

func TestListZones(t *testing.T) {
	fake := cf.NewFake()
	fake.AddZone(cf.Zone{ID: "z1", Name: "example.com"})
	fake.AddZone(cf.Zone{ID: "z2", Name: "sub.example.com"})

	zones, err := fake.ListZones(context.Background())
	require.NoError(t, err)
	assert.Len(t, zones, 2)
	assert.Equal(t, "example.com", zones[0].Name)
}

func TestGetZone(t *testing.T) {
	fake := cf.NewFake()
	fake.AddZone(cf.Zone{ID: "z1", Name: "example.com"})

	zone, err := fake.GetZone(context.Background(), "z1")
	require.NoError(t, err)
	assert.Equal(t, "example.com", zone.Name)

	_, err = fake.GetZone(context.Background(), "missing")
	assert.ErrorIs(t, err, cf.ErrNotFound)
}

func TestResolveZone_byName(t *testing.T) {
	fake := cf.NewFake()
	fake.AddZone(cf.Zone{ID: "z1", Name: "example.com"})
	fake.AddZone(cf.Zone{ID: "z2", Name: "other.com"})

	zoneID, err := fake.ResolveZone(context.Background(), "app.other.com", "other.com", "")
	require.NoError(t, err)
	assert.Equal(t, "z2", zoneID)
}

func TestResolveZone_longestSuffix(t *testing.T) {
	fake := cf.NewFake()
	fake.AddZone(cf.Zone{ID: "z1", Name: "example.com"})
	fake.AddZone(cf.Zone{ID: "z2", Name: "sub.example.com"})

	zoneID, err := fake.ResolveZone(context.Background(), "app.sub.example.com", "", "")
	require.NoError(t, err)
	assert.Equal(t, "z2", zoneID)

	zoneID, err = fake.ResolveZone(context.Background(), "app.example.com", "", "")
	require.NoError(t, err)
	assert.Equal(t, "z1", zoneID)
}

func TestResolveZone_fallback(t *testing.T) {
	fake := cf.NewFake()
	fake.AddZone(cf.Zone{ID: "z1", Name: "example.com"})

	zoneID, err := fake.ResolveZone(context.Background(), "unknown.com", "", "fallback-z")
	require.NoError(t, err)
	assert.Equal(t, "fallback-z", zoneID)
}

func TestResolveZone_notFound(t *testing.T) {
	fake := cf.NewFake()
	fake.AddZone(cf.Zone{ID: "z1", Name: "example.com"})

	_, err := fake.ResolveZone(context.Background(), "unknown.com", "", "")
	assert.ErrorIs(t, err, cf.ErrNotFound)
}

func TestGetIngress_empty(t *testing.T) {
	fake := cf.NewFake()

	cfg, err := fake.GetIngress(context.Background(), "abc")
	require.NoError(t, err)
	assert.Empty(t, cfg.Rules)
}

func TestGetIngress_withRules(t *testing.T) {
	fake := cf.NewFake()
	fake.SetIngress("abc", cf.IngressConfig{
		Version: 5,
		Rules: []rules.IngressEntry{
			{Hostname: "app.example.com", Service: "http://app:80"},
			{Service: "http_status:404"},
		},
	})

	cfg, err := fake.GetIngress(context.Background(), "abc")
	require.NoError(t, err)
	assert.Equal(t, int64(5), cfg.Version)
	assert.Len(t, cfg.Rules, 2)
}

func TestPutIngress(t *testing.T) {
	fake := cf.NewFake()

	version, err := fake.PutIngress(context.Background(), "abc", cf.IngressConfig{
		Rules: []rules.IngressEntry{
			{Hostname: "app.example.com", Service: "http://app:80"},
			{Service: "http_status:404"},
		},
	})
	require.NoError(t, err)
	assert.Equal(t, int64(1), version)
}

func TestListDNSRecords(t *testing.T) {
	fake := cf.NewFake()
	fake.AddDNSRecord("z1", cf.DNSRecord{
		ID: "r1", Name: "app.example.com", Type: "CNAME",
		Content: "abc-123.cfargotunnel.com", Proxied: true, ZoneID: "z1",
	})

	records, err := fake.ListDNSRecords(context.Background(), "z1", "app.example.com", "CNAME")
	require.NoError(t, err)
	assert.Len(t, records, 1)
	assert.Equal(t, "r1", records[0].ID)
}

func TestCreateDNSRecord(t *testing.T) {
	fake := cf.NewFake()

	id, err := fake.CreateDNSRecord(context.Background(), cf.DNSRecord{
		Name: "app.example.com", Type: "CNAME",
		Content: "abc.cfargotunnel.com", Proxied: true, ZoneID: "z1",
	})
	require.NoError(t, err)
	assert.NotEmpty(t, id)
}

func TestDeleteDNSRecord(t *testing.T) {
	fake := cf.NewFake()
	fake.AddDNSRecord("z1", cf.DNSRecord{
		ID: "r1", Name: "app.example.com", Type: "CNAME",
		Content: "abc.cfargotunnel.com", ZoneID: "z1",
	})

	err := fake.DeleteDNSRecord(context.Background(), "r1")
	require.NoError(t, err)

	records, _ := fake.ListDNSRecords(context.Background(), "z1", "", "")
	assert.Len(t, records, 0)
}

func TestPlan_emptyDiff(t *testing.T) {
	fake := cf.NewFake()
	fake.AddTunnel(cf.Tunnel{ID: "abc", Name: "homelab", ConfigSrc: "cloudflare"})
	fake.AddZone(cf.Zone{ID: "z1", Name: "example.com"})

	desired := map[string]rules.Rule{
		"app.example.com|": {
			Hostname: "app.example.com",
			Service:  "http://app:80",
			Status:   "active",
		},
	}
	localIngress := rules.RenderIngress(desired)
	fake.SetIngress("abc", cf.IngressConfig{Rules: localIngress})

	result, err := fake.Plan(context.Background(), "abc", desired, rules.NewState(), false)
	require.NoError(t, err)

	var addActions, removeActions int
	for _, a := range result.IngressActions {
		switch a.Kind {
		case "add":
			addActions++
		case "remove":
			removeActions++
		}
	}
	assert.Equal(t, 0, addActions)
	assert.Equal(t, 0, removeActions)
}

func TestPlan_addRule(t *testing.T) {
	fake := cf.NewFake()
	fake.AddTunnel(cf.Tunnel{ID: "abc", Name: "homelab", ConfigSrc: "cloudflare"})
	fake.AddZone(cf.Zone{ID: "z1", Name: "example.com"})

	fake.SetIngress("abc", cf.IngressConfig{
		Rules: []rules.IngressEntry{{Service: "http_status:404"}},
	})

	desired := map[string]rules.Rule{
		"app.example.com|": {
			Hostname: "app.example.com",
			Service:  "http://app:80",
			Status:   "active",
		},
	}

	result, err := fake.Plan(context.Background(), "abc", desired, rules.NewState(), false)
	require.NoError(t, err)

	var addAction *cf.PlanAction
	for _, a := range result.IngressActions {
		if a.Kind == "add" {
			addAction = &a
			break
		}
	}
	require.NotNil(t, addAction)
	assert.Contains(t, addAction.Detail, "app.example.com")
}

func TestPlan_removeRemoteRule(t *testing.T) {
	fake := cf.NewFake()
	fake.AddTunnel(cf.Tunnel{ID: "abc", Name: "homelab", ConfigSrc: "cloudflare"})
	fake.AddZone(cf.Zone{ID: "z1", Name: "example.com"})

	fake.SetIngress("abc", cf.IngressConfig{
		Rules: []rules.IngressEntry{
			{Hostname: "old.example.com", Service: "http://old:80"},
			{Service: "http_status:404"},
		},
	})

	desired := map[string]rules.Rule{}

	result, err := fake.Plan(context.Background(), "abc", desired, rules.NewState(), false)
	require.NoError(t, err)

	var removeAction *cf.PlanAction
	for _, a := range result.IngressActions {
		if a.Kind == "remove" {
			removeAction = &a
			break
		}
	}
	require.NotNil(t, removeAction)
	assert.Equal(t, "not from labels", removeAction.Detail)
}

func TestPlan_dnsCreate(t *testing.T) {
	fake := cf.NewFake()
	fake.AddTunnel(cf.Tunnel{ID: "abc", Name: "homelab", ConfigSrc: "cloudflare"})
	fake.AddZone(cf.Zone{ID: "z1", Name: "example.com"})

	desired := map[string]rules.Rule{
		"app.example.com|": {
			Hostname: "app.example.com",
			Service:  "http://app:80",
			Status:   "active",
		},
	}

	result, err := fake.Plan(context.Background(), "abc", desired, rules.NewState(), false)
	require.NoError(t, err)

	var createAction *cf.PlanAction
	for _, a := range result.DNSActions {
		if a.Kind == "dns_create" {
			createAction = &a
			break
		}
	}
	require.NotNil(t, createAction)
	assert.Contains(t, createAction.Detail, "abc.cfargotunnel.com")
}

func TestPlan_dnsAdopt(t *testing.T) {
	fake := cf.NewFake()
	fake.AddTunnel(cf.Tunnel{ID: "abc", Name: "homelab", ConfigSrc: "cloudflare"})
	fake.AddZone(cf.Zone{ID: "z1", Name: "example.com"})
	fake.AddDNSRecord("z1", cf.DNSRecord{
		ID: "r1", Name: "app.example.com", Type: "CNAME",
		Content: "abc.cfargotunnel.com", Proxied: true, ZoneID: "z1",
	})

	desired := map[string]rules.Rule{
		"app.example.com|": {
			Hostname: "app.example.com",
			Service:  "http://app:80",
			Status:   "active",
		},
	}

	result, err := fake.Plan(context.Background(), "abc", desired, rules.NewState(), false)
	require.NoError(t, err)

	var adoptAction *cf.PlanAction
	for _, a := range result.DNSActions {
		if a.Kind == "dns_adopt" {
			adoptAction = &a
			break
		}
	}
	require.NotNil(t, adoptAction)
}

func TestPlan_dnsConflict(t *testing.T) {
	fake := cf.NewFake()
	fake.AddTunnel(cf.Tunnel{ID: "abc", Name: "homelab", ConfigSrc: "cloudflare"})
	fake.AddZone(cf.Zone{ID: "z1", Name: "example.com"})
	fake.AddDNSRecord("z1", cf.DNSRecord{
		ID: "r1", Name: "app.example.com", Type: "CNAME",
		Content: "other-tunnel.cfargotunnel.com", Proxied: true, ZoneID: "z1",
	})

	desired := map[string]rules.Rule{
		"app.example.com|": {
			Hostname: "app.example.com",
			Service:  "http://app:80",
			Status:   "active",
		},
	}

	result, err := fake.Plan(context.Background(), "abc", desired, rules.NewState(), false)
	require.NoError(t, err)

	var conflictAction *cf.PlanAction
	for _, a := range result.DNSActions {
		if a.Kind == "dns_conflict" {
			conflictAction = &a
			break
		}
	}
	require.NotNil(t, conflictAction)
	assert.Contains(t, conflictAction.Detail, "other-tunnel")
}

func TestPlan_orphanDNS(t *testing.T) {
	fake := cf.NewFake()
	fake.AddTunnel(cf.Tunnel{ID: "abc", Name: "homelab", ConfigSrc: "cloudflare"})
	fake.AddZone(cf.Zone{ID: "z1", Name: "example.com"})
	fake.AddDNSRecord("z1", cf.DNSRecord{
		ID: "r1", Name: "orphan.example.com", Type: "CNAME",
		Content: "abc.cfargotunnel.com", Proxied: true, ZoneID: "z1",
	})

	desired := map[string]rules.Rule{}

	result, err := fake.Plan(context.Background(), "abc", desired, rules.NewState(), false)
	require.NoError(t, err)

	assert.Len(t, result.OrphanDNS, 1)
	assert.Equal(t, "orphan.example.com", result.OrphanDNS[0].Name)
}

func TestStats(t *testing.T) {
	fake := cf.NewFake()
	fake.AddTunnel(cf.Tunnel{ID: "abc", Name: "homelab", ConfigSrc: "cloudflare"})

	_, _ = fake.FindTunnel(context.Background(), "homelab")
	_, _ = fake.ListZones(context.Background())

	stats := fake.GetStats()
	assert.Greater(t, stats.Calls, 0)
	assert.Contains(t, stats.ByResource, "tunnels")
}

func TestZoneResolver_resolveByZonename(t *testing.T) {
	resolver := cf.NewZoneResolver("")
	resolver.SetZones([]cf.Zone{
		{ID: "z1", Name: "example.com"},
		{ID: "z2", Name: "other.com"},
	})

	zoneID, err := resolver.Resolve("app.other.com", "other.com")
	require.NoError(t, err)
	assert.Equal(t, "z2", zoneID)
}

func TestZoneResolver_longestSuffix(t *testing.T) {
	resolver := cf.NewZoneResolver("")
	resolver.SetZones([]cf.Zone{
		{ID: "z1", Name: "example.com"},
		{ID: "z2", Name: "sub.example.com"},
	})

	zoneID, err := resolver.Resolve("app.sub.example.com", "")
	require.NoError(t, err)
	assert.Equal(t, "z2", zoneID)
}

func TestZoneResolver_fallback(t *testing.T) {
	resolver := cf.NewZoneResolver("fallback-z")
	resolver.SetZones([]cf.Zone{
		{ID: "z1", Name: "example.com"},
	})

	zoneID, err := resolver.Resolve("unknown.com", "")
	require.NoError(t, err)
	assert.Equal(t, "fallback-z", zoneID)
}

func TestZoneResolver_notFound(t *testing.T) {
	resolver := cf.NewZoneResolver("")
	resolver.SetZones([]cf.Zone{
		{ID: "z1", Name: "example.com"},
	})

	_, err := resolver.Resolve("unknown.com", "")
	assert.ErrorIs(t, err, cf.ErrNotFound)
}

func TestPlan_tunnelNotFound(t *testing.T) {
	fake := cf.NewFake()

	_, err := fake.Plan(context.Background(), "missing", map[string]rules.Rule{}, rules.NewState(), false)
	assert.ErrorIs(t, err, cf.ErrNotFound)
}

func TestPlan_overwriteConflicts(t *testing.T) {
	fake := cf.NewFake()
	fake.AddTunnel(cf.Tunnel{ID: "abc", Name: "homelab", ConfigSrc: "cloudflare"})
	fake.AddZone(cf.Zone{ID: "z1", Name: "example.com"})
	fake.AddDNSRecord("z1", cf.DNSRecord{
		ID: "r1", Name: "app.example.com", Type: "CNAME",
		Content: "other.cfargotunnel.com", ZoneID: "z1",
	})

	desired := map[string]rules.Rule{
		"app.example.com|": {
			Hostname: "app.example.com",
			Service:  "http://app:80",
			Status:   "active",
		},
	}

	result, err := fake.Plan(context.Background(), "abc", desired, rules.NewState(), true)
	require.NoError(t, err)

	for _, a := range result.DNSActions {
		assert.NotEqual(t, "dns_conflict", a.Kind)
	}
}

func TestPlan_dnsNoZone(t *testing.T) {
	fake := cf.NewFake()
	fake.AddTunnel(cf.Tunnel{ID: "abc", Name: "homelab", ConfigSrc: "cloudflare"})
	fake.AddZone(cf.Zone{ID: "z1", Name: "example.com"})

	desired := map[string]rules.Rule{
		"app.unknown.net|": {
			Hostname: "app.unknown.net",
			Service:  "http://app:80",
			Status:   "active",
		},
	}

	result, err := fake.Plan(context.Background(), "abc", desired, rules.NewState(), false)
	require.NoError(t, err)

	var noZoneAction *cf.PlanAction
	for _, a := range result.DNSActions {
		if a.Kind == "dns_no_zone" {
			noZoneAction = &a
			break
		}
	}
	require.NotNil(t, noZoneAction)
	assert.Contains(t, noZoneAction.Detail, "no zone found")
}

func TestPlan_catchAllNotRemoved(t *testing.T) {
	fake := cf.NewFake()
	fake.AddTunnel(cf.Tunnel{ID: "abc", Name: "homelab", ConfigSrc: "cloudflare"})
	fake.SetIngress("abc", cf.IngressConfig{
		Rules: []rules.IngressEntry{{Service: "http_status:404"}},
	})

	desired := map[string]rules.Rule{}
	result, err := fake.Plan(context.Background(), "abc", desired, rules.NewState(), false)
	require.NoError(t, err)

	for _, a := range result.IngressActions {
		if a.Kind == "remove" {
			assert.NotEmpty(t, a.RuleKey)
		}
	}
}

func TestPlan_localIngressRendered(t *testing.T) {
	fake := cf.NewFake()
	fake.AddTunnel(cf.Tunnel{ID: "abc", Name: "homelab", ConfigSrc: "cloudflare"})

	desired := map[string]rules.Rule{
		"app.example.com|": {
			Hostname: "app.example.com",
			Service:  "http://app:80",
			Status:   "active",
		},
	}

	result, err := fake.Plan(context.Background(), "abc", desired, rules.NewState(), false)
	require.NoError(t, err)

	assert.Len(t, result.LocalIngress, 2)
	assert.Equal(t, "app.example.com", result.LocalIngress[0].Hostname)
	assert.Equal(t, "http_status:404", result.LocalIngress[1].Service)
}

func TestPlan_remoteIngressPreserved(t *testing.T) {
	fake := cf.NewFake()
	fake.AddTunnel(cf.Tunnel{ID: "abc", Name: "homelab", ConfigSrc: "cloudflare"})
	fake.SetIngress("abc", cf.IngressConfig{
		Rules: []rules.IngressEntry{
			{Hostname: "remote.example.com", Service: "http://remote:80"},
			{Service: "http_status:404"},
		},
	})

	desired := map[string]rules.Rule{}
	result, err := fake.Plan(context.Background(), "abc", desired, rules.NewState(), false)
	require.NoError(t, err)

	assert.Len(t, result.RemoteIngress, 2)
	assert.Equal(t, "remote.example.com", result.RemoteIngress[0].Hostname)
}

func TestPlan_tunnelInfo(t *testing.T) {
	fake := cf.NewFake()
	fake.AddTunnel(cf.Tunnel{ID: "abc", Name: "homelab", ConfigSrc: "cloudflare", Status: "healthy"})

	desired := map[string]rules.Rule{}
	result, err := fake.Plan(context.Background(), "abc", desired, rules.NewState(), false)
	require.NoError(t, err)

	assert.Equal(t, "abc", result.Tunnel.ID)
	assert.Equal(t, "homelab", result.Tunnel.Name)
	assert.Equal(t, "healthy", result.Tunnel.Status)
}

func TestPlan_originRequest(t *testing.T) {
	fake := cf.NewFake()
	fake.AddTunnel(cf.Tunnel{ID: "abc", Name: "homelab", ConfigSrc: "cloudflare"})

	desired := map[string]rules.Rule{
		"app.example.com|": {
			Hostname:         "app.example.com",
			Service:          "https://app:443",
			NoTLSVerify:      true,
			OriginServerName: "app.internal",
			Status:           "active",
		},
	}

	result, err := fake.Plan(context.Background(), "abc", desired, rules.NewState(), false)
	require.NoError(t, err)

	assert.NotNil(t, result.LocalIngress[0].OriginRequest)
	assert.True(t, result.LocalIngress[0].OriginRequest.NoTLSVerify)
	assert.Equal(t, "app.internal", result.LocalIngress[0].OriginRequest.OriginServerName)
}

func TestPlan_wildcardOrdering(t *testing.T) {
	fake := cf.NewFake()
	fake.AddTunnel(cf.Tunnel{ID: "abc", Name: "homelab", ConfigSrc: "cloudflare"})

	desired := map[string]rules.Rule{
		"*.example.com|": {
			Hostname: "*.example.com",
			Service:  "http://wildcard:80",
			Status:   "active",
		},
		"app.example.com|": {
			Hostname: "app.example.com",
			Service:  "http://app:80",
			Status:   "active",
		},
	}

	result, err := fake.Plan(context.Background(), "abc", desired, rules.NewState(), false)
	require.NoError(t, err)

	assert.Equal(t, "app.example.com", result.LocalIngress[0].Hostname)
	assert.Equal(t, "*.example.com", result.LocalIngress[1].Hostname)
}

func TestPlan_pathRules(t *testing.T) {
	fake := cf.NewFake()
	fake.AddTunnel(cf.Tunnel{ID: "abc", Name: "homelab", ConfigSrc: "cloudflare"})
	fake.SetIngress("abc", cf.IngressConfig{
		Rules: []rules.IngressEntry{
			{Hostname: "app.example.com", Path: "/old", Service: "http://app:80"},
			{Service: "http_status:404"},
		},
	})

	desired := map[string]rules.Rule{
		"app.example.com|/new": {
			Hostname: "app.example.com",
			Path:     "/new",
			Service:  "http://app:80",
			Status:   "active",
		},
	}

	result, err := fake.Plan(context.Background(), "abc", desired, rules.NewState(), false)
	require.NoError(t, err)

	var hasAdd, hasRemove bool
	for _, a := range result.IngressActions {
		if a.Kind == "add" {
			hasAdd = true
			assert.Contains(t, a.RuleKey, "/new")
		}
		if a.Kind == "remove" {
			hasRemove = true
			assert.Contains(t, a.RuleKey, "/old")
		}
	}
	assert.True(t, hasAdd)
	assert.True(t, hasRemove)
}

func TestFake_apiCallCounting(t *testing.T) {
	fake := cf.NewFake()
	fake.AddTunnel(cf.Tunnel{ID: "abc", Name: "homelab", ConfigSrc: "cloudflare"})
	fake.AddZone(cf.Zone{ID: "z1", Name: "example.com"})

	ctx := context.Background()
	_, _ = fake.FindTunnel(ctx, "homelab")
	_, _ = fake.FindTunnel(ctx, "missing")
	_, _ = fake.ListZones(ctx)

	assert.Equal(t, 2, fake.TunnelLookups)
	assert.Equal(t, 1, fake.ZoneLookups)
}

func TestFake_PutIngress_versionIncrement(t *testing.T) {
	fake := cf.NewFake()

	v1, _ := fake.PutIngress(context.Background(), "abc", cf.IngressConfig{
		Rules: []rules.IngressEntry{{Service: "http_status:404"}},
	})
	v2, _ := fake.PutIngress(context.Background(), "abc", cf.IngressConfig{
		Rules: []rules.IngressEntry{{Service: "http_status:404"}},
	})

	assert.Equal(t, int64(1), v1)
	assert.Equal(t, int64(2), v2)
}

func TestFake_DeleteDNSRecord_notFound(t *testing.T) {
	fake := cf.NewFake()
	err := fake.DeleteDNSRecord(context.Background(), "missing")
	assert.NoError(t, err)
}

func TestFake_ListDNSRecords_noRecords(t *testing.T) {
	fake := cf.NewFake()
	records, err := fake.ListDNSRecords(context.Background(), "z1", "", "")
	require.NoError(t, err)
	assert.Empty(t, records)
}

func TestFake_GetStats_initial(t *testing.T) {
	fake := cf.NewFake()
	stats := fake.GetStats()
	assert.Equal(t, 0, stats.Calls)
	assert.Equal(t, 0, stats.Errors)
	assert.NotNil(t, stats.ByResource)
}

func TestZoneResolver_exactMatch(t *testing.T) {
	resolver := cf.NewZoneResolver("")
	resolver.SetZones([]cf.Zone{{ID: "z1", Name: "example.com"}})

	zoneID, err := resolver.Resolve("example.com", "")
	require.NoError(t, err)
	assert.Equal(t, "z1", zoneID)
}

func TestZoneResolver_zonenameNotFound(t *testing.T) {
	resolver := cf.NewZoneResolver("")
	resolver.SetZones([]cf.Zone{{ID: "z1", Name: "example.com"}})

	_, err := resolver.Resolve("app.missing.com", "missing.com")
	assert.ErrorIs(t, err, cf.ErrNotFound)
}

func TestZoneResolver_cacheExpiry(t *testing.T) {
	resolver := cf.NewZoneResolver("")
	resolver.SetZones([]cf.Zone{{ID: "z1", Name: "example.com"}})
	assert.True(t, resolver.IsCacheValid())
}

func TestPlan_multipleRules(t *testing.T) {
	fake := cf.NewFake()
	fake.AddTunnel(cf.Tunnel{ID: "abc", Name: "homelab", ConfigSrc: "cloudflare"})
	fake.AddZone(cf.Zone{ID: "z1", Name: "example.com"})

	desired := map[string]rules.Rule{
		"a.example.com|": {Hostname: "a.example.com", Service: "http://a:80", Status: "active"},
		"b.example.com|": {Hostname: "b.example.com", Service: "http://b:80", Status: "active"},
	}

	result, err := fake.Plan(context.Background(), "abc", desired, rules.NewState(), false)
	require.NoError(t, err)

	assert.Len(t, result.DNSActions, 2)
	assert.Len(t, result.LocalIngress, 3)
}

func TestPlan_multipleOrphans(t *testing.T) {
	fake := cf.NewFake()
	fake.AddTunnel(cf.Tunnel{ID: "abc", Name: "homelab", ConfigSrc: "cloudflare"})
	fake.AddZone(cf.Zone{ID: "z1", Name: "example.com"})
	fake.AddDNSRecord("z1", cf.DNSRecord{
		ID: "r1", Name: "orphan1.example.com", Type: "CNAME",
		Content: "abc.cfargotunnel.com", ZoneID: "z1",
	})
	fake.AddDNSRecord("z1", cf.DNSRecord{
		ID: "r2", Name: "orphan2.example.com", Type: "CNAME",
		Content: "abc.cfargotunnel.com", ZoneID: "z1",
	})

	desired := map[string]rules.Rule{}
	result, err := fake.Plan(context.Background(), "abc", desired, rules.NewState(), false)
	require.NoError(t, err)

	assert.Len(t, result.OrphanDNS, 2)
}

func TestPlan_noOrphanForActiveRule(t *testing.T) {
	fake := cf.NewFake()
	fake.AddTunnel(cf.Tunnel{ID: "abc", Name: "homelab", ConfigSrc: "cloudflare"})
	fake.AddZone(cf.Zone{ID: "z1", Name: "example.com"})
	fake.AddDNSRecord("z1", cf.DNSRecord{
		ID: "r1", Name: "app.example.com", Type: "CNAME",
		Content: "abc.cfargotunnel.com", ZoneID: "z1",
	})

	desired := map[string]rules.Rule{
		"app.example.com|": {Hostname: "app.example.com", Service: "http://app:80", Status: "active"},
	}

	result, err := fake.Plan(context.Background(), "abc", desired, rules.NewState(), false)
	require.NoError(t, err)

	assert.Empty(t, result.OrphanDNS)
}

func TestPlan_nilState(t *testing.T) {
	fake := cf.NewFake()
	fake.AddTunnel(cf.Tunnel{ID: "abc", Name: "homelab", ConfigSrc: "cloudflare"})
	fake.AddZone(cf.Zone{ID: "z1", Name: "example.com"})

	desired := map[string]rules.Rule{
		"app.example.com|": {Hostname: "app.example.com", Service: "http://app:80", Status: "active"},
	}

	result, err := fake.Plan(context.Background(), "abc", desired, nil, false)
	require.NoError(t, err)
	assert.NotEmpty(t, result.Tunnel.ID)
}

func TestPlan_emptyDesired(t *testing.T) {
	fake := cf.NewFake()
	fake.AddTunnel(cf.Tunnel{ID: "abc", Name: "homelab", ConfigSrc: "cloudflare"})
	fake.SetIngress("abc", cf.IngressConfig{
		Rules: []rules.IngressEntry{
			{Hostname: "app.example.com", Service: "http://app:80"},
			{Service: "http_status:404"},
		},
	})

	result, err := fake.Plan(context.Background(), "abc", map[string]rules.Rule{}, rules.NewState(), false)
	require.NoError(t, err)

	var removeAction *cf.PlanAction
	for _, a := range result.IngressActions {
		if a.Kind == "remove" {
			removeAction = &a
			break
		}
	}
	require.NotNil(t, removeAction)
	assert.Contains(t, removeAction.RuleKey, "app.example.com")
}

func TestPlan_statsIncremented(t *testing.T) {
	fake := cf.NewFake()
	fake.AddTunnel(cf.Tunnel{ID: "abc", Name: "homelab", ConfigSrc: "cloudflare"})
	fake.AddZone(cf.Zone{ID: "z1", Name: "example.com"})

	desired := map[string]rules.Rule{
		"app.example.com|": {Hostname: "app.example.com", Service: "http://app:80", Status: "active"},
	}

	_, _ = fake.Plan(context.Background(), "abc", desired, rules.NewState(), false)
	stats := fake.GetStats()
	assert.Greater(t, stats.Calls, 0)
}
