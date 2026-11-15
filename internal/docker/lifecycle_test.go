package docker

import (
	"context"
	"log/slog"
	"os"
	"testing"

	"github.com/rm-hull/cf-tunnel-sync/internal/cf"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

func TestEnsureCloudflared_externalModeSkipsContainer(t *testing.T) {
	api := NewFake()
	cfAPI := cf.NewFake()
	cfg := CloudflaredConfig{
		ContainerName: "cf-tunnel-sync-cloudflared",
		Image:         "cloudflare/cloudflared:latest",
		Network:       "cloudflare-net",
		MetricsPort:   2000,
	}

	err := EnsureCloudflared(context.Background(), api, cfAPI, "tunnel-1", cfg, "external-id-123", false, newTestLogger())
	require.NoError(t, err)

	// Should not have created or started anything
	fake := api.(*fakeAPI)
	assert.Empty(t, fake.CreatedContainers)
	assert.Empty(t, fake.StartedContainers)
}

func TestEnsureCloudflared_adoptsRunningContainer(t *testing.T) {
	api := NewFake(Container{
		ID:      "existing-cf",
		Name:    "cf-tunnel-sync-cloudflared",
		Image:   "cloudflare/cloudflared:latest",
		State:   "running",
		Network: "cloudflare-net",
	})
	cfAPI := cf.NewFake()
	cfg := CloudflaredConfig{
		ContainerName: "cf-tunnel-sync-cloudflared",
		Image:         "cloudflare/cloudflared:latest",
		Network:       "cloudflare-net",
		MetricsPort:   2000,
	}

	err := EnsureCloudflared(context.Background(), api, cfAPI, "tunnel-1", cfg, "", false, newTestLogger())
	require.NoError(t, err)

	fake := api.(*fakeAPI)
	assert.Empty(t, fake.CreatedContainers)
	assert.Empty(t, fake.StartedContainers)
}

func TestEnsureCloudflared_warnsWrongNetwork(t *testing.T) {
	api := NewFake(Container{
		ID:      "existing-cf",
		Name:    "cf-tunnel-sync-cloudflared",
		Image:   "cloudflare/cloudflared:latest",
		State:   "running",
		Network: "wrong-net",
	})
	cfAPI := cf.NewFake()
	cfg := CloudflaredConfig{
		ContainerName: "cf-tunnel-sync-cloudflared",
		Image:         "cloudflare/cloudflared:latest",
		Network:       "cloudflare-net",
		MetricsPort:   2000,
	}

	// Capture log output to verify warning
	var logBuf testLogWriter
	log := slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelWarn}))

	err := EnsureCloudflared(context.Background(), api, cfAPI, "tunnel-1", cfg, "", false, log)
	require.NoError(t, err)

	assert.Contains(t, logBuf.String(), "wrong network")
}

func TestEnsureCloudflared_startsStoppedContainer(t *testing.T) {
	api := NewFake(Container{
		ID:    "stopped-cf",
		Name:  "cf-tunnel-sync-cloudflared",
		Image: "cloudflare/cloudflared:latest",
		State: "exited",
	})
	cfAPI := cf.NewFake()
	cfg := CloudflaredConfig{
		ContainerName: "cf-tunnel-sync-cloudflared",
		Image:         "cloudflare/cloudflared:latest",
		Network:       "cloudflare-net",
		MetricsPort:   2000,
	}

	err := EnsureCloudflared(context.Background(), api, cfAPI, "tunnel-1", cfg, "", false, newTestLogger())
	require.NoError(t, err)

	fake := api.(*fakeAPI)
	assert.Empty(t, fake.CreatedContainers)
	assert.Equal(t, []string{"stopped-cf"}, fake.StartedContainers)

	// Verify container state changed to running
	container, err := api.FindContainerByName(context.Background(), "cf-tunnel-sync-cloudflared")
	require.NoError(t, err)
	assert.Equal(t, "running", container.State)
}

func TestEnsureCloudflared_createsNewContainer(t *testing.T) {
	api := NewFake()
	cfAPI := cf.NewFake()
	cfAPI.AddTunnel(cf.Tunnel{ID: "tunnel-1", Name: "homelab", ConfigSrc: "cloudflare"})
	cfAPI.SetToken("tunnel-1", "secret-token-123")
	cfg := CloudflaredConfig{
		ContainerName: "cf-tunnel-sync-cloudflared",
		Image:         "cloudflare/cloudflared:latest",
		Network:       "cloudflare-net",
		MetricsPort:   2000,
	}

	err := EnsureCloudflared(context.Background(), api, cfAPI, "tunnel-1", cfg, "", false, newTestLogger())
	require.NoError(t, err)

	fake := api.(*fakeAPI)
	assert.Equal(t, []string{"cf-tunnel-sync-cloudflared"}, fake.CreatedContainers)
	assert.Equal(t, []string{"new-cf-tunnel-sync-cloudflared"}, fake.StartedContainers)
	assert.Contains(t, fake.PulledImages, "cloudflare/cloudflared:latest")
}

func TestEnsureCloudflared_dryRunDoesNotCreate(t *testing.T) {
	api := NewFake()
	cfAPI := cf.NewFake()
	cfAPI.AddTunnel(cf.Tunnel{ID: "tunnel-1", Name: "homelab", ConfigSrc: "cloudflare"})
	cfAPI.SetToken("tunnel-1", "secret-token-123")
	cfg := CloudflaredConfig{
		ContainerName: "cf-tunnel-sync-cloudflared",
		Image:         "cloudflare/cloudflared:latest",
		Network:       "cloudflare-net",
		MetricsPort:   2000,
	}

	err := EnsureCloudflared(context.Background(), api, cfAPI, "tunnel-1", cfg, "", true, newTestLogger())
	require.NoError(t, err)

	fake := api.(*fakeAPI)
	assert.Empty(t, fake.CreatedContainers)
	assert.Empty(t, fake.StartedContainers)
	assert.Empty(t, fake.PulledImages)
}

func TestEnsureCloudflared_dryRunDoesNotStart(t *testing.T) {
	api := NewFake(Container{
		ID:    "stopped-cf",
		Name:  "cf-tunnel-sync-cloudflared",
		Image: "cloudflare/cloudflared:latest",
		State: "exited",
	})
	cfAPI := cf.NewFake()
	cfg := CloudflaredConfig{
		ContainerName: "cf-tunnel-sync-cloudflared",
		Image:         "cloudflare/cloudflared:latest",
		Network:       "cloudflare-net",
		MetricsPort:   2000,
	}

	err := EnsureCloudflared(context.Background(), api, cfAPI, "tunnel-1", cfg, "", true, newTestLogger())
	require.NoError(t, err)

	fake := api.(*fakeAPI)
	assert.Empty(t, fake.StartedContainers)

	// Container should still be stopped
	container, err := api.FindContainerByName(context.Background(), "cf-tunnel-sync-cloudflared")
	require.NoError(t, err)
	assert.Equal(t, "exited", container.State)
}

func TestEnsureCloudflared_tokenError(t *testing.T) {
	api := NewFake()
	cfAPI := cf.NewFake()
	// No token set — GetTunnelToken will return ErrNotFound
	cfg := CloudflaredConfig{
		ContainerName: "cf-tunnel-sync-cloudflared",
		Image:         "cloudflare/cloudflared:latest",
		Network:       "cloudflare-net",
		MetricsPort:   2000,
	}

	err := EnsureCloudflared(context.Background(), api, cfAPI, "tunnel-1", cfg, "", false, newTestLogger())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "get tunnel token")
}

type testLogWriter struct {
	buf []byte
}

func (w *testLogWriter) Write(p []byte) (int, error) {
	w.buf = append(w.buf, p...)
	return len(p), nil
}

func (w *testLogWriter) String() string {
	return string(w.buf)
}
