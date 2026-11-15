package internal

import (
	"log/slog"
	"sort"

	"github.com/rm-hull/cf-tunnel-sync/internal/labels"
)

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

// SortKeys returns sorted keys for deterministic output.
func SortKeys(rules map[string]labels.Rule) []string {
	keys := make([]string, 0, len(rules))
	for k := range rules {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
