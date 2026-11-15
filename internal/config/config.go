// Package config provides configuration loading and validation.
package config

import (
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/joho/godotenv"
	"go.yaml.in/yaml/v3"
)

// Config holds the full application configuration.
type Config struct {
	Server     ServerConfig     `yaml:"server" json:"server"`
	Cloudflare CloudflareConfig `yaml:"cloudflare" json:"cloudflare"`
	Tunnel     TunnelConfig     `yaml:"tunnel" json:"tunnel"`
	Docker     DockerConfig     `yaml:"docker" json:"docker"`
	Labels     LabelsConfig     `yaml:"labels" json:"labels"`
	Reconcile  ReconcileConfig  `yaml:"reconcile" json:"reconcile"`
	DNS        DNSConfig        `yaml:"dns" json:"dns"`
}

// ServerConfig holds server-related settings.
type ServerConfig struct {
	Listen        string `yaml:"listen" json:"listen" jsonschema:"description=Status page listen address"`
	LogLevel      string `yaml:"log_level" json:"log_level" jsonschema:"description=Log level (DEBUG|INFO|WARN|ERROR)"`
	LogFormat     string `yaml:"log_format" json:"log_format" jsonschema:"description=Log format (json|text)"`
	StatePath     string `yaml:"state_path" json:"state_path" jsonschema:"description=Path to state file"`
	StatusRefresh string `yaml:"status_refresh" json:"status_refresh" jsonschema:"description=Meta-refresh interval; 0 disables"`
	Metrics       bool   `yaml:"metrics" json:"metrics" jsonschema:"description=Expose /metrics endpoint"`
	DryRun        bool   `yaml:"dry_run" json:"dry_run" jsonschema:"description=Log actions without making changes"`
}

// CloudflareConfig holds Cloudflare API settings.
type CloudflareConfig struct {
	APIToken  Secret `yaml:"api_token" json:"api_token" jsonschema:"description=Cloudflare API token (REQUIRED)"`
	AccountID string `yaml:"account_id" json:"account_id" jsonschema:"description=Cloudflare account ID (REQUIRED)"`
	ZoneID    string `yaml:"zone_id" json:"zone_id" jsonschema:"description=Optional fallback zone ID"`
}

// TunnelConfig holds tunnel-related settings.
type TunnelConfig struct {
	Name        string            `yaml:"name" json:"name" jsonschema:"description=Tunnel name (REQUIRED unless external_id is set)"`
	ExternalID  string            `yaml:"external_id" json:"external_id" jsonschema:"description=Use externally-run cloudflared"`
	Cloudflared CloudflaredConfig `yaml:"cloudflared" json:"cloudflared"`
}

// CloudflaredConfig holds cloudflared container settings.
type CloudflaredConfig struct {
	ContainerName string `yaml:"container_name" json:"container_name" jsonschema:"description=Container name for cloudflared"`
	Image         string `yaml:"image" json:"image" jsonschema:"description=Cloudflared Docker image"`
	Network       string `yaml:"network" json:"network" jsonschema:"description=Docker network for cloudflared"`
	MetricsPort   int    `yaml:"metrics_port" json:"metrics_port" jsonschema:"description=Metrics port for cloudflared"`
}

// DockerConfig holds Docker API settings.
type DockerConfig struct {
	Host string `yaml:"host" json:"host" jsonschema:"description=Docker host (default: DOCKER_HOST / platform socket)"`
}

// LabelsConfig holds label parsing settings.
type LabelsConfig struct {
	Prefixes               []string `yaml:"prefixes" json:"prefixes" jsonschema:"description=Label prefixes to watch"`
	AllowUnprotectedAccess bool     `yaml:"allow_unprotected_access" json:"allow_unprotected_access" jsonschema:"description=Allow rules with access labels"`
}

// ReconcileConfig holds reconciliation settings.
type ReconcileConfig struct {
	GracePeriod    string `yaml:"grace_period" json:"grace_period" jsonschema:"description=Removal delay after a rule disappears"`
	ResyncInterval string `yaml:"resync_interval" json:"resync_interval" jsonschema:"description=Full resync cadence"`
	Debounce       string `yaml:"debounce" json:"debounce" jsonschema:"description=Debounce interval for triggers"`
}

// DNSConfig holds DNS settings.
type DNSConfig struct {
	OverwriteConflicts bool `yaml:"overwrite_conflicts" json:"overwrite_conflicts" jsonschema:"description=Overwrite conflicting DNS records"`
}

// Defaults returns a Config with default values.
func Defaults() Config {
	return Config{
		Server: ServerConfig{
			Listen:        ":5000",
			LogLevel:      "INFO",
			LogFormat:     "json",
			StatePath:     "/data/state.json",
			StatusRefresh: "15s",
			Metrics:       false,
			DryRun:        false,
		},
		Cloudflare: CloudflareConfig{
			APIToken:  "",
			AccountID: "",
			ZoneID:    "",
		},
		Tunnel: TunnelConfig{
			Name:       "homelab",
			ExternalID: "",
			Cloudflared: CloudflaredConfig{
				ContainerName: "",
				Image:         "cloudflare/cloudflared:latest",
				Network:       "cloudflare-net",
				MetricsPort:   2000,
			},
		},
		Docker: DockerConfig{
			Host: "",
		},
		Labels: LabelsConfig{
			Prefixes:               []string{"cloudflare.tunnel", "dockflare"},
			AllowUnprotectedAccess: false,
		},
		Reconcile: ReconcileConfig{
			GracePeriod:    "8h",
			ResyncInterval: "5m",
			Debounce:       "2s",
		},
		DNS: DNSConfig{
			OverwriteConflicts: false,
		},
	}
}

// Discover finds the config file path.
func Discover(flagPath string) (string, []string, error) {
	searched := []string{}

	// 1. --config flag
	if flagPath != "" {
		return flagPath, searched, nil
	}

	// 2. ./config.yaml, then ./config.yml
	for _, name := range []string{"config.yaml", "config.yml"} {
		if _, err := os.Stat(name); err == nil {
			return name, searched, nil
		}
		searched = append(searched, name)
	}

	// 3. $XDG_CONFIG_DIRS
	if xdgDirs := os.Getenv("XDG_CONFIG_DIRS"); xdgDirs != "" {
		for _, dir := range strings.Split(xdgDirs, ":") {
			path := filepath.Join(dir, "cf-tunnel-sync", "config.yaml")
			if _, err := os.Stat(filepath.Clean(path)); err == nil { //nolint:gosec // path is constructed from safe components
				return path, searched, nil
			}
			searched = append(searched, path)
		}
	} else {
		// Default XDG_CONFIG_DIRS
		path := filepath.Join("/etc/xdg", "cf-tunnel-sync", "config.yaml")
		if _, err := os.Stat(filepath.Clean(path)); err == nil { //nolint:gosec // path is constructed from safe components
			return path, searched, nil
		}
		searched = append(searched, path)
	}

	// 4. $XDG_CONFIG_HOME
	if xdgHome := os.Getenv("XDG_CONFIG_HOME"); xdgHome != "" {
		path := filepath.Join(xdgHome, "cf-tunnel-sync", "config.yaml")
		if _, err := os.Stat(filepath.Clean(path)); err == nil { //nolint:gosec // path is constructed from safe components
			return path, searched, nil
		}
		searched = append(searched, path)
	} else {
		// Default XDG_CONFIG_HOME
		if u, err := user.Current(); err == nil {
			path := filepath.Join(u.HomeDir, ".config", "cf-tunnel-sync", "config.yaml")
			if _, err := os.Stat(filepath.Clean(path)); err == nil { //nolint:gosec // path is constructed from safe components
				return path, searched, nil
			}
			searched = append(searched, path)
		}
	}

	return "", searched, nil
}

// Load loads and validates the configuration.
func Load(flagPath string) (*Config, error) {
	path, searched, err := Discover(flagPath)
	if err != nil {
		return nil, err
	}

	cfg := Defaults()

	if path != "" {
		// Load .env file from the same directory as the config
		if err := loadEnvFile(filepath.Dir(path)); err != nil {
			return nil, err
		}

		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read config %s: %w", path, err)
		}

		// Parse YAML
		var raw map[string]any
		if err := yaml.Unmarshal(data, &raw); err != nil {
			return nil, fmt.Errorf("parse config: %w", err)
		}

		// Check for unknown keys
		if err := checkUnknownKeys(raw); err != nil {
			return nil, err
		}

		// Apply env substitution to scalar values
		substituted, err := substituteEnv(data)
		if err != nil {
			return nil, err
		}

		// Remove $schema if present (it's a special key)
		if raw["$schema"] != nil {
			delete(raw, "$schema")
		}

		// Decode into config - use strict mode but allow $schema
		decoder := yaml.NewDecoder(strings.NewReader(string(substituted)))
		decoder.KnownFields(true)
		if err := decoder.Decode(&cfg); err != nil {
			// If the error is about $schema, ignore it and try again
			if !strings.Contains(err.Error(), "$schema") {
				return nil, fmt.Errorf("decode config: %w", err)
			}
			// Re-decode without strict mode for $schema
			decoder = yaml.NewDecoder(strings.NewReader(string(substituted)))
			if err := decoder.Decode(&cfg); err != nil {
				return nil, fmt.Errorf("decode config: %w", err)
			}
		}
	}

	// Apply environment overrides
	if err := applyEnvOverrides(&cfg); err != nil {
		return nil, err
	}

	// Validate
	if err := Validate(&cfg, searched); err != nil {
		return nil, err
	}

	return &cfg, nil
}

// Validate checks the configuration and returns all errors.
func Validate(cfg *Config, searchedPaths []string) error {
	var errs []string

	// Cloudflare validation
	if cfg.Cloudflare.APIToken == "" {
		errs = append(errs, "cloudflare.api_token: required")
	}
	if cfg.Cloudflare.AccountID == "" {
		errs = append(errs, "cloudflare.account_id: required")
	}

	// Tunnel validation
	if cfg.Tunnel.Name == "" && cfg.Tunnel.ExternalID == "" {
		errs = append(errs, "tunnel.name: required unless tunnel.external_id is set")
	}

	// Labels validation
	if len(cfg.Labels.Prefixes) == 0 {
		errs = append(errs, "labels.prefixes: must not be empty")
	} else {
		seen := make(map[string]bool)
		for _, p := range cfg.Labels.Prefixes {
			if seen[p] {
				errs = append(errs, fmt.Sprintf("labels.prefixes: duplicate prefix %q", p))
			}
			seen[p] = true
			if strings.HasPrefix(p, ".") || strings.HasSuffix(p, ".") {
				errs = append(errs, fmt.Sprintf("labels.prefixes: prefix %q must not have leading/trailing '.'", p))
			}
		}
	}

	// Server validation
	validLevels := map[string]bool{"DEBUG": true, "INFO": true, "WARN": true, "ERROR": true}
	if !validLevels[cfg.Server.LogLevel] {
		errs = append(errs, fmt.Sprintf("server.log_level: must be one of DEBUG, INFO, WARN, ERROR (got %q)", cfg.Server.LogLevel))
	}

	validFormats := map[string]bool{"json": true, "text": true}
	if !validFormats[cfg.Server.LogFormat] {
		errs = append(errs, fmt.Sprintf("server.log_format: must be one of json, text (got %q)", cfg.Server.LogFormat))
	}

	// Parse listen address
	if _, err := parseHostPort(cfg.Server.Listen); err != nil {
		errs = append(errs, fmt.Sprintf("server.listen: %v", err))
	}

	// Duration validation
	if d, err := time.ParseDuration(cfg.Reconcile.GracePeriod); err != nil {
		errs = append(errs, fmt.Sprintf("reconcile.grace_period: invalid duration: %v", err))
	} else if d < 0 {
		errs = append(errs, "reconcile.grace_period: must be >= 0")
	}

	if d, err := time.ParseDuration(cfg.Reconcile.ResyncInterval); err != nil {
		errs = append(errs, fmt.Sprintf("reconcile.resync_interval: invalid duration: %v", err))
	} else if d < 10*time.Second {
		errs = append(errs, "reconcile.resync_interval: must be >= 10s")
	}

	if _, err := time.ParseDuration(cfg.Reconcile.Debounce); err != nil {
		errs = append(errs, fmt.Sprintf("reconcile.debounce: invalid duration: %v", err))
	}

	// Metrics port validation
	if cfg.Tunnel.Cloudflared.MetricsPort < 1 || cfg.Tunnel.Cloudflared.MetricsPort > 65535 {
		errs = append(errs, "tunnel.cloudflared.metrics_port: must be 1-65535")
	}

	if len(errs) > 0 {
		if len(searchedPaths) > 0 {
			return fmt.Errorf("validation errors:\n%s\n\nsearched paths: %s", strings.Join(errs, "\n"), strings.Join(searchedPaths, ", "))
		}
		return fmt.Errorf("validation errors:\n%s", strings.Join(errs, "\n"))
	}

	return nil
}

// parseHostPort validates a host:port string.
func parseHostPort(s string) (string, error) {
	if s == "" {
		return "", fmt.Errorf("empty address")
	}
	// Simple check: should end with :port
	lastColon := strings.LastIndex(s, ":")
	if lastColon < 0 {
		return "", fmt.Errorf("missing port")
	}
	port := s[lastColon+1:]
	if port == "" {
		return "", fmt.Errorf("empty port")
	}
	// Check port is numeric
	for _, c := range port {
		if c < '0' || c > '9' {
			return "", fmt.Errorf("invalid port: %s", port)
		}
	}
	return s, nil
}

// substituteEnv replaces ${VAR}, ${VAR:-default}, and $$ in YAML content.
func substituteEnv(data []byte) ([]byte, error) {
	// Parse YAML to find scalar strings
	var node yaml.Node
	if err := yaml.Unmarshal(data, &node); err != nil {
		return nil, err
	}

	// Process scalar nodes
	if err := substituteNode(&node, ""); err != nil {
		return nil, err
	}

	// Marshal back
	result, err := yaml.Marshal(&node)
	if err != nil {
		return nil, err
	}

	return result, nil
}

// substituteNode recursively processes YAML nodes.
func substituteNode(node *yaml.Node, keyPath string) error {
	switch node.Kind {
	case yaml.DocumentNode:
		// Process document children
		for _, child := range node.Content {
			if err := substituteNode(child, keyPath); err != nil {
				return err
			}
		}
	case yaml.MappingNode:
		for i := 0; i < len(node.Content); i += 2 {
			keyNode := node.Content[i]
			valueNode := node.Content[i+1]
			childPath := keyPath + "." + keyNode.Value
			if keyPath == "" {
				childPath = keyNode.Value
			}
			if err := substituteNode(valueNode, childPath); err != nil {
				return err
			}
		}
	case yaml.SequenceNode:
		for i, item := range node.Content {
			childPath := fmt.Sprintf("%s[%d]", keyPath, i)
			if err := substituteNode(item, childPath); err != nil {
				return err
			}
		}
	case yaml.ScalarNode:
		if node.Tag == "!!str" {
			replaced, err := expandEnv(node.Value, keyPath)
			if err != nil {
				return err
			}
			node.Value = replaced
		}
	}
	return nil
}

// expandEnv expands environment variables in a string.
func expandEnv(s, keyPath string) (string, error) {
	// First, handle $$ escape
	s = strings.ReplaceAll(s, "$$", "\x00")

	re := regexp.MustCompile(`\$\{([^}]+)\}`)
	result := re.ReplaceAllStringFunc(s, func(match string) string {
		inner := match[2 : len(match)-1] // Strip ${ and }

		// Check for default value
		if before, after, ok := strings.Cut(inner, ":-"); ok {
			varName := before
			defaultValue := after
			if val, ok := os.LookupEnv(varName); ok {
				return val
			}
			return defaultValue
		}

		// No default
		if val, ok := os.LookupEnv(inner); ok {
			return val
		}

		// Return error marker
		return fmt.Sprintf("\x01ERROR:%s\x01", keyPath)
	})

	// Check for errors
	if idx := strings.Index(result, "\x01ERROR:"); idx >= 0 {
		end := strings.Index(result[idx+7:], "\x01")
		if end >= 0 {
			keyPath = result[idx+7 : idx+7+end]
			return "", fmt.Errorf("%s: environment variable is not set", keyPath)
		}
	}

	// Restore $$
	result = strings.ReplaceAll(result, "\x00", "$")

	return result, nil
}

// applyEnvOverrides applies environment variable overrides.
func applyEnvOverrides(cfg *Config) error {
	if val := os.Getenv("LOG_LEVEL"); val != "" {
		cfg.Server.LogLevel = val
	}

	if val := os.Getenv("DRY_RUN"); val != "" {
		switch strings.ToLower(val) {
		case "true", "1", "yes":
			cfg.Server.DryRun = true
		case "false", "0", "no":
			cfg.Server.DryRun = false
		default:
			return fmt.Errorf("DRY_RUN: invalid value %q (must be true/false)", val)
		}
	}

	if val := os.Getenv("LISTEN_ADDR"); val != "" {
		cfg.Server.Listen = val
	}

	if val := os.Getenv("DOCKER_HOST"); val != "" {
		cfg.Docker.Host = val
	}

	return nil
}

// loadEnvFile reads a .env file from the given directory and sets the
// key-value pairs as environment variables. Only sets a variable if it is
// not already set, so explicit env vars always take precedence.
// A missing .env file is silently ignored.
func loadEnvFile(dir string) error {
	env, err := godotenv.Read(filepath.Join(dir, ".env"))
	if err != nil {
		// Missing .env is fine; godotenv returns os.ErrNotExist when the file is absent
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read .env: %w", err)
	}

	for key, value := range env {
		// Only set if not already present
		if _, exists := os.LookupEnv(key); !exists {
			os.Setenv(key, value)
		}
	}

	return nil
}

// checkUnknownKeys checks for unknown keys in the config.
func checkUnknownKeys(raw map[string]any) error {
	validKeys := map[string]bool{
		"$schema":    true,
		"server":     true,
		"cloudflare": true,
		"tunnel":     true,
		"docker":     true,
		"labels":     true,
		"reconcile":  true,
		"dns":        true,
	}

	var errs []string
	for key := range raw {
		if !validKeys[key] {
			errs = append(errs, fmt.Sprintf("unknown key: %s", key))
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("config errors:\n%s", strings.Join(errs, "\n"))
	}

	return nil
}
