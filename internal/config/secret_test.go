package config_test

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/rm-hull/cf-tunnel-sync/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"
)

func TestSecretString(t *testing.T) {
	secret := config.Secret("my-secret-token")
	assert.Equal(t, config.SecretValue, secret.String())
}

func TestSecretGoString(t *testing.T) {
	secret := config.Secret("my-secret-token")
	assert.Equal(t, config.SecretValue, secret.GoString())
}

func TestSecretLogValue(t *testing.T) {
	secret := config.Secret("my-secret-token")
	val := secret.LogValue()
	assert.Equal(t, config.SecretValue, fmt.Sprintf("%v", val.Any()))
}

func TestSecretMarshalJSON(t *testing.T) {
	secret := config.Secret("my-secret-token")
	data, err := json.Marshal(secret)
	require.NoError(t, err)
	assert.Equal(t, `"[redacted]"`, string(data))
}

func TestSecretMarshalYAML(t *testing.T) {
	secret := config.Secret("my-secret-token")
	data, err := yaml.Marshal(secret)
	require.NoError(t, err)
	// YAML marshals strings with quotes, so we expect '[redacted]'
	assert.Equal(t, "'[redacted]'", strings.TrimSpace(string(data)))
}

func TestSecretUnmarshalJSON(t *testing.T) {
	var secret config.Secret
	require.NoError(t, json.Unmarshal([]byte(`"test-token"`), &secret))
	assert.Equal(t, "test-token", string(secret))
	// But String() should still redact
	assert.Equal(t, config.SecretValue, secret.String())
}

func TestSecretUnmarshalYAML(t *testing.T) {
	var secret config.Secret
	require.NoError(t, yaml.Unmarshal([]byte("test-token"), &secret))
	assert.Equal(t, "test-token", string(secret))
}

func TestSecretInStructJSON(t *testing.T) {
	type Config struct {
		Name  string        `json:"name"`
		Token config.Secret `json:"token"`
	}

	cfg := Config{Name: "test", Token: config.Secret("secret123")}
	data, err := json.Marshal(cfg)
	require.NoError(t, err)

	assert.NotContains(t, string(data), "secret123")
	assert.Contains(t, string(data), "[redacted]")
}

func TestSecretInStructYAML(t *testing.T) {
	type Config struct {
		Name  string        `yaml:"name"`
		Token config.Secret `yaml:"token"`
	}

	cfg := Config{Name: "test", Token: config.Secret("secret123")}
	data, err := yaml.Marshal(cfg)
	require.NoError(t, err)

	assert.NotContains(t, string(data), "secret123")
	assert.Contains(t, string(data), "[redacted]")
}

func TestSecretInSlog(t *testing.T) {
	secret := config.Secret("my-secret-token")

	var buf strings.Builder
	handler := slog.NewTextHandler(&buf, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})
	logger := slog.New(handler)

	logger.Info("test", "token", secret)
	output := buf.String()

	assert.NotContains(t, output, "my-secret-token")
	assert.Contains(t, output, "[redacted]")
}

func TestSecretWithPrintf(t *testing.T) {
	secret := config.Secret("my-secret-token")

	// Test %v
	outV := fmt.Sprintf("%v", secret)
	assert.NotContains(t, outV, "my-secret-token")

	// Test %+v
	outPlusV := fmt.Sprintf("%+v", secret)
	assert.NotContains(t, outPlusV, "my-secret-token")

	// Test %#v
	outHashV := fmt.Sprintf("%#v", secret)
	assert.NotContains(t, outHashV, "my-secret-token")
}
