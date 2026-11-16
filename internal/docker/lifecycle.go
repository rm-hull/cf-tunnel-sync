package docker

import (
	"context"
	"fmt"
	"io"
	"log/slog"

	"github.com/rm-hull/cf-tunnel-sync/internal/cf"
)

// CloudflaredConfig holds the settings for managing the cloudflared container.
type CloudflaredConfig struct {
	ContainerName string
	Image         string
	Network       string
	MetricsPort   int
}

// EnsureCloudflared ensures the cloudflared container is running.
// It adopts a running container, starts a stopped one, or creates a new one.
// In external mode (externalID set), it does nothing.
// In dry-run mode, it logs what it would do without acting.
func EnsureCloudflared(ctx context.Context, api API, cfAPI cf.API,
	tunnelID string, cfg CloudflaredConfig, externalID string, dryRun bool, log *slog.Logger,
) error {
	// External mode: skip container management entirely
	if externalID != "" {
		log.Info("external mode, skipping cloudflared container management")
		return nil
	}

	// Find existing container
	existing, err := api.FindContainerByName(ctx, cfg.ContainerName)
	if err != nil {
		return fmt.Errorf("find cloudflared container: %w", err)
	}

	// Container exists
	if existing.ID != "" {
		switch existing.State {
		case "running":
			log.Info("cloudflared container is running, adopting", "id", existing.ID, "name", existing.Name)
			if existing.Network != cfg.Network {
				log.Warn("cloudflared container on wrong network",
					"expected", cfg.Network, "actual", existing.Network)
			}
			return nil

		case "exited", "created", "dead":
			if dryRun {
				log.Info("would_start_cloudflared", "id", existing.ID)
				return nil
			}
			log.Info("starting stopped cloudflared container", "id", existing.ID)
			if err := api.StartContainer(ctx, existing.ID); err != nil {
				// The container may be stale (eg its network was removed).
				// Remove it and recreate from scratch.
				log.Warn("failed to start cloudflared container, recreating", "id", existing.ID, "error", err)
				if removeErr := api.RemoveContainer(ctx, existing.ID); removeErr != nil {
					log.Warn("failed to remove stale cloudflared container", "id", existing.ID, "error", removeErr)
					return fmt.Errorf("start cloudflared container: %w", err)
				}
				log.Info("removed stale cloudflared container", "id", existing.ID)
				// Fall through to create a new one below.
			} else {
				return nil
			}

		default:
			log.Warn("cloudflared container in unexpected state", "state", existing.State, "id", existing.ID)
			return nil
		}
	}

	// Container does not exist — create it
	if dryRun {
		log.Info("would_create_cloudflared", "name", cfg.ContainerName, "image", cfg.Image)
		return nil
	}

	log.Info("creating cloudflared container", "name", cfg.ContainerName, "image", cfg.Image)

	// Get tunnel token
	token, err := cfAPI.GetTunnelToken(ctx, tunnelID)
	if err != nil {
		return fmt.Errorf("get tunnel token: %w", err)
	}

	// Pull image if needed (best-effort)
	if pullErr := api.PullImage(ctx, cfg.Image, io.Discard); pullErr != nil {
		log.Warn("pull image failed, using cached", "image", cfg.Image, "error", pullErr)
	}

	cmd := []string{
		"tunnel", "--no-autoupdate",
		"--metrics", fmt.Sprintf("0.0.0.0:%d", cfg.MetricsPort),
		"run",
	}
	env := []string{
		"TUNNEL_TOKEN=" + token,
	}

	id, err := api.CreateContainer(ctx, cfg.ContainerName, cfg.Image, cmd, env, cfg.Network)
	if err != nil {
		return fmt.Errorf("create cloudflared container: %w", err)
	}

	if err := api.StartContainer(ctx, id); err != nil {
		return fmt.Errorf("start cloudflared container: %w", err)
	}

	log.Info("cloudflared container created and started", "id", id)
	return nil
}

// StopCloudflared stops the cloudflared container if it is running.
// In external mode (externalID set), it does nothing.
// In dry-run mode, it logs what it would do without acting.
func StopCloudflared(ctx context.Context, api API, cfg CloudflaredConfig, externalID string, dryRun bool, log *slog.Logger) error {
	// External mode: skip container management entirely
	if externalID != "" {
		log.Info("external mode, skipping cloudflared container cleanup")
		return nil
	}

	existing, err := api.FindContainerByName(ctx, cfg.ContainerName)
	if err != nil {
		return fmt.Errorf("find cloudflared container: %w", err)
	}

	if existing.ID == "" {
		log.Info("cloudflared container not found, nothing to stop")
		return nil
	}

	if existing.State != "running" {
		log.Info("cloudflared container is not running", "state", existing.State)
		return nil
	}

	if dryRun {
		log.Info("would stop cloudflared container", "id", existing.ID)
		return nil
	}

	log.Info("stopping cloudflared container", "id", existing.ID)
	if err := api.StopContainer(ctx, existing.ID); err != nil {
		return fmt.Errorf("stop cloudflared container: %w", err)
	}

	log.Info("cloudflared container stopped", "id", existing.ID)
	return nil
}
