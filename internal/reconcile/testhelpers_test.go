package reconcile_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rm-hull/cf-tunnel-sync/internal/cf"
	"github.com/rm-hull/cf-tunnel-sync/internal/config"
	"github.com/rm-hull/cf-tunnel-sync/internal/docker"
	"github.com/rm-hull/cf-tunnel-sync/internal/labels"
	"github.com/rm-hull/cf-tunnel-sync/internal/reconcile"
)

func discardLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// ---- stubDocker: controllable DesiredRules, everything else from the in-memory fake.

type stubDocker struct {
	docker.API
	mu          sync.Mutex
	rules       map[string]labels.Rule
	allowUnprot bool
	hook        func() // called (outside the lock) at the start of every DesiredRules
}

func newStubDocker() *stubDocker {
	return &stubDocker{API: docker.NewFake(), rules: map[string]labels.Rule{}}
}

//nolint:unused // used by regression tests
func (s *stubDocker) setRules(rs ...labels.Rule) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rules = map[string]labels.Rule{}
	for _, r := range rs {
		s.rules[r.Key()] = r
	}
}

//nolint:unused // used by regression tests
func (s *stubDocker) lastAllowUnprotected() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.allowUnprot
}

func (s *stubDocker) DesiredRules(_ context.Context, _ []string, allowUnprotected bool) (map[string]labels.Rule, []labels.Issue, error) {
	s.mu.Lock()
	s.allowUnprot = allowUnprotected
	hook := s.hook
	out := make(map[string]labels.Rule, len(s.rules))
	for k, v := range s.rules {
		out[k] = v
	}
	s.mu.Unlock()
	if hook != nil {
		hook()
	}
	return out, nil, nil
}

func testRule(host, path, service, container string) labels.Rule {
	return labels.Rule{
		Hostname: host, Path: path, Service: service,
		ContainerName: container, SourcePrefix: "cloudflare.tunnel",
	}
}

// ---- flakyCF: *cf.Fake with switchable failures.

type flakyCF struct {
	*cf.Fake
	failCreateDNS  atomic.Bool
	failDeleteDNS  atomic.Bool
	failFindTunnel atomic.Bool
}

var errInjected = errors.New("injected failure")

func (f *flakyCF) CreateDNSRecord(ctx context.Context, r cf.DNSRecord) (string, error) {
	if f.failCreateDNS.Load() {
		return "", errInjected
	}
	return f.Fake.CreateDNSRecord(ctx, r)
}

func (f *flakyCF) DeleteDNSRecord(ctx context.Context, zoneID, id string) error {
	if f.failDeleteDNS.Load() {
		return errInjected
	}
	return f.Fake.DeleteDNSRecord(ctx, zoneID, id)
}

func (f *flakyCF) FindTunnel(ctx context.Context, name string) (cf.Tunnel, error) {
	if f.failFindTunnel.Load() {
		return cf.Tunnel{}, errInjected
	}
	return f.Fake.FindTunnel(ctx, name)
}

// ---- builders

func newEngineAPI(t *testing.T, d docker.API, c cf.API, statePath string, grace time.Duration, opts ...reconcile.Option) *reconcile.Engine {
	t.Helper()
	return reconcile.New(discardLogger(), d, c, statePath, "homelab", []string{"cloudflare.tunnel"}, grace, opts...)
}

func newTestDaemon(t *testing.T, d docker.API, c cf.API, cfg reconcile.DaemonConfig) *reconcile.Daemon {
	t.Helper()
	statePath := filepath.Join(t.TempDir(), "state.json")
	eng := newEngineAPI(t, d, c, statePath, 8*time.Hour)
	// externalTunnelID "external" => EnsureCloudflared/StopCloudflared are no-ops.
	return reconcile.NewDaemon(discardLogger(), eng, d, c, cfg, config.CloudflaredConfig{}, "external", false)
}

// runDaemon starts d.Run in a goroutine. The returned stop() cancels and waits; it is
// also registered with t.Cleanup and is safe to call twice.
func runDaemon(t *testing.T, d *reconcile.Daemon) (stop func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { d.Run(ctx); close(done) }()
	var once sync.Once
	stop = func() { once.Do(func() { cancel(); <-done }) }
	t.Cleanup(stop)
	return stop
}

//nolint:unused // used by regression tests
func ingressHosts(f *cf.Fake, tunnelID string) []string {
	var out []string
	for _, r := range f.Ingress[tunnelID].Rules {
		if r.Hostname != "" {
			out = append(out, r.Hostname)
		}
	}
	return out
}

// countingDocker returns an event stream that is already dead.
type countingDocker struct {
	docker.API
	calls atomic.Int64
}

func (c *countingDocker) Events(context.Context, []string) docker.EventStream {
	c.calls.Add(1)
	evt := make(chan docker.Event)
	close(evt)
	errc := make(chan error)
	close(errc)
	return docker.EventStream{Events: evt, Reconnected: make(chan struct{}, 1), Err: errc}
}

// Test that helpers compile
func TestHelpersCompile(t *testing.T) {
	_ = discardLogger()
	_ = newStubDocker()
	_ = testRule("a.example.com", "", "http://a:80", "a")
	_ = &flakyCF{Fake: cf.NewFake()}
	_ = newEngineAPI(t, newStubDocker(), cf.NewFake(), filepath.Join(t.TempDir(), "s.json"), time.Hour)
	_ = &countingDocker{API: docker.NewFake()}
	fmt.Println("helpers compile")
}
