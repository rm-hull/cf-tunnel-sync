package logx_test

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"testing"

	"github.com/rm-hull/cf-tunnel-sync/internal/logx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRingBufferKeepsLastNRecords(t *testing.T) {
	var buf bytes.Buffer
	jsonHandler := slog.NewJSONHandler(&buf, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	})

	capacity := 5
	ring := logx.NewRingBufferHandler(capacity, jsonHandler)
	logger := slog.New(ring)

	// Log more records than capacity
	for i := 0; i < capacity+3; i++ {
		logger.Info("test", "index", i)
	}

	records := ring.Records()
	require.Len(t, records, capacity)

	// Check that the oldest records were dropped
	// The first record should be index 3 (0,1,2 were dropped)
	var firstRecord map[string]any
	require.NoError(t, json.Unmarshal(records[0], &firstRecord))
	idx, ok := firstRecord["index"].(float64)
	require.True(t, ok)
	assert.Equal(t, 3, int(idx))

	// The last record should be index 7
	var lastRecord map[string]any
	require.NoError(t, json.Unmarshal(records[len(records)-1], &lastRecord))
	idx, ok = lastRecord["index"].(float64)
	require.True(t, ok)
	assert.Equal(t, 7, int(idx))
}

func TestRingBufferRecordsContainsRequiredFields(t *testing.T) {
	var buf bytes.Buffer
	jsonHandler := slog.NewJSONHandler(&buf, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	})

	ring := logx.NewRingBufferHandler(10, jsonHandler)
	logger := slog.New(ring)

	logger.Info("hello", "component", "test", "key", "value")

	records := ring.Records()
	require.Len(t, records, 1)

	var record map[string]any
	require.NoError(t, json.Unmarshal(records[0], &record))

	// Check required fields
	requiredFields := []string{"time", "level", "msg"}
	for _, field := range requiredFields {
		assert.Contains(t, record, field, "record should contain field %q", field)
	}

	// Check custom attributes
	assert.Equal(t, "test", record["component"])
	assert.Equal(t, "value", record["key"])
}

func TestRingBufferReturnsCopy(t *testing.T) {
	var buf bytes.Buffer
	jsonHandler := slog.NewJSONHandler(&buf, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	})

	ring := logx.NewRingBufferHandler(10, jsonHandler)
	logger := slog.New(ring)

	logger.Info("test1")
	records1 := ring.Records()
	logger.Info("test2")
	records2 := ring.Records()

	// records1 should still have only 1 record
	assert.Equal(t, 1, len(records1), "records1 should be a copy")
	assert.Equal(t, 2, len(records2))
}

func TestNewJSONLogger(t *testing.T) {
	logger, ring := logx.NewJSONLogger(slog.LevelInfo, 100)

	logger.Info("test message", "key", "value")

	records := ring.Records()
	require.Len(t, records, 1)

	var record map[string]any
	require.NoError(t, json.Unmarshal(records[0], &record))

	assert.Equal(t, "test message", record["msg"])
}

func TestRingBufferWithAttributes(t *testing.T) {
	var buf bytes.Buffer
	jsonHandler := slog.NewJSONHandler(&buf, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	})

	ring := logx.NewRingBufferHandler(10, jsonHandler)
	logger := slog.New(ring)

	// Use WithAttrs
	childLogger := logger.With("service", "cf-tunnel-sync")
	childLogger.Info("started", "port", 5000)

	// Verify the underlying handler received the attributes
	output := buf.String()
	assert.Contains(t, output, "cf-tunnel-sync")
	assert.Contains(t, output, "started")

	// Verify the ring buffer captured the record
	records := ring.Records()
	require.Len(t, records, 1)

	var record map[string]any
	require.NoError(t, json.Unmarshal(records[0], &record))

	assert.Equal(t, "started", record["msg"])
	// port is from the log call, so it should be in the record
	assert.Equal(t, float64(5000), record["port"])
}

func TestRingBufferWithGroup(t *testing.T) {
	var buf bytes.Buffer
	jsonHandler := slog.NewJSONHandler(&buf, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	})

	ring := logx.NewRingBufferHandler(10, jsonHandler)
	logger := slog.New(ring)

	// Use WithGroup
	childLogger := logger.WithGroup("docker")
	childLogger.Info("connected", "host", "unix:///var/run/docker.sock")

	// Verify the underlying handler received the group
	output := buf.String()
	assert.Contains(t, output, "docker")
	assert.Contains(t, output, "connected")

	// Verify the ring buffer captured the record
	records := ring.Records()
	require.Len(t, records, 1)

	var record map[string]any
	require.NoError(t, json.Unmarshal(records[0], &record))

	assert.Equal(t, "connected", record["msg"])
	// host is from the log call, so it should be in the record
	assert.Equal(t, "unix:///var/run/docker.sock", record["host"])
}

func TestRingBufferEmpty(t *testing.T) {
	var buf bytes.Buffer
	jsonHandler := slog.NewJSONHandler(&buf, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	})

	ring := logx.NewRingBufferHandler(10, jsonHandler)
	_ = slog.New(ring)

	records := ring.Records()
	assert.Empty(t, records)
}

func TestRingBufferLevelFiltering(t *testing.T) {
	var buf bytes.Buffer
	jsonHandler := slog.NewJSONHandler(&buf, &slog.HandlerOptions{
		Level: slog.LevelInfo, // Only INFO and above
	})

	ring := logx.NewRingBufferHandler(10, jsonHandler)
	logger := slog.New(ring)

	logger.Debug("debug message")
	logger.Info("info message")

	records := ring.Records()
	// Debug should be filtered out
	require.Len(t, records, 1)

	var record map[string]any
	require.NoError(t, json.Unmarshal(records[0], &record))
	assert.Equal(t, "info message", record["msg"])
}

func TestRingBufferJSONOutput(t *testing.T) {
	var buf bytes.Buffer
	jsonHandler := slog.NewJSONHandler(&buf, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	})

	ring := logx.NewRingBufferHandler(10, jsonHandler)
	logger := slog.New(ring)

	logger.Info("test")

	// The underlying handler should also receive the record
	output := buf.String()
	assert.Contains(t, output, "test")
}
