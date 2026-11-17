package reconcile_test

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/rm-hull/cf-tunnel-sync/internal/cf"
	"github.com/rm-hull/cf-tunnel-sync/internal/config"
	"github.com/rm-hull/cf-tunnel-sync/internal/docker"
	"github.com/rm-hull/cf-tunnel-sync/internal/reconcile"
	"github.com/stretchr/testify/assert"
)

func testLoggerDaemon() *slog.Logger {
	return slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

func tempStateDirDaemon(t *testing.T) string {
	t.Helper()
	return t.TempDir()
}

func setupFakeCFDaemon(t *testing.T) *cf.Fake {
	t.Helper()
	fake := cf.NewFake()
	fake.AddTunnel(cf.Tunnel{
		ID:        "tunnel-uuid",
		Name:      "homelab",
		ConfigSrc: "cloudflare",
		Status:    "healthy",
	})
	fake.AddZone(cf.Zone{ID: "zone-1", Name: "example.com"})
	return fake
}

// testEventStream provides controllable event channels for testing.
type testEventStream struct {
	events      chan docker.Event
	reconnected chan struct{}
}

func (s *testEventStream) sendEvent(action string) {
	s.events <- docker.Event{Action: action}
}

func (s *testEventStream) sendReconnect() {
	s.reconnected <- struct{}{}
}

// fakeDockerWithEvents returns a docker.API with controllable event streams.
type fakeDockerWithEvents struct {
	docker.API
	stream *testEventStream
}

func (f *fakeDockerWithEvents) Events(_ context.Context, _ []string) docker.EventStream {
	return docker.EventStream{
		Events:      f.stream.events,
		Reconnected: f.stream.reconnected,
		Err:         make(chan error, 10),
	}
}

func newTestEventStream() *testEventStream {
	return &testEventStream{
		events:      make(chan docker.Event, 10),
		reconnected: make(chan struct{}, 10),
	}
}

// TestDaemon_debounce_burst verifies that a burst of events within the debounce
// window produces only one reconciliation pass.
func TestDaemon_debounce_burst(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	stateDir := tempStateDirDaemon(t)
	statePath := filepath.Join(stateDir, "state.json")

	cfAPI := setupFakeCFDaemon(t)
	stream := newTestEventStream()
	dockerAPI := &fakeDockerWithEvents{
		API:    docker.NewFake(),
		stream: stream,
	}

	eng := reconcile.New(
		testLoggerDaemon(),
		dockerAPI,
		cfAPI,
		statePath,
		"homelab",
		[]string{"cloudflare.tunnel"},
		8*time.Hour,
	)

	daemon := reconcile.NewDaemon(
		testLoggerDaemon(),
		eng,
		dockerAPI,
		cfAPI,
		reconcile.DaemonConfig{
			Debounce:        100 * time.Millisecond,
			ResyncInterval:  5 * time.Minute,
			MinimumInterval: 0,
		},
		config.CloudflaredConfig{},
		"",
		false,
	)

	done := make(chan struct{})
	go func() {
		daemon.Run(ctx)
		close(done)
	}()

	// Send 5 events rapidly — should debounce into 1 pass
	for range 5 {
		stream.sendEvent("start")
	}

	// Wait for debounce to fire and the pass to complete
	time.Sleep(300 * time.Millisecond)

	// Cancel to stop the daemon
	cancel()
	<-done

	// Exactly one ingress PUT should have been made (the debounced pass)
	assert.Equal(t, 1, cfAPI.IngressPutCalls, "burst of 5 events should produce exactly 1 pass")
}

// TestDaemon_resync_interval verifies that the resync timer fires periodically
// and triggers a reconciliation pass even without events.
func TestDaemon_resync_interval(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	stateDir := tempStateDirDaemon(t)
	statePath := filepath.Join(stateDir, "state.json")

	cfAPI := setupFakeCFDaemon(t)
	stream := newTestEventStream()
	dockerAPI := &fakeDockerWithEvents{
		API:    docker.NewFake(),
		stream: stream,
	}

	eng := reconcile.New(
		testLoggerDaemon(),
		dockerAPI,
		cfAPI,
		statePath,
		"homelab",
		[]string{"cloudflare.tunnel"},
		8*time.Hour,
	)

	daemon := reconcile.NewDaemon(
		testLoggerDaemon(),
		eng,
		dockerAPI,
		cfAPI,
		reconcile.DaemonConfig{
			Debounce:        50 * time.Millisecond,
			ResyncInterval:  100 * time.Millisecond,
			MinimumInterval: 0,
		},
		config.CloudflaredConfig{},
		"",
		false,
	)

	done := make(chan struct{})
	go func() {
		daemon.Run(ctx)
		close(done)
	}()

	// Wait for 2 resync cycles
	time.Sleep(350 * time.Millisecond)

	// Cancel to stop the daemon
	cancel()
	<-done

	// At least 2 passes should have occurred (initial debounce + at least 1 resync)
	// Use IngressGetCalls because subsequent passes are idempotent and don't PUT
	assert.GreaterOrEqual(t, cfAPI.IngressGetCalls, 2, "resync should fire periodically")
}

// TestDaemon_backoff_on_failure verifies that failed passes retry with
// exponential backoff (30s initial, 5min cap).
func TestDaemon_backoff_on_failure(t *testing.T) {
	stateDir := tempStateDirDaemon(t)
	statePath := filepath.Join(stateDir, "state.json")

	// CF API that fails twice then succeeds
	cfAPI := setupFakeCFDaemon(t)
	failingCF := &failingCFWrapper{
		Fake:    cfAPI,
		failCnt: 2,
	}

	stream := newTestEventStream()
	dockerAPI := &fakeDockerWithEvents{
		API:    docker.NewFake(),
		stream: stream,
	}

	eng := reconcile.New(
		testLoggerDaemon(),
		dockerAPI,
		failingCF,
		statePath,
		"homelab",
		[]string{"cloudflare.tunnel"},
		8*time.Hour,
	)

	daemon := reconcile.NewDaemon(
		testLoggerDaemon(),
		eng,
		dockerAPI,
		failingCF,
		reconcile.DaemonConfig{
			Debounce:        50 * time.Millisecond,
			ResyncInterval:  5 * time.Minute,
			BackoffInitial:  10 * time.Millisecond, // fast backoff for testing
			BackoffMax:      500 * time.Millisecond,
			MinimumInterval: 0,
		},
		config.CloudflaredConfig{},
		"",
		false,
	)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	done := make(chan struct{})
	go func() {
		daemon.Run(ctx)
		close(done)
	}()

	// Trigger a pass
	stream.sendEvent("start")

	// Wait for retries to complete
	<-done

	// The daemon should have retried and eventually succeeded
	assert.Equal(t, 1, cfAPI.IngressPutCalls, "should succeed after retries")
}

// TestDaemon_reconnect_forces_pass verifies that when the Docker event stream
// reconnects, a full reconciliation pass is triggered immediately.
func TestDaemon_reconnect_forces_pass(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	stateDir := tempStateDirDaemon(t)
	statePath := filepath.Join(stateDir, "state.json")

	cfAPI := setupFakeCFDaemon(t)
	stream := newTestEventStream()
	dockerAPI := &fakeDockerWithEvents{
		API:    docker.NewFake(),
		stream: stream,
	}

	eng := reconcile.New(
		testLoggerDaemon(),
		dockerAPI,
		cfAPI,
		statePath,
		"homelab",
		[]string{"cloudflare.tunnel"},
		8*time.Hour,
	)

	daemon := reconcile.NewDaemon(
		testLoggerDaemon(),
		eng,
		dockerAPI,
		cfAPI,
		reconcile.DaemonConfig{
			Debounce:        50 * time.Millisecond,
			ResyncInterval:  5 * time.Minute,
			MinimumInterval: 0,
		},
		config.CloudflaredConfig{},
		"",
		false,
	)

	done := make(chan struct{})
	go func() {
		daemon.Run(ctx)
		close(done)
	}()

	// Let the initial pass complete
	time.Sleep(200 * time.Millisecond)

	// Simulate a reconnect — should trigger a new pass immediately
	stream.sendReconnect()

	// Wait for the reconnect-triggered pass
	time.Sleep(200 * time.Millisecond)

	// Cancel to stop the daemon and wait for it to finish
	cancel()
	<-done

	// Read stats after daemon has stopped — avoids data race
	assert.Greater(t, cfAPI.IngressGetCalls, 1, "reconnect should trigger a new pass")
}

// TestDaemon_graceful_shutdown verifies that on context cancellation (SIGINT),
// the daemon completes the current pass before exiting.
func TestDaemon_graceful_shutdown(t *testing.T) {
	stateDir := tempStateDirDaemon(t)
	statePath := filepath.Join(stateDir, "state.json")

	cfAPI := setupFakeCFDaemon(t)
	stream := newTestEventStream()
	dockerAPI := &fakeDockerWithEvents{
		API:    docker.NewFake(),
		stream: stream,
	}

	eng := reconcile.New(
		testLoggerDaemon(),
		dockerAPI,
		cfAPI,
		statePath,
		"homelab",
		[]string{"cloudflare.tunnel"},
		8*time.Hour,
	)

	daemon := reconcile.NewDaemon(
		testLoggerDaemon(),
		eng,
		dockerAPI,
		cfAPI,
		reconcile.DaemonConfig{
			Debounce:        50 * time.Millisecond,
			ResyncInterval:  5 * time.Minute,
			MinimumInterval: 0,
		},
		config.CloudflaredConfig{},
		"",
		false,
	)

	ctx, cancel := context.WithCancel(context.Background())

	passComplete := make(chan struct{})
	var wg sync.WaitGroup

	wg.Go(func() {
		daemon.Run(ctx)
		close(passComplete)
	})

	// Trigger a pass
	stream.sendEvent("start")

	// Wait for the pass to start
	time.Sleep(100 * time.Millisecond)

	// Cancel context (simulates SIGINT)
	cancel()

	// Daemon should complete and exit
	wg.Wait()
	select {
	case <-passComplete:
		// Good — daemon exited cleanly
	default:
		t.Fatal("daemon did not exit after context cancellation")
	}

	// The pass should have completed (ingress PUT was made)
	assert.Equal(t, 1, cfAPI.IngressPutCalls, "pass should complete before shutdown")
}

// TestDaemon_sighup_trigger verifies that sending a signal trigger causes
// a reconciliation pass.
func TestDaemon_sighup_trigger(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	stateDir := tempStateDirDaemon(t)
	statePath := filepath.Join(stateDir, "state.json")

	cfAPI := setupFakeCFDaemon(t)
	stream := newTestEventStream()
	dockerAPI := &fakeDockerWithEvents{
		API:    docker.NewFake(),
		stream: stream,
	}

	eng := reconcile.New(
		testLoggerDaemon(),
		dockerAPI,
		cfAPI,
		statePath,
		"homelab",
		[]string{"cloudflare.tunnel"},
		8*time.Hour,
	)

	daemon := reconcile.NewDaemon(
		testLoggerDaemon(),
		eng,
		dockerAPI,
		cfAPI,
		reconcile.DaemonConfig{
			Debounce:        50 * time.Millisecond,
			ResyncInterval:  5 * time.Minute,
			MinimumInterval: 0,
		},
		config.CloudflaredConfig{},
		"",
		false,
	)

	done := make(chan struct{})
	go func() {
		daemon.Run(ctx)
		close(done)
	}()

	// Let the initial pass complete
	time.Sleep(200 * time.Millisecond)

	// Trigger via SIGHUP channel
	daemon.Trigger()

	// Wait for the triggered pass
	time.Sleep(200 * time.Millisecond)

	// Cancel to stop the daemon and wait for it to finish
	cancel()
	<-done

	// Read stats after daemon has stopped — avoids data race
	assert.Greater(t, cfAPI.IngressGetCalls, 1, "SIGHUP trigger should cause a new pass")
}

// TestDaemon_snapshot_stats verifies that after each pass, the daemon publishes
// a snapshot with correct statistics.
func TestDaemon_snapshot_stats(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	stateDir := tempStateDirDaemon(t)
	statePath := filepath.Join(stateDir, "state.json")

	cfAPI := setupFakeCFDaemon(t)
	stream := newTestEventStream()
	dockerAPI := &fakeDockerWithEvents{
		API:    docker.NewFake(),
		stream: stream,
	}

	eng := reconcile.New(
		testLoggerDaemon(),
		dockerAPI,
		cfAPI,
		statePath,
		"homelab",
		[]string{"cloudflare.tunnel"},
		8*time.Hour,
	)

	daemon := reconcile.NewDaemon(
		testLoggerDaemon(),
		eng,
		dockerAPI,
		cfAPI,
		reconcile.DaemonConfig{
			Debounce:        50 * time.Millisecond,
			ResyncInterval:  5 * time.Minute,
			MinimumInterval: 0,
		},
		config.CloudflaredConfig{},
		"",
		false,
	)

	done := make(chan struct{})
	go func() {
		daemon.Run(ctx)
		close(done)
	}()

	// Wait for the initial pass to complete
	time.Sleep(200 * time.Millisecond)

	// Cancel to stop the daemon
	cancel()
	<-done

	// Get the snapshot
	snap := daemon.Snapshot()

	// Snapshot should reflect the completed pass
	assert.Equal(t, "tunnel-uuid", snap.TunnelID, "snapshot should have tunnel ID")
	assert.Equal(t, "homelab", snap.TunnelName, "snapshot should have tunnel name")
	assert.GreaterOrEqual(t, snap.PassCount, 1, "snapshot should show at least 1 pass")
	assert.True(t, snap.LastDuration > 0, "snapshot should record pass duration")
	assert.Nil(t, snap.LastError, "snapshot should have no error after successful pass")
}

// TestDaemon_minimum_interval verifies that rapid triggers are skipped when
// the minimum interval between passes has not elapsed.
func TestDaemon_minimum_interval(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	stateDir := tempStateDirDaemon(t)
	statePath := filepath.Join(stateDir, "state.json")

	cfAPI := setupFakeCFDaemon(t)
	stream := newTestEventStream()
	dockerAPI := &fakeDockerWithEvents{
		API:    docker.NewFake(),
		stream: stream,
	}

	eng := reconcile.New(
		testLoggerDaemon(),
		dockerAPI,
		cfAPI,
		statePath,
		"homelab",
		[]string{"cloudflare.tunnel"},
		8*time.Hour,
	)

	// Minimum interval longer than debounce so we can verify skipping
	daemon := reconcile.NewDaemon(
		testLoggerDaemon(),
		eng,
		dockerAPI,
		cfAPI,
		reconcile.DaemonConfig{
			Debounce:        20 * time.Millisecond,
			ResyncInterval:  5 * time.Minute,
			MinimumInterval: 150 * time.Millisecond,
		},
		config.CloudflaredConfig{},
		"",
		false,
	)

	done := make(chan struct{})
	go func() {
		daemon.Run(ctx)
		close(done)
	}()

	// Send first event — should trigger a pass after debounce
	stream.sendEvent("start")
	time.Sleep(100 * time.Millisecond) // wait for debounce + pass

	// Send burst of events — all within minimum interval, should be skipped
	for range 3 {
		stream.sendEvent("start")
	}
	time.Sleep(50 * time.Millisecond) // wait for debounce to fire (pass skipped)

	// Verify only 1 pass so far
	snap := daemon.Snapshot()
	assert.Equal(t, 1, snap.PassCount, "should have 1 pass before interval elapses")

	// Wait for minimum interval to elapse
	time.Sleep(150 * time.Millisecond)

	// Send event after interval — should trigger a new pass
	stream.sendEvent("start")
	time.Sleep(100 * time.Millisecond) // wait for debounce + pass

	// Cancel to stop the daemon
	cancel()
	<-done

	// Should have 2 passes: first + one after interval elapsed
	snap = daemon.Snapshot()
	assert.Equal(t, 2, snap.PassCount, "should run 2 passes (first + after interval)")
}

// TestDaemon_minimum_interval_disabled verifies that when MinimumInterval is 0,
// passes are not rate-limited.
func TestDaemon_minimum_interval_disabled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	stateDir := tempStateDirDaemon(t)
	statePath := filepath.Join(stateDir, "state.json")

	cfAPI := setupFakeCFDaemon(t)
	stream := newTestEventStream()
	dockerAPI := &fakeDockerWithEvents{
		API:    docker.NewFake(),
		stream: stream,
	}

	eng := reconcile.New(
		testLoggerDaemon(),
		dockerAPI,
		cfAPI,
		statePath,
		"homelab",
		[]string{"cloudflare.tunnel"},
		8*time.Hour,
	)

	daemon := reconcile.NewDaemon(
		testLoggerDaemon(),
		eng,
		dockerAPI,
		cfAPI,
		reconcile.DaemonConfig{
			Debounce:        50 * time.Millisecond,
			ResyncInterval:  5 * time.Minute,
			MinimumInterval: 0, // disabled
		},
		config.CloudflaredConfig{},
		"",
		false,
	)

	done := make(chan struct{})
	go func() {
		daemon.Run(ctx)
		close(done)
	}()

	// Send event, wait for debounce + pass
	stream.sendEvent("start")
	time.Sleep(150 * time.Millisecond)

	// Send another event immediately — should NOT be skipped (interval disabled)
	stream.sendEvent("start")
	time.Sleep(150 * time.Millisecond)

	// Cancel to stop the daemon
	cancel()
	<-done

	// Both passes should have run (no minimum interval)
	assert.Equal(t, 2, cfAPI.IngressGetCalls, "should run 2 passes when minimum interval is disabled")
}

// failingCFWrapper wraps cf.Fake and fails the first N FindTunnel calls.
type failingCFWrapper struct {
	*cf.Fake
	failCnt int
	callMu  sync.Mutex
	callCnt int
}

func (f *failingCFWrapper) FindTunnel(ctx context.Context, name string) (cf.Tunnel, error) {
	f.callMu.Lock()
	f.callCnt++
	n := f.callCnt
	f.callMu.Unlock()

	if n <= f.failCnt {
		return cf.Tunnel{}, os.ErrNotExist
	}
	return f.Fake.FindTunnel(ctx, name)
}
