# RUNBOOK.md — Running cf-tunnel-sync

## Quick Start

```bash
# 1. Clone and configure
git clone https://github.com/rm-hull/cf-tunnel-sync.git
cd cf-tunnel-sync

# 2. Copy and edit config
cp config.example.yaml ./cf-tunnel-sync/config.yaml
# Edit ./cf-tunnel-sync/config.yaml with your values

# 3. Set environment variables
export CF_API_TOKEN="your-token"
export CF_ACCOUNT_ID="your-account-id"
export CF_ZONE_ID="your-zone-id"  # optional fallback zone

# 4. Start with docker compose
docker compose -f docker-compose.example.yml up -d
```

## Docker Socket Access

The daemon needs Docker API access to manage containers and listen for events. There are two approaches:

### Option A: Group Access (Simpler)

Add the Docker group GID to the container:

```yaml
group_add:
  - "998"  # replace with your Docker group GID
```

Find the GID on your host:
```bash
getent group docker | cut -d: -f3
# or
stat -c '%g' /var/run/docker.sock
```

### Option B: Socket Proxy (More Secure)

Use a socket proxy to limit API access to only the operations cf-tunnel-sync needs:

```yaml
services:
  cf-tunnel-sync:
    environment:
      DOCKER_HOST: tcp://docker-proxy:2375
    # Do not mount the socket directly

  docker-proxy:
    image: tecnativa/docker-socket-proxy
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock:ro
    environment:
      CONTAINERS: 1
      EVENTS: 1
      NETWORKS: 1
      IMAGES: 1
      POST: 1
      PING: 1
      INFO: 1
```

This restricts access to only the endpoints the daemon uses (listing containers, events, creating/starting the cloudflared container).

## Configuration

See `config.example.yaml` for a complete example. Key settings:

| Key | Description |
|---|---|
| `cloudflare.api_token` | Cloudflare API token with Tunnel: Edit, DNS: Edit scopes |
| `cloudflare.account_id` | Cloudflare account ID |
| `tunnel.name` | Name of the Cloudflare Tunnel to manage |
| `labels.prefixes` | Label prefixes to watch (default: `cloudflare.tunnel`, `dockflare`) |
| `reconcile.grace_period` | How long to keep DNS records after a rule disappears (default: `8h`) |
| `server.dry_run` | If true, log actions without making changes |

## Dry Run First

Always start with `dry_run: true` to verify the plan before making changes:

```bash
# Check what would change
docker compose exec cf-tunnel-sync /app/cf-tunnel-sync plan

# Or use the status page at http://localhost:8080
```

Review the output: any "remove (not from labels)" lines are ingress rules not produced by Docker labels. Confirm these are expected before disabling dry run.

## Health Checks

The container has a built-in healthcheck that calls `/app/cf-tunnel-sync healthcheck`. Docker will report the container as healthy after the first successful reconcile pass.

```bash
# Check container health
docker inspect --format '{{.State.Health.Status}}' cf-tunnel-sync

# Manual healthcheck
docker compose exec cf-tunnel-sync /app/cf-tunnel-sync healthcheck
```

## Logs

```bash
# Follow logs
docker compose logs -f cf-tunnel-sync

# Check recent reconcile summaries
docker compose logs cf-tunnel-sync | grep "reconcile done"
```

## Troubleshooting

### Container not connecting to tunnel

Check the cloudflared container is running and connected:
```bash
docker ps | grep cloudflared
docker compose exec cf-tunnel-sync /app/cf-tunnel-sync plan
```

### DNS records not created

Check zone resolution in the status page or plan output. Ensure `cloudflare.zone_id` is set or the hostname's zone is in your Cloudflare account.

### State file corrupted

The daemon will rename a corrupt state file to `.corrupt-<timestamp>` and start fresh. The state is rebuildable from Docker labels and Cloudflare API.

## Rollback

If migrating from DockFlare v2.1.7, the original `state.json` was never touched. To rollback:
1. Stop cf-tunnel-sync
2. Restore the v2.1.7 service definition
3. The existing cloudflared container will be adopted automatically
