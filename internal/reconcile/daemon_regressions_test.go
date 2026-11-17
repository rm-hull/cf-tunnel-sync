package reconcile_test

import (
	"testing"
	"time"

	"github.com/rm-hull/cf-tunnel-sync/internal/docker"
	"github.com/rm-hull/cf-tunnel-sync/internal/reconcile"
	"github.com/stretchr/testify/require"
)

// R2 — Passes skipped by MinimumInterval are dropped, not deferred
func TestDaemon_EventWithinMinimumIntervalStillTriggersPass(t *testing.T) {
	stream := newTestEventStream()
	dockerAPI := &fakeDockerWithEvents{API: docker.NewFake(), stream: stream}
	d := newTestDaemon(t, dockerAPI, setupFakeCF(t), reconcile.DaemonConfig{
		Debounce:        20 * time.Millisecond,
		ResyncInterval:  time.Hour,
		MinimumInterval: 400 * time.Millisecond,
	})
	runDaemon(t, d)

	require.Eventually(t, func() bool { return d.Snapshot().PassCount >= 1 },
		2*time.Second, 10*time.Millisecond, "initial pass should run")

	stream.sendEventWithContainer("start", "whoami") // lands inside MinimumInterval

	require.Eventually(t, func() bool { return d.Snapshot().PassCount >= 2 },
		3*time.Second, 20*time.Millisecond,
		"event inside MinimumInterval must be deferred, not dropped")
}
