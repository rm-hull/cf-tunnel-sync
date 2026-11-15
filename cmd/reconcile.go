package cmd

import (
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/rm-hull/cf-tunnel-sync/internal"
	"github.com/rm-hull/cf-tunnel-sync/internal/cf"
	"github.com/rm-hull/cf-tunnel-sync/internal/config"
	"github.com/rm-hull/cf-tunnel-sync/internal/docker"
	"github.com/rm-hull/cf-tunnel-sync/internal/reconcile"
	"github.com/spf13/cobra"
)

func NewReconcileCmd() *cobra.Command {
	dryRun := false
	cmd := &cobra.Command{
		Use:   "reconcile",
		Short: "Run a one-shot reconciliation pass",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfgPath := cmd.Flag("config").Value.String()
			cfg, err := config.Load(cfgPath)
			if err != nil {
				return err
			}

			logLevel := internal.ParseLogLevel(cfg.Server.LogLevel)
			log := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{
				Level: logLevel,
			}))

			dockerAPI, err := docker.New(cfg.Docker.Host, log)
			if err != nil {
				return err
			}

			cfAPI := cf.New("", cfg.Cloudflare.AccountID, cfg.Cloudflare.ZoneID, log)

			grace, err := time.ParseDuration(cfg.Reconcile.GracePeriod)
			if err != nil {
				return fmt.Errorf("parse grace_period: %w", err)
			}

			eng := reconcile.New(
				log,
				dockerAPI,
				cfAPI,
				cfg.Server.StatePath,
				cfg.Tunnel.Name,
				cfg.Labels.Prefixes,
				grace,
				reconcile.WithDryRun(dryRun || cfg.Server.DryRun),
			)

			res := eng.Run(cmd.Context())
			if res.LastError != nil {
				return res.LastError
			}

			mode := "live"
			if res.DryRun {
				mode = "dry-run"
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Reconcile %s: added=%d updated=%d pending=%d expired=%d dns_created=%d ingress_updated=%v (%v)\n",
				mode, res.RulesAdded, res.RulesUpdated, res.RulesPending, res.RulesExpired,
				res.DNSCreated, res.IngressUpdated, res.Duration)

			return nil
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "simulate without making changes")
	return cmd
}
