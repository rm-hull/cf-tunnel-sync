package labels

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Recognised field names (without prefix/index).
var knownFields = map[string]struct{}{
	"enable":                      {},
	"hostname":                    {},
	"service":                     {},
	"path":                        {},
	"zonename":                    {},
	"no_tls_verify":               {},
	"originsrvname":               {},
	"access.policy":               {},
	"access.group":                {},
	"access.email":                {},
	"access.custom_rules":         {},
	"access.name":                 {},
	"access.session_duration":     {},
	"access.app_launcher_visible": {},
	"access.allowed_idps":         {},
}

// Allowed service schemes.
var allowedSchemes = map[string]struct{}{
	"http":     {},
	"https":    {},
	"tcp":      {},
	"ssh":      {},
	"rdp":      {},
	"smb":      {},
	"unix":     {},
	"unix+tls": {},
}

// hostnameRe validates RFC 1123 hostnames with optional leading "*.".
var hostnameRe = regexp.MustCompile(`^(\*\.)?([a-zA-Z0-9]([a-zA-Z0-9\-]{0,61}[a-zA-Z0-9])?\.)+[a-zA-Z]{2,}$`)

// prefixData holds parsed labels for a single prefix.
type prefixData struct {
	prefix     string
	enabled    bool
	indexed    map[int]map[string]string
	nonIndexed map[string]string
}

// Rule represents a single tunnel routing rule derived from container labels.
type Rule struct {
	Hostname         string
	Path             string
	Service          string
	NoTLSVerify      bool
	OriginServerName string
	ZoneName         string
	ContainerName    string
	SourcePrefix     string
	SourceIndex      *int // nil for non-indexed rules
}

// Key returns the deterministic rule key: lower(hostname) + "|" + path.
// If path is empty, the key is just the lowercased hostname.
func (r Rule) Key() string {
	if r.Path == "" {
		return strings.ToLower(r.Hostname)
	}
	return strings.ToLower(r.Hostname) + "|" + r.Path
}

// Issue represents a problem found during label parsing.
type Issue struct {
	Kind      string // "invalid", "label_conflict", "unknown_label", "blocked_access_unsupported", "conflict"
	Field     string // the label field name that caused the issue
	Container string // container name (for cross-container issues)
	Detail    string // human-readable detail message
}

// ContainerLabels pairs a container name with its labels.
type ContainerLabels struct {
	Name   string
	Labels map[string]string
}

// ParseOptions holds configuration for the parser.
type ParseOptions struct {
	AllowUnprotectedAccess bool
}

// ParseOption is a functional option for Parse.
type ParseOption func(*ParseOptions)

// ParseOptionAllowUnprotectedAccess controls whether access-protected rules are blocked.
func ParseOptionAllowUnprotectedAccess(allow bool) ParseOption {
	return func(o *ParseOptions) {
		o.AllowUnprotectedAccess = allow
	}
}

// IsEnabled returns true if the value represents a truthy enable label.
// It accepts "true", "1", "yes" (case-insensitive). (R10)
func IsEnabled(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "true", "1", "yes":
		return true
	}
	return false
}

// parseOpts applies functional options to default options.
func parseOpts(opts ...ParseOption) ParseOptions {
	o := ParseOptions{}
	for _, fn := range opts {
		fn(&o)
	}
	return o
}

// Parse extracts tunnel rules from a single container's labels.
func Parse(containerName string, labels map[string]string, prefixes []string, opts ...ParseOption) ([]Rule, []Issue) {
	return parseOne(containerName, labels, prefixes, parseOpts(opts...))
}

// ParseAll extracts tunnel rules from multiple containers and resolves cross-container conflicts.
func ParseAll(containers []ContainerLabels, prefixes []string, opts ...ParseOption) ([]Rule, []Issue) {
	o := parseOpts(opts...)
	var allRules []Rule
	var allIssues []Issue

	sorted := make([]ContainerLabels, len(containers))
	copy(sorted, containers)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].Name < sorted[j].Name
	})

	seenKeys := make(map[string]int)

	for _, c := range sorted {
		rules, issues := parseOne(c.Name, c.Labels, prefixes, o)
		for _, r := range rules {
			key := r.Key()
			if idx, exists := seenKeys[key]; exists {
				allIssues = append(allIssues, Issue{
					Kind:      "conflict",
					Container: c.Name,
					Detail:    fmt.Sprintf("duplicate rule key %q (owned by %s)", key, allRules[idx].ContainerName),
				})
			} else {
				seenKeys[key] = len(allRules)
				allRules = append(allRules, r)
			}
		}
		allIssues = append(allIssues, issues...)
	}

	return allRules, allIssues
}

// parseOne parses labels for a single container.
func parseOne(containerName string, labels map[string]string, prefixes []string, opt ParseOptions) ([]Rule, []Issue) {
	var issues []Issue

	var pdList []prefixData

	for _, prefix := range prefixes {
		pd := prefixData{
			prefix:     prefix,
			indexed:    make(map[int]map[string]string),
			nonIndexed: make(map[string]string),
		}

		prefixDot := prefix + "."

		for key, value := range labels {
			if !strings.HasPrefix(key, prefixDot) {
				continue
			}

			remainder := key[len(prefixDot):]
			parts := strings.SplitN(remainder, ".", 2)

			if len(parts) == 1 {
				field := parts[0]
				if field == "enable" {
					pd.enabled = parseBool(value) == trueBool
				}
				pd.nonIndexed[field] = value
			} else {
				// Could be indexed (prefix.N.field) or dotted field (prefix.access.group)
				firstPart := parts[0]
				secondPart := parts[1]

				// Check if it's an access.* label (dotted field name)
				if firstPart == "access" {
					pd.nonIndexed["access."+secondPart] = value
					continue
				}

				if idx, err := strconv.Atoi(firstPart); err == nil && idx >= 0 {
					if pd.indexed[idx] == nil {
						pd.indexed[idx] = make(map[string]string)
					}
					pd.indexed[idx][secondPart] = value
				}
			}
		}

		// Check for unknown labels
		for field := range pd.nonIndexed {
			if _, known := knownFields[field]; !known && !strings.HasPrefix(field, "access.") {
				issues = append(issues, Issue{
					Kind:   "unknown_label",
					Field:  field,
					Detail: fmt.Sprintf("unknown label %s.%s", prefix, field),
				})
			}
		}
		for _, fields := range pd.indexed {
			for field := range fields {
				if _, known := knownFields[field]; !known && !strings.HasPrefix(field, "access.") {
					issues = append(issues, Issue{
						Kind:   "unknown_label",
						Field:  field,
						Detail: fmt.Sprintf("unknown label %s.%s", prefix, field),
					})
				}
			}
		}

		pdList = append(pdList, pd)
	}

	// Check if any prefix has enable=true
	enabled := false
	for _, pd := range pdList {
		if pd.enabled {
			enabled = true
			break
		}
	}
	if !enabled {
		return nil, issues
	}

	// Collect all indices across all prefixes
	allIndices := make(map[int]struct{})
	for _, pd := range pdList {
		for idx := range pd.indexed {
			allIndices[idx] = struct{}{}
		}
	}

	var rules []Rule

	if len(allIndices) == 0 {
		rule, ruleIssues := buildRule(containerName, pdList, nil, opt)
		issues = append(issues, ruleIssues...)
		if rule != nil {
			rules = append(rules, *rule)
		}
	} else {
		indices := make([]int, 0, len(allIndices))
		for idx := range allIndices {
			indices = append(indices, idx)
		}
		sort.Ints(indices)

		for _, idx := range indices {
			pIdx := idx
			rule, ruleIssues := buildRule(containerName, pdList, &pIdx, opt)
			issues = append(issues, ruleIssues...)
			if rule != nil {
				rules = append(rules, *rule)
			}
		}
	}

	return rules, issues
}

// buildRule constructs a single rule by merging fields across prefixes in priority order.
func buildRule(containerName string, pdList []prefixData, index *int, opt ParseOptions) (*Rule, []Issue) {
	var issues []Issue

	type fieldValue struct {
		value  string
		prefix string
	}

	getField := func(field string) fieldValue {
		var val fieldValue
		for _, pd := range pdList {
			var found bool
			var v string

			// Try indexed first
			if index != nil {
				if fields, ok := pd.indexed[*index]; ok {
					if vv, ok := fields[field]; ok {
						v = vv
						found = true
					}
				}
			}
			// Fall back to non-indexed (defaults)
			if !found {
				if vv, ok := pd.nonIndexed[field]; ok {
					v = vv
					found = true
				}
			}
			if found {
				if val.value == "" {
					val = fieldValue{value: v, prefix: pd.prefix}
				} else if val.prefix != pd.prefix && val.value != v {
					issues = append(issues, Issue{
						Kind:   "label_conflict",
						Field:  field,
						Detail: fmt.Sprintf("%s from %s conflicts with %s from %s", field, val.prefix, v, pd.prefix),
					})
				}
				// Don't break — continue checking remaining prefixes for conflicts
			}
		}
		return val
	}

	// Determine source prefix
	sourcePrefix := ""
	for _, pd := range pdList {
		if index != nil {
			if _, ok := pd.indexed[*index]; ok && len(pd.indexed[*index]) > 0 {
				sourcePrefix = pd.prefix
				break
			}
		}
		if len(pd.nonIndexed) > 0 {
			sourcePrefix = pd.prefix
			break
		}
	}

	// Gather fields
	hostnameVal := getField("hostname")
	serviceVal := getField("service")
	pathVal := getField("path")
	zonenameVal := getField("zonename")
	noTLSVal := getField("no_tls_verify")
	originSrvVal := getField("originsrvname")

	// Check access labels for blocking
	hasBlockedAccess := false
	for _, pd := range pdList {
		accessLabels := collectAccessLabels(pd, index)
		for field, value := range accessLabels {
			switch field {
			case "policy":
				if value == "authenticate" {
					hasBlockedAccess = true
				}
			case "group", "email", "custom_rules":
				hasBlockedAccess = true
			}
		}
	}

	if hasBlockedAccess && !opt.AllowUnprotectedAccess {
		return nil, append(issues, Issue{
			Kind:   "blocked_access_unsupported",
			Detail: "access labels require Access application management (not implemented)",
		})
	}

	// Validate required fields
	if hostnameVal.value == "" {
		return nil, append(issues, Issue{
			Kind:   "invalid",
			Field:  "hostname",
			Detail: "hostname is required",
		})
	}
	if serviceVal.value == "" {
		return nil, append(issues, Issue{
			Kind:   "invalid",
			Field:  "service",
			Detail: "service is required",
		})
	}

	// Validate hostname format
	if !hostnameRe.MatchString(hostnameVal.value) {
		return nil, append(issues, Issue{
			Kind:   "invalid",
			Field:  "hostname",
			Detail: fmt.Sprintf("invalid hostname %q: must be RFC 1123 with optional leading *.", hostnameVal.value),
		})
	}

	// Validate path (R8): must be valid regex if present
	if pathVal.value != "" {
		if _, err := regexp.Compile(pathVal.value); err != nil {
			return nil, append(issues, Issue{
				Kind:   "invalid",
				Field:  "path",
				Detail: fmt.Sprintf("invalid path regex %q: %v", pathVal.value, err),
			})
		}
	}

	// Validate service scheme
	if !validateService(serviceVal.value) {
		return nil, append(issues, Issue{
			Kind:   "invalid",
			Field:  "service",
			Detail: fmt.Sprintf("invalid service %q: unsupported scheme", serviceVal.value),
		})
	}

	// Validate bool fields
	if noTLSVal.value != "" {
		b := parseBool(noTLSVal.value)
		if b == unknownBool {
			return nil, append(issues, Issue{
				Kind:   "invalid",
				Field:  "no_tls_verify",
				Detail: fmt.Sprintf("invalid bool value %q for no_tls_verify", noTLSVal.value),
			})
		}
	}

	rule := &Rule{
		Hostname:         hostnameVal.value,
		Path:             pathVal.value,
		Service:          serviceVal.value,
		NoTLSVerify:      parseBool(noTLSVal.value) == trueBool,
		OriginServerName: originSrvVal.value,
		ZoneName:         zonenameVal.value,
		ContainerName:    containerName,
		SourcePrefix:     sourcePrefix,
		SourceIndex:      index,
	}

	return rule, issues
}

// collectAccessLabels gathers access.* labels from a prefix data set.
func collectAccessLabels(pd prefixData, index *int) map[string]string {
	result := make(map[string]string)

	if index != nil {
		if fields, ok := pd.indexed[*index]; ok {
			for k, v := range fields {
				if strings.HasPrefix(k, "access.") {
					result[k[7:]] = v
				}
			}
		}
	}
	for k, v := range pd.nonIndexed {
		if strings.HasPrefix(k, "access.") {
			result[k[7:]] = v
		}
	}
	return result
}

type boolResult int

const (
	unknownBool boolResult = iota
	falseBool
	trueBool
)

func parseBool(s string) boolResult {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "true", "1":
		return trueBool
	case "false", "0":
		return falseBool
	default:
		return unknownBool
	}
}

func validateService(s string) bool {
	if s == "" {
		return false
	}
	if strings.HasPrefix(s, "http_status:") {
		return true
	}
	if idx := strings.Index(s, "://"); idx > 0 {
		scheme := s[:idx]
		if _, ok := allowedSchemes[scheme]; !ok {
			return false
		}
		// R8: service URL must have a host (non-empty after ://)
		rest := s[idx+3:]
		if rest == "" {
			return false
		}
		// Strip port if present
		host := rest
		if colonIdx := strings.Index(rest, ":"); colonIdx > 0 {
			host = rest[:colonIdx]
		} else if slashIdx := strings.Index(rest, "/"); slashIdx > 0 {
			host = rest[:slashIdx]
		}
		return host != ""
	}
	return false
}
