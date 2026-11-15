// Package cli provides the cobra command structure.
package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sort"

	"github.com/jedib0t/go-pretty/v6/table"
	"github.com/rm-hull/cf-tunnel-sync/internal/cf"
	"github.com/rm-hull/cf-tunnel-sync/internal/config"
	"github.com/rm-hull/cf-tunnel-sync/internal/docker"
	"github.com/rm-hull/cf-tunnel-sync/internal/labels"
	rulespkg "github.com/rm-hull/cf-tunnel-sync/internal/rules"
	"github.com/spf13/cobra"
	"go.yaml.in/yaml/v3"
)

// Version and Commit are set by ldflags at build time.
var (
	Version = "dev"
	Commit  = "unknown"
)

// Deps holds injectable dependencies for testing.
type Deps struct {
	// Reserved for future injection points.
}

// NewRootCmd creates the root cobra command.
func NewRootCmd(_ *Deps) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "cf-tunnel-sync",
		Short: "Keep Cloudflare Tunnel ingress rules in sync with Docker labels",
		PersistentPreRunE: func(_ *cobra.Command, _ []string) error {
			return nil
		},
	}

	cfgPath := cmd.PersistentFlags().String("config", "", "path to config file")

	cmd.AddCommand(newVersionCmd())
	cmd.AddCommand(newRunCmd())
	cmd.AddCommand(newReconcileCmd())
	cmd.AddCommand(newPlanCmd())
	cmd.AddCommand(newRulesCmd())
	cmd.AddCommand(newConfigCmd(cfgPath))
	cmd.AddCommand(newHealthcheckCmd())

	return cmd
}

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print version information",
		RunE: func(cmd *cobra.Command, _ []string) error {
			fmt.Fprintf(cmd.OutOrStdout(), "cf-tunnel-sync %s (commit: %s)\n", Version, Commit)
			return nil
		},
	}
}

func newRunCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "run",
		Short: "Start the daemon (events + resync + status page)",
		RunE: func(_ *cobra.Command, _ []string) error {
			return fmt.Errorf("not implemented")
		},
	}
}

func newReconcileCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "reconcile",
		Short: "Run a one-shot reconciliation pass",
		RunE: func(_ *cobra.Command, _ []string) error {
			return fmt.Errorf("not implemented")
		},
	}
}

func newPlanCmd() *cobra.Command {
	jsonOut := false
	cmd := &cobra.Command{
		Use:   "plan",
		Short: "Dry-run reconciliation and print the diff",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.Load(cmd.Flag("config").Value.String())
			if err != nil {
				return err
			}

			logLevel := parseLogLevel(cfg.Server.LogLevel)
			log := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{
				Level: logLevel,
			}))

			dockerAPI, err := docker.New(cfg.Docker.Host, log)
			if err != nil {
				return err
			}

			cfAPI := cf.New(
				string(cfg.Cloudflare.APIToken),
				cfg.Cloudflare.AccountID,
				cfg.Cloudflare.ZoneID,
				log,
			)

			desiredRules, _, err := dockerAPI.DesiredRules(cmd.Context(), cfg.Labels.Prefixes, cfg.Labels.AllowUnprotectedAccess)
			if err != nil {
				return err
			}

			// Convert labels.Rule to rules.Rule
			desired := make(map[string]rulespkg.Rule)
			for key, r := range desiredRules {
				desired[key] = rulespkg.Rule{
					Hostname:         r.Hostname,
					Path:             r.Path,
					Service:          r.Service,
					NoTLSVerify:      r.NoTLSVerify,
					OriginServerName: r.OriginServerName,
					ZoneName:         r.ZoneName,
					ContainerName:    r.ContainerName,
					SourcePrefix:     r.SourcePrefix,
					SourceIndex:      r.SourceIndex,
					Status:           "active",
				}
			}

			tunnel, err := cfAPI.FindTunnel(cmd.Context(), cfg.Tunnel.Name)
			if err != nil {
				return err
			}

			result, err := cfAPI.Plan(cmd.Context(), tunnel.ID, desired, rulespkg.NewState(), cfg.DNS.OverwriteConflicts)
			if err != nil {
				return err
			}

			if jsonOut {
				return printPlanJSON(cmd.OutOrStdout(), result)
			}
			return printPlanTable(cmd.OutOrStdout(), result)
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "output as JSON")
	return cmd
}

func printPlanTable(w io.Writer, result cf.PlanResult) error {
	fmt.Fprintf(w, "Tunnel: %s (%s)\n", result.Tunnel.Name, result.Tunnel.ID)
	fmt.Fprintln(w)

	if len(result.IngressActions) > 0 {
		fmt.Fprintln(w, "INGRESS CHANGES:")
		for _, a := range result.IngressActions {
			switch a.Kind {
			case "add":
				fmt.Fprintf(w, "  + %s\n", a.Detail)
			case "remove":
				fmt.Fprintf(w, "  - %s (%s)\n", a.RuleKey, a.Detail)
			}
		}
		fmt.Fprintln(w)
	}

	if len(result.DNSActions) > 0 {
		fmt.Fprintln(w, "DNS CHANGES:")
		for _, a := range result.DNSActions {
			switch a.Kind {
			case "dns_create":
				fmt.Fprintf(w, "  + %s\n", a.Detail)
			case "dns_adopt":
				fmt.Fprintf(w, "  ~ %s (adopt)\n", a.Detail)
			case "dns_conflict":
				fmt.Fprintf(w, "  ! %s\n", a.Detail)
			case "dns_no_zone":
				fmt.Fprintf(w, "  ? %s\n", a.Detail)
			}
		}
		fmt.Fprintln(w)
	}

	if len(result.OrphanDNS) > 0 {
		fmt.Fprintln(w, "ORPHAN DNS:")
		for _, r := range result.OrphanDNS {
			fmt.Fprintf(w, "  %s -> %s (zone: %s)\n", r.Name, r.Content, r.ZoneName)
		}
		fmt.Fprintln(w)
	}

	if len(result.IngressActions) == 0 && len(result.DNSActions) == 0 && len(result.OrphanDNS) == 0 {
		fmt.Fprintln(w, "No changes needed.")
	}

	return nil
}

func printPlanJSON(w io.Writer, result cf.PlanResult) error {
	type output struct {
		Tunnel         cf.Tunnel       `json:"tunnel"`
		IngressActions []cf.PlanAction `json:"ingress_actions"`
		DNSActions     []cf.PlanAction `json:"dns_actions"`
		OrphanDNS      []cf.DNSRecord  `json:"orphan_dns"`
	}

	out := output{
		Tunnel:         result.Tunnel,
		IngressActions: result.IngressActions,
		DNSActions:     result.DNSActions,
		OrphanDNS:      result.OrphanDNS,
	}

	if out.IngressActions == nil {
		out.IngressActions = []cf.PlanAction{}
	}
	if out.DNSActions == nil {
		out.DNSActions = []cf.PlanAction{}
	}
	if out.OrphanDNS == nil {
		out.OrphanDNS = []cf.DNSRecord{}
	}

	data, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return err
	}
	fmt.Fprintln(w, string(data))
	return nil
}

func newRulesCmd() *cobra.Command {
	jsonOut := false
	cmd := &cobra.Command{
		Use:   "rules",
		Short: "Print desired rules from live Docker",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.Load(cmd.Flag("config").Value.String())
			if err != nil {
				return err
			}

			logLevel := parseLogLevel(cfg.Server.LogLevel)
			log := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{
				Level: logLevel,
			}))

			api, err := docker.New(cfg.Docker.Host, log)
			if err != nil {
				return err
			}

			rules, issues, err := api.DesiredRules(cmd.Context(), cfg.Labels.Prefixes, cfg.Labels.AllowUnprotectedAccess)
			if err != nil {
				return err
			}

			if jsonOut {
				return printRulesJSON(cmd.OutOrStdout(), rules, issues)
			}
			return printRulesTable(cmd.OutOrStdout(), rules, issues)
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "output as JSON")
	return cmd
}

func printRulesTable(w io.Writer, rules map[string]labels.Rule, issues []labels.Issue) error {
	// Sort rules by key for deterministic output
	keys := make([]string, 0, len(rules))
	for k := range rules {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	tbl := table.NewWriter()
	tbl.SetOutputMirror(w)
	tbl.SetStyle(table.StyleRounded)

	tbl.AppendHeader(table.Row{"HOST", "PATH", "SERVICE", "CONTAINER", "SOURCE"})
	for _, key := range keys {
		r := rules[key]
		tbl.AppendRow(table.Row{r.Hostname, r.Path, r.Service, r.ContainerName, r.SourcePrefix})
	}
	if len(keys) > 0 {
		tbl.Render()
	}

	if len(issues) > 0 {
		fmt.Fprintln(w)
		fmt.Fprintln(w, "ISSUES:")
		for _, iss := range issues {
			fmt.Fprintf(w, "  %s (%s): %s\n", iss.Kind, iss.Container, iss.Detail)
		}
	}
	return nil
}

func printRulesJSON(w io.Writer, rules map[string]labels.Rule, issues []labels.Issue) error {
	type ruleEntry struct {
		Key  string      `json:"key"`
		Rule labels.Rule `json:"rule"`
	}
	type issueEntry struct {
		Kind      string `json:"kind"`
		Container string `json:"container,omitempty"`
		Field     string `json:"field,omitempty"`
		Detail    string `json:"detail"`
	}
	type output struct {
		Rules  []ruleEntry  `json:"rules"`
		Issues []issueEntry `json:"issues,omitempty"`
	}

	// Sort rules by key for deterministic output
	keys := make([]string, 0, len(rules))
	for k := range rules {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	out := output{}
	for _, key := range keys {
		out.Rules = append(out.Rules, ruleEntry{
			Key:  key,
			Rule: rules[key],
		})
	}
	for _, iss := range issues {
		out.Issues = append(out.Issues, issueEntry{
			Kind:      iss.Kind,
			Container: iss.Container,
			Field:     iss.Field,
			Detail:    iss.Detail,
		})
	}

	data, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return err
	}
	fmt.Fprintln(w, string(data))
	return nil
}

func newConfigCmd(cfgPath *string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Configuration management",
	}

	cmd.AddCommand(&cobra.Command{
		Use:   "print",
		Short: "Print the effective configuration",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.Load(*cfgPath)
			if err != nil {
				return err
			}
			data, err := yaml.Marshal(cfg)
			if err != nil {
				return err
			}
			fmt.Fprint(cmd.OutOrStdout(), string(data))
			return nil
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use:   "validate",
		Short: "Validate the configuration file",
		RunE: func(_ *cobra.Command, _ []string) error {
			_, err := config.Load(*cfgPath)
			return err
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use:   "schema",
		Short: "Print the JSON schema",
		RunE: func(cmd *cobra.Command, _ []string) error {
			schema := config.Schema()
			data, err := json.MarshalIndent(schema, "", "  ")
			if err != nil {
				return err
			}
			fmt.Fprint(cmd.OutOrStdout(), string(data))
			return nil
		},
	})

	return cmd
}

func newHealthcheckCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "healthcheck",
		Short: "Check if the status page is responding",
		RunE: func(_ *cobra.Command, _ []string) error {
			return fmt.Errorf("not implemented")
		},
	}
}

// parseLogLevel converts a config log level string to slog.Level.
func parseLogLevel(level string) slog.Level {
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

// Execute runs the root command.
func Execute() {
	root := NewRootCmd(&Deps{})
	if err := root.Execute(); err != nil {
		os.Exit(1)
	}
}
