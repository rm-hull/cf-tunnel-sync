package cmd

import (
	"context"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/rm-hull/cf-tunnel-sync/internal/cf"
	"github.com/rm-hull/cf-tunnel-sync/internal/config"
	"github.com/rm-hull/cf-tunnel-sync/internal/docker"
	"github.com/rm-hull/cf-tunnel-sync/internal/logx"
	"github.com/rm-hull/cf-tunnel-sync/internal/reconcile"
	"github.com/rm-hull/cf-tunnel-sync/internal/web"
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
				reconcile.WithOverwriteConflicts(cfg.DNS.OverwriteConflicts),
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

			// Create web server
			webServer := web.NewServer(log, daemon, ring, cfAPI, cfg.Server.StatePath, web.Config{
				Listen:         cfg.Server.Listen,
				Metrics:        cfg.Server.Metrics,
				StatusRefresh:  cfg.Server.StatusRefresh,
				DryRun:         dryRun || cfg.Server.DryRun,
				ResyncInterval: cfg.Reconcile.ResyncInterval,
			})

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
				"listen", cfg.Server.Listen,
			)

			// Start web server in background
			webErr := make(chan error, 1)
			go func() {
				webErr <- webServer.Start(sigCtx)
			}()

			// Check for immediate web server startup errors
			select {
			case err := <-webErr:
				if err != nil {
					log.Error("web server failed to start", "error", err)
				}
			case <-time.After(2 * time.Second):
				// Web server is starting up in background, continue
			}

			// Run daemon (blocks until shutdown)
			daemon.Run(sigCtx)

			// Wait for web server to shut down
			select {
			case err := <-webErr:
				if err != nil {
					log.Error("web server error", "error", err)
				}
			case <-time.After(5 * time.Second):
				log.Warn("web server shutdown timed out")
			}

			return nil
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "simulate without making changes")
	return cmd
}
