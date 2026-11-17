package reconcile_test

import (
	"context"
	"fmt"
	"sync"
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

// R4 — Snapshot() shares EventCounts with the writer (data race)
func TestDaemon_SnapshotIsIsolatedFromLaterEvents(t *testing.T) {
	stream := newTestEventStream()
	dockerAPI := &fakeDockerWithEvents{API: docker.NewFake(), stream: stream}
	d := newTestDaemon(t, dockerAPI, setupFakeCF(t), reconcile.DaemonConfig{
		Debounce: time.Hour, ResyncInterval: time.Hour,
	})
	runDaemon(t, d)

	stream.sendEventWithContainer("start", "whoami")
	require.Eventually(t, func() bool { return d.Snapshot().EventCounts["whoami"]["start"] == 1 },
		2*time.Second, 5*time.Millisecond)
	snap := d.Snapshot()

	stream.sendEventWithContainer("start", "whoami")
	require.Eventually(t, func() bool { return d.Snapshot().EventCounts["whoami"]["start"] == 2 },
		2*time.Second, 5*time.Millisecond)

	assert.Equal(t, 1, snap.EventCounts["whoami"]["start"],
		"an earlier snapshot must not change when new events arrive")
}

func TestDaemon_SnapshotConcurrentReadWhileEventsArrive(t *testing.T) {
	stream := newTestEventStream()
	dockerAPI := &fakeDockerWithEvents{API: docker.NewFake(), stream: stream}
	d := newTestDaemon(t, dockerAPI, setupFakeCF(t), reconcile.DaemonConfig{
		Debounce: time.Hour, ResyncInterval: time.Hour,
	})
	runDaemon(t, d)

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { // writer: events for several containers
		defer wg.Done()
		for i := 0; i < 300; i++ {
			stream.sendEventWithContainer("start", fmt.Sprintf("c%d", i%5))
		}
	}()
	go func() { // reader: what the metrics handler does
		defer wg.Done()
		for i := 0; i < 300; i++ {
			for _, actions := range d.Snapshot().EventCounts {
				for range actions {
				}
			}
		}
	}()
	wg.Wait()
}

// R12 — Run does not wait for in-flight passes on shutdown
func TestDaemon_ShutdownWaitsForInFlightPass(t *testing.T) {
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(release) }) })

	dockerAPI := newStubDocker()
	dockerAPI.hook = func() {
		select {
		case entered <- struct{}{}:
		default:
		}
		<-release // block the pass inside the engine
	}
	d := newTestDaemon(t, dockerAPI, setupFakeCF(t), reconcile.DaemonConfig{
		Debounce: time.Hour, ResyncInterval: time.Hour,
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { d.Run(ctx); close(done) }()

	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("initial pass never started")
	}
	cancel()

	select {
	case <-done:
		t.Fatal("Run returned while a pass was still in flight")
	case <-time.After(300 * time.Millisecond):
	}

	once.Do(func() { close(release) })
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not return after the in-flight pass finished")
	}
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

// R9 — Retry backoff uses total pass count
func TestDaemon_BackoffUsesConsecutiveFailures(t *testing.T) {
	cfAPI := &flakyCF{Fake: setupFakeCF(t)}
	d := newTestDaemon(t, newStubDocker(), cfAPI, reconcile.DaemonConfig{
		Debounce: time.Hour, ResyncInterval: time.Hour, MinimumInterval: 0,
		BackoffInitial: 20 * time.Millisecond, BackoffMax: 10 * time.Second,
	})
	runDaemon(t, d)

	// Five healthy passes.
	require.Eventually(t, func() bool { return d.Snapshot().PassCount >= 1 }, 2*time.Second, 10*time.Millisecond)
	for i := 2; i <= 5; i++ {
		d.Trigger()
		want := i
		require.Eventually(t, func() bool { return d.Snapshot().PassCount >= want }, 2*time.Second, 10*time.Millisecond)
	}

	// One failing pass (#6), then recover.
	cfAPI.failFindTunnel.Store(true)
	d.Trigger()
	require.Eventually(t, func() bool {
		s := d.Snapshot()
		return s.PassCount >= 6 && s.LastError != nil
	}, 2*time.Second, 10*time.Millisecond)
	cfAPI.failFindTunnel.Store(false)

	// First failure => retry after BackoffInitial (20ms), not 20ms*2^5 = 640ms.
	require.Eventually(t, func() bool { return d.Snapshot().PassCount >= 7 },
		300*time.Millisecond, 10*time.Millisecond, "retry delay must not depend on total pass count")
}

// R2 — Docker events should bypass MinimumInterval and trigger immediately.
func TestDaemon_EventWithinMinimumIntervalTriggersImmediately(t *testing.T) {
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

	// The event should trigger immediately, not wait for the minimum interval to elapse.
	// With a 400ms minimum interval, waiting 200ms should be enough for an immediate
	// trigger, but too short for a deferred one.
	require.Eventually(t, func() bool { return d.Snapshot().PassCount >= 2 },
		200*time.Millisecond, 10*time.Millisecond,
		"Docker event should bypass MinimumInterval and trigger immediately")
}
