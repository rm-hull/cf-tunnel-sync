package config

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestSchemaGeneration verifies that config.schema.json matches the generated schema.
func TestSchemaGeneration(t *testing.T) {
	schema := Schema()

	// Generate JSON
	generated, err := json.MarshalIndent(schema, "", "  ")
	require.NoError(t, err)

	// Read committed file
	committed, err := os.ReadFile("../../config.schema.json")
	require.NoError(t, err)

	// Compare
	require.Equal(t, string(generated), string(committed),
		"config.schema.json is out of date. Run: go run cmd/cf-tunnel-sync/main.go config schema > config.schema.json")
}
