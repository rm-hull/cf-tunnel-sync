package config_test

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/rm-hull/cf-tunnel-sync/internal/config"
	"go.yaml.in/yaml/v3"
)

func TestSecretString(t *testing.T) {
	secret := config.Secret("my-secret-token")
	if got := secret.String(); got != config.SecretValue {
		t.Errorf("String() = %q, want %q", got, config.SecretValue)
	}
}

func TestSecretGoString(t *testing.T) {
	secret := config.Secret("my-secret-token")
	if got := secret.GoString(); got != config.SecretValue {
		t.Errorf("GoString() = %q, want %q", got, config.SecretValue)
	}
}

func TestSecretLogValue(t *testing.T) {
	secret := config.Secret("my-secret-token")
	val := secret.LogValue()
	if got := val.Any(); fmt.Sprintf("%v", got) != config.SecretValue {
		t.Errorf("LogValue() = %v, want %q", got, config.SecretValue)
	}
}

func TestSecretMarshalJSON(t *testing.T) {
	secret := config.Secret("my-secret-token")
	data, err := json.Marshal(secret)
	if err != nil {
		t.Fatalf("MarshalJSON failed: %v", err)
	}
	if got := string(data); got != `"[redacted]"` {
		t.Errorf("MarshalJSON() = %s, want %q", got, `"[redacted]"`)
	}
}

func TestSecretMarshalYAML(t *testing.T) {
	secret := config.Secret("my-secret-token")
	data, err := yaml.Marshal(secret)
	if err != nil {
		t.Fatalf("MarshalYAML failed: %v", err)
	}
	// YAML marshals strings with quotes, so we expect '[redacted]'
	got := strings.TrimSpace(string(data))
	if got != "'[redacted]'" {
		t.Errorf("MarshalYAML() = %q, want %q", got, "'[redacted]'")
	}
}

func TestSecretUnmarshalJSON(t *testing.T) {
	var secret config.Secret
	if err := json.Unmarshal([]byte(`"test-token"`), &secret); err != nil {
		t.Fatalf("UnmarshalJSON failed: %v", err)
	}
	if string(secret) != "test-token" {
		t.Errorf("UnmarshalJSON: underlying value = %q, want %q", string(secret), "test-token")
	}
	// But String() should still redact
	if secret.String() != config.SecretValue {
		t.Errorf("String() after unmarshal = %q, want %q", secret.String(), config.SecretValue)
	}
}

func TestSecretUnmarshalYAML(t *testing.T) {
	var secret config.Secret
	if err := yaml.Unmarshal([]byte("test-token"), &secret); err != nil {
		t.Fatalf("UnmarshalYAML failed: %v", err)
	}
	if string(secret) != "test-token" {
		t.Errorf("UnmarshalYAML: underlying value = %q, want %q", string(secret), "test-token")
	}
}

func TestSecretInStructJSON(t *testing.T) {
	type Config struct {
		Name  string        `json:"name"`
		Token config.Secret `json:"token"`
	}

	cfg := Config{Name: "test", Token: config.Secret("secret123")}
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}

	if strings.Contains(string(data), "secret123") {
		t.Errorf("secret value leaked in JSON: %s", string(data))
	}
	if !strings.Contains(string(data), "[redacted]") {
		t.Errorf("redacted marker missing in JSON: %s", string(data))
	}
}

func TestSecretInStructYAML(t *testing.T) {
	type Config struct {
		Name  string        `yaml:"name"`
		Token config.Secret `yaml:"token"`
	}

	cfg := Config{Name: "test", Token: config.Secret("secret123")}
	data, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}

	if strings.Contains(string(data), "secret123") {
		t.Errorf("secret value leaked in YAML: %s", string(data))
	}
	if !strings.Contains(string(data), "[redacted]") {
		t.Errorf("redacted marker missing in YAML: %s", string(data))
	}
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

	if strings.Contains(output, "my-secret-token") {
		t.Errorf("secret value leaked in slog output: %s", output)
	}
	if !strings.Contains(output, "[redacted]") {
		t.Errorf("redacted marker missing in slog output: %s", output)
	}
}

func TestSecretWithPrintf(t *testing.T) {
	secret := config.Secret("my-secret-token")

	// Test %v
	outV := fmt.Sprintf("%v", secret)
	if strings.Contains(outV, "my-secret-token") {
		t.Errorf("%%v leaked secret: %s", outV)
	}

	// Test %+v
	outPlusV := fmt.Sprintf("%+v", secret)
	if strings.Contains(outPlusV, "my-secret-token") {
		t.Errorf("%%+v leaked secret: %s", outPlusV)
	}

	// Test %#v
	outHashV := fmt.Sprintf("%#v", secret)
	if strings.Contains(outHashV, "my-secret-token") {
		t.Errorf("%%#v leaked secret: %s", outHashV)
	}
}
