package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/rm-hull/cf-tunnel-sync/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEnvFileLoading(t *testing.T) {
	// Each subtest uses unique env var names to avoid cross-test pollution
	// since os.Setenv is global and persistent across tests.
	t.Run("loads .env alongside config", func(t *testing.T) {
		tmpDir := t.TempDir()
		configPath := filepath.Join(tmpDir, "config.yaml")
		envPath := filepath.Join(tmpDir, ".env")

		require.NoError(t, os.WriteFile(envPath, []byte("ENV_TOKEN_A=env-file-token\nENV_ACCOUNT_A=env-file-account\n"), 0o600))
		require.NoError(t, os.WriteFile(configPath, []byte(`
cloudflare:
  api_token: ${ENV_TOKEN_A}
  account_id: ${ENV_ACCOUNT_A}
`), 0o600))

		cfg, err := config.Load(configPath)
		require.NoError(t, err)
		assert.Equal(t, config.Secret("env-file-token"), cfg.Cloudflare.APIToken)
		assert.Equal(t, "env-file-account", cfg.Cloudflare.AccountID)
	})

	t.Run("loads .env from config directory", func(t *testing.T) {
		tmpDir := t.TempDir()
		configPath := filepath.Join(tmpDir, "config.yaml")
		envPath := filepath.Join(tmpDir, ".env")

		require.NoError(t, os.WriteFile(envPath, []byte("ENV_TOKEN_B=dir-token\nENV_ACCOUNT_B=dir-account\n"), 0o600))
		require.NoError(t, os.WriteFile(configPath, []byte(`
cloudflare:
  api_token: ${ENV_TOKEN_B}
  account_id: ${ENV_ACCOUNT_B}
`), 0o600))

		cfg, err := config.Load(configPath)
		require.NoError(t, err)
		assert.Equal(t, config.Secret("dir-token"), cfg.Cloudflare.APIToken)
	})

	t.Run("existing env var takes precedence over .env", func(t *testing.T) {
		tmpDir := t.TempDir()
		configPath := filepath.Join(tmpDir, "config.yaml")
		envPath := filepath.Join(tmpDir, ".env")

		require.NoError(t, os.WriteFile(envPath, []byte("ENV_TOKEN_C=env-file-token\nENV_ACCOUNT_C=env-file-account\n"), 0o600))
		require.NoError(t, os.WriteFile(configPath, []byte(`
cloudflare:
  api_token: ${ENV_TOKEN_C}
  account_id: ${ENV_ACCOUNT_C}
`), 0o600))

		t.Setenv("ENV_TOKEN_C", "explicit-token")

		cfg, err := config.Load(configPath)
		require.NoError(t, err)
		assert.Equal(t, config.Secret("explicit-token"), cfg.Cloudflare.APIToken)
		assert.Equal(t, "env-file-account", cfg.Cloudflare.AccountID)
	})

	t.Run("missing .env is ignored", func(t *testing.T) {
		tmpDir := t.TempDir()
		configPath := filepath.Join(tmpDir, "config.yaml")
		// No .env file

		require.NoError(t, os.WriteFile(configPath, []byte(`
cloudflare:
  api_token: ${ENV_TOKEN_D}
  account_id: ${ENV_ACCOUNT_D}
`), 0o600))

		t.Setenv("ENV_TOKEN_D", "direct-token")
		t.Setenv("ENV_ACCOUNT_D", "direct-account")

		cfg, err := config.Load(configPath)
		require.NoError(t, err)
		assert.Equal(t, config.Secret("direct-token"), cfg.Cloudflare.APIToken)
	})

	t.Run("env file with comments and blank lines", func(t *testing.T) {
		tmpDir := t.TempDir()
		configPath := filepath.Join(tmpDir, "config.yaml")
		envPath := filepath.Join(tmpDir, ".env")

		require.NoError(t, os.WriteFile(envPath, []byte(`# This is a comment
ENV_TOKEN_E=token-from-env

# Another comment
ENV_ACCOUNT_E=account-from-env
`), 0o600))
		require.NoError(t, os.WriteFile(configPath, []byte(`
cloudflare:
  api_token: ${ENV_TOKEN_E}
  account_id: ${ENV_ACCOUNT_E}
`), 0o600))

		cfg, err := config.Load(configPath)
		require.NoError(t, err)
		assert.Equal(t, config.Secret("token-from-env"), cfg.Cloudflare.APIToken)
		assert.Equal(t, "account-from-env", cfg.Cloudflare.AccountID)
	})

	t.Run("env file with quoted values", func(t *testing.T) {
		tmpDir := t.TempDir()
		configPath := filepath.Join(tmpDir, "config.yaml")
		envPath := filepath.Join(tmpDir, ".env")

		require.NoError(t, os.WriteFile(envPath, []byte(`ENV_TOKEN_F="quoted-token"
ENV_ACCOUNT_F='single-quoted-account'
`), 0o600))
		require.NoError(t, os.WriteFile(configPath, []byte(`
cloudflare:
  api_token: ${ENV_TOKEN_F}
  account_id: ${ENV_ACCOUNT_F}
`), 0o600))

		cfg, err := config.Load(configPath)
		require.NoError(t, err)
		assert.Equal(t, config.Secret("quoted-token"), cfg.Cloudflare.APIToken)
		assert.Equal(t, "single-quoted-account", cfg.Cloudflare.AccountID)
	})
}
