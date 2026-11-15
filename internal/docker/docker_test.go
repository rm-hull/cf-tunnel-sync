package docker

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDesiredRules_empty(t *testing.T) {
	api := NewFake()
	rules, issues, err := api.DesiredRules(context.Background(), []string{"cloudflare.tunnel"}, false)
	require.NoError(t, err)
	assert.Empty(t, rules)
	assert.Empty(t, issues)
}

func TestDesiredRules_singleContainer(t *testing.T) {
	api := NewFake(Container{
		ID:   "abc123",
		Name: "whoami",
		Labels: map[string]string{
			"cloudflare.tunnel.enable":   "true",
			"cloudflare.tunnel.hostname": "whoami.example.com",
			"cloudflare.tunnel.service":  "http://whoami:80",
		},
	})
	rules, issues, err := api.DesiredRules(context.Background(), []string{"cloudflare.tunnel"}, false)
	require.NoError(t, err)
	require.Len(t, rules, 1)
	r := rules["whoami.example.com|"]
	assert.Equal(t, "whoami.example.com", r.Hostname)
	assert.Equal(t, "http://whoami:80", r.Service)
	assert.Equal(t, "whoami", r.ContainerName)
	assert.Empty(t, issues)
}

func TestDesiredRules_multipleContainers(t *testing.T) {
	api := NewFake(
		Container{
			ID:   "c1",
			Name: "whoami",
			Labels: map[string]string{
				"cloudflare.tunnel.enable":   "true",
				"cloudflare.tunnel.hostname": "whoami.example.com",
				"cloudflare.tunnel.service":  "http://whoami:80",
			},
		},
		Container{
			ID:   "c2",
			Name: "vaultwarden",
			Labels: map[string]string{
				"cloudflare.tunnel.enable":   "true",
				"cloudflare.tunnel.hostname": "vault.example.com",
				"cloudflare.tunnel.service":  "http://vaultwarden:80",
			},
		},
	)
	rules, issues, err := api.DesiredRules(context.Background(), []string{"cloudflare.tunnel"}, false)
	require.NoError(t, err)
	assert.Len(t, rules, 2)
	assert.Empty(t, issues)
}

func TestDesiredRules_excludesDisabled(t *testing.T) {
	api := NewFake(
		Container{
			ID:   "c1",
			Name: "whoami",
			Labels: map[string]string{
				"cloudflare.tunnel.enable":   "true",
				"cloudflare.tunnel.hostname": "whoami.example.com",
				"cloudflare.tunnel.service":  "http://whoami:80",
			},
		},
		Container{
			ID:   "c2",
			Name: "disabled",
			Labels: map[string]string{
				"cloudflare.tunnel.enable":   "false",
				"cloudflare.tunnel.hostname": "disabled.example.com",
				"cloudflare.tunnel.service":  "http://disabled:80",
			},
		},
	)
	rules, _, err := api.DesiredRules(context.Background(), []string{"cloudflare.tunnel"}, false)
	require.NoError(t, err)
	assert.Len(t, rules, 1)
}

func TestDesiredRules_multiPrefix(t *testing.T) {
	api := NewFake(Container{
		ID:   "c1",
		Name: "whoami",
		Labels: map[string]string{
			"cloudflare.tunnel.enable":   "true",
			"cloudflare.tunnel.hostname": "whoami.example.com",
			"cloudflare.tunnel.service":  "http://whoami:80",
			"dockflare.enable":           "true",
			"dockflare.hostname":         "whoami.example.com",
			"dockflare.service":          "http://whoami:80",
		},
	})
	rules, issues, err := api.DesiredRules(context.Background(), []string{"cloudflare.tunnel", "dockflare"}, false)
	require.NoError(t, err)
	assert.Len(t, rules, 1)
	// Container appears only once (merged by ID)
	assert.Empty(t, issues)
}

func TestDesiredRules_indexedLabels(t *testing.T) {
	api := NewFake(Container{
		ID:   "c1",
		Name: "multi",
		Labels: map[string]string{
			"cloudflare.tunnel.enable":     "true",
			"cloudflare.tunnel.0.hostname": "app1.example.com",
			"cloudflare.tunnel.0.service":  "http://app1:80",
			"cloudflare.tunnel.1.hostname": "app2.example.com",
			"cloudflare.tunnel.1.service":  "http://app2:80",
		},
	})
	rules, issues, err := api.DesiredRules(context.Background(), []string{"cloudflare.tunnel"}, false)
	require.NoError(t, err)
	assert.Len(t, rules, 2)
	assert.Empty(t, issues)
}

func TestDesiredRules_duplicateKey(t *testing.T) {
	api := NewFake(
		Container{
			ID:   "c1",
			Name: "alpha",
			Labels: map[string]string{
				"cloudflare.tunnel.enable":   "true",
				"cloudflare.tunnel.hostname": "same.example.com",
				"cloudflare.tunnel.service":  "http://alpha:80",
			},
		},
		Container{
			ID:   "c2",
			Name: "beta",
			Labels: map[string]string{
				"cloudflare.tunnel.enable":   "true",
				"cloudflare.tunnel.hostname": "same.example.com",
				"cloudflare.tunnel.service":  "http://beta:80",
			},
		},
	)
	rules, issues, err := api.DesiredRules(context.Background(), []string{"cloudflare.tunnel"}, false)
	require.NoError(t, err)
	assert.Len(t, rules, 1)
	// alpha wins (sorted first)
	assert.Equal(t, "alpha", rules["same.example.com|"].ContainerName)
	assert.Len(t, issues, 1)
	assert.Equal(t, "conflict", issues[0].Kind)
}

func TestDesiredRules_blockedAccess(t *testing.T) {
	api := NewFake(Container{
		ID:   "c1",
		Name: "protected",
		Labels: map[string]string{
			"cloudflare.tunnel.enable":        "true",
			"cloudflare.tunnel.hostname":      "protected.example.com",
			"cloudflare.tunnel.service":       "http://protected:80",
			"cloudflare.tunnel.access.policy": "authenticate",
		},
	})
	rules, issues, err := api.DesiredRules(context.Background(), []string{"cloudflare.tunnel"}, false)
	require.NoError(t, err)
	// Blocked access rules are rejected entirely — no rules published
	assert.Empty(t, rules)
	assert.Len(t, issues, 1)
	assert.Equal(t, "blocked_access_unsupported", issues[0].Kind)
}

func TestDesiredRules_allowUnprotected(t *testing.T) {
	api := NewFake(Container{
		ID:   "c1",
		Name: "protected",
		Labels: map[string]string{
			"cloudflare.tunnel.enable":        "true",
			"cloudflare.tunnel.hostname":      "protected.example.com",
			"cloudflare.tunnel.service":       "http://protected:80",
			"cloudflare.tunnel.access.policy": "authenticate",
		},
	})
	rules, issues, err := api.DesiredRules(context.Background(), []string{"cloudflare.tunnel"}, true)
	require.NoError(t, err)
	assert.Len(t, rules, 1)
	// No blocked issue when allowUnprotected is true
	assert.Empty(t, issues)
}

func TestListContainers_filter(t *testing.T) {
	api := NewFake(
		Container{
			ID:   "c1",
			Name: "whoami",
			Labels: map[string]string{
				"cloudflare.tunnel.enable": "true",
			},
		},
		Container{
			ID:   "c2",
			Name: "other",
			Labels: map[string]string{
				"cloudflare.tunnel.enable": "false",
			},
		},
	)
	containers, err := api.ListContainers(context.Background(), "cloudflare.tunnel.enable=true")
	require.NoError(t, err)
	assert.Len(t, containers, 1)
	assert.Equal(t, "whoami", containers[0].Name)
}
