// Package logx provides logging utilities with a ring buffer for recent log entries.
package logx

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

// sharedBuffer holds the shared state for ring buffer handlers.
type sharedBuffer struct {
	mu       sync.Mutex
	records  []json.RawMessage
	capacity int
}

// RingBufferHandler wraps an slog.Handler and stores the last N records.
type RingBufferHandler struct {
	buf    *sharedBuffer
	next   slog.Handler
	attrs  []slog.Attr // accumulated attributes from WithAttrs
	groups []string    // accumulated group names from WithGroup
}

// NewRingBufferHandler creates a handler that stores the last capacity records.
func NewRingBufferHandler(capacity int, next slog.Handler) *RingBufferHandler {
	return &RingBufferHandler{
		buf: &sharedBuffer{
			records:  make([]json.RawMessage, 0, capacity),
			capacity: capacity,
		},
		next: next,
	}
}

// Enabled implements slog.Handler.
func (h *RingBufferHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.next.Enabled(ctx, level)
}

// Handle implements slog.Handler.
func (h *RingBufferHandler) Handle(ctx context.Context, r slog.Record) error {
	// Include accumulated attributes from WithAttrs
	record := map[string]any{
		"time":  r.Time,
		"level": r.Level,
		"msg":   r.Message,
	}

	// Add pre-set attributes from With()
	for _, a := range h.attrs {
		record[a.Key] = resolveValue(a.Value)
	}

	// Add attributes from the record itself
	r.Attrs(func(a slog.Attr) bool {
		record[a.Key] = resolveValue(a.Value)
		return true
	})

	data, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("ring buffer: marshal error: %w", err)
	}

	h.buf.mu.Lock()
	if len(h.buf.records) >= h.buf.capacity {
		h.buf.records = h.buf.records[1:]
	}
	h.buf.records = append(h.buf.records, data)
	h.buf.mu.Unlock()

	return h.next.Handle(ctx, r)
}

// WithAttrs implements slog.Handler.
func (h *RingBufferHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	newAttrs := make([]slog.Attr, len(h.attrs)+len(attrs))
	copy(newAttrs, h.attrs)
	copy(newAttrs[len(h.attrs):], attrs)
	return &RingBufferHandler{
		buf:    h.buf,
		next:   h.next.WithAttrs(attrs),
		attrs:  newAttrs,
		groups: h.groups,
	}
}

// WithGroup implements slog.Handler.
func (h *RingBufferHandler) WithGroup(name string) slog.Handler {
	newGroups := make([]string, len(h.groups)+1)
	copy(newGroups, h.groups)
	newGroups[len(h.groups)] = name
	return &RingBufferHandler{
		buf:    h.buf,
		next:   h.next.WithGroup(name),
		attrs:  h.attrs,
		groups: newGroups,
	}
}

// Records returns a copy of the stored records.
func (h *RingBufferHandler) Records() []json.RawMessage {
	h.buf.mu.Lock()
	defer h.buf.mu.Unlock()
	result := make([]json.RawMessage, len(h.buf.records))
	copy(result, h.buf.records)
	return result
}

// shortenPath strips the project root from an absolute file path so only the
// repository-relative path remains (e.g. "internal/logx/logx.go:42").
func shortenPath(file string) string {
	root := getProjectRoot()
	if root == "" {
		return filepath.Base(file)
	}
	if after, ok := strings.CutPrefix(file, root); ok {
		rel := after
		rel = strings.TrimPrefix(rel, string(filepath.Separator))
		return rel
	}
	return filepath.Base(file)
}

var (
	projectRootOnce sync.Once
	projectRoot     string
)

func getProjectRoot() string {
	projectRootOnce.Do(func() {
		// Use runtime.Caller to get the absolute path of this function,
		// which works for both compiled binaries and go run.
		_, file, _, _ := runtime.Caller(0)
		// This file lives in internal/logx/logx.go, so strip that suffix
		// to get the project root.
		dir := filepath.Dir(file)
		// Walk up from internal/logx to find the project root.
		// The project root is two levels up: internal/logx -> internal -> root.
		projectRoot = filepath.Dir(filepath.Dir(dir))
	})
	return projectRoot
}

// ReplaceAttr is a slog.HandlerOptions.ReplaceAttr function that
// formats time.Duration values as human-readable strings (e.g. "2.318s")
// and shortens source file paths.
func ReplaceAttr(_ []string, a slog.Attr) slog.Attr {
	if a.Key == slog.SourceKey {
		if source, ok := a.Value.Any().(*slog.Source); ok {
			file := shortenPath(source.File)
			return slog.String("caller", fmt.Sprintf("%s:%d", file, source.Line))
		}
	}

	if a.Value.Kind() == slog.KindDuration {
		return slog.String(a.Key, a.Value.Duration().String())
	}

	if a.Value.Kind() != slog.KindAny {
		return a
	}

	val := a.Value.Any()
	processed := processValue(val)
	return slog.Any(a.Key, processed)
}

// resolveValue converts an slog.Value to a Go value suitable for JSON marshaling.
// It properly expands slog groups into a flat map[string]any.
func resolveValue(v slog.Value) any {
	switch v.Kind() {
	case slog.KindGroup:
		// This case handles groups from WithGroup() which produce KindGroup.
		// v.Group() returns []slog.Attr for KindGroup.
		result := make(map[string]any)
		for _, a := range v.Any().([]slog.Attr) {
			result[a.Key] = resolveValue(a.Value)
		}
		return result
	case slog.KindDuration:
		return v.Duration().String()
	case slog.KindTime:
		return v.Time()
	case slog.KindString:
		return v.String()
	case slog.KindInt64:
		return v.Int64()
	case slog.KindUint64:
		return v.Uint64()
	case slog.KindFloat64:
		return v.Float64()
	case slog.KindBool:
		return v.Bool()
	default:
		// slog.KindAny — resolve the inner value recursively
		return processValue(v.Any())
	}
}

// processValue recursively converts time.Duration values to their string
// representation so they appear as human-readable durations in log output.
// It also expands []slog.Attr (from slog.Group) into a flat map.
func processValue(v any) any {
	switch val := v.(type) {
	case time.Duration:
		return val.String()
	case error:
		return val.Error()
	case []slog.Attr:
		// Expand slog.Group into a flat map
		result := make(map[string]any, len(val))
		for _, a := range val {
			result[a.Key] = resolveValue(a.Value)
		}
		return result
	case map[string]any:
		newMap := make(map[string]any, len(val))
		for k, v := range val {
			newMap[k] = processValue(v)
		}
		return newMap
	case []any:
		newSlice := make([]any, len(val))
		for i, v := range val {
			newSlice[i] = processValue(v)
		}
		return newSlice
	default:
		return v
	}
}

// NewJSONLogger creates a new slog.Logger with JSON output to stdout and a ring buffer.
// Call site information (file, line, function) is included in every log entry.
// Duration values are automatically formatted as human-readable strings.
func NewJSONLogger(level slog.Level, ringCapacity int) (*slog.Logger, *RingBufferHandler) {
	jsonHandler := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level:       level,
		AddSource:   true,
		ReplaceAttr: ReplaceAttr,
	})

	ring := NewRingBufferHandler(ringCapacity, jsonHandler)
	logger := slog.New(ring)
	return logger, ring
}

// ParseLogLevel converts a config log level string to slog.Level.
func ParseLogLevel(level string) slog.Level {
	switch level {
	case "DEBUG":
		return slog.LevelDebug
	case "WARN":
		return slog.LevelWarn
	case "ERROR":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
