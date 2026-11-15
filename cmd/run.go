package cmd

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/rm-hull/cf-tunnel-sync/internal/cf"
	"github.com/rm-hull/cf-tunnel-sync/internal/config"
	"github.com/rm-hull/cf-tunnel-sync/internal/docker"
	"github.com/rm-hull/cf-tunnel-sync/internal/logx"
	"github.com/rm-hull/cf-tunnel-sync/internal/reconcile"
	"github.com/spf13/cobra"
)

func NewRunCmd() *cobra.Command {
	dryRun := false
	cmd := &cobra.Command{
		Use:   "run",
		Short: "Start the daemon (events + resync + status page)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfgPath := cmd.Flag("config").Value.String()
			cfg, err := config.Load(cfgPath)
			if err != nil {
				return err
			}

			logLevel := logx.ParseLogLevel(cfg.Server.LogLevel)
			log, ring := logx.NewJSONLogger(logLevel, 1000)
			_ = ring // will be used by S10 web status page

			dockerAPI, err := docker.New(cfg.Docker.Host, log)
			if err != nil {
				return err
			}

			cfAPI, err := cf.New(string(cfg.Cloudflare.APIToken), cfg.Cloudflare.AccountID, cfg.Cloudflare.ZoneID, log)
			if err != nil {
				return err
			}

			eng := reconcile.New(
				log,
				dockerAPI,
				cfAPI,
				cfg.Server.StatePath,
				cfg.Tunnel.Name,
				cfg.Labels.Prefixes,
				cfg.Reconcile.GracePeriod,
				reconcile.WithDryRun(dryRun || cfg.Server.DryRun),
				reconcile.WithCloudflared(cfg.Tunnel.Cloudflared, cfg.Tunnel.ExternalID),
			)

			daemon := reconcile.NewDaemon(
				log,
				eng,
				dockerAPI,
				cfAPI,
				reconcile.DaemonConfig{
					Debounce:       cfg.Reconcile.Debounce,
					ResyncInterval: cfg.Reconcile.ResyncInterval,
				},
				cfg.Tunnel.Cloudflared,
				cfg.Tunnel.ExternalID,
				dryRun || cfg.Server.DryRun,
			)

			// Set up signal handling
			sigCtx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
			defer stop()

			hupChan := make(chan os.Signal, 1)
			signal.Notify(hupChan, syscall.SIGHUP)

			// Run daemon in background
			go func() {
				<-sigCtx.Done()
				log.Info("received shutdown signal, stopping daemon...")
			}()

			// SIGHUP handler
			go func() {
				for {
					select {
					case <-hupChan:
						log.Info("received SIGHUP, triggering reconciliation")
						daemon.Trigger()
					case <-sigCtx.Done():
						return
					}
				}
			}()

			log.Info("starting daemon",
				"tunnel", cfg.Tunnel.Name,
				"debounce", cfg.Reconcile.Debounce,
				"resync_interval", cfg.Reconcile.ResyncInterval,
				"grace_period", cfg.Reconcile.GracePeriod,
				"dry_run", dryRun || cfg.Server.DryRun,
			)

			// Block until SIGINT/SIGTERM
			daemon.Run(sigCtx)

			return nil
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "simulate without making changes")
	return cmd
}
