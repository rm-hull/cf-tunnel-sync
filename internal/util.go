package internal

import (
	"sort"

	"github.com/rm-hull/cf-tunnel-sync/internal/labels"
)

// SortKeys returns sorted keys for deterministic output.
func SortKeys(rules map[string]labels.Rule) []string {
	keys := make([]string, 0, len(rules))
	for k := range rules {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
