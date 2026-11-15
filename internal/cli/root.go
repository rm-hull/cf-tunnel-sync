// Package cli provides the cobra command structure.
package cli

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/rm-hull/cf-tunnel-sync/internal/config"
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
	return &cobra.Command{
		Use:   "plan",
		Short: "Dry-run reconciliation and print the diff",
		RunE: func(_ *cobra.Command, _ []string) error {
			return fmt.Errorf("not implemented")
		},
	}
}

func newRulesCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "rules",
		Short: "Print desired rules from live Docker",
		RunE: func(_ *cobra.Command, _ []string) error {
			return fmt.Errorf("not implemented")
		},
	}
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

// Execute runs the root command.
func Execute() {
	root := NewRootCmd(&Deps{})
	if err := root.Execute(); err != nil {
		os.Exit(1)
	}
}
