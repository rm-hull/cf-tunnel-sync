# Migration Guide from DockFlare v2.1.7

This guide helps migrate from DockFlare v2.1.7 to cf-tunnel-sync.

## Configuration Mapping

| DockFlare v2.1.7 env | cf-tunnel-sync key | Notes |
|---|---|---|
| `CF_API_TOKEN` | `cloudflare.api_token` via `${CF_API_TOKEN}` | Use env substitution |
| `CF_ACCOUNT_ID` | `cloudflare.account_id` via `${CF_ACCOUNT_ID}` | Use env substitution |
| `CF_ZONE_ID` | `cloudflare.zone_id` via `${CF_ZONE_ID:-}` | Optional fallback zone |
| `TUNNEL_NAME` | `tunnel.name` | Default: `homelab` |
| `LABEL_PREFIX` | `labels.prefixes` | Keep `cloudflare.tunnel`, add `dockflare` |
| `GRACE_PERIOD_SECONDS` | `reconcile.grace_period` | 28800 s = `8h` |
| `CLEANUP_INTERVAL_SECONDS` | `reconcile.resync_interval` | Use Go duration syntax |
| `CLOUDFLARED_NETWORK_NAME` | `tunnel.cloudflared.network` | Default: `cloudflare-net` |
| `CLOUDFLARED_METRICS_PORT` | `tunnel.cloudflared.metrics_port` | Default: `2000` |
| `USE_EXTERNAL_CLOUDFLARED` + `EXTERNAL_TUNNEL_ID` | `tunnel.external_id` | Non-empty = external mode |
| `STATE_FILE_PATH` | `server.state_path` | Do not reuse v2 state.json |
| `DOCKER_HOST` | `docker.host` or env | Also honored natively by Docker client |

## Key Changes

1. **Configuration file**: cf-tunnel-sync uses `config.yaml` instead of environment variables only.
2. **Label prefixes**: Both `cloudflare.tunnel` and `dockflare` are supported by default.
3. **State file**: The new state file format is not compatible with v2.1.7's state.json. Use a new path.
4. **Duration format**: Uses Go duration syntax (e.g., `8h`, `5m`, `2s`) instead of seconds.
5. **No web UI**: cf-tunnel-sync has no web configuration interface.
6. **No auth**: The status page is read-only and unauthenticated.
7. **No email notifications**: Removed entirely.
8. **No Redis**: Removed entirely.
9. **No manual rules**: All rules are derived from Docker labels.

## Migration Steps

1. Back up your current DockFlare configuration and state.
2. Create `config.yaml` using `config.example.yaml` as a template.
3. Set environment variables for secrets (`CF_API_TOKEN`, `CF_ACCOUNT_ID`, `CF_ZONE_ID`).
4. Test with `cf-tunnel-sync config validate`.
5. Review the plan with `cf-tunnel-sync plan`.
6. Start with `dry_run: true` to verify behavior.
7. Once verified, set `dry_run: false` and start the daemon.

## Rollback

To rollback, restore the v2.1.7 service definition. The old `state.json` was never touched, so rollback is safe.
