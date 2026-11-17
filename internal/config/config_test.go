package config_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/rm-hull/cf-tunnel-sync/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"
)

func TestDefaults(t *testing.T) {
	cfg := config.Defaults()

	assert.Equal(t, ":8080", cfg.Server.Listen)
	assert.Equal(t, "INFO", cfg.Server.LogLevel)
	assert.Equal(t, "json", cfg.Server.LogFormat)
	assert.Equal(t, "/data/state.json", cfg.Server.StatePath)
	assert.Equal(t, 15*time.Second, cfg.Server.StatusRefresh)
	assert.False(t, cfg.Server.Metrics)
	assert.False(t, cfg.Server.DryRun)

	assert.Equal(t, "homelab", cfg.Tunnel.Name)
	assert.Equal(t, "cloudflare/cloudflared:latest", cfg.Tunnel.Cloudflared.Image)
	assert.Equal(t, "cloudflare-net", cfg.Tunnel.Cloudflared.Network)
	assert.Equal(t, 2000, cfg.Tunnel.Cloudflared.MetricsPort)

	assert.Equal(t, []string{"cloudflare.tunnel", "dockflare"}, cfg.Labels.Prefixes)
	assert.False(t, cfg.Labels.AllowUnprotectedAccess)

	assert.Equal(t, 8*time.Hour, cfg.Reconcile.GracePeriod)
	assert.Equal(t, 5*time.Minute, cfg.Reconcile.ResyncInterval)
	assert.Equal(t, 30*time.Second, cfg.Reconcile.Debounce)

	assert.False(t, cfg.DNS.OverwriteConflicts)
}

func TestDiscovery_order(t *testing.T) {
	// Test that --config flag takes precedence
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")
	require.NoError(t, os.WriteFile(configPath, []byte("server:\n  listen: \":8080\"\n"), 0o600))

	path, searched, err := config.Discover(configPath)
	require.NoError(t, err)
	assert.Equal(t, configPath, path)
	assert.Empty(t, searched)
}

func TestDiscovery_no_file(t *testing.T) {
	// When no config exists, return empty path with searched paths
	path, searched, err := config.Discover("")
	require.NoError(t, err)
	assert.Empty(t, path)
	assert.NotEmpty(t, searched)
}

func TestEnvSubstitution(t *testing.T) {
	t.Run("simple variable", func(t *testing.T) {
		tmpDir := t.TempDir()
		configPath := filepath.Join(tmpDir, "config.yaml")
		require.NoError(t, os.WriteFile(configPath, []byte(`
cloudflare:
  api_token: ${TEST_TOKEN}
  account_id: ${TEST_ACCOUNT}
`), 0o600))

		t.Setenv("TEST_TOKEN", "mytoken")
		t.Setenv("TEST_ACCOUNT", "myaccount")

		cfg, err := config.Load(configPath)
		require.NoError(t, err)
		assert.Equal(t, config.Secret("mytoken"), cfg.Cloudflare.APIToken)
		assert.Equal(t, "myaccount", cfg.Cloudflare.AccountID)
	})

	t.Run("variable with default", func(t *testing.T) {
		tmpDir := t.TempDir()
		configPath := filepath.Join(tmpDir, "config.yaml")
		require.NoError(t, os.WriteFile(configPath, []byte(`
cloudflare:
  api_token: ${TEST_TOKEN}
  account_id: ${TEST_ACCOUNT}
  zone_id: ${TEST_ZONE:-default-zone}
`), 0o600))

		t.Setenv("TEST_TOKEN", "mytoken")
		t.Setenv("TEST_ACCOUNT", "myaccount")
		// Don't set TEST_ZONE

		cfg, err := config.Load(configPath)
		require.NoError(t, err)
		assert.Equal(t, "default-zone", cfg.Cloudflare.ZoneID)
	})

	t.Run("dollar escape", func(t *testing.T) {
		tmpDir := t.TempDir()
		configPath := filepath.Join(tmpDir, "config.yaml")
		require.NoError(t, os.WriteFile(configPath, []byte(`
cloudflare:
  api_token: ${TEST_TOKEN}
  account_id: ${TEST_ACCOUNT}
tunnel:
  name: $$homelab
`), 0o600))

		t.Setenv("TEST_TOKEN", "mytoken")
		t.Setenv("TEST_ACCOUNT", "myaccount")

		cfg, err := config.Load(configPath)
		require.NoError(t, err)
		assert.Equal(t, "$homelab", cfg.Tunnel.Name)
	})

	t.Run("unset variable without default", func(t *testing.T) {
		tmpDir := t.TempDir()
		configPath := filepath.Join(tmpDir, "config.yaml")
		require.NoError(t, os.WriteFile(configPath, []byte(`
cloudflare:
  api_token: ${UNSET_VAR}
  account_id: ${TEST_ACCOUNT}
`), 0o600))

		t.Setenv("TEST_ACCOUNT", "myaccount")
		// Don't set UNSET_VAR

		_, err := config.Load(configPath)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "environment variable is not set")
	})

	t.Run("env value with special chars", func(t *testing.T) {
		tmpDir := t.TempDir()
		configPath := filepath.Join(tmpDir, "config.yaml")
		require.NoError(t, os.WriteFile(configPath, []byte(`
cloudflare:
  api_token: ${TEST_TOKEN}
  account_id: ${TEST_ACCOUNT}
`), 0o600))

		t.Setenv("TEST_TOKEN", "value: with # special\nchars")
		t.Setenv("TEST_ACCOUNT", "myaccount")

		cfg, err := config.Load(configPath)
		require.NoError(t, err)
		assert.Equal(t, config.Secret("value: with # special\nchars"), cfg.Cloudflare.APIToken)
	})
}

func TestUnknownKey(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")
	require.NoError(t, os.WriteFile(configPath, []byte(`
cloudflare:
  api_token: ${TEST_TOKEN}
  account_id: ${TEST_ACCOUNT}
unknown_key: value
`), 0o600))

	t.Setenv("TEST_TOKEN", "mytoken")
	t.Setenv("TEST_ACCOUNT", "myaccount")

	_, err := config.Load(configPath)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown key")
}

func TestSchemaKey(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")
	require.NoError(t, os.WriteFile(configPath, []byte(`
$schema: https://example.com/schema.json
cloudflare:
  api_token: ${TEST_TOKEN}
  account_id: ${TEST_ACCOUNT}
`), 0o600))

	t.Setenv("TEST_TOKEN", "mytoken")
	t.Setenv("TEST_ACCOUNT", "myaccount")

	_, err := config.Load(configPath)
	require.NoError(t, err)
}

func TestValidation(t *testing.T) {
	t.Run("missing api_token", func(t *testing.T) {
		cfg := config.Defaults()
		cfg.Cloudflare.AccountID = "test"
		err := config.Validate(&cfg, nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "cloudflare.api_token")
	})

	t.Run("missing account_id", func(t *testing.T) {
		cfg := config.Defaults()
		cfg.Cloudflare.APIToken = "test"
		err := config.Validate(&cfg, nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "cloudflare.account_id")
	})

	t.Run("missing tunnel name without external_id", func(t *testing.T) {
		cfg := config.Defaults()
		cfg.Cloudflare.APIToken = "test"
		cfg.Cloudflare.AccountID = "test"
		cfg.Tunnel.Name = ""
		err := config.Validate(&cfg, nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "tunnel.name")
	})

	t.Run("tunnel name not required with external_id", func(t *testing.T) {
		cfg := config.Defaults()
		cfg.Cloudflare.APIToken = "test"
		cfg.Cloudflare.AccountID = "test"
		cfg.Tunnel.Name = ""
		cfg.Tunnel.ExternalID = "external-tunnel-id"
		err := config.Validate(&cfg, nil)
		require.NoError(t, err)
	})

	t.Run("empty prefixes", func(t *testing.T) {
		cfg := config.Defaults()
		cfg.Cloudflare.APIToken = "test"
		cfg.Cloudflare.AccountID = "test"
		cfg.Labels.Prefixes = []string{}
		err := config.Validate(&cfg, nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "labels.prefixes")
	})

	t.Run("duplicate prefixes", func(t *testing.T) {
		cfg := config.Defaults()
		cfg.Cloudflare.APIToken = "test"
		cfg.Cloudflare.AccountID = "test"
		cfg.Labels.Prefixes = []string{"cloudflare.tunnel", "cloudflare.tunnel"}
		err := config.Validate(&cfg, nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "duplicate")
	})

	t.Run("prefix with leading dot", func(t *testing.T) {
		cfg := config.Defaults()
		cfg.Cloudflare.APIToken = "test"
		cfg.Cloudflare.AccountID = "test"
		cfg.Labels.Prefixes = []string{".cloudflare.tunnel"}
		err := config.Validate(&cfg, nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "leading/trailing")
	})

	t.Run("invalid log level", func(t *testing.T) {
		cfg := config.Defaults()
		cfg.Cloudflare.APIToken = "test"
		cfg.Cloudflare.AccountID = "test"
		cfg.Server.LogLevel = "INVALID"
		err := config.Validate(&cfg, nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "server.log_level")
	})

	t.Run("invalid log format", func(t *testing.T) {
		cfg := config.Defaults()
		cfg.Cloudflare.APIToken = "test"
		cfg.Cloudflare.AccountID = "test"
		cfg.Server.LogFormat = "xml"
		err := config.Validate(&cfg, nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "server.log_format")
	})

	t.Run("invalid listen address", func(t *testing.T) {
		cfg := config.Defaults()
		cfg.Cloudflare.APIToken = "test"
		cfg.Cloudflare.AccountID = "test"
		cfg.Server.Listen = "invalid"
		err := config.Validate(&cfg, nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "server.listen")
	})

	t.Run("negative grace period", func(t *testing.T) {
		cfg := config.Defaults()
		cfg.Cloudflare.APIToken = "test"
		cfg.Cloudflare.AccountID = "test"
		cfg.Reconcile.GracePeriod = -1 * time.Hour
		err := config.Validate(&cfg, nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "reconcile.grace_period")
	})

	t.Run("resync interval too short", func(t *testing.T) {
		cfg := config.Defaults()
		cfg.Cloudflare.APIToken = "test"
		cfg.Cloudflare.AccountID = "test"
		cfg.Reconcile.ResyncInterval = 5 * time.Second
		err := config.Validate(&cfg, nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "reconcile.resync_interval")
	})

	t.Run("invalid metrics port", func(t *testing.T) {
		cfg := config.Defaults()
		cfg.Cloudflare.APIToken = "test"
		cfg.Cloudflare.AccountID = "test"
		cfg.Tunnel.Cloudflared.MetricsPort = 0
		err := config.Validate(&cfg, nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "tunnel.cloudflared.metrics_port")
	})

	t.Run("multiple errors reported together", func(t *testing.T) {
		cfg := config.Defaults()
		cfg.Cloudflare.APIToken = ""
		cfg.Cloudflare.AccountID = ""
		cfg.Server.LogLevel = "INVALID"
		err := config.Validate(&cfg, nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "cloudflare.api_token")
		assert.Contains(t, err.Error(), "cloudflare.account_id")
		assert.Contains(t, err.Error(), "server.log_level")
	})
}

func TestEnvOverrides(t *testing.T) {
	t.Run("LOG_LEVEL override", func(t *testing.T) {
		tmpDir := t.TempDir()
		configPath := filepath.Join(tmpDir, "config.yaml")
		require.NoError(t, os.WriteFile(configPath, []byte(`
cloudflare:
  api_token: ${TEST_TOKEN}
  account_id: ${TEST_ACCOUNT}
`), 0o600))

		t.Setenv("TEST_TOKEN", "mytoken")
		t.Setenv("TEST_ACCOUNT", "myaccount")
		t.Setenv("LOG_LEVEL", "DEBUG")

		cfg, err := config.Load(configPath)
		require.NoError(t, err)
		assert.Equal(t, "DEBUG", cfg.Server.LogLevel)
	})

	t.Run("DRY_RUN override", func(t *testing.T) {
		tmpDir := t.TempDir()
		configPath := filepath.Join(tmpDir, "config.yaml")
		require.NoError(t, os.WriteFile(configPath, []byte(`
cloudflare:
  api_token: ${TEST_TOKEN}
  account_id: ${TEST_ACCOUNT}
`), 0o600))

		t.Setenv("TEST_TOKEN", "mytoken")
		t.Setenv("TEST_ACCOUNT", "myaccount")
		t.Setenv("DRY_RUN", "true")

		cfg, err := config.Load(configPath)
		require.NoError(t, err)
		assert.True(t, cfg.Server.DryRun)
	})

	t.Run("invalid DRY_RUN", func(t *testing.T) {
		tmpDir := t.TempDir()
		configPath := filepath.Join(tmpDir, "config.yaml")
		require.NoError(t, os.WriteFile(configPath, []byte(`
cloudflare:
  api_token: ${TEST_TOKEN}
  account_id: ${TEST_ACCOUNT}
`), 0o600))

		t.Setenv("TEST_TOKEN", "mytoken")
		t.Setenv("TEST_ACCOUNT", "myaccount")
		t.Setenv("DRY_RUN", "invalid")

		_, err := config.Load(configPath)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "DRY_RUN")
	})

	t.Run("LISTEN_ADDR override", func(t *testing.T) {
		tmpDir := t.TempDir()
		configPath := filepath.Join(tmpDir, "config.yaml")
		require.NoError(t, os.WriteFile(configPath, []byte(`
cloudflare:
  api_token: ${TEST_TOKEN}
  account_id: ${TEST_ACCOUNT}
`), 0o600))

		t.Setenv("TEST_TOKEN", "mytoken")
		t.Setenv("TEST_ACCOUNT", "myaccount")
		t.Setenv("LISTEN_ADDR", ":8080")

		cfg, err := config.Load(configPath)
		require.NoError(t, err)
		assert.Equal(t, ":8080", cfg.Server.Listen)
	})

	t.Run("DOCKER_HOST override", func(t *testing.T) {
		tmpDir := t.TempDir()
		configPath := filepath.Join(tmpDir, "config.yaml")
		require.NoError(t, os.WriteFile(configPath, []byte(`
cloudflare:
  api_token: ${TEST_TOKEN}
  account_id: ${TEST_ACCOUNT}
`), 0o600))

		t.Setenv("TEST_TOKEN", "mytoken")
		t.Setenv("TEST_ACCOUNT", "myaccount")
		t.Setenv("DOCKER_HOST", "unix:///var/run/docker.sock")

		cfg, err := config.Load(configPath)
		require.NoError(t, err)
		assert.Equal(t, "unix:///var/run/docker.sock", cfg.Docker.Host)
	})
}

func TestSecretRedaction(t *testing.T) {
	cfg := config.Defaults()
	cfg.Cloudflare.APIToken = "my-secret-token"

	// JSON marshalling
	data, err := json.Marshal(cfg)
	require.NoError(t, err)
	assert.NotContains(t, string(data), "my-secret-token")
	assert.Contains(t, string(data), "[redacted]")

	// YAML marshalling
	data, err = yaml.Marshal(cfg)
	require.NoError(t, err)
	assert.NotContains(t, string(data), "my-secret-token")
	assert.Contains(t, string(data), "[redacted]")
}

func TestSchemaGeneration(t *testing.T) {
	schema := config.Schema()

	// Schema should have additionalProperties set (not nil means it's explicitly set)
	// When AllowAdditionalProperties is false in Reflector, AdditionalProperties should be nil or false
	assert.NotNil(t, schema.AdditionalProperties)

	// Schema should have properties
	assert.NotNil(t, schema.Properties)

	// Check that key properties exist
	_, present := schema.Properties.Get("server")
	assert.True(t, present)
	_, present = schema.Properties.Get("cloudflare")
	assert.True(t, present)
	_, present = schema.Properties.Get("tunnel")
	assert.True(t, present)
	_, present = schema.Properties.Get("docker")
	assert.True(t, present)
	_, present = schema.Properties.Get("labels")
	assert.True(t, present)
	_, present = schema.Properties.Get("reconcile")
	assert.True(t, present)
	_, present = schema.Properties.Get("dns")
	assert.True(t, present)
}

func TestExampleConfig(t *testing.T) {
	// Test that config.example.yaml loads and validates
	t.Setenv("CF_API_TOKEN", "test-token")
	t.Setenv("CF_ACCOUNT_ID", "test-account")
	t.Setenv("CF_ZONE_ID", "test-zone")

	cfg, err := config.Load("../../config.example.yaml")
	require.NoError(t, err)
	assert.Equal(t, "test-tunnel", cfg.Tunnel.Name)
	assert.Equal(t, "test-token", string(cfg.Cloudflare.APIToken))
}
