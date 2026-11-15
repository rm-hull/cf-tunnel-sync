// Package config provides configuration loading and the Secret type.
package config

import (
	"encoding/json"
	"fmt"
	"log/slog"

	"go.yaml.in/yaml/v3"
)

// Secret is a string-based type that redacts its value in logs, JSON, and YAML output.
type Secret string

// SecretValue is the redacted string shown when a Secret is rendered.
const SecretValue = "[redacted]"

// String implements fmt.Stringer.
func (s Secret) String() string {
	return SecretValue
}

// GoString implements fmt.GoStringer.
func (s Secret) GoString() string {
	return SecretValue
}

// LogValue implements slog.LogValuer.
func (s Secret) LogValue() slog.Value {
	return slog.StringValue(SecretValue)
}

// MarshalJSON implements json.Marshaler.
func (s Secret) MarshalJSON() ([]byte, error) {
	return json.Marshal(SecretValue)
}

// MarshalYAML implements yaml.Marshaler.
func (s Secret) MarshalYAML() (any, error) {
	return SecretValue, nil
}

// UnmarshalJSON implements json.Unmarshaler.
func (s *Secret) UnmarshalJSON(data []byte) error {
	var str string
	if err := json.Unmarshal(data, &str); err != nil {
		return fmt.Errorf("secret: unmarshal json: %w", err)
	}
	*s = Secret(str)
	return nil
}

// UnmarshalYAML implements yaml.Unmarshaler.
func (s *Secret) UnmarshalYAML(node *yaml.Node) error {
	var str string
	if err := node.Decode(&str); err != nil {
		return fmt.Errorf("secret: unmarshal yaml: %w", err)
	}
	*s = Secret(str)
	return nil
}
