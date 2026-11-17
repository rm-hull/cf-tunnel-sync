# REVIEW.md — Bug-hunt findings and implementation plan

This document is a work order. Each item (R1…R15) is a bug found by static review
of `cf-tunnel-sync`, with:

- **Where** the bug is (file + symbol; line numbers are from commit `5598ee5` and will drift).
- **Why** it is wrong.
- A **RED test** that demonstrates the broken behaviour (must fail before the fix).
- An exact **Fix** specification.
- **GREEN criteria** (what "done" means).

Nothing in this file was executed (no Go toolchain was available when it was
written). **Treat every "expected RED output" as a prediction.** If a RED test does
not fail as described, stop and read the "Constitution" rule 8.

---

## 0. Constitution (rules that apply to every item)

This extends `AGENTS.md` (Core Rule 1: test-first RED/GREEN). Where they conflict,
`AGENTS.md` wins.

1. **RED first, always.** For each item, the first commit contains *only* the new
   test(s) (plus, if unavoidable, a no-op symbol so the test compiles — see rule 4).
   Commit message: `test(<scope>): <behaviour> (RED)`.
2. **A RED test must compile and fail on an assertion.** Not a panic, not a nil
   dereference, not a bare timeout. For time-based waits use
   `require.Eventually(t, cond, timeout, tick, "message explaining what should have happened")`
   so the failure is readable.
3. **Show the RED.** Run the test with the exact command given in the item and paste the
   failing output into the PR description. A RED you did not observe is not a RED.
4. **New API symbols:** if a RED test needs a symbol that does not exist yet (a new
   option, field, or function), first add that symbol as a *behaviour-neutral stub*
   (e.g. an option that stores the value but nothing reads it). Commit it together
   with the test. The test must then fail on its assertion, not on a compile error.
5. **GREEN = minimal fix.** Second commit: `fix(<scope>): <behaviour>`. Make the RED
   test pass without editing the test. Do not refactor unrelated code.
6. **Gates.** Before each push run `make ci` (format check, `go vet`, `golangci-lint`,
   `go test -race ./...`). Race-detector failures are test failures.
7. **Flake check.** Once green, run the new tests with
   `go test -race -count=20 -run '<TestName>' ./<pkg>` and require 20/20 passes.
   Timing tests in this file use generous margins; if one flakes, widen the margin,
   never add bare `time.Sleep` for synchronisation (the only allowed sleeps are the
   ones written in the snippets below).
8. **If a RED test passes unexpectedly:** stop. Do not "fix" anything. Write down what
   you observed and ask the maintainer — the review's premise is wrong or the code
   changed.
9. **Existing tests:** never delete or weaken one to make the build pass. If an
   existing test genuinely encodes the old, buggy behaviour, change it in the *same
   commit as the fix* and explain why in the commit body.
10. **One item per branch / PR:** `fix/R<n>-<slug>`. Items that edit the same function
    are listed in the recommended order (section 2); rebase, don't batch.
11. **No scope creep.** Ideas that are not in an item go to `docs/FUTURE.md`.
12. **Go style:** Go version per `go.mod`; format with `gofumpt`; wrap errors with `%w`;
    log with the existing `slog` conventions. No new dependencies.

---

## 1. Shared test helpers (do this first, as its own commit)

Create `internal/reconcile/testhelpers_test.go` (package `reconcile_test`).
Existing helpers you may reuse: `setupFakeCF(t)` (tunnel `tunnel-uuid`/`homelab`, zone
`zone-1`=`example.com`), `testLoggerDaemon()`, `newTestEventStream()`,
`fakeDockerWithEvents`, `stream.sendEventWithContainer(action, name)`.

```go
package reconcile_test

import (
	"context"
	"errors"
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

func (s *stubDocker) setRules(rs ...labels.Rule) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rules = map[string]labels.Rule{}
	for _, r := range rs {
		s.rules[r.Key()] = r
	}
}

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

func (f *flakyCF) DeleteDNSRecord(ctx context.Context, id string) error {
	if f.failDeleteDNS.Load() {
		return errInjected
	}
	return f.Fake.DeleteDNSRecord(ctx, id)
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

func ingressHosts(f *cf.Fake, tunnelID string) []string {
	var out []string
	for _, r := range f.Ingress[tunnelID].Rules {
		if r.Hostname != "" {
			out = append(out, r.Hostname)
		}
	}
	return out
}
```

**Caveat for daemon tests:** `cf.Fake` and its counters are *not* goroutine-safe. While
a daemon is running, only read `d.Snapshot()`. Read `Fake` fields (`IngressPutCalls`,
`Ingress`, …) only after `stop()` has returned.

Commit: `test(reconcile): add shared helpers for review regression tests`.

---

## 2. Recommended order and index

| Order | ID  | Severity | Area | One-line summary |
|-------|-----|----------|------|------------------|
| 1 | R1  | High   | rules/reconcile | A DNS error makes the route vanish from ingress permanently |
| 2 | R2  | High   | daemon | Passes skipped by `MinimumInterval` are dropped, not deferred |
| 3 | R13 | Low    | daemon/config | `MinimumInterval < 0` defaults to 1m (inverted check) — touches same code as R2 |
| 4 | R3  | High   | daemon | Event stream reconnect is a hot loop; closed channels mishandled |
| 5 | R4  | High   | daemon | `Snapshot()` shares a map with the writer (data race / fatal crash) |
| 6 | R12 | Medium | daemon | `Run` does not wait for in-flight passes on shutdown |
| 7 | R5  | High   | reconcile | Expiry deletes DNS still used by sibling rules; failed deletes orphan records; dry-run persists state |
| 8 | R9  | Medium | daemon | Retry backoff grows with *total* passes, not consecutive failures |
| 9 | R7  | Medium | reconcile/cmd | Engine ignores `allow_unprotected_access` |
| 10 | R6  | Medium | docker | Duplicate-hostname winner is random (map iteration) |
| 11 | R8  | Medium | labels | One invalid `path`/`service` label breaks ingress for every container |
| 12 | R11 | Medium | reconcile | `ingressEqual` treats nil and empty `OriginRequest` as different |
| 13 | R10 | Low    | docker | `enable` value matching is inconsistent (`True`/`1` vs `true`) |
| 14 | R15 | Low    | reconcile | Dry-run fails the pass when the tunnel does not exist yet |
| 15 | R14 | Low    | cf | `DeleteDNSRecord` tries every zone and swallows real errors |
| 16 | M1–M8 | Minor | various | Small cleanups (section 18) |

Items R2, R9, R13 all edit `Daemon.runPass`; R3 and R12 both edit `Daemon.Run`. Do
them in the order above and rebase between them.

---

## R1 — DNS error/conflict status drops the route from ingress forever

**Where:** `internal/rules/rules.go` (`Rule.applyDesired`, `RenderIngress`),
`internal/reconcile/reconcile.go` step 7 (writes `Status = "dns_error" | "dns_conflict"`).

**Why it's wrong:** Step 7 stores `dns_error`/`dns_conflict` in the persisted rule
`Status`. On the next pass `applyDesired` only resets `pending → active`, so the status
sticks. `RenderIngress` skips every rule whose status is not `active`/`pending`, so the
next ingress PUT *removes the route*. A single transient Cloudflare DNS failure
therefore takes a service offline until its container goes away. It is never reset even
after DNS recovers.

**RED test 1 — pure, fast** (`internal/rules/lifecycle_test.go`, package `rules`):

```go
func TestApply_DesiredRuleResetsDNSStatusToActive(t *testing.T) {
	now := time.Now()
	st := NewState()
	st.Rules["app.example.com"] = Rule{
		Hostname: "app.example.com", Service: "http://app:80",
		Status: "dns_error", FirstSeen: now.Add(-time.Hour), LastSeen: now.Add(-time.Minute),
	}
	desired := map[string]Rule{
		"app.example.com": {Hostname: "app.example.com", Service: "http://app:80", Status: "active"},
	}

	_, active, _, _ := Apply(st, desired, now, time.Hour)

	assert.Equal(t, "active", active["app.example.com"].Status)
	assert.Len(t, RenderIngress(active), 2, "route + catch-all must be rendered")
}
```

Run: `go test -race -run TestApply_DesiredRuleResetsDNSStatusToActive ./internal/rules`
Expected RED: `Not equal: expected "active", actual "dns_error"`.

**RED test 2 — engine level** (new file `internal/reconcile/engine_regressions_test.go`):

```go
func TestReconcile_DNSErrorDoesNotDropIngressRoute(t *testing.T) {
	ctx := context.Background()
	statePath := filepath.Join(t.TempDir(), "state.json")
	fake := setupFakeCF(t)
	cfAPI := &flakyCF{Fake: fake}
	dockerAPI := newStubDocker()
	dockerAPI.setRules(testRule("app.example.com", "", "http://app:80", "app"))
	eng := newEngineAPI(t, dockerAPI, cfAPI, statePath, 8*time.Hour)

	// Pass 1: DNS creation fails (per-rule failure, so the pass itself succeeds).
	cfAPI.failCreateDNS.Store(true)
	require.NoError(t, eng.Run(ctx).LastError)
	require.Contains(t, ingressHosts(fake, "tunnel-uuid"), "app.example.com")

	// Pass 2: Cloudflare DNS is healthy again.
	cfAPI.failCreateDNS.Store(false)
	require.NoError(t, eng.Run(ctx).LastError)

	assert.Contains(t, ingressHosts(fake, "tunnel-uuid"), "app.example.com",
		"route must survive a transient DNS error")
	recs, _ := fake.ListDNSRecords(ctx, "zone-1", "app.example.com", "CNAME")
	assert.Len(t, recs, 1, "DNS record should be created on retry")
	st, err := state.Load(statePath)
	require.NoError(t, err)
	assert.Equal(t, "active", st.Rules["app.example.com"].Status)
}
```

Run: `go test -race -run TestReconcile_DNSErrorDoesNotDropIngressRoute ./internal/reconcile`
Expected RED: the route is missing from `ingressHosts` after pass 2 (and status is
`dns_error`).

**Fix:**
1. In `Rule.applyDesired` (`rules.go`), always set `r.Status = "active"`, `r.PendingSince = nil`,
   `r.DeleteAfter = nil` (not only when pending). Update the doc comment.
2. No change to `RenderIngress` — `dns_*` statuses are now informational and are
   recomputed from scratch each pass by step 7 (set after `Apply`, before `Save`).

**GREEN:** both tests pass; existing `internal/rules` and `internal/reconcile` tests pass.
A DNS *conflict* now keeps its route in ingress on every pass, consistent with pass 1 and
with the existing comment in `ensureDNS` ("still in ingress; DNS untouched").

---

## R2 — Passes skipped by `MinimumInterval` are dropped, not deferred

**Where:** `internal/reconcile/daemon.go` — `runPass` (the `MinimumInterval` early
return) and `Run` (the `debounceTimer` created armed at startup).

**Why it's wrong:** Default `minimum_interval` is 1m, debounce is 2s. When a trigger
arrives <1m after the previous pass, `runPass` returns silently and nothing re-arms it,
so a container started shortly after a pass is not reconciled until the 5m resync. The
same happens to failure retries (first backoff is 30s < 1m). Also the debounce timer is
created armed, so it fires once at startup with no event.

**RED test** (`internal/reconcile/daemon_regressions_test.go`):

```go
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
```

Run: `go test -race -run TestDaemon_EventWithinMinimumIntervalStillTriggersPass ./internal/reconcile`
Expected RED: Eventually fails after 3s with the message above (PassCount stays 1).

**Fix:**
1. Add to `Daemon`: `deferredPass bool` (guarded by `d.mu`).
2. In `runPass`, replace the early-return block with:
   ```go
   d.mu.Lock()
   sinceLast := time.Since(d.snap.LastPassTime)
   if d.cfg.MinimumInterval > 0 && sinceLast < d.cfg.MinimumInterval {
       remaining := d.cfg.MinimumInterval - sinceLast
       schedule := !d.deferredPass
       d.deferredPass = true
       d.mu.Unlock()
       if schedule {
           time.AfterFunc(remaining+time.Millisecond, func() {
               d.mu.Lock()
               d.deferredPass = false
               d.mu.Unlock()
               d.Trigger()
           })
       }
       d.log.Debug("deferring pass — minimum interval not reached", "remaining", remaining)
       return
   }
   d.mu.Unlock()
   ```
   (`LastPassTime` zero ⇒ `sinceLast` is huge ⇒ the first pass is never deferred.)
3. In `Run`, create the debounce timer **stopped**: after `time.NewTimer(d.cfg.Debounce)`
   call `debounceTimer.Stop()` immediately (and drain `C` if `Stop` returns false). It is
   still `Reset` on every event, so debounce behaviour is unchanged.

**GREEN:** the RED test passes; `TestDaemon_debounce_burst` and
`TestDaemon_resync_interval` still pass (adjust only if they relied on the startup timer
firing — if so, note it in the commit body).

---

## R13 — `MinimumInterval < 0` becomes 1 minute (inverted default)

**Where:** `daemon.go` `NewDaemon`: `if cfg.MinimumInterval < 0 { cfg.MinimumInterval = 1 * time.Minute }`;
`internal/config/config.go` `validate`.

**Why:** All the other defaults use `<= 0`. Here a *negative* value silently becomes 1m
while `0` means "no minimum" (existing tests rely on `0`). The production default
(1m) is already supplied by config, so the daemon-level default is unnecessary.

**RED tests:**

`internal/reconcile/daemon_regressions_test.go`:
```go
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
```
`internal/config/config_test.go` (follow the existing validation-test style in that
file; load/validate a config with `reconcile.minimum_interval: -1s`):
```go
// expect validation error containing "reconcile.minimum_interval: must be >= 0"
```
Expected RED: first test times out; second finds no error.

**Fix:** in `NewDaemon` change to `if cfg.MinimumInterval < 0 { cfg.MinimumInterval = 0 }`.
In `config.validate` add, next to the `GracePeriod` check:
`if cfg.Reconcile.MinimumInterval < 0 { errs = append(errs, "reconcile.minimum_interval: must be >= 0") }`.

**GREEN:** both pass.

---

## R3 — Docker event stream reconnect is a hot loop

**Where:** `internal/reconcile/daemon.go` `Run` (the `stream.Events` `!ok` branch and the
`stream.Err` case); `daemon.go` `DaemonConfig`.

**Why:** When the stream goroutine in `moby_impl.go` exits (Docker down, socket gone) it
closes `evtCh` and `errCh`. `Run` immediately calls `Events()` again with no delay: if
Docker is unavailable this loops as fast as the CPU allows, re-triggering passes. Also
`case err := <-stream.Err` fires on the *closed* channel with a nil error, logging bogus
"stream error" lines, and the `Reconnected` signal actually means "disconnected".

**RED test** (`daemon_regressions_test.go`):

```go
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
```

Run: `go test -race -run TestDaemon_ReconnectIsRateLimited ./internal/reconcile`
Expected RED: thousands of calls.

**Fix:**
1. Add to `DaemonConfig`: `ReconnectBackoffInitial`, `ReconnectBackoffMax` (`time.Duration`).
   Defaults in `NewDaemon` (`<= 0`): `1s` and `30s`. (Add the fields as a stub commit
   with the RED test if the GREEN-only test below is committed in the same PR.)
2. Add `func reconnectDelay(cfg DaemonConfig, failures int) time.Duration` returning
   `min(cfg.ReconnectBackoffInitial << min(failures, 10), cfg.ReconnectBackoffMax)`.
3. Restructure `Run`'s stream handling:
   ```go
   stream := d.dockerAPI.Events(ctx, prefixes)
   connectedAt := time.Now()
   failures := 0
   var reconnect <-chan time.Time // nil while connected

   // in the select:
   case evt, ok := <-stream.Events:
       if !ok {
           d.log.Warn("docker event stream closed, will reconnect")
           stream.Events, stream.Err, stream.Reconnected = nil, nil, nil // nil channels block forever
           if time.Since(connectedAt) > 30*time.Second { failures = 0 }
           reconnect = time.After(reconnectDelay(d.cfg, failures))
           failures++
           continue
       }
       failures = 0
       // ... existing event handling unchanged ...
   case err, ok := <-stream.Err:
       if !ok { stream.Err = nil; continue }
       d.log.Error("docker event stream error", "error", err)
   case <-reconnect:
       reconnect = nil
       stream = d.dockerAPI.Events(ctx, prefixes)
       connectedAt = time.Now()
       d.Trigger() // reconcile anything missed while disconnected
   ```
4. Change the `stream.Reconnected` case log message to `"docker event stream interrupted, triggering pass"`.

**Second test (GREEN-only behaviour check, add in the fix commit):**
```go
func TestDaemon_ReconnectsAfterBackoff(t *testing.T) {
	cd := &countingDocker{API: docker.NewFake()}
	d := newTestDaemon(t, cd, setupFakeCF(t), reconcile.DaemonConfig{
		Debounce: time.Hour, ResyncInterval: time.Hour, MinimumInterval: time.Minute,
		ReconnectBackoffInitial: 20 * time.Millisecond, ReconnectBackoffMax: 200 * time.Millisecond,
	})
	stop := runDaemon(t, d)
	time.Sleep(time.Second)
	stop()
	n := cd.calls.Load()
	assert.GreaterOrEqual(t, n, int64(3), "must keep reconnecting")
	assert.LessOrEqual(t, n, int64(15), "must not hot-loop")
}
```

**GREEN:** both tests pass; all existing daemon tests pass (they use `fakeDockerWithEvents`,
whose channels never close).

---

## R4 — `Snapshot()` shares `EventCounts` with the writer

**Where:** `daemon.go` `Snapshot()` (returns `d.snap` by value — maps are shared);
writer at the event case in `Run`; reader `internal/web/metrics.go` (iterates
`snap.EventCounts`).

**Why:** The metrics handler iterates the map while the daemon goroutine increments it.
Concurrent map read/write is a fatal runtime error in Go (process abort), and a data race
otherwise.

**RED tests** (`daemon_regressions_test.go`):

```go
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

// Only RED under the race detector (which `make test` always enables).
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
```

Run: `go test -race -run 'TestDaemon_Snapshot' ./internal/reconcile`
Expected RED: first test `expected: 1, actual: 2`; second `WARNING: DATA RACE`.

**Fix:** in `Snapshot()` deep-copy `EventCounts` while holding `d.mu`:
```go
s := d.snap
if d.snap.EventCounts != nil {
    s.EventCounts = make(map[string]map[string]int, len(d.snap.EventCounts))
    for k, v := range d.snap.EventCounts {
        s.EventCounts[k] = maps.Clone(v)
    }
}
return s
```
(`CFStats.ByResource` is already a fresh map per `GetStats()` call — no change.)

**GREEN:** both tests pass under `-race -count=20`.

---

## R12 — `Run` does not wait for in-flight passes on shutdown

**Where:** `daemon.go` `Run` — every `go d.runPass(ctx)` and the `ctx.Done()` branch.
The doc comment on `Run` already promises "waits for any in-progress pass to complete".

**Why:** On cancel, `Run` calls `stopCloudflared` and returns while a pass may still be
creating/deleting DNS or writing state.

**RED test** (`daemon_regressions_test.go`):

```go
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
```

Run: `go test -race -run TestDaemon_ShutdownWaitsForInFlightPass ./internal/reconcile`
Expected RED: `Run returned while a pass was still in flight`.

**Fix:**
1. Add `passWG sync.WaitGroup` to `Daemon`.
2. Add `func (d *Daemon) startPass(ctx context.Context)` doing
   `d.passWG.Add(1); go func() { defer d.passWG.Done(); d.runPass(ctx) }()` and replace all
   three `go d.runPass(ctx)` call sites with `d.startPass(ctx)`. (`Add` is only ever called
   from the `Run` goroutine, which is also the one that `Wait`s — safe.)
3. In the `ctx.Done()` branch, before `stopCloudflared`, wait for passes with a bound:
   ```go
   waited := make(chan struct{})
   go func() { d.passWG.Wait(); close(waited) }()
   select {
   case <-waited:
   case <-time.After(30 * time.Second):
       d.log.Warn("timed out waiting for in-flight reconciliation pass")
   }
   ```

**GREEN:** test passes; `-race -count=20` clean.

---

## R5 — Expiry cleanup: shared hostnames, failed deletes, dry-run state

Three related defects in `reconcile.go` step 8/9 (`deleteDNSIfOrphaned`, state save).
Implement as three commits (5a, 5b, 5c), each RED then GREEN.

### R5a — Deleting DNS still needed by a sibling rule

**Why:** Rule keys include the path, so `app.example.com` and `app.example.com|/api` are
separate rules sharing one CNAME. When the `/api` rule expires, its hostname's CNAME is
deleted although the root rule still needs it.

**RED test** (`engine_regressions_test.go`):

```go
func TestReconcile_ExpiringPathRuleKeepsDNSForSharedHostname(t *testing.T) {
	ctx := context.Background()
	statePath := filepath.Join(t.TempDir(), "state.json")
	fake := setupFakeCF(t)
	fake.AddDNSRecord("zone-1", cf.DNSRecord{
		ID: "rec-1", Name: "app.example.com", Type: "CNAME",
		Content: "tunnel-uuid.cfargotunnel.com", Proxied: true, ZoneID: "zone-1",
	})
	dockerAPI := newStubDocker()
	root := testRule("app.example.com", "", "http://app:80", "app")
	api := testRule("app.example.com", "/api", "http://api:80", "api")
	dockerAPI.setRules(root, api)
	eng := newEngineAPI(t, dockerAPI, fake, statePath, 0) // grace 0: expires one pass after going pending

	require.NoError(t, eng.Run(ctx).LastError) // both active
	dockerAPI.setRules(root)                    // the /api container goes away
	require.NoError(t, eng.Run(ctx).LastError) // /api -> pending
	require.NoError(t, eng.Run(ctx).LastError) // /api -> expired

	recs, err := fake.ListDNSRecords(ctx, "zone-1", "app.example.com", "CNAME")
	require.NoError(t, err)
	assert.Len(t, recs, 1, "CNAME is still needed by the root rule")
}
```
Expected RED: `Should have 1 item(s), but has 0`.

**Fix:** in `Engine.Run` step 8, build `inUse := map[string]struct{}` of lowercased
hostnames from every rule in `merged` (active+pending). When processing an expired rule,
skip `deleteDNSIfOrphaned` if `strings.ToLower(rule.Hostname)` is in `inUse`.

### R5b — A failed DNS delete orphans the record forever

**Why:** `deleteDNSIfOrphaned` swallows errors; `rules.Apply` has already removed the
expired rule from state, so nothing retries.

**RED test:**

```go
func TestReconcile_FailedDNSDeleteIsRetried(t *testing.T) {
	ctx := context.Background()
	statePath := filepath.Join(t.TempDir(), "state.json")
	fake := setupFakeCF(t)
	fake.AddDNSRecord("zone-1", cf.DNSRecord{
		ID: "rec-1", Name: "app.example.com", Type: "CNAME",
		Content: "tunnel-uuid.cfargotunnel.com", Proxied: true, ZoneID: "zone-1",
	})
	cfAPI := &flakyCF{Fake: fake}
	dockerAPI := newStubDocker()
	dockerAPI.setRules(testRule("app.example.com", "", "http://app:80", "app"))
	eng := newEngineAPI(t, dockerAPI, cfAPI, statePath, 0)

	require.NoError(t, eng.Run(ctx).LastError) // active
	dockerAPI.setRules()                        // container gone
	require.NoError(t, eng.Run(ctx).LastError) // pending
	cfAPI.failDeleteDNS.Store(true)
	require.NoError(t, eng.Run(ctx).LastError) // expired, delete fails

	st, err := state.Load(statePath)
	require.NoError(t, err)
	assert.Contains(t, st.Rules, "app.example.com", "rule must stay in state until its DNS is deleted")

	cfAPI.failDeleteDNS.Store(false)
	require.NoError(t, eng.Run(ctx).LastError) // retry

	recs, _ := fake.ListDNSRecords(ctx, "zone-1", "app.example.com", "CNAME")
	assert.Empty(t, recs)
	st, err = state.Load(statePath)
	require.NoError(t, err)
	assert.NotContains(t, st.Rules, "app.example.com")
}
```
Expected RED: `"map[]" should contain "app.example.com"`.

**Fix:** change `deleteDNSIfOrphaned` to return `error` (return the first
`ResolveZone`/`ListDNSRecords`/`DeleteDNSRecord` error; `nil` when nothing to delete or
dry-run). In step 8, on error: log a warning and **re-insert** the rule into `st.Rules`
unchanged (`st.Rules[key] = rule` — it is still `pending` with `DeleteAfter` in the past,
so the next pass classifies it as expired again and retries). On success, `delete(st.Rules, key)`.
Do **not** put it back in the `pending` map used for ingress rendering (ingress was
already rendered).

### R5c — Dry-run must not persist state

**Why:** `Engine.Run` always calls `state.Save`. In dry-run, expired rules are removed
from state while the DNS delete is only logged, so a dry run silently orphans records and
advances rule lifecycles.

**RED test:**

```go
func TestReconcile_DryRunDoesNotPersistState(t *testing.T) {
	ctx := context.Background()
	statePath := filepath.Join(t.TempDir(), "state.json")
	dockerAPI := newStubDocker()
	dockerAPI.setRules(testRule("app.example.com", "", "http://app:80", "app"))
	eng := newEngineAPI(t, dockerAPI, setupFakeCF(t), statePath, time.Hour, reconcile.WithDryRun(true))

	require.NoError(t, eng.Run(ctx).LastError)

	_, err := os.Stat(statePath)
	assert.True(t, os.IsNotExist(err), "dry-run must not write the state file")
}
```
Expected RED: `Should be true` (file exists).

**Fix:** guard step 9: `if !e.dryRun { state.Save(...) }`. (Existing dry-run tests do not
assert on the state file, so none should break.)

**GREEN (R5 overall):** all three tests pass; `TestReconcile_removal_order` and the rest of
`internal/reconcile` still pass.

---

## R9 — Retry backoff uses total pass count

**Where:** `daemon.go` `runPass` (`backoffFor(d.cfg, d.snap.PassCount)`, read twice and
without the lock).

**Why:** `PassCount` is the number of passes ever run. After a long healthy period a single
failure gets the *maximum* backoff. Backoff must depend on *consecutive failures*.

**RED test** (`daemon_regressions_test.go`):

```go
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
```
Expected RED: Eventually times out (retry is scheduled ~640ms out).

**Fix:** add `consecutiveFailures int` to `Daemon` (guarded by `d.mu`). In `runPass`'s
state-update block: if `res.LastError != nil` increment it, else reset to 0; copy the value
to a local while holding the lock and compute `retryAfter := backoffFor(d.cfg, failures)`
from the local (also use it in the log line — remove the unlocked `d.snap.PassCount`
reads). `backoffFor` itself is unchanged (`passCount<=1 → initial`).

**GREEN:** test passes; `-race -count=20` clean.

---

## R7 — Engine ignores `labels.allow_unprotected_access`

**Where:** `reconcile.go` `Engine.Run` calls `DesiredRules(ctx, e.prefixes, false)`;
`cmd/run.go` never passes the config value. `cmd/plan.go` and `cmd/rules.go` *do* use
`cfg.Labels.AllowUnprotectedAccess`, so `plan` can show rules the daemon never applies.

**RED test** (`engine_regressions_test.go`), after adding the **stub** option
`reconcile.WithAllowUnprotectedAccess(bool)` (stores an `allowUnprotected` field on
`Engine`, nothing reads it yet — rule 4):

```go
func TestEngine_PassesAllowUnprotectedAccessToDocker(t *testing.T) {
	dockerAPI := newStubDocker()
	eng := newEngineAPI(t, dockerAPI, setupFakeCF(t),
		filepath.Join(t.TempDir(), "state.json"), time.Hour,
		reconcile.WithAllowUnprotectedAccess(true))

	require.NoError(t, eng.Run(context.Background()).LastError)

	assert.True(t, dockerAPI.lastAllowUnprotected())
}
```
Expected RED: `Should be true`.

**Fix:** use `e.allowUnprotected` in the `DesiredRules` call; in `cmd/run.go` add
`reconcile.WithAllowUnprotectedAccess(cfg.Labels.AllowUnprotectedAccess)` to the
`reconcile.New(...)` options.

**GREEN:** test passes; default (`false`) behaviour unchanged.

---

## R6 — Duplicate-hostname winner is random in the real Docker implementation

**Where:** `internal/docker/moby_impl.go` `DesiredRules` — `for _, c := range containerMap`
iterates a map. The comment says "first container by sorted name wins". `fake.go` *does*
sort, so tests never saw the bug.

**Why:** Two containers claiming the same hostname → the winner (and thus the ingress
service) can change between passes → ingress PUT flapping.

**Step 0 (refactor only, no behaviour change):** create `internal/docker/desired.go`:
```go
// mergeDesiredRules parses labels for each container and merges the rules,
// reporting duplicate keys as "conflict" issues.
func mergeDesiredRules(containers map[string]Container, prefixes []string, allowUnprotected bool) (map[string]labels.Rule, []labels.Issue)
```
Move the existing parse-and-merge loop from `mobyAPI.DesiredRules` into it **verbatim**
(still ranging over the map). `mobyAPI.DesiredRules` builds `containerMap` and calls it.
Leave `fake.go` untouched.

**RED test** (`internal/docker/desired_test.go`, package `docker`):

```go
func TestMergeDesiredRules_DuplicateWinnerIsDeterministic(t *testing.T) {
	names := []string{"delta", "alpha", "echo", "charlie", "bravo", "foxtrot", "golf", "hotel"}
	for i := 0; i < 100; i++ {
		containers := map[string]Container{}
		for j, n := range names {
			id := fmt.Sprintf("id-%d", j)
			containers[id] = Container{ID: id, Name: n, State: "running", Labels: map[string]string{
				"cloudflare.tunnel.enable":   "true",
				"cloudflare.tunnel.hostname": "app.example.com",
				"cloudflare.tunnel.service":  "http://" + n + ":80",
			}}
		}
		rules, issues := mergeDesiredRules(containers, []string{"cloudflare.tunnel"}, false)
		require.Len(t, rules, 1)
		assert.Equal(t, "alpha", rules["app.example.com"].ContainerName, "iteration %d", i)
		assert.Len(t, issues, len(names)-1)
	}
}
```
Run: `go test -race -run TestMergeDesiredRules ./internal/docker`
Expected RED: fails on almost every iteration (winner is random).

**Fix:** in `mergeDesiredRules`, build a slice of containers and sort it by `(Name, ID)`
before the loop; iterate the slice. Keep issue order = processing order.

**GREEN:** test passes (100 iterations); existing `TestDesiredRules_*` pass.

---

## R8 — One invalid `path`/`service` label blocks ingress for everyone

**Where:** `internal/labels/labels.go` `buildRule`, `validateService`.

**Why:** `path` is never validated (cloudflared treats it as a Go regexp) and
`http://` (no host) passes the scheme-only service check. Cloudflare rejects the whole
tunnel config PUT, so `putIngressIfChanged` fails every pass, DNS work is skipped, and
*all* containers are affected by one bad label.

**RED tests** (`internal/labels/labels_test.go`, package `labels`):

```go
func baseLabels(extra map[string]string) map[string]string {
	m := map[string]string{
		"cloudflare.tunnel.enable":   "true",
		"cloudflare.tunnel.hostname": "a.example.com",
		"cloudflare.tunnel.service":  "http://web:80",
	}
	for k, v := range extra {
		m[k] = v
	}
	return m
}

func TestParse_RejectsInvalidPathRegex(t *testing.T) {
	rules, issues := Parse("web", baseLabels(map[string]string{"cloudflare.tunnel.path": "(["}),
		[]string{"cloudflare.tunnel"})
	assert.Empty(t, rules)
	require.Len(t, issues, 1)
	assert.Equal(t, "invalid", issues[0].Kind)
	assert.Equal(t, "path", issues[0].Field)
}

func TestParse_RejectsServiceWithoutHost(t *testing.T) {
	rules, issues := Parse("web", baseLabels(map[string]string{"cloudflare.tunnel.service": "http://"}),
		[]string{"cloudflare.tunnel"})
	assert.Empty(t, rules)
	require.Len(t, issues, 1)
	assert.Equal(t, "service", issues[0].Field)
}
```
Expected RED: `rules` is non-empty in both.

**Fix:**
1. In `buildRule`, after the service check: if `pathVal.value != ""`, require
   `regexp.Compile(pathVal.value)` to succeed; otherwise return
   `Issue{Kind:"invalid", Field:"path", Detail: fmt.Sprintf("invalid path regexp %q: %v", ...)}`.
2. In `validateService`, for schemes other than `unix`/`unix+tls`, parse with
   `net/url.Parse` and require `u.Host != ""`. `http_status:` values and `unix`/`unix+tls`
   are exempt. Keep the existing scheme allow-list.
3. Make sure all existing label tests still pass (they use well-formed values).

**GREEN:** both tests pass; `internal/labels` fully green.

---

## R11 — `ingressEqual` treats nil and empty `OriginRequest` as different

**Where:** `reconcile.go` `ingressEqual`.

**Why:** If Cloudflare returns `"originRequest": {}` for rules that have none (the SDK
wrapper then produces a non-nil empty struct), local `nil` vs remote `&{}` is "different"
every pass → a PUT and an "ingress updated" log on every resync. *(This depends on
Cloudflare's actual response shape and was not verified against the real API; the
normalisation is harmless either way.)*

**RED test** (`engine_regressions_test.go`):

```go
func TestReconcile_EmptyRemoteOriginRequestIsNotADifference(t *testing.T) {
	ctx := context.Background()
	fake := setupFakeCF(t)
	fake.SetIngress("tunnel-uuid", cf.IngressConfig{Version: 3, Rules: []rules.IngressEntry{
		{Hostname: "app.example.com", Service: "http://app:80", OriginRequest: &rules.OriginRequest{}},
		{Service: "http_status:404", OriginRequest: &rules.OriginRequest{}},
	}})
	fake.AddDNSRecord("zone-1", cf.DNSRecord{
		ID: "rec-1", Name: "app.example.com", Type: "CNAME",
		Content: "tunnel-uuid.cfargotunnel.com", Proxied: true, ZoneID: "zone-1",
	})
	dockerAPI := newStubDocker()
	dockerAPI.setRules(testRule("app.example.com", "", "http://app:80", "app"))
	eng := newEngineAPI(t, dockerAPI, fake, filepath.Join(t.TempDir(), "state.json"), time.Hour)

	res := eng.Run(ctx)

	require.NoError(t, res.LastError)
	assert.False(t, res.IngressUpdated)
	assert.Equal(t, 0, fake.IngressPutCalls)
}
```
Expected RED: `Should be false` / `expected: 0, actual: 1`.

**Fix:** in `ingressEqual` replace the nil-vs-non-nil logic with a normaliser:
```go
norm := func(o *rules.OriginRequest) rules.OriginRequest {
    if o == nil { return rules.OriginRequest{} }
    return *o
}
if norm(ae.OriginRequest) != norm(be.OriginRequest) { return false }
```

**GREEN:** test passes; existing ingress tests pass.

---

## R10 — `enable` value matching is inconsistent

**Where:** `moby_impl.go` `DesiredRules` lists with the exact filter `prefix + ".enable=true"`,
but `labels.parseBool` accepts `true/TRUE/1`. `hasEnableLabel` accepts *any* value
(including `false`), so disabled containers trigger passes.

**RED tests:**

`internal/docker/moby_impl_test.go` (package `docker`; create if absent):
```go
func TestHasEnableLabel_RespectsValue(t *testing.T) {
	p := []string{"cloudflare.tunnel"}
	assert.False(t, hasEnableLabel(map[string]string{"cloudflare.tunnel.enable": "false"}, p))
	assert.True(t, hasEnableLabel(map[string]string{"cloudflare.tunnel.enable": "TRUE"}, p))
	assert.True(t, hasEnableLabel(map[string]string{"cloudflare.tunnel.enable": "1"}, p))
	assert.False(t, hasEnableLabel(map[string]string{"other.enable": "true"}, p))
}

func TestEnableFilter_IsKeyOnly(t *testing.T) {
	assert.Equal(t, "cloudflare.tunnel.enable", enableFilter("cloudflare.tunnel"))
}
```
Stub for compile (rule 4): `func enableFilter(prefix string) string { return prefix + ".enable=true" }`.
Expected RED: first test fails on the `"false"` assertion; second `expected "cloudflare.tunnel.enable"`.

**Fix:**
1. Export from `labels`: `func IsEnabled(v string) bool { return parseBool(v) == trueBool }` and use it inside `parseOne`.
2. `hasEnableLabel`: `if v, ok := attrs[p+".enable"]; ok && labels.IsEnabled(v) { return true }`.
3. `enableFilter` returns `prefix + ".enable"` (key-only Docker label filter); use it in `DesiredRules`.
   `labels.Parse` already ignores containers whose enable value is not truthy.
4. Update `fake.go` `ListContainers`/`splitLabelFilter`: a filter with no `=` matches by key presence. Add a test in `docker_test.go` for that.

**GREEN:** tests pass; all `TestDesiredRules_*` pass.

---

## R15 — Dry-run fails the pass if the tunnel does not exist

**Where:** `reconcile.go` `ensureTunnel`: in dry-run it logs `would_create_tunnel` and then
`return cf.Tunnel{}, err`, so the pass still ends with `LastError` and the daemon schedules retries.

**RED test** (`engine_regressions_test.go`):
```go
func TestReconcile_DryRunMissingTunnelIsNotAnError(t *testing.T) {
	fake := cf.NewFake() // no tunnel registered
	fake.AddZone(cf.Zone{ID: "zone-1", Name: "example.com"})
	eng := newEngineAPI(t, newStubDocker(), fake,
		filepath.Join(t.TempDir(), "state.json"), time.Hour, reconcile.WithDryRun(true))

	res := eng.Run(context.Background())

	assert.NoError(t, res.LastError)
	assert.Contains(t, res.PlannedActions, "would_create_tunnel")
}
```
First grep for existing tests asserting `ErrNotFound` on a *dry-run* engine and update
them in the fix commit if they exist (rule 9); a non-dry-run missing tunnel must still error.

Expected RED: `Received unexpected error: ...not found`.

**Fix:** in `Engine.Run` step 2: if `e.dryRun && errors.Is(err, cf.ErrNotFound)`, append
`"would_create_tunnel"` to `res.PlannedActions`, log it, and `return res` with
`LastError == nil` (skip the remaining Cloudflare steps — there is no tunnel ID).
`ensureTunnel` keeps returning the error; the decision lives in `Run`.

**GREEN:** test passes; non-dry-run missing-tunnel test still gets `cf.ErrNotFound`.

---

## R14 — `DeleteDNSRecord` tries every zone and swallows real errors

**Where:** `internal/cf/sdk.go` `DeleteDNSRecord(ctx, recordID)`; interface in `cf.go`;
`fake.go`; sole production caller `reconcile.go` `deleteDNSIfOrphaned` (which already
knows `zoneID`).

**Why:** One DELETE per zone until one succeeds, plus a `ListZones` call per deletion;
429/5xx/permission errors are treated as "wrong zone, try next" and finally reported as
"not found in any zone".

**Step 0 (signature stub):** change to `DeleteDNSRecord(ctx, zoneID, recordID string) error`
everywhere: `cf.API`, `sdkAPI` (ignore `zoneID` for now), `Fake` (ignore `zoneID` for now),
the `flakyCF` test helper, `cf_test.go` (lines ~218 and ~719), and `deleteDNSIfOrphaned`
(pass `zoneID`). `go build ./... && go test ./...` must still pass.

**RED test** (`internal/cf/sdk_delete_test.go`, package `cf`; imports:
`net/http`, `net/http/httptest`, `fmt`, `io`, `log/slog`, `sync`, `testing`, testify,
`cfdns "github.com/cloudflare/cloudflare-go"`):

```go
func TestSDKDeleteDNSRecord_UsesGivenZoneOnly(t *testing.T) {
	var mu sync.Mutex
	var hits []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits = append(hits, r.Method+" "+r.URL.Path)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodDelete && r.URL.Path == "/zones/zone-2/dns_records/rec-1":
			fmt.Fprint(w, `{"success":true,"errors":[],"messages":[],"result":{"id":"rec-1"}}`)
		case r.Method == http.MethodGet && r.URL.Path == "/zones":
			fmt.Fprint(w, `{"success":true,"errors":[],"messages":[],"result":[{"id":"zone-1","name":"example.com"},{"id":"zone-2","name":"example.org"}],"result_info":{"page":1,"per_page":50,"total_pages":1,"count":2,"total_count":2}}`)
		default:
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `{"success":false,"errors":[{"code":81044,"message":"Record does not exist."}],"messages":[],"result":null}`)
		}
	}))
	defer srv.Close()

	api, err := cfdns.NewWithAPIToken("token", cfdns.BaseURL(srv.URL))
	require.NoError(t, err)
	s := &sdkAPI{
		cf: api, log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		zoneResolver: NewZoneResolver(""), stats: Stats{ByResource: map[string]int{}},
	}

	require.NoError(t, s.DeleteDNSRecord(context.Background(), "zone-2", "rec-1"))

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, []string{"DELETE /zones/zone-2/dns_records/rec-1"}, hits)
}
```
If the request paths differ from the ones in the handler (SDK version differences), print
`hits`, adapt the `case` paths, and keep the final assertion's intent: **exactly one
request, a DELETE to the given zone, and no zone listing.**

Expected RED: `hits` contains `GET /zones` and a DELETE to `zone-1` first.

**Fix:** `sdkAPI.DeleteDNSRecord` does a single `s.cf.DeleteDNSRecord(ctx, cfdns.ZoneIdentifier(zoneID), recordID)`,
counting the call and (on error) the error, and returning `fmt.Errorf("delete DNS record: %w", err)`.
`Fake.DeleteDNSRecord` should only search the given zone and return an error if the
record is missing there (update the two `cf_test.go` tests accordingly).

**GREEN:** new test passes; `go test ./internal/cf ./internal/reconcile` green.

---

## 18. Minor cleanups (no RED test required; must not break any test)

Do these last, one small commit each (`chore:`/`fix:` as appropriate).

- **M1** — *(done as part of R2)* debounce timer armed at startup.
- **M2** — `moby_impl.go` `FindContainerByName`: guard `len(s.Names) == 0` before `s.Names[0]`;
  wrap the name with `regexp.QuoteMeta` in the `^/…$` filter; make the "primary network"
  deterministic (sort network names). Better: add `Networks []string` to `Container` and
  have `EnsureCloudflared` warn only if `cfg.Network` is not in the list.
- **M3** — `labels.go` `hostnameRe`: allow punycode TLDs (`xn--…`) and reject hostnames
  longer than 253 chars. Add a tiny `TestParse_AcceptsPunycodeTLD` (RED → GREEN) in `labels_test.go`.
- **M4** — `cmd/run.go`: after the early `select` consumes `webErr`, the later shutdown
  `select` waits 5s on a channel that never delivers and logs a bogus timeout. Track whether
  `webErr` was already received (or make it a buffered channel you close) so shutdown
  doesn't wait/warn spuriously.
- **M5** — Stale comments: `daemon.go` `Run` ("with label filters so only containers…"
  — filtering is client-side now); `docker/docker.go` `Event.Action` comment lists
  `stop`/`destroy` (only `start`/`die` are subscribed); `sdk.go` `GetZone` has an unused
  `rc` (`_ = rc`) and lists all zones to find one.
- **M6** — `sdk.go` `ResolveZone`: `resolver` can be nil but is dereferenced in
  `resolver.SetZones`; check for nil first. Also don't ignore a `ListZones` error silently
  (log at Warn).
- **M7** — `sdk.go` `Plan`: DNS/orphan action lists come from map iteration, so `plan`
  output order is nondeterministic. Sort keys before iterating.
- **M8** — `Plan` orphan check compares `rule.Hostname == rec.Name` case-sensitively;
  use `strings.EqualFold`.

---

## 19. Final checklist (before declaring the review done)

- [ ] Every R-item has a merged RED commit followed by a GREEN commit (or a documented
      reason it was skipped).
- [ ] `make ci` passes on the final branch.
- [ ] `go test -race -count=5 ./...` passes (timing tests included).
- [ ] No test was deleted or weakened (rule 9); any edited existing test is explained.
- [ ] `docs/FUTURE.md` lists anything discovered but out of scope.
