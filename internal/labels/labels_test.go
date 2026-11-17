package labels

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Tests for the owner's three real containers (§3 S3 RED)
// ---------------------------------------------------------------------------

func TestParse_whoami_cloudflare_tunnel(t *testing.T) {
	labels := map[string]string{
		"cloudflare.tunnel.enable":   "true",
		"cloudflare.tunnel.hostname": "whoami.example.org",
		"cloudflare.tunnel.service":  "http://whoami:80",
	}
	rules, issues := Parse("whoami", labels, []string{"cloudflare.tunnel"})

	require.Len(t, rules, 1)
	assert.Equal(t, "whoami.example.org", rules[0].Hostname)
	assert.Equal(t, "http://whoami:80", rules[0].Service)
	assert.Empty(t, rules[0].Path)
	assert.Empty(t, issues)
}

func TestParse_vaultwarden_cloudflare_tunnel(t *testing.T) {
	labels := map[string]string{
		"cloudflare.tunnel.enable":   "true",
		"cloudflare.tunnel.hostname": "vault.example.org",
		"cloudflare.tunnel.service":  "http://vaultwarden:80",
	}
	rules, issues := Parse("vaultwarden", labels, []string{"cloudflare.tunnel"})

	require.Len(t, rules, 1)
	assert.Equal(t, "vault.example.org", rules[0].Hostname)
	assert.Equal(t, "http://vaultwarden:80", rules[0].Service)
	assert.Empty(t, issues)
}

func TestParse_cors_proxy_cloudflare_tunnel(t *testing.T) {
	labels := map[string]string{
		"cloudflare.tunnel.enable":   "true",
		"cloudflare.tunnel.hostname": "cors-proxy.example.org",
		"cloudflare.tunnel.service":  "http://cors-proxy:8080",
	}
	rules, issues := Parse("cors-proxy", labels, []string{"cloudflare.tunnel"})

	require.Len(t, rules, 1)
	assert.Equal(t, "cors-proxy.example.org", rules[0].Hostname)
	assert.Equal(t, "http://cors-proxy:8080", rules[0].Service)
	assert.Empty(t, issues)
}

// ---------------------------------------------------------------------------
// Dockflare prefix yields identical rules
// ---------------------------------------------------------------------------

func TestParse_whoami_dockflare(t *testing.T) {
	labels := map[string]string{
		"dockflare.enable":   "true",
		"dockflare.hostname": "whoami.example.org",
		"dockflare.service":  "http://whoami:80",
	}
	rules, issues := Parse("whoami", labels, []string{"dockflare"})

	require.Len(t, rules, 1)
	assert.Equal(t, "whoami.example.org", rules[0].Hostname)
	assert.Equal(t, "http://whoami:80", rules[0].Service)
	assert.Empty(t, issues)
}

// ---------------------------------------------------------------------------
// Both prefixes on one container: priority order wins, conflict detected
// ---------------------------------------------------------------------------

func TestParse_both_prefixes_priority_and_conflict(t *testing.T) {
	labels := map[string]string{
		"cloudflare.tunnel.enable":   "true",
		"cloudflare.tunnel.hostname": "whoami.example.org",
		"cloudflare.tunnel.service":  "http://whoami:80",
		"dockflare.enable":           "true",
		"dockflare.hostname":         "other.example.com", // different → conflict
		"dockflare.service":          "http://whoami:80",
	}
	rules, issues := Parse("whoami", labels, []string{"cloudflare.tunnel", "dockflare"})

	require.Len(t, rules, 1)
	// cloudflare.tunnel has higher priority, so its hostname wins
	assert.Equal(t, "whoami.example.org", rules[0].Hostname)

	// dockflare.hostname differs → conflict warning
	require.Len(t, issues, 1)
	assert.Equal(t, "label_conflict", issues[0].Kind)
	assert.Equal(t, "hostname", issues[0].Field)
}

// ---------------------------------------------------------------------------
// Indexed rules
// ---------------------------------------------------------------------------

func TestParse_indexed_rules(t *testing.T) {
	labels := map[string]string{
		"cloudflare.tunnel.enable":     "true",
		"cloudflare.tunnel.0.hostname": "app1.example.com",
		"cloudflare.tunnel.0.service":  "http://app1:80",
		"cloudflare.tunnel.1.hostname": "app2.example.com",
		"cloudflare.tunnel.1.service":  "http://app2:80",
		"cloudflare.tunnel.2.hostname": "app3.example.com",
		"cloudflare.tunnel.2.service":  "http://app3:80",
	}
	rules, issues := Parse("multi-app", labels, []string{"cloudflare.tunnel"})

	require.Len(t, rules, 3)
	assert.Empty(t, issues)
	assert.Equal(t, "app1.example.com", rules[0].Hostname)
	assert.Equal(t, "app2.example.com", rules[1].Hostname)
	assert.Equal(t, "app3.example.com", rules[2].Hostname)
}

func TestParse_indexed_with_non_indexed_defaults(t *testing.T) {
	labels := map[string]string{
		"cloudflare.tunnel.enable":     "true",
		"cloudflare.tunnel.service":    "http://default:80", // non-indexed default
		"cloudflare.tunnel.0.hostname": "app1.example.com",
		"cloudflare.tunnel.1.hostname": "app2.example.com",
		"cloudflare.tunnel.1.service":  "http://app2:9090", // overrides default
	}
	rules, issues := Parse("multi-app", labels, []string{"cloudflare.tunnel"})

	require.Len(t, rules, 2)
	assert.Empty(t, issues)
	// Rule 0: hostname from indexed, service from non-indexed default
	assert.Equal(t, "app1.example.com", rules[0].Hostname)
	assert.Equal(t, "http://default:80", rules[0].Service)
	// Rule 1: both from indexed (service overrides default)
	assert.Equal(t, "app2.example.com", rules[1].Hostname)
	assert.Equal(t, "http://app2:9090", rules[1].Service)
}

// ---------------------------------------------------------------------------
// Wildcard hostname
// ---------------------------------------------------------------------------

func TestParse_wildcard_hostname(t *testing.T) {
	labels := map[string]string{
		"cloudflare.tunnel.enable":   "true",
		"cloudflare.tunnel.hostname": "*.example.com",
		"cloudflare.tunnel.service":  "http://app:80",
	}
	rules, issues := Parse("wildcard", labels, []string{"cloudflare.tunnel"})

	require.Len(t, rules, 1)
	assert.Equal(t, "*.example.com", rules[0].Hostname)
	assert.Empty(t, issues)
}

// ---------------------------------------------------------------------------
// Optional fields: path, no_tls_verify, originsrvname, zonename
// ---------------------------------------------------------------------------

func TestParse_path(t *testing.T) {
	labels := map[string]string{
		"cloudflare.tunnel.enable":   "true",
		"cloudflare.tunnel.hostname": "app.example.com",
		"cloudflare.tunnel.service":  "http://app:80",
		"cloudflare.tunnel.path":     "^/api/.*",
	}
	rules, issues := Parse("app", labels, []string{"cloudflare.tunnel"})

	require.Len(t, rules, 1)
	assert.Equal(t, "^/api/.*", rules[0].Path)
	assert.Empty(t, issues)
}

func TestParse_no_tls_verify(t *testing.T) {
	labels := map[string]string{
		"cloudflare.tunnel.enable":        "true",
		"cloudflare.tunnel.hostname":      "app.example.com",
		"cloudflare.tunnel.service":       "http://app:80",
		"cloudflare.tunnel.no_tls_verify": "true",
	}
	rules, issues := Parse("app", labels, []string{"cloudflare.tunnel"})

	require.Len(t, rules, 1)
	assert.True(t, rules[0].NoTLSVerify)
	assert.Empty(t, issues)
}

func TestParse_originsrvname(t *testing.T) {
	labels := map[string]string{
		"cloudflare.tunnel.enable":        "true",
		"cloudflare.tunnel.hostname":      "app.example.com",
		"cloudflare.tunnel.service":       "http://app:80",
		"cloudflare.tunnel.originsrvname": "internal.example.com",
	}
	rules, issues := Parse("app", labels, []string{"cloudflare.tunnel"})

	require.Len(t, rules, 1)
	assert.Equal(t, "internal.example.com", rules[0].OriginServerName)
	assert.Empty(t, issues)
}

func TestParse_zonename(t *testing.T) {
	labels := map[string]string{
		"cloudflare.tunnel.enable":   "true",
		"cloudflare.tunnel.hostname": "app.example.com",
		"cloudflare.tunnel.service":  "http://app:80",
		"cloudflare.tunnel.zonename": "example.com",
	}
	rules, issues := Parse("app", labels, []string{"cloudflare.tunnel"})

	require.Len(t, rules, 1)
	assert.Equal(t, "example.com", rules[0].ZoneName)
	assert.Empty(t, issues)
}

// ---------------------------------------------------------------------------
// Invalid rules: reported, not published
// ---------------------------------------------------------------------------

func TestParse_missing_hostname(t *testing.T) {
	labels := map[string]string{
		"cloudflare.tunnel.enable":  "true",
		"cloudflare.tunnel.service": "http://app:80",
	}
	rules, issues := Parse("app", labels, []string{"cloudflare.tunnel"})

	assert.Empty(t, rules)
	require.Len(t, issues, 1)
	assert.Equal(t, "invalid", issues[0].Kind)
	assert.Contains(t, issues[0].Detail, "hostname")
}

func TestParse_missing_service(t *testing.T) {
	labels := map[string]string{
		"cloudflare.tunnel.enable":   "true",
		"cloudflare.tunnel.hostname": "app.example.com",
	}
	rules, issues := Parse("app", labels, []string{"cloudflare.tunnel"})

	assert.Empty(t, rules)
	require.Len(t, issues, 1)
	assert.Equal(t, "invalid", issues[0].Kind)
	assert.Contains(t, issues[0].Detail, "service")
}

func TestParse_bad_scheme(t *testing.T) {
	labels := map[string]string{
		"cloudflare.tunnel.enable":   "true",
		"cloudflare.tunnel.hostname": "app.example.com",
		"cloudflare.tunnel.service":  "ftp://app:21",
	}
	rules, issues := Parse("app", labels, []string{"cloudflare.tunnel"})

	assert.Empty(t, rules)
	require.Len(t, issues, 1)
	assert.Equal(t, "invalid", issues[0].Kind)
	assert.Contains(t, issues[0].Detail, "scheme")
}

func TestParse_bad_bool(t *testing.T) {
	labels := map[string]string{
		"cloudflare.tunnel.enable":        "true",
		"cloudflare.tunnel.hostname":      "app.example.com",
		"cloudflare.tunnel.service":       "http://app:80",
		"cloudflare.tunnel.no_tls_verify": "maybe",
	}
	rules, issues := Parse("app", labels, []string{"cloudflare.tunnel"})

	assert.Empty(t, rules)
	require.Len(t, issues, 1)
	assert.Equal(t, "invalid", issues[0].Kind)
	assert.Contains(t, issues[0].Detail, "no_tls_verify")
}

func TestParse_bad_hostname(t *testing.T) {
	labels := map[string]string{
		"cloudflare.tunnel.enable":   "true",
		"cloudflare.tunnel.hostname": "not a valid hostname!!!",
		"cloudflare.tunnel.service":  "http://app:80",
	}
	rules, issues := Parse("app", labels, []string{"cloudflare.tunnel"})

	assert.Empty(t, rules)
	require.Len(t, issues, 1)
	assert.Equal(t, "invalid", issues[0].Kind)
	assert.Contains(t, issues[0].Detail, "hostname")
}

// ---------------------------------------------------------------------------
// Access labels
// ---------------------------------------------------------------------------

func TestParse_access_blocked(t *testing.T) {
	labels := map[string]string{
		"cloudflare.tunnel.enable":        "true",
		"cloudflare.tunnel.hostname":      "app.example.com",
		"cloudflare.tunnel.service":       "http://app:80",
		"cloudflare.tunnel.access.policy": "authenticate",
	}
	rules, issues := Parse("app", labels, []string{"cloudflare.tunnel"}, ParseOptionAllowUnprotectedAccess(false))

	assert.Empty(t, rules)
	require.Len(t, issues, 1)
	assert.Equal(t, "blocked_access_unsupported", issues[0].Kind)
}

func TestParse_access_group_blocked(t *testing.T) {
	labels := map[string]string{
		"cloudflare.tunnel.enable":       "true",
		"cloudflare.tunnel.hostname":     "app.example.com",
		"cloudflare.tunnel.service":      "http://app:80",
		"cloudflare.tunnel.access.group": "my-group",
	}
	rules, issues := Parse("app", labels, []string{"cloudflare.tunnel"}, ParseOptionAllowUnprotectedAccess(false))

	assert.Empty(t, rules)
	require.Len(t, issues, 1)
	assert.Equal(t, "blocked_access_unsupported", issues[0].Kind)
}

func TestParse_access_email_blocked(t *testing.T) {
	labels := map[string]string{
		"cloudflare.tunnel.enable":       "true",
		"cloudflare.tunnel.hostname":     "app.example.com",
		"cloudflare.tunnel.service":      "http://app:80",
		"cloudflare.tunnel.access.email": "user@example.com",
	}
	rules, issues := Parse("app", labels, []string{"cloudflare.tunnel"}, ParseOptionAllowUnprotectedAccess(false))

	assert.Empty(t, rules)
	require.Len(t, issues, 1)
	assert.Equal(t, "blocked_access_unsupported", issues[0].Kind)
}

func TestParse_access_custom_rules_blocked(t *testing.T) {
	labels := map[string]string{
		"cloudflare.tunnel.enable":              "true",
		"cloudflare.tunnel.hostname":            "app.example.com",
		"cloudflare.tunnel.service":             "http://app:80",
		"cloudflare.tunnel.access.custom_rules": `[{"action":"allow"}]`,
	}
	rules, issues := Parse("app", labels, []string{"cloudflare.tunnel"}, ParseOptionAllowUnprotectedAccess(false))

	assert.Empty(t, rules)
	require.Len(t, issues, 1)
	assert.Equal(t, "blocked_access_unsupported", issues[0].Kind)
}

func TestParse_access_bypass_published(t *testing.T) {
	labels := map[string]string{
		"cloudflare.tunnel.enable":        "true",
		"cloudflare.tunnel.hostname":      "app.example.com",
		"cloudflare.tunnel.service":       "http://app:80",
		"cloudflare.tunnel.access.policy": "bypass",
	}
	rules, issues := Parse("app", labels, []string{"cloudflare.tunnel"})

	require.Len(t, rules, 1)
	assert.Equal(t, "app.example.com", rules[0].Hostname)
	// bypass is allowed; may produce an info issue
	for _, iss := range issues {
		assert.NotEqual(t, "blocked_access_unsupported", iss.Kind)
	}
}

func TestParse_access_default_tld_published(t *testing.T) {
	labels := map[string]string{
		"cloudflare.tunnel.enable":        "true",
		"cloudflare.tunnel.hostname":      "app.example.com",
		"cloudflare.tunnel.service":       "http://app:80",
		"cloudflare.tunnel.access.policy": "default_tld",
	}
	rules, issues := Parse("app", labels, []string{"cloudflare.tunnel"})

	require.Len(t, rules, 1)
	assert.Equal(t, "app.example.com", rules[0].Hostname)
	for _, iss := range issues {
		assert.NotEqual(t, "blocked_access_unsupported", iss.Kind)
	}
}

func TestParse_access_blocked_unblocked_by_flag(t *testing.T) {
	labels := map[string]string{
		"cloudflare.tunnel.enable":        "true",
		"cloudflare.tunnel.hostname":      "app.example.com",
		"cloudflare.tunnel.service":       "http://app:80",
		"cloudflare.tunnel.access.policy": "authenticate",
	}
	rules, issues := Parse("app", labels, []string{"cloudflare.tunnel"}, ParseOptionAllowUnprotectedAccess(true))

	require.Len(t, rules, 1)
	assert.Equal(t, "app.example.com", rules[0].Hostname)
	for _, iss := range issues {
		assert.NotEqual(t, "blocked_access_unsupported", iss.Kind)
	}
}

// ---------------------------------------------------------------------------
// Container without enable=true → no rules
// ---------------------------------------------------------------------------

func TestParse_no_enable(t *testing.T) {
	labels := map[string]string{
		"cloudflare.tunnel.hostname": "app.example.com",
		"cloudflare.tunnel.service":  "http://app:80",
	}
	rules, issues := Parse("app", labels, []string{"cloudflare.tunnel"})

	assert.Empty(t, rules)
	assert.Empty(t, issues)
}

func TestParse_enable_false(t *testing.T) {
	labels := map[string]string{
		"cloudflare.tunnel.enable":   "false",
		"cloudflare.tunnel.hostname": "app.example.com",
		"cloudflare.tunnel.service":  "http://app:80",
	}
	rules, issues := Parse("app", labels, []string{"cloudflare.tunnel"})

	assert.Empty(t, rules)
	assert.Empty(t, issues)
}

// ---------------------------------------------------------------------------
// Unrelated labels ignored silently
// ---------------------------------------------------------------------------

func TestParse_unrelated_labels_ignored(t *testing.T) {
	labels := map[string]string{
		"cloudflare.tunnel.enable":              "true",
		"cloudflare.tunnel.hostname":            "app.example.com",
		"cloudflare.tunnel.service":             "http://app:80",
		"traefik.enable":                        "true",
		"traefik.http.routers.app.rule":         "Host(`app.example.com`)",
		"com.centurylinklabs.watchtower.enable": "false",
	}
	rules, issues := Parse("app", labels, []string{"cloudflare.tunnel"})

	require.Len(t, rules, 1)
	assert.Equal(t, "app.example.com", rules[0].Hostname)
	assert.Empty(t, issues)
}

// ---------------------------------------------------------------------------
// Unknown label under a configured prefix → warning
// ---------------------------------------------------------------------------

func TestParse_unknown_label_warning(t *testing.T) {
	labels := map[string]string{
		"cloudflare.tunnel.enable":        "true",
		"cloudflare.tunnel.hostname":      "app.example.com",
		"cloudflare.tunnel.service":       "http://app:80",
		"cloudflare.tunnel.unknown_field": "some-value",
	}
	rules, issues := Parse("app", labels, []string{"cloudflare.tunnel"})

	require.Len(t, rules, 1)
	require.Len(t, issues, 1)
	assert.Equal(t, "unknown_label", issues[0].Kind)
	assert.Equal(t, "unknown_field", issues[0].Field)
}

// ---------------------------------------------------------------------------
// Duplicate key across containers → first by name wins
// ---------------------------------------------------------------------------

func TestParse_duplicate_key_across_containers(t *testing.T) {
	labelsA := map[string]string{
		"cloudflare.tunnel.enable":   "true",
		"cloudflare.tunnel.hostname": "app.example.com",
		"cloudflare.tunnel.service":  "http://app:80",
	}
	labelsB := map[string]string{
		"cloudflare.tunnel.enable":   "true",
		"cloudflare.tunnel.hostname": "app.example.com",
		"cloudflare.tunnel.service":  "http://other:80",
	}

	allRules, allIssues := ParseAll([]ContainerLabels{
		{Name: "beta-app", Labels: labelsB},
		{Name: "alpha-app", Labels: labelsA},
	}, []string{"cloudflare.tunnel"})

	// alpha-app sorts first, so it wins
	require.Len(t, allRules, 1)
	assert.Equal(t, "alpha-app", allRules[0].ContainerName)
	assert.Equal(t, "http://app:80", allRules[0].Service)

	require.Len(t, allIssues, 1)
	assert.Equal(t, "conflict", allIssues[0].Kind)
	assert.Equal(t, "beta-app", allIssues[0].Container)
}

// ---------------------------------------------------------------------------
// Bool parsing variations
// ---------------------------------------------------------------------------

func TestParse_bool_variations(t *testing.T) {
	for _, v := range []string{"true", "TRUE", "True", "1"} {
		t.Run(v, func(t *testing.T) {
			labels := map[string]string{
				"cloudflare.tunnel.enable":        v,
				"cloudflare.tunnel.hostname":      "app.example.com",
				"cloudflare.tunnel.service":       "http://app:80",
				"cloudflare.tunnel.no_tls_verify": "1",
			}
			rules, issues := Parse("app", labels, []string{"cloudflare.tunnel"})
			require.Len(t, rules, 1)
			assert.True(t, rules[0].NoTLSVerify)
			assert.Empty(t, issues)
		})
	}
}

func TestParse_bool_false_variations(t *testing.T) {
	for _, v := range []string{"false", "FALSE", "False", "0"} {
		t.Run(v, func(t *testing.T) {
			labels := map[string]string{
				"cloudflare.tunnel.enable":        v,
				"cloudflare.tunnel.hostname":      "app.example.com",
				"cloudflare.tunnel.service":       "http://app:80",
				"cloudflare.tunnel.no_tls_verify": "0",
			}
			rules, _ := Parse("app", labels, []string{"cloudflare.tunnel"})
			if v == "false" || v == "FALSE" || v == "False" || v == "0" {
				// enable=false → no rules
				assert.Empty(t, rules)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Service URL schemes
// ---------------------------------------------------------------------------

func TestParse_service_schemes(t *testing.T) {
	for _, scheme := range []string{"http", "https", "tcp", "ssh", "rdp", "smb", "unix", "unix+tls"} {
		t.Run(scheme, func(t *testing.T) {
			labels := map[string]string{
				"cloudflare.tunnel.enable":   "true",
				"cloudflare.tunnel.hostname": "app.example.com",
				"cloudflare.tunnel.service":  scheme + "://app:80",
			}
			rules, issues := Parse("app", labels, []string{"cloudflare.tunnel"})
			require.Len(t, rules, 1)
			assert.Equal(t, scheme+"://app:80", rules[0].Service)
			assert.Empty(t, issues)
		})
	}
}

func TestParse_http_status_service(t *testing.T) {
	labels := map[string]string{
		"cloudflare.tunnel.enable":   "true",
		"cloudflare.tunnel.hostname": "app.example.com",
		"cloudflare.tunnel.service":  "http_status:404",
	}
	rules, issues := Parse("app", labels, []string{"cloudflare.tunnel"})

	require.Len(t, rules, 1)
	assert.Equal(t, "http_status:404", rules[0].Service)
	assert.Empty(t, issues)
}

// ---------------------------------------------------------------------------
// Indexed enable is ignored (non-indexed only)
// ---------------------------------------------------------------------------

func TestParse_indexed_enable_ignored(t *testing.T) {
	labels := map[string]string{
		"cloudflare.tunnel.enable":     "true",
		"cloudflare.tunnel.0.enable":   "false", // should be ignored
		"cloudflare.tunnel.0.hostname": "app.example.com",
		"cloudflare.tunnel.0.service":  "http://app:80",
	}
	rules, issues := Parse("app", labels, []string{"cloudflare.tunnel"})

	require.Len(t, rules, 1)
	assert.Equal(t, "app.example.com", rules[0].Hostname)
	assert.Empty(t, issues)
}

// ---------------------------------------------------------------------------
// Source tracking
// ---------------------------------------------------------------------------

func TestParse_source_tracking(t *testing.T) {
	labels := map[string]string{
		"cloudflare.tunnel.enable":   "true",
		"cloudflare.tunnel.hostname": "app.example.com",
		"cloudflare.tunnel.service":  "http://app:80",
	}
	rules, _ := Parse("app", labels, []string{"cloudflare.tunnel"})

	require.Len(t, rules, 1)
	assert.Equal(t, "cloudflare.tunnel", rules[0].SourcePrefix)
	assert.Nil(t, rules[0].SourceIndex) // non-indexed
}

func TestParse_source_tracking_indexed(t *testing.T) {
	labels := map[string]string{
		"cloudflare.tunnel.enable":     "true",
		"cloudflare.tunnel.0.hostname": "app.example.com",
		"cloudflare.tunnel.0.service":  "http://app:80",
	}
	rules, _ := Parse("app", labels, []string{"cloudflare.tunnel"})

	require.Len(t, rules, 1)
	assert.Equal(t, "cloudflare.tunnel", rules[0].SourcePrefix)
	require.NotNil(t, rules[0].SourceIndex)
	assert.Equal(t, 0, *rules[0].SourceIndex)
}

// ---------------------------------------------------------------------------
// Rule key generation
// ---------------------------------------------------------------------------

func TestRule_Key(t *testing.T) {
	r := Rule{Hostname: "App.Example.COM", Path: "^/api/"}
	assert.Equal(t, "app.example.com|^/api/", r.Key())
}

func TestRule_Key_no_path(t *testing.T) {
	r := Rule{Hostname: "App.Example.COM"}
	assert.Equal(t, "app.example.com", r.Key())
}
