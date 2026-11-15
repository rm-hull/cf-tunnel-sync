# PLAN.md: cf-tunnel-sync

A small Go daemon that keeps a Cloudflare Tunnel's ingress rules and DNS records in sync with Docker container labels, plus a read-only status page.
It is a behavioural reimplementation of the core of [DockFlare](https://github.com/ChrispyBacon-dev/DockFlare/tree/v2.1.7) **v2.1.7** (label-driven only):
no web configuration, no auth, no redis, no email, no hand-managed rules.

| | |
|---|---|
| Repo | `github.com/rm-hull/cf-tunnel-sync` |
| Go module | `github.com/rm-hull/cf-tunnel-sync` |
| Binary / image | `cf-tunnel-sync` / `ghcr.io/rm-hull/cf-tunnel-sync` |
| Go | `1.27` |

> `cfargotunnel.com` below is Cloudflare's DNS suffix for tunnel CNAME targets (`<TUNNEL_UUID>.cfargotunnel.com`).

---

## 0. How the implementing agent must work

1. **One step = one pull request** (branch `step/Sn-<slug>`), Conventional Commits. Do not start step N+1 until step N's **gate** passes.
2. **Test-first, RED/GREEN (mandatory).** For every step:
   1. **RED.** Write the tests listed under the step's *RED* heading first. Tests assert the **behaviours specified in this document**, not implementation details. Add minimal compiling stubs (zero values, no logic) so the tests *compile and fail on assertions*. Run them and **paste the failing output** (truncated) into the PR description. A compile error or `panic("unimplemented")` is not an acceptable RED.
   2. Commit as `test(<scope>): <behaviour> (RED)`. This commit may fail CI; the PR's **last** commit must pass.
   3. **GREEN.** Write the least code that makes the tests pass. Commit as `feat(<scope>): ...`.
   4. **REFACTOR** with tests green, only if needed (`refactor(<scope>): ...`).
   5. **Never weaken, delete or skip a test to get green.** If a test encodes a wrong expectation, change this PLAN (or `docs/DECISIONS.md`) first, in its own commit, then the test.
   6. Bug found later → write the failing regression test first.
3. **Gates are of two kinds.**
   - `AUTO`: you run it and show it passing (`make ci`).
   - `HUMAN`: needs the owner's Docker host or Cloudflare account. **Stop, report what you built, list the exact commands for the owner to run, and wait.** Never fake a human gate.
4. **Do not trust library API sketches in this document.** Resolve versions with `go get <module>@latest`, then read the real API with `go doc <pkg>`. If reality differs from the plan, adapt and record the difference in `docs/DECISIONS.md` (one short entry each).
5. **No scope creep.** Extra ideas go in `docs/FUTURE.md`, not in code. Extensions (§11) are **not** built.
6. **No secrets** in the repo, tests, logs or status page. Use the `Secret` type (§9).
7. **Clean-room.** DockFlare is GPL-3.0. Implement from the behaviour documented here; do not copy its source.
8. Every PR ends with `make ci` green: `gofmt/gofumpt`, `go vet ./...`, `golangci-lint run`, `go test -race ./...`.

---

## 1. Goals / non-goals

**Goals**
- Configuration **only** via `config.yaml` (secrets referenced from environment) and Docker labels.
- Tunnel config is **derived solely from Docker labels**: anything in the remote ingress that is not produced by a label is removed.
- Label compatibility with DockFlare v2.1.7 **and** the `dockflare.*` dialect (§3).
- Maintain: tunnel ingress (remote-managed config), proxied CNAME DNS records, the `cloudflared` container lifecycle, grace-period removal.
- Status page (HTML) + JSON + health endpoints + structured JSON logs.

**Non-goals:** web configuration, authentication, email, multi-host agents, backup/restore UI, i18n, Redis, manual-rule support, Access applications (see Extension E1).

---

## 2. Stack decisions (resolved)

| Concern | Choice | Notes |
|---|---|---|
| Go | `go 1.27` in `go.mod` | CI: `actions/setup-go` with `go-version-file: go.mod`; Docker build image `golang:1.27` |
| CLI | `github.com/spf13/cobra` | **No viper**: its key normalisation and weak unknown-key detection conflict with §4 |
| HTTP/UI | `github.com/gin-gonic/gin` (latest; v1.12 needs ≥ Go 1.25) | `gin.New()` + own middleware; `gin.ReleaseMode` |
| Styling | **Tailwind CSS v4** via the **standalone CLI (no Node)** | compiled CSS committed and embedded (§8.3) |
| Logging | stdlib `log/slog` + `slog.NewJSONHandler` | one handler for the process |
| Request logs | `github.com/samber/slog-gin` (v1.21.x) | skip `/healthz`, `/readyz` |
| Cloudflare | `github.com/cloudflare/cloudflare-go/v7` | v7 is the current major |
| Docker | `github.com/moby/moby/client` + `github.com/moby/moby/api` | **`github.com/docker/docker` has no v29 tags; do not use it.** moby client is pre-1.0: pin exact version, wrap behind our own interface |
| YAML | `go.yaml.in/yaml/v3` (maintained fork of `gopkg.in/yaml.v3`; confirm it is current, else `goccy/go-yaml`) | strict decoding (`KnownFields(true)`), node-level access for substitution |
| Env substitution | `github.com/a8m/envsubst` (supports `${VAR:-default}`) | applied to scalar nodes only (§4.3); if its semantics don't match §4.3 tests, write a ~30-line replacer |
| JSON schema | `github.com/invopop/jsonschema` | generated from Go structs, as dot-block does |
| Backoff | `github.com/cenkalti/backoff/v5` | event-stream reconnect, reconcile retry |
| Atomic file write | `github.com/google/renameio/v2` | state persistence |
| Metrics (optional) | `github.com/prometheus/client_golang` | owner runs Prometheus |
| Tests | stdlib `testing`, `github.com/stretchr/testify`, `net/http/httptest`, `testing/synctest` | CF SDK pointed at httptest via `option.WithBaseURL`; golden files in `testdata/` |
| Templates | `html/template` + `embed` | no JavaScript |
| Lint | `golangci-lint` v2 | `govet, staticcheck, errcheck, gosec, revive, gofumpt, errorlint, bodyclose, noctx` |

**Library rules**
- Never hand-roll an HTTP client for Cloudflare or Docker.
- Wrap each SDK in an **interface owned by this repo** (`internal/cf.API`, `internal/docker.API`) using our own domain types. Business logic never imports SDK types.
- Count Cloudflare API calls via the SDK middleware hook (`option.WithMiddleware`); use SDK auto-paging iterators and built-in retries (`option.WithMaxRetries`).

---

## 3. Label compatibility specification

### 3.1 Prefixes
Other services in the owner's environment use **`dockflare.*`**; this stack uses **`cloudflare.tunnel.*`**. Both are first-class.

- `labels.prefixes` (config): ordered by priority. Default `[cloudflare.tunnel, dockflare]`.
- A container is *managed* if **any** prefix has `<prefix>.enable` = `true` (case-insensitive).
- Field lookup: the first prefix in order that defines the field wins. If a lower-priority prefix defines the same field with a **different** value → `WARN label_conflict` (once per container+field per process), shown on the status page.
- Unrecognised labels under a configured prefix → `WARN unknown_label` (once), never an error (may be newer-DockFlare keys).

### 3.2 Grammar
```
<prefix>.<field>             single rule (non-indexed)
<prefix>.<N>.<field>         indexed rule, N = 0,1,2...
```

| Field | Type | Req | Meaning |
|---|---|---|---|
| `enable` | bool | yes, **non-indexed only** | master switch (indexed `enable` ignored) |
| `hostname` | string | yes | public hostname; leading `*.` wildcard allowed |
| `service` | URL | yes | origin, e.g. `http://whoami:80`; schemes `http https tcp ssh rdp smb unix unix+tls`, or `http_status:NNN` |
| `path` | string | no | cloudflared ingress path (a **regex**; passed through verbatim) |
| `zonename` | string | no | force DNS zone by name |
| `no_tls_verify` | bool | no | → `originRequest.noTLSVerify` |
| `originsrvname` | string | no | → `originRequest.originServerName` |
| `access.*` | various | no | see §3.4 |

### 3.3 Rule derivation (deterministic)
1. Collect indices `N` present across **all** prefixes.
2. No indices → **one rule** from non-indexed fields.
3. Indices present → **one rule per index**; non-indexed fields are **defaults only** and do not form an extra rule. *(VERIFY against v2.1.7 using the S0 baseline; record the result in DECISIONS.md.)*
4. Per field in scope `N`: lookup order is indexed (prefix order) → non-indexed (prefix order).
5. Validate: `hostname`, `service` required; hostname is RFC 1123 with optional leading `*.`; scheme allowed; bools parse (`true/false/1/0`, any case). Invalid rule → **not published**, status `invalid` with reason.
6. Rule key: `lower(hostname) + "|" + path`.
7. Duplicate key across containers: the container whose name sorts first wins; others are `conflict` (reported, not published, WARN).

### 3.4 Access labels: fail-closed in the core
Access applications are **not implemented** (Extension E1). Publishing a rule whose labels ask for protection would silently drop it, so a rule carrying any of `access.policy=authenticate`, `access.group`, `access.email`, `access.custom_rules` is **blocked** (status `blocked_access_unsupported`; not added to ingress or DNS), unless `labels.allow_unprotected_access: true`.
Other access keys (`access.policy=bypass|default_tld`, `session_duration`, `name`, `app_launcher_visible`, `allowed_idps`) → publish normally, `INFO access_label_ignored`.

---

## 4. Configuration (`config.yaml`)

Mirrors the approach used in `rm-hull/dot-block` (documented behaviour; its `config.go` could not be read while writing this plan. If the agent can read `rm-hull/dot-block/internal/config/config.go`, align structure and naming with it wherever it doesn't contradict this section).

### 4.1 Discovery (first match wins)
1. `--config <path>` (root persistent flag)
2. `./config.yaml`, then `./config.yml`
3. `$XDG_CONFIG_DIRS/cf-tunnel-sync/config.yaml` (default `/etc/xdg`; each colon-separated dir in order)
4. `$XDG_CONFIG_HOME/cf-tunnel-sync/config.yaml` (default `~/.config`)

No file found → start from defaults; validation then fails with a message listing every path searched.

### 4.2 Precedence (later overrides earlier)
1. built-in defaults → 2. config file (after `${VAR}` substitution) → 3. a **small, explicit** set of environment overrides → 4. CLI flags (`--config`, plus per-command flags such as `--json`, `--dry-run`, `--url`).

Environment overrides (the *entire* list; everything else, including all secrets, goes through `${VAR}` in the file):

| Env | Overrides |
|---|---|
| `LOG_LEVEL` | `server.log_level` |
| `DRY_RUN` | `server.dry_run` |
| `LISTEN_ADDR` | `server.listen` |
| `DOCKER_HOST` | `docker.host` (also honoured natively by the moby client when `docker.host` is empty) |

### 4.3 Substitution rules
- `${VAR}` and `${VAR:-default}` are expanded **only inside YAML scalar string values**, after parsing (so an env value containing `: `, `#` or newlines can never change the document structure).
- `${VAR}` with `VAR` unset and no default → config error naming the YAML key path (e.g. `cloudflare.api_token: environment variable CF_API_TOKEN is not set`).
- Set-but-empty is allowed (required-field validation reports it separately).
- `$$` escapes a literal `$`.

### 4.4 Schema (all keys optional unless marked; unknown keys are an error)
```yaml
$schema: https://raw.githubusercontent.com/rm-hull/cf-tunnel-sync/main/config.schema.json

server:
  listen: ":5000"                 # status page
  log_level: INFO                 # DEBUG | INFO | WARN | ERROR
  log_format: json                # json | text (text for local dev only)
  state_path: /data/state.json
  status_refresh: 15s             # meta-refresh on the HTML page; 0 disables
  metrics: false                  # expose /metrics
  dry_run: false                  # log would_* actions, make no mutating calls

cloudflare:
  api_token: ${CF_API_TOKEN}      # REQUIRED (Secret)
  account_id: ${CF_ACCOUNT_ID}    # REQUIRED
  zone_id: ${CF_ZONE_ID:-}        # optional fallback zone

tunnel:
  name: homelab                   # REQUIRED unless external_id is set
  external_id: ""                 # set => use an externally-run cloudflared; no container management
  cloudflared:
    container_name: ""            # default: cloudflared-<tunnel.name>
    image: cloudflare/cloudflared:latest
    network: cloudflare-net
    metrics_port: 2000

docker:
  host: ""                        # default: DOCKER_HOST / platform socket

labels:
  prefixes: [cloudflare.tunnel, dockflare]
  allow_unprotected_access: false # see §3.4

reconcile:
  grace_period: 8h                # removal delay after a rule disappears
  resync_interval: 5m             # full resync cadence
  debounce: 2s

dns:
  overwrite_conflicts: false      # see §6.5
```
Durations use Go syntax (`8h`, `5m`, `15s`).

### 4.5 Validation (all errors reported together, each with its key path)
`cloudflare.api_token`, `cloudflare.account_id` non-empty; `tunnel.name` non-empty unless `tunnel.external_id` set; `labels.prefixes` non-empty, unique, no leading/trailing `.`; `log_level`/`log_format` in enum; `reconcile.grace_period ≥ 0`; `reconcile.resync_interval ≥ 10s`; `reconcile.debounce ≥ 0`; `tunnel.cloudflared.metrics_port` 1-65535; `server.listen` parses as host:port.

### 4.6 Types and artefacts
- `config.Secret` (string-based) implements `slog.LogValuer`, `fmt.Stringer`, `fmt.GoStringer`, `json.Marshaler`, `yaml.Marshaler`: all render `[redacted]`. It unmarshals from a plain string.
- `config.schema.json` is **generated from the Go structs** by a test (`go test ./internal/config -run TestSchemaGeneration`), committed, never hand-edited; CI fails if `git diff --exit-code config.schema.json` is non-empty after tests. `$schema` is an accepted top-level key.
- `config.example.yaml` (§12.1) is committed and **must load and validate** in a test (with the referenced env vars set).

### 4.7 v2.1.7 migration map (goes in `docs/MIGRATION.md`)
| v2.1.7 env | New key |
|---|---|
| `CF_API_TOKEN` / `CF_ACCOUNT_ID` / `CF_ZONE_ID` | `cloudflare.api_token` / `account_id` / `zone_id` via `${...}` |
| `TUNNEL_NAME` | `tunnel.name` |
| `LABEL_PREFIX` | `labels.prefixes` (keep `cloudflare.tunnel`, add `dockflare`) |
| `GRACE_PERIOD_SECONDS` | `reconcile.grace_period` (28800 s = `8h`) |
| `CLEANUP_INTERVAL_SECONDS` | `reconcile.resync_interval` |
| `CLOUDFLARED_NETWORK_NAME` | `tunnel.cloudflared.network` |
| `CLOUDFLARED_METRICS_PORT` | `tunnel.cloudflared.metrics_port` |
| `USE_EXTERNAL_CLOUDFLARED` + `EXTERNAL_TUNNEL_ID` | `tunnel.external_id` (non-empty = external) |
| `STATE_FILE_PATH` | **not reused**: new `server.state_path`; never point it at the v2 `state.json` (keeps rollback possible) |
| `DOCKER_HOST` | `docker.host` or env |

---

## 5. Architecture

```
cmd/cf-tunnel-sync/main.go      # 5 lines: cli.Execute()
internal/cli/                   # cobra commands (§7); NewRootCmd(deps) for testability
internal/config/                # load, discover, substitute, validate, Secret, schema
internal/logx/                  # slog setup, ring-buffer handler
internal/labels/                # pure parser: map[string]string → []Rule, []Issue
internal/rules/                 # Rule, key, lifecycle (active→pending→expired), ingress rendering (pure)
internal/state/                 # JSON state, atomic persistence
internal/docker/                # API interface + moby impl: list, events+reconnect, cloudflared container mgmt
internal/cf/                    # API interface + SDK impl: tunnel, config, DNS, zones, connections
internal/reconcile/             # engine (one pass), trigger loop, retry
internal/web/                   # gin router, handlers, templates, assets/ (tailwind input), static/ (built css)
internal/stats/                 # counters, last-N durations, snapshot
testdata/                       # fixtures, golden files
```

Concurrency:
- **One** reconcile goroutine. Triggers (Docker events, resync ticker, retry timer, `SIGHUP`) feed a `chan struct{}` of capacity 1 (coalescing) with `reconcile.debounce` after the first trigger.
- The engine publishes an immutable `Snapshot` via `atomic.Pointer[Snapshot]`; the web layer only reads it.
- Pure packages (`labels`, `rules`) take `now time.Time` as an argument.

---

## 6. Behaviour specification

### 6.1 Desired state from Docker
- For each prefix, list **running** containers with filter `label=<prefix>.enable=true`; merge by container ID. Labels come from the list response.
- Event subscription: type `container`, actions `start, die, stop, destroy`. Relevant events (container has or had a managed label) → trigger. Stream errors reconnect with backoff (cap 30 s) and **always trigger a full pass**.

### 6.2 Rule lifecycle (pure; heavily tested)
`Apply(state, desired, now, grace) → (state', active, pending, expired)`

| Situation | Result |
|---|---|
| in desired, not in state | new `active`; `firstSeen=now` |
| in desired and state (pending) | back to `active`; clear pending fields |
| in state (active), not in desired | `pending`; `pendingSince=now`; `deleteAfter=now+grace` |
| pending and `now >= deleteAfter` | `expired` |
| in desired, fields changed | updated in place |

Pending rules **stay in ingress and DNS** until expiry (a redeploy never causes an outage).

### 6.3 Ingress rendering (pure; golden-tested)
- Input: all `active` + `pending` rules **only**. The remote configuration is never merged in: **labels are the sole source of truth**; any remote rule not produced by a label is removed on the next non-dry-run write.
- Order (first match wins in cloudflared): non-wildcard before wildcard → within a hostname, rules with path before without, longer path first → hostname asc → service asc.
- Entry: `{hostname, path?, service, originRequest?}`; `originRequest` only if `noTLSVerify` or `originServerName` set.
- **Always** end with the catch-all `{service: "http_status:404"}`.
- Apply: `GET` remote config → normalise (drop nil/empty, ignore `version`) → compare → `PUT` only if different. Steady state = zero writes.

### 6.4 Zone resolution (per rule, cached 10 min)
1. `zonename` label → look up by name. 2. Longest-suffix match of hostname against the account's zones (listed once, cached). 3. `cloudflare.zone_id`. 4. Else rule error `no_zone` (still in ingress; DNS untouched).

### 6.5 DNS
- Record: `CNAME <hostname> → <tunnelUUID>.cfargotunnel.com`, `proxied=true`, TTL auto, comment `managed-by=cf-tunnel-sync`.
- Ensure: list by `name`+`type`. Absent → create. Present with correct content → adopt (store record ID). Present with **different** content → do not modify; rule `dns_conflict` unless `dns.overwrite_conflicts`.
- Delete on expiry only if: record content equals our tunnel target **and** no remaining rule uses that hostname.
- **Orphans** (CNAMEs to our tunnel target with no rule in desired/state, e.g. left by v2.1.7): **reported only** (`plan` and status page), never deleted automatically.
- `event` passes skip DNS lookups for rules with `dns.ok`; `resync` passes re-verify all.

### 6.6 Tunnel and cloudflared
- Tunnel: find by name (`is_deleted=false`). Missing → create with `config_src=cloudflare`. >1 match → fatal config error. `config_src != cloudflare` → refuse to manage (error on status page; process stays up).
- External mode (`tunnel.external_id` set): use that tunnel ID; skip all container handling.
- Container (managed mode): running → **adopt as-is** (WARN if not on `tunnel.cloudflared.network`); stopped → start; absent → pull image if missing, create and start with `cmd=["tunnel","--no-autoupdate","--metrics","0.0.0.0:<metrics_port>","run"]`, env `TUNNEL_TOKEN=<token>` (**never** on the command line), `restart=unless-stopped`, attached to the configured network, label `cf-tunnel-sync.managed=true`.
- Health: connection count from the Cloudflare API (cached 30 s); optional best-effort `GET http://<container>:<port>/ready`.

### 6.7 Reconcile pass
```
1  now := time.Now()
2  desired, issues := docker.DesiredRules()
3  (active, pending, expired) := rules.Apply(...)
4  resolve zones for active+pending
5  ingress := rules.RenderIngress(active+pending)
6  cf.PutIngressIfChanged(ingress)            // on failure: skip DNS work this pass
7  for r in active:  dns.Ensure(r)             // per-rule error isolation
8  for r in expired: dns.DeleteIfOrphaned(r); drop from state
9  ensure cloudflared (first pass and each resync)
10 persist state (atomic); publish Snapshot; log one summary line
```
Ingress is always written **before** DNS changes (add and remove alike). A failed pass retries with backoff (30 s → 5 min). Per-rule failures never block other rules. `server.dry_run`: every mutating call is replaced by a `would_*` log line + recorded planned action (this is also what `plan` prints).

### 6.8 State file (`server.state_path`)
```json
{
  "version": 1,
  "tunnel": {"id": "uuid", "name": "homelab"},
  "rules": {
    "whoami.example.com|": {
      "hostname": "", "path": "", "service": "http://whoami:80",
      "noTLSVerify": false, "originServerName": "",
      "zoneID": "", "container": {"id": "", "name": "whoami"},
      "source": {"prefix": "cloudflare.tunnel", "index": null},
      "firstSeen": "RFC3339", "lastSeen": "RFC3339",
      "pendingSince": null, "deleteAfter": null,
      "dns": {"recordID": "", "ok": true, "checkedAt": "RFC3339"},
      "status": "active", "error": ""
    }
  }
}
```
Unknown `version` → refuse to start. Corrupt file → rename to `.corrupt-<ts>`, start empty, WARN (rebuildable from labels + Cloudflare).

---

## 7. CLI (cobra)

```
cf-tunnel-sync [--config PATH] [--version]
  run                    daemon: events + resync + status page
  reconcile              one-shot pass (honours server.dry_run; --dry-run overrides)
  plan [--json]          one-shot dry run: print the diff, make no mutating calls
  rules [--json]         print desired rules from live Docker
  config print           effective config, secrets as [redacted]
  config validate        exit 0/1; prints every error with its key path
  config schema          print the JSON schema
  healthcheck [--url U]  GET /healthz (default http://localhost:5000/healthz); exit 0/1
```
Conventions: `SilenceUsage: true`; commands use `RunE` and return errors; `main` maps error → exit 1. `NewRootCmd(deps Deps)` takes injectable factories (config loader, `docker.API`, `cf.API`, clock, `io.Writer`s) so tests drive commands with `cmd.SetArgs(...)` and fakes. `run` uses `signal.NotifyContext(SIGINT, SIGTERM)`.

---

## 8. Status page (gin + Tailwind)

### 8.1 Routes
| Route | Purpose |
|---|---|
| `GET /` | server-rendered HTML |
| `GET /static/app.css` | embedded compiled Tailwind CSS (`?v=<sha8>`; `Cache-Control: public, max-age=31536000, immutable`) |
| `GET /api/status` | JSON snapshot |
| `GET /healthz` | liveness: 200 while the process is up |
| `GET /readyz` | 200 only if ≥1 reconcile completed **and** Docker reachable **and** last pass < 3× `resync_interval` ago, else 503 |
| `GET /metrics` | Prometheus (only if `server.metrics`) |

Read-only, **no auth** (documented). Every response carries `Content-Security-Policy: default-src 'none'; style-src 'self'; img-src 'self' data:; base-uri 'none'; form-action 'none'; frame-ancestors 'none'`.

**`/api/status` fields:** `version, commit, startedAt, uptimeSeconds, dryRun, tunnel{name,id,configSrc,connections,status}, cloudflared{mode,container,state,uptimeSeconds,network,ready}, counts{active,pending,invalid,conflict,blocked,dnsConflict,error}, reconcile{count,lastAt,lastDurationMs,avgDurationMs,maxDurationMs,lastError,nextResyncAt,changes{ingressUpdates,dnsCreated,dnsDeleted,rulesExpired}}, cloudflareApi{calls,errors,byResource,lastErrorAt}, docker{connected,eventsReceived,lastEventAt,reconnects}, rules[{hostname,path,service,container,source,status,dns,pendingRemovalInSeconds,firstSeen,error}], orphanDns[], labelWarnings[{container,field,kind,detail}], recentEvents[{time,level,msg,attrs}] (last 100)`.

### 8.2 Page design
- One page, **no JavaScript**, no external fonts/CSS/images. `<meta http-equiv="refresh">` from `server.status_refresh`.
- Opens with one plain sentence ("Tunnel *homelab* is healthy: 3 routes live, 1 pending removal in 7h 12m."); when unhealthy the sentence names the problem first.
- Then: stats strip (connections, last reconcile + duration, API calls/errors, Docker events) → **rules table** (primary) → orphan DNS and label warnings → recent events.
- System UI font stack; monospace only for hostnames/services/IDs. Sentence-case labels; no all-caps eyebrows, gradients or decorative cards. Status is never colour-only (always a text label). Responsive (table stacks under ~640px), visible focus rings, WCAG AA.
- Design tokens in `@theme` (light / dark via `prefers-color-scheme`, which is Tailwind v4's default `dark:` behaviour): `paper #eef1f0` / `night #121715`, `ink #1b2321` / `snow #dfe7e4`, `ok #1f6f5c` / `ok-dark #5fb89f`, `warn #b7791f` / `warn-dark #e0b25a`, `bad #b3402f` / `bad-dark #e07a68`.

### 8.3 Tailwind pipeline (no Node)
- Input `internal/web/assets/tailwind.css`: `@import "tailwindcss"; @source "../templates"; @theme { ...tokens... }`. No `tailwind.config.js`.
- Output `internal/web/static/app.css` (minified) is **committed** and embedded via `//go:embed`, so `go build` and agent sandboxes need no Tailwind or network.
- Tool: Tailwind **standalone CLI** (tailwindlabs release binary). Pin version + sha256 in `tools/tailwind.version`; `make tools` downloads it to `./bin/tailwindcss` and verifies the checksum.
- `make css` regenerates; `make css-check` = `make css && git diff --exit-code internal/web/static/app.css` (CI job).
- Template rule: **complete, static class strings only**; never build class names by concatenation (`bg-{{.}}`); conditional styling selects between full literal class strings.

---

## 9. Logging

- `slog.NewJSONHandler(os.Stdout, ...)`; **one line per event**.
- Common attrs: `component` (`config|docker|cf|reconcile|state|web`), `action`, `rule` (hostname), `container`, `dur_ms`, `err`.
- Reconcile summary (INFO): `msg="reconcile done" trigger=event|resync|retry added updated pending expired dns_created dns_deleted ingress_updated dur_ms`.
- `config.Secret` is the only way a token enters a struct that might be logged (§4.6).
- Fan the handler into an in-memory ring buffer (last 100) for the status page: use `slog.NewMultiHandler` if present in Go 1.27 (`go doc log/slog.NewMultiHandler`), else a ~20-line fan-out handler.
- Request logging via `slog-gin` on the same logger.

---

## 10. Implementation steps

Each step: **RED** (write first; behaviours as test-name sentences), **GREEN** (deliverables), **Gate**, **Commit**.

### S0: Baseline inventory  *(HUMAN; no Go code; parallel with S1-S5)*
Owner captures into `docs/baseline/` (**redact tokens; `.gitignore` anything `*.secret`**):
- `docker inspect` of the current cloudflared container (name, image, command, network, restart policy).
- v2.1.7 environment values (token redacted) for the keys in §4.7, and its `state.json` (redacted).
- Current tunnel ingress config and `config_src`; all DNS CNAMEs targeting `*.cfargotunnel.com`; any `DockFlare-*` Access apps.
- Every container on the host carrying `cloudflare.tunnel.*` **or** `dockflare.*` labels, **including other compose projects**.
- Any ingress rules that are **not** produced by labels (these will be deleted at cutover: the owner confirms that is intended).
**Gate (HUMAN):** `docs/baseline/README.md` summarises the above. Blocks the HUMAN gate of S6, and S7, S12. Resolve each `VERIFY` here and record in `docs/DECISIONS.md`.

### S1: Scaffold, cobra root, logging, agent guidance
**RED**
- `--version` prints version and commit.
- unknown subcommand exits non-zero with a message.
- `--help` lists `run, reconcile, plan, rules, config, healthcheck`.
- logger emits one-line JSON with `time, level, msg` and requested attrs.
- a `config.Secret`-typed value never appears in log output via `slog`, `%v`, `%+v`, `%#v`, JSON.
- ring buffer keeps exactly the last 100 records, oldest dropped.
**GREEN:** `go.mod` (`go 1.27`), layout (§5), `Makefile` (`ci test lint build css css-check tools`), `.golangci.yml`, `.github/workflows/ci.yml`, cobra root + stub subcommands (return "not implemented"), `internal/logx`, `Secret` type. **`AGENTS.md`** (condensed §0 + pointer to PLAN.md) with short pointers `.github/copilot-instructions.md` and `.junie/guidelines.md` → `AGENTS.md`. `docs/extensions/E1-access-applications.md` (copy of §11).
**Gate (AUTO):** `make ci`; CI green.
**Commit:** `chore: scaffold, cobra root, logging, agent guidance`

### S2: Configuration
**RED** (fixtures in `testdata/config/`)
- discovery order: `--config` beats `./config.yaml` beats `./config.yml` beats `$XDG_CONFIG_DIRS` beats `$XDG_CONFIG_HOME`; nothing found → error listing every searched path.
- omitted keys take the §4.4 defaults.
- `${VAR}`, `${VAR:-default}`, `$$` work; unset without default → error naming the key path; an env value containing `: `, `#` or a newline cannot alter document structure.
- unknown key → error with key and line; `$schema` accepted.
- durations parse; invalid duration → error with key path.
- each §4.5 validation rule fails with its key path; multiple errors reported together.
- `LOG_LEVEL`, `DRY_RUN`, `LISTEN_ADDR` override the file; invalid `DRY_RUN` errors; **no other** env var overrides anything.
- `Secret` redacted in `config print` and in JSON/YAML marshalling.
- `config validate` exit codes; `config print` output golden; `config schema` prints the schema.
- `TestSchemaGeneration`: schema has `additionalProperties:false`, every field, and matches the committed file.
- `config.example.yaml` loads and validates.
**GREEN:** `internal/config`, `config` subcommands, `config.schema.json`, `config.example.yaml`, `docs/MIGRATION.md` (§4.7).
**Gate (AUTO):** `make ci`; `git diff --exit-code config.schema.json` clean after tests.
**Commit:** `feat(config): yaml config with env substitution and json schema`

### S3: Label parser (pure)
**RED** (`testdata/labels/*.json`)
- the owner's three real sets under `cloudflare.tunnel.*` yield the expected rules: `whoami` (`whoami.destructuring-bind.org` → `http://whoami:80`), `vaultwarden` (`vault.destructuring-bind.org` → `http://vaultwarden:80`), `cors-proxy` (`cors-proxy.destructuring-bind.org` → `http://cors-proxy:8080`).
- the same three under `dockflare.*` yield identical rules.
- both prefixes on one container: priority order wins, differing lower-priority value produces a `label_conflict` issue.
- indexed 0..2; indexed with non-indexed defaults; wildcard hostname; `path`; `no_tls_verify`; `originsrvname`; `zonename`.
- invalid rules reported, not published: missing hostname, missing service, bad scheme, bad bool, bad hostname.
- `access.policy=authenticate`, `access.group`, `access.email`, `access.custom_rules` → `blocked_access_unsupported`; `bypass`/`default_tld` → published; override flag unblocks.
- container without `enable=true` → no rules; `traefik.*` and `com.centurylinklabs.*` labels ignored silently; unknown label under a prefix → `unknown_label` issue.
- duplicate key across containers → first by name wins, other `conflict`.
**GREEN:** `internal/labels.Parse(name, labels, prefixes) ([]Rule, []Issue)`.
**Gate (AUTO):** `make ci`; coverage of `internal/labels` ≥ 90%.
**Commit:** `feat(labels): multi-prefix label parser`

### S4: Rule lifecycle, ingress rendering, state store
**RED**
- lifecycle: add → active; disappear → pending with `deleteAfter=now+grace`; return within grace → active, pending fields cleared; expiry at `now >= deleteAfter`; field change updates in place; state round-trips across a simulated restart.
- ingress: wildcard after specific; path before no-path; longer path first; catch-all `http_status:404` last; `originRequest` only when needed; output identical for shuffled input; **golden** `testdata/ingress/homelab.golden.json` for the 3-rule set.
- remote-only rules never appear in rendered output.
- state: corrupt file renamed and replaced; unknown version refused; failed write leaves the previous file intact.
**GREEN:** `internal/rules`, `internal/state`.
**Gate (AUTO):** `make ci`.
**Commit:** `feat(rules,state): lifecycle, ingress rendering and state store`

### S5: Docker wrapper and `rules` command
**RED**
- (fake `docker.API`) `DesiredRules` merges results across prefixes, excludes stopped containers and containers without `enable`.
- (moby client against an `httptest` Docker-API stub) list filters are sent per prefix; events are filtered to `start/die/stop/destroy`; a dropped stream reconnects with backoff and emits a `Reconnected` event.
- `rules` prints a table; `rules --json` prints stable JSON (goldens).
**GREEN:** `internal/docker.API` + moby impl (container-management methods stubbed for S8); `rules` command.
**Gate (AUTO):** `make ci`.
**Gate (HUMAN):** on the owner's host `cf-tunnel-sync rules` shows exactly the expected rules (3 here, plus any from S0); `docker restart whoami` produces start/die events in debug logs.
**Commit:** `feat(docker): label discovery, event stream and rules command`

### S6: Cloudflare read-only client and `plan`
**RED** (SDK against `httptest`)
- finds the tunnel by name; two matches → error; `config_src != cloudflare` → error.
- paginated zone listing; hostname → zone by longest suffix; `zonename` override; fallback `zone_id`; none → `no_zone`.
- API calls and errors counted per resource; 429 is retried.
- `plan` goldens: empty diff; rule to add; **remote rule not produced by labels shown as `remove (not from labels)`**; DNS to create/adopt; DNS conflict; orphan DNS reported; `--json` stable.
- mutating methods return `ErrReadOnly` in this step.
**GREEN:** `internal/cf.API` + SDK read paths; `plan` command.
**Gate (AUTO):** `make ci`.
**Gate (HUMAN):** with the owner's real token, `plan` output is exactly what the owner expects (any "remove" lines are rules they confirmed in S0).
**Commit:** `feat(cf): read-only cloudflare client and plan command`

### S7: Cloudflare writes and `reconcile`
**RED** (`httptest`-backed SDK)
- second consecutive run issues **zero** mutating requests.
- adds ingress then DNS; removals write ingress before deleting DNS.
- existing DNS with different content is not modified (`dns_conflict`) unless `dns.overwrite_conflicts`.
- hostname shared by two paths keeps its DNS record until both rules expire.
- DNS delete refused when record content isn't our tunnel target.
- one rule's DNS failure doesn't block others; state persisted anyway; ingress failure skips DNS this pass.
- `dry_run` issues zero mutating requests and records `would_*` actions.
- tunnel adopted when present, created with `config_src=cloudflare` when absent.
- remote-only ingress rules are removed by the PUT.
**GREEN:** write paths, zone resolver, engine (§6.7), `reconcile` command; dry-run guard implemented **once**, at the `cf` and `docker` wrapper boundary.
**Gate (AUTO):** `make ci`.
**Gate (HUMAN):** against a **separate test tunnel** (`tunnel.name: homelab-test`) and a scratch hostname with `grace_period: 30s`: create → DNS+ingress appear; remove → pending → cleaned after 30 s; rerun → zero writes. **Never run S7 against the production tunnel.**
**Commit:** `feat(reconcile): cloudflare write paths and one-shot reconcile`

### S8: cloudflared lifecycle
**RED** (fake `docker.API`)
- running container adopted untouched; stopped container started; absent container created.
- created container gets the token via env **and not** in `cmd`; correct network, metrics flag, restart policy, managed label.
- adopted container on the wrong network → WARN.
- external mode touches no container.
- `dry_run` creates/starts nothing.
**GREEN:** container management in `internal/docker`, `/ready` probe, connection-count health.
**Gate (AUTO):** `make ci`.
**Gate (HUMAN):** on the test tunnel the container comes up, connections > 0, and `https://<scratch-host>` returns the whoami response end to end.
**Commit:** `feat(docker): cloudflared container lifecycle`

### S9: Daemon (`run`)
**RED** (fakes + `testing/synctest`; fall back to an injected `now` if unsuitable)
- burst of events inside `debounce` produces one pass.
- resync fires every `resync_interval`.
- failed pass retries with backoff 30 s → 5 min cap, then recovers.
- event-stream reconnect forces a full pass.
- `SIGINT` during a pass completes the pass, flushes state, exits 0.
- `SIGHUP` triggers a pass.
- Snapshot published after every pass; stats (counts, durations, API counters) correct.
**GREEN:** trigger loop, shutdown, snapshot/stats, `run` command (without web).
**Gate (AUTO):** `make ci` and `go test -race -count=20 ./internal/reconcile/...`.
**Gate (HUMAN):** 1 h soak on the test tunnel: `compose down/up` labelled containers; `kill -9` the daemon mid-pass and restart; it converges; steady state shows zero Cloudflare writes.
**Commit:** `feat(reconcile): event-driven daemon loop`

### S10: Status page (gin + Tailwind), JSON, health, metrics
**RED** (handlers via `httptest` against fixture Snapshots: healthy, degraded, empty, dry-run)
- `/` returns 200 `text/html`; healthy fixture opens with the healthy sentence; degraded fixture names the problem first.
- rules table lists every rule with host, service, container, status text, DNS state; pending rule shows a removal countdown.
- orphan DNS and label warnings sections appear only when non-empty.
- output contains **no** `<script`, no external URLs; CSP header present.
- every Tailwind utility class used in the templates has a rule in `static/app.css` (escape `:`/`/`/`[` in selectors); `/static/app.css` served with immutable cache header.
- `/api/status` matches the JSON schema in §8.1; `/healthz` 200; `/readyz` 503 before first pass, with Docker down, and when stale; 200 otherwise.
- `/metrics` 404 unless enabled; when enabled exposes rules-by-status, reconcile duration histogram, CF API calls/errors, Docker events, tunnel connections, seconds since last successful pass.
- `healthcheck --url` exits 0 on 200, 1 otherwise.
**GREEN:** `internal/web` (gin, `slog-gin`, `gin.Recovery`, templates), `assets/tailwind.css`, built `static/app.css`, `tools/tailwind.version`, `make tools/css/css-check`, `healthcheck` command, wire into `run`.
**Gate (AUTO):** `make ci` incl. `css-check`.
**Gate (HUMAN):** page renders at the owner's Traefik URL with no label changes; numbers agree with `plan`; dark mode and a phone-width view are legible.
**Commit:** `feat(web): status page, json api, health and metrics`

### S11: Packaging
**RED:** `scripts/smoke.sh` (CI-run, fails until the image exists): builds the image; `docker run ... --version` prints version; `config validate` passes with the mounted `config.example.yaml` and dummy env; `healthcheck` exits 1 with nothing listening; image size < 30 MB; `docker compose -f docker-compose.example.yml config` validates.
**GREEN:** multi-stage `Dockerfile` (`golang:1.27`, `CGO_ENABLED=0`, `-trimpath`, `-ldflags "-s -w -X ...version -X ...commit"`; runtime `gcr.io/distroless/static` nonroot; `ENTRYPOINT ["/cf-tunnel-sync"]`, `CMD ["run"]`; `HEALTHCHECK CMD ["/cf-tunnel-sync","healthcheck"]`), `docker-compose.example.yml` (§12.2), `docs/RUNBOOK.md` (socket access: `group_add: ["<docker gid>"]` or a socket proxy with `CONTAINERS=1 EVENTS=1 NETWORKS=1 IMAGES=1 POST=1 PING=1 INFO=1`), CI job building the image (no push); optional GoReleaser config.
**Gate (AUTO):** smoke script green in CI.
**Gate (HUMAN):** image runs on the owner's network against the test tunnel.
**Commit:** `build: dockerfile, compose example and runbook`

### S12: Production cutover  *(HUMAN runbook; agent only writes `docs/CUTOVER.md`)*
1. Back up `./dockflare/data/`; export the current tunnel config.
2. Stop the v2.1.7 `dockflare` service (its cloudflared container keeps running and is adopted).
3. Start cf-tunnel-sync with `tunnel.name: homelab`, `server.dry_run: true`. Review `plan`/logs: every "remove (not from labels)" line must be acknowledged by the owner (no hand-added rules survive).
4. Set `dry_run: false`; verify the public URLs.
5. Rollback = restore the v2.1.7 service definition (its `state.json` was never touched).
**Gate (HUMAN):** 24 h with no unexpected writes in logs.
**Commit (owner's homelab repo):** `chore: replace dockflare v2.1.7 with cf-tunnel-sync`

---

## 11. Extensions (NOT part of this build; to be filed as GitHub issues)

### E1: Access application management via labels
*Issue title:* **Manage Cloudflare Access applications from container labels**
*Why:* v2.1.7 and the `dockflare.*` dialect can protect a hostname with a Zero Trust Access app. The core build deliberately blocks such rules (§3.4); this extension removes the block for the keys it implements.
*Scope:*
- Labels: `access.policy` (`bypass`, `authenticate`, `default_tld`), `access.name` (default `DockFlare-<hostname>`; **adopt existing apps by name or domain**), `access.session_duration` (default `24h`), `access.app_launcher_visible`, `access.allowed_idps`, `access.custom_rules` (JSON array; each entry's `action` (`allow|deny|bypass|non_identity`, default `allow`) selects the policy decision, all other keys are Access include-rules; group entries by action).
- Decide and document how `access.group` (reusable policy reference) and `access.email` (`a@x.com,@x.com`) map; implement or keep blocked.
- Delete the Access app when its rule expires; update in place when labels change; record app/policy IDs in state.
- Remove the fail-closed block **only** for implemented keys.
- Token scope: add *Access: Apps and Policies: Edit*.
*Acceptance:* httptest-backed tests for create, adopt, update-in-place, delete-on-expiry, and shrinking of the blocked set; manual verification on a scratch app against the test tunnel.
*Notes:* the SDK's Access types are large unions; keep them inside `internal/cf` behind the existing interface (a thin raw-request fallback in that package is acceptable; record it in DECISIONS.md).

---

## 12. Appendices

### 12.1 `config.example.yaml` (owner's values)
```yaml
$schema: https://raw.githubusercontent.com/rm-hull/cf-tunnel-sync/main/config.schema.json

server:
  listen: ":5000"
  log_level: INFO
  state_path: /data/state.json

cloudflare:
  api_token: ${CF_API_TOKEN}
  account_id: ${CF_ACCOUNT_ID}
  zone_id: ${CF_ZONE_ID:-}

tunnel:
  name: homelab
  cloudflared:
    network: homelab-net
    metrics_port: 2000

labels:
  prefixes: [cloudflare.tunnel, dockflare]

reconcile:
  grace_period: 8h
  resync_interval: 5m
```

### 12.2 Target compose service
```yaml
  cf-tunnel-sync:
    container_name: cf-tunnel-sync
    image: ghcr.io/rm-hull/cf-tunnel-sync:<version>
    restart: unless-stopped
    command: ["run", "--config", "/config/config.yaml"]
    environment:                       # only the vars config.yaml references; NOT env_file: .env
      TZ: Europe/London
      CF_API_TOKEN: ${CF_API_TOKEN}
      CF_ACCOUNT_ID: ${CF_ACCOUNT_ID}
      CF_ZONE_ID: ${CF_ZONE_ID}
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock
      - ./cf-tunnel-sync/config.yaml:/config/config.yaml:ro
      - ./cf-tunnel-sync/data:/data
    group_add:
      - "<docker gid>"
    healthcheck:
      test: ["CMD", "/cf-tunnel-sync", "healthcheck"]
      interval: 30s
      timeout: 5s
      retries: 3
      start_period: 10s
    labels:
      - "com.centurylinklabs.watchtower.enable=false"   # until trusted; then remove
      - "traefik.enable=true"
      - "traefik.http.routers.cf-tunnel-sync.entrypoints=websecure"
      - "traefik.http.routers.cf-tunnel-sync.rule=Host(`dockflare.homelab.destructuring-bind.org`)"
      - "traefik.http.services.cf-tunnel-sync.loadbalancer.server.port=5000"
    networks:
      - homelab-net
```

### 12.3 Security notes (go in README)
- The status page is **unauthenticated and read-only**; keep it behind Traefik/Cloudflare Access.
- The daemon has full Docker API access (it can create containers) because it manages `cloudflared`. Prefer a socket proxy, or `tunnel.external_id` to avoid needing write access.
- Token scopes: Account → Cloudflare Tunnel: Edit; Zone → DNS: Edit; Zone → Zone: Read; Account Settings: Read.
- Tunnel token reaches `cloudflared` via env var only.
- Publishing a hostname is the dangerous operation: the fail-closed access behaviour (§3.4), no-overwrite DNS policy (§6.5) and dry-run-first cutover are deliberate. Do not weaken them without an explicit owner decision.

### 12.4 Open items to resolve and record in `docs/DECISIONS.md`
1. Indexed + non-indexed label semantics against real v2.1.7 behaviour (§3.3 rule 3).
2. Exact cloudflare-go v7 types (tunnel-create secret requirement, DNS record union params, config `PUT` shape) and moby client `Events`/`ContainerCreate` signatures.
3. `cloudflared` `/ready` response shape.
4. Whether `slog.NewMultiHandler` exists in Go 1.27; whether `testing/synctest` suits the S9 timer tests.
5. `a8m/envsubst` and `go.yaml.in/yaml/v3` behaviour vs the §4.3 tests (replace if they don't match).
6. Current Tailwind v4 standalone CLI version and checksum (pin in `tools/tailwind.version`).
