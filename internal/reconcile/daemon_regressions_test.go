package reconcile_test

import (
	"testing"
	"time"

	"github.com/rm-hull/cf-tunnel-sync/internal/docker"
	"github.com/rm-hull/cf-tunnel-sync/internal/reconcile"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// R3 — Docker event stream reconnect is a hot loop
func TestDaemon_ReconnectIsRateLimited(t *testing.T) {
	cd := &countingDocker{API: docker.NewFake()}
	d := newTestDaemon(t, cd, setupFakeCF(t), reconcile.DaemonConfig{
		Debounce: time.Hour, ResyncInterval: time.Hour, MinimumInterval: time.Minute,
	})
	stop := runDaemon(t, d)

	time.Sleep(300 * time.Millisecond) // the only allowed sleep: we measure a rate
	stop()

	assert.LessOrEqual(t, cd.calls.Load(), int64(3),
		"Events() was called %d times in 300ms: reconnect must back off", cd.calls.Load())
}

// R13 — Negative MinimumInterval means no minimum
func TestNewDaemon_NegativeMinimumIntervalMeansNoMinimum(t *testing.T) {
	d := newTestDaemon(t, newStubDocker(), setupFakeCF(t), reconcile.DaemonConfig{
		Debounce: time.Hour, ResyncInterval: time.Hour, MinimumInterval: -time.Second,
	})
	runDaemon(t, d)
	require.Eventually(t, func() bool { return d.Snapshot().PassCount >= 1 }, 2*time.Second, 10*time.Millisecond)

	d.Trigger()

	require.Eventually(t, func() bool { return d.Snapshot().PassCount >= 2 },
		time.Second, 10*time.Millisecond, "negative MinimumInterval must not enforce a 1m gap")
}

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
