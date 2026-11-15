package rules

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRenderIngress_order tests the ordering rules:
// non-wildcard before wildcard, path before no-path, longer path first, hostname asc, service asc.
func TestRenderIngress_order(t *testing.T) {
	rules := map[string]Rule{
		"*.example.com": {
			Hostname: "*.example.com",
			Service:  "http://catchall:80",
			Status:   "active",
		},
		"app.example.com|/": {
			Hostname: "app.example.com",
			Path:     "/",
			Service:  "http://app:80",
			Status:   "active",
		},
		"app.example.com|/api": {
			Hostname: "app.example.com",
			Path:     "/api",
			Service:  "http://api:80",
			Status:   "active",
		},
		"app.example.com|/api/v1": {
			Hostname: "app.example.com",
			Path:     "/api/v1",
			Service:  "http://api-v1:80",
			Status:   "active",
		},
		"app.example.com": {
			Hostname: "app.example.com",
			Service:  "http://app-root:80",
			Status:   "active",
		},
		"web.example.com": {
			Hostname: "web.example.com",
			Service:  "http://web:80",
			Status:   "active",
		},
	}

	ingress := RenderIngress(rules)

	// Non-wildcard before wildcard
	assert.NotEqual(t, "*.example.com", ingress[0].Hostname, "wildcard should not be first")
	// Within same hostname, path before no-path
	// /api/v1 before /api (longer first)
	// /api before /
	// / before no-path
	foundAPI := false
	foundSlash := false
	foundNoPath := false
	for i, entry := range ingress {
		switch {
		case entry.Hostname == "app.example.com" && entry.Path == "/api/v1":
			assert.False(t, foundAPI || foundSlash || foundNoPath, "/api/v1 should come before shorter paths, got position %d", i)
		case entry.Hostname == "app.example.com" && entry.Path == "/api":
			assert.False(t, foundSlash || foundNoPath, "/api should come before shorter paths, got position %d", i)
			foundAPI = true
		case entry.Hostname == "app.example.com" && entry.Path == "/":
			assert.False(t, foundNoPath, "/ should come before no-path, got position %d", i)
			foundSlash = true
		case entry.Hostname == "app.example.com" && entry.Path == "":
			foundNoPath = true
		}
	}
	// Catch-all should be last
	last := ingress[len(ingress)-1]
	assert.Equal(t, "http_status:404", last.Service, "last entry should be catch-all")
}

// TestRenderIngress_catch_all tests that the catch-all is always present.
func TestRenderIngress_catch_all(t *testing.T) {
	rules := map[string]Rule{}
	ingress := RenderIngress(rules)

	require.Len(t, ingress, 1)
	assert.Equal(t, "http_status:404", ingress[0].Service)
}

// TestRenderIngress_origin_request tests that originRequest is only included when needed.
func TestRenderIngress_origin_request(t *testing.T) {
	rules := map[string]Rule{
		"simple.example.com": {
			Hostname: "simple.example.com",
			Service:  "http://simple:80",
			Status:   "active",
		},
		"tls.example.com": {
			Hostname:    "tls.example.com",
			Service:     "http://tls:80",
			NoTLSVerify: true,
			Status:      "active",
		},
		"sni.example.com": {
			Hostname:         "sni.example.com",
			Service:          "http://sni:80",
			OriginServerName: "sni.example.com",
			Status:           "active",
		},
	}

	ingress := RenderIngress(rules)

	for _, entry := range ingress {
		switch entry.Hostname {
		case "simple.example.com":
			assert.Nil(t, entry.OriginRequest, "simple rule should not have originRequest")
		case "tls.example.com":
			require.NotNil(t, entry.OriginRequest, "tls rule should have originRequest")
			assert.True(t, entry.OriginRequest.NoTLSVerify)
		case "sni.example.com":
			require.NotNil(t, entry.OriginRequest, "sni rule should have originRequest")
			assert.Equal(t, "sni.example.com", entry.OriginRequest.OriginServerName)
		}
	}
}

// TestRenderIngress_deterministic tests that output is identical for shuffled input.
func TestRenderIngress_deterministic(t *testing.T) {
	rules := map[string]Rule{
		"whoami.example.com": {
			Hostname: "whoami.example.com",
			Service:  "http://whoami:80",
			Status:   "active",
		},
		"vault.example.com": {
			Hostname: "vault.example.com",
			Service:  "http://vault:80",
			Status:   "active",
		},
		"web.example.com": {
			Hostname: "web.example.com",
			Service:  "http://web:80",
			Status:   "active",
		},
	}

	// Render multiple times and compare
	first := RenderIngress(rules)
	for i := 0; i < 10; i++ {
		second := RenderIngress(rules)
		assert.Equal(t, first, second, "render %d produced different output", i+1)
	}
}

// TestRenderIngress_golden tests the golden file for the 3-rule homelab set.
func TestRenderIngress_golden(t *testing.T) {
	now := time.Now()
	rules := map[string]Rule{
		"whoami.destructuring-bind.org": {
			Hostname:  "whoami.destructuring-bind.org",
			Service:   "http://whoami:80",
			Status:    "active",
			FirstSeen: now,
			LastSeen:  now,
		},
		"vault.destructuring-bind.org": {
			Hostname:  "vault.destructuring-bind.org",
			Service:   "http://vaultwarden:80",
			Status:    "active",
			FirstSeen: now,
			LastSeen:  now,
		},
		"cors-proxy.destructuring-bind.org": {
			Hostname:  "cors-proxy.destructuring-bind.org",
			Service:   "http://cors-proxy:8080",
			Status:    "active",
			FirstSeen: now,
			LastSeen:  now,
		},
	}

	ingress := RenderIngress(rules)

	goldenPath := filepath.Join("..", "..", "testdata", "ingress", "homelab.golden.json")
	goldenData, err := os.ReadFile(goldenPath)
	require.NoError(t, err, "read golden file")

	got, err := json.MarshalIndent(ingress, "", "  ")
	require.NoError(t, err, "marshal ingress")

	// Add trailing newline to match golden file format
	got = append(got, '\n')

	assert.Equal(t, string(goldenData), string(got), "ingress output does not match golden file")
}

// TestRenderIngress_excludes_expired tests that expired rules are not included.
func TestRenderIngress_excludes_expired(t *testing.T) {
	rules := map[string]Rule{
		"active.example.com": {
			Hostname: "active.example.com",
			Service:  "http://active:80",
			Status:   "active",
		},
		"expired.example.com": {
			Hostname: "expired.example.com",
			Service:  "http://expired:80",
			Status:   "expired",
		},
	}

	ingress := RenderIngress(rules)

	for _, entry := range ingress {
		assert.NotEqual(t, "expired.example.com", entry.Hostname, "expired rule should not be in ingress")
	}
}

// TestRenderIngress_includes_pending tests that pending rules are included in ingress.
func TestRenderIngress_includes_pending(t *testing.T) {
	rules := map[string]Rule{
		"pending.example.com": {
			Hostname: "pending.example.com",
			Service:  "http://pending:80",
			Status:   "pending",
		},
	}

	ingress := RenderIngress(rules)

	found := false
	for _, entry := range ingress {
		if entry.Hostname == "pending.example.com" {
			found = true
			break
		}
	}
	assert.True(t, found, "pending rule should be in ingress")
}
