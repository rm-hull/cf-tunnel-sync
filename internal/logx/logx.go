// Package logx provides logging utilities with a ring buffer for recent log entries.
package logx

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"sync"
)

// sharedBuffer holds the shared state for ring buffer handlers.
type sharedBuffer struct {
	mu       sync.Mutex
	records  []json.RawMessage
	capacity int
}

// RingBufferHandler wraps an slog.Handler and stores the last N records.
type RingBufferHandler struct {
	buf  *sharedBuffer
	next slog.Handler
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
	entries := make([]slog.Attr, 0, r.NumAttrs())
	r.Attrs(func(a slog.Attr) bool {
		entries = append(entries, a)
		return true
	})

	record := map[string]any{
		"time":  r.Time,
		"level": r.Level,
		"msg":   r.Message,
	}
	for _, a := range entries {
		record[a.Key] = a.Value.Any()
	}

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
	return &RingBufferHandler{
		buf:  h.buf,
		next: h.next.WithAttrs(attrs),
	}
}

// WithGroup implements slog.Handler.
func (h *RingBufferHandler) WithGroup(name string) slog.Handler {
	return &RingBufferHandler{
		buf:  h.buf,
		next: h.next.WithGroup(name),
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

// NewJSONLogger creates a new slog.Logger with JSON output to stdout and a ring buffer.
func NewJSONLogger(level slog.Level, ringCapacity int) (*slog.Logger, *RingBufferHandler) {
	jsonHandler := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: level,
	})

	ring := NewRingBufferHandler(ringCapacity, jsonHandler)
	logger := slog.New(ring)
	return logger, ring
}
