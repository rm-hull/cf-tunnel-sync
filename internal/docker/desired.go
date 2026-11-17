package docker

import (
	"fmt"
	"sort"

	"github.com/rm-hull/cf-tunnel-sync/internal/labels"
)

// mergeDesiredRules parses labels for each container and merges the rules,
// reporting duplicate keys as "conflict" issues. Containers are sorted by
// (Name, ID) before processing so the winner is deterministic (R6).
func mergeDesiredRules(containers map[string]Container, prefixes []string, allowUnprotected bool) (map[string]labels.Rule, []labels.Issue) {
	// Build a sorted slice of containers for deterministic ordering (R6).
	sorted := make([]Container, 0, len(containers))
	for _, c := range containers {
		sorted = append(sorted, c)
	}
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].Name != sorted[j].Name {
			return sorted[i].Name < sorted[j].Name
		}
		return sorted[i].ID < sorted[j].ID
	})

	var allIssues []labels.Issue
	rules := make(map[string]labels.Rule)

	var parseOpts []labels.ParseOption
	if allowUnprotected {
		parseOpts = append(parseOpts, labels.ParseOptionAllowUnprotectedAccess(true))
	}

	for _, c := range sorted {
		ruleList, issues := labels.Parse(c.Name, c.Labels, prefixes, parseOpts...)
		allIssues = append(allIssues, issues...)
		for _, r := range ruleList {
			key := r.Key()
			if existing, ok := rules[key]; ok {
				allIssues = append(allIssues, labels.Issue{
					Kind:      "conflict",
					Field:     "hostname",
					Container: c.Name,
					Detail:    fmt.Sprintf("duplicate key %q from %s and %s", key, existing.ContainerName, c.Name),
				})
				continue
			}
			rules[key] = r
		}
	}

	return rules, allIssues
}
