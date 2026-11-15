package logx_test

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/rm-hull/cf-tunnel-sync/internal/logx"
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
	if len(records) != capacity {
		t.Fatalf("expected %d records, got %d", capacity, len(records))
	}

	// Check that the oldest records were dropped
	// The first record should be index 3 (0,1,2 were dropped)
	var firstRecord map[string]any
	if err := json.Unmarshal(records[0], &firstRecord); err != nil {
		t.Fatalf("failed to unmarshal first record: %v", err)
	}
	if idx, ok := firstRecord["index"].(float64); !ok || int(idx) != 3 {
		t.Errorf("first record index = %v, want 3", firstRecord["index"])
	}

	// The last record should be index 7
	var lastRecord map[string]any
	if err := json.Unmarshal(records[len(records)-1], &lastRecord); err != nil {
		t.Fatalf("failed to unmarshal last record: %v", err)
	}
	if idx, ok := lastRecord["index"].(float64); !ok || int(idx) != 7 {
		t.Errorf("last record index = %v, want 7", lastRecord["index"])
	}
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
	if len(records) != 1 {
		t.Fatalf("expected 1 record, got %d", len(records))
	}

	var record map[string]any
	if err := json.Unmarshal(records[0], &record); err != nil {
		t.Fatalf("failed to unmarshal record: %v", err)
	}

	// Check required fields
	requiredFields := []string{"time", "level", "msg"}
	for _, field := range requiredFields {
		if _, ok := record[field]; !ok {
			t.Errorf("missing required field %q in record", field)
		}
	}

	// Check custom attributes
	if record["component"] != "test" {
		t.Errorf("component = %v, want 'test'", record["component"])
	}
	if record["key"] != "value" {
		t.Errorf("key = %v, want 'value'", record["key"])
	}
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
	if len(records1) != 1 {
		t.Errorf("records1 length = %d, want 1 (should be a copy)", len(records1))
	}
	if len(records2) != 2 {
		t.Errorf("records2 length = %d, want 2", len(records2))
	}
}

func TestNewJSONLogger(t *testing.T) {
	logger, ring := logx.NewJSONLogger(slog.LevelInfo, 100)

	logger.Info("test message", "key", "value")

	records := ring.Records()
	if len(records) != 1 {
		t.Fatalf("expected 1 record, got %d", len(records))
	}

	var record map[string]any
	if err := json.Unmarshal(records[0], &record); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	if record["msg"] != "test message" {
		t.Errorf("msg = %v, want 'test message'", record["msg"])
	}
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
	if !strings.Contains(output, "cf-tunnel-sync") {
		t.Errorf("underlying handler output missing 'cf-tunnel-sync': %s", output)
	}
	if !strings.Contains(output, "started") {
		t.Errorf("underlying handler output missing 'started': %s", output)
	}

	// Verify the ring buffer captured the record
	records := ring.Records()
	if len(records) != 1 {
		t.Fatalf("expected 1 record, got %d", len(records))
	}

	var record map[string]any
	if err := json.Unmarshal(records[0], &record); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	if record["msg"] != "started" {
		t.Errorf("msg = %v, want 'started'", record["msg"])
	}
	// port is from the log call, so it should be in the record
	if record["port"] != float64(5000) {
		t.Errorf("port = %v, want 5000", record["port"])
	}
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
	if !strings.Contains(output, "docker") {
		t.Errorf("underlying handler output missing 'docker': %s", output)
	}
	if !strings.Contains(output, "connected") {
		t.Errorf("underlying handler output missing 'connected': %s", output)
	}

	// Verify the ring buffer captured the record
	records := ring.Records()
	if len(records) != 1 {
		t.Fatalf("expected 1 record, got %d", len(records))
	}

	var record map[string]any
	if err := json.Unmarshal(records[0], &record); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	if record["msg"] != "connected" {
		t.Errorf("msg = %v, want 'connected'", record["msg"])
	}
	// host is from the log call, so it should be in the record
	if record["host"] != "unix:///var/run/docker.sock" {
		t.Errorf("host = %v, want 'unix:///var/run/docker.sock'", record["host"])
	}
}

func TestRingBufferEmpty(t *testing.T) {
	var buf bytes.Buffer
	jsonHandler := slog.NewJSONHandler(&buf, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	})

	ring := logx.NewRingBufferHandler(10, jsonHandler)
	_ = slog.New(ring)

	records := ring.Records()
	if len(records) != 0 {
		t.Errorf("expected 0 records, got %d", len(records))
	}
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
	if len(records) != 1 {
		t.Errorf("expected 1 record (debug filtered), got %d", len(records))
	}

	if len(records) > 0 {
		var record map[string]any
		if err := json.Unmarshal(records[0], &record); err != nil {
			t.Fatalf("failed to unmarshal: %v", err)
		}
		if record["msg"] != "info message" {
			t.Errorf("msg = %v, want 'info message'", record["msg"])
		}
	}
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
	if !strings.Contains(output, "test") {
		t.Errorf("underlying handler output missing 'test': %s", output)
	}
}
