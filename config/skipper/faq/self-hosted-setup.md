# Self-Hosted Edge Setup

## Cluster Tiers

FrameWorks supports three cluster tiers for different deployment models:

| Tier               | Description                                  | Foghorn             | Edges                       | Best for                                 |
| ------------------ | -------------------------------------------- | ------------------- | --------------------------- | ---------------------------------------- |
| `shared-community` | FrameWorks-managed infrastructure, free tier | Shared multi-tenant | FrameWorks-owned            | Getting started, low-volume              |
| `shared-lb`        | Shared Foghorn, tenant-owned edges           | Shared multi-tenant | Tenant enrolls own nodes    | Self-hosted operators, regional coverage |
| `dedicated`        | Full dedicated cluster                       | Single-tenant       | Tenant-owned or provisioned | High-volume, data sovereignty            |

New accounts start on `shared-community`. Activate self-hosting to join a `shared-lb` cluster where you bring your own edge nodes.

## Why Self-Host?

- **Lower latency**: Deploy edges close to your audience for geographic proximity
- **Data sovereignty**: Media traffic stays in your infrastructure, your jurisdiction
- **Cost control**: Use your own hardware instead of metered bandwidth at scale
- **Coverage**: Extend reach to regions where FrameWorks doesn't have edges yet

## Self-Hosting Flow

1. **Browse marketplace**: Dashboard → Infrastructure → Marketplace (or use the `browse_marketplace` MCP tool)
2. **Subscribe to a cluster**: Click "Connect" (or "Request Access" if approval is required)
3. **Set preferred cluster**: Make the subscribed cluster your primary for DNS routing
4. **Get enrollment token**: Create a private cluster or get a token from the cluster operator
5. **Provision edges**: Run the CLI command on each server you want to add

## Edge Provisioning (CLI)

The CLI handles the full provisioning pipeline in one command:

```bash
frameworks edge provision \
  --enrollment-token enroll_xxx \
  --ssh ubuntu@my-edge.example.com
```

The 7-step pipeline:

1. **Preflight** — Validates SSH access, Docker, ports, DNS prerequisites
2. **Tune** — Configures OS kernel parameters for media streaming
3. **Register** — Calls `PreRegisterEdge` → gets assigned domain, node ID, TLS cert
4. **Cert stage** — Writes wildcard TLS certificate for HTTPS
5. **Install** — Generates and uploads `docker-compose.edge.yml`, `.edge.env`, `Caddyfile`
6. **Start** — Runs `docker compose up -d`
7. **Verify** — Checks container health and HTTPS readiness

### What the operator provides

- **Enrollment token** — from the cluster operator or self-created via private cluster
- **SSH access** — to the target server

Everything else is cluster-assigned automatically:

- Node ID (6-byte hex)
- Edge domain: `edge-{node_id}.{cluster_slug}.frameworks.network`
- Wildcard TLS certificate (pushed via ConfigSeed)
- Foghorn gRPC address (for Helmsman control plane)

### What gets deployed

| Component      | Role                                                                                   |
| -------------- | -------------------------------------------------------------------------------------- |
| **MistServer** | Media engine — handles WHIP ingest, HLS/DASH/WHEP playback, DTSC replication           |
| **Helmsman**   | Sidecar — connects to Foghorn (gRPC), reports health, receives stream routing commands |
| **Caddy**      | Reverse proxy — TLS termination, HTTPS for all protocols                               |

All three run as Docker containers via Docker Compose.

### Ports

The default edge template routes all traffic through Caddy on HTTPS:

| Port    | Protocol | Notes                                            |
| ------- | -------- | ------------------------------------------------ |
| 443/tcp | HTTPS    | All ingest (WHIP) and playback (WHEP, HLS, DASH) |
| 80/tcp  | HTTP     | Redirect to HTTPS                                |

MistServer listens on 8080 internally; Caddy reverse-proxies to it. No RTMP or SRT ports are exposed by default in the edge template. Operators who need RTMP (1935) or SRT (8889) can add port bindings to `docker-compose.edge.yml` for their MistServer container.

### Server requirements

- Linux (Ubuntu 22.04+ recommended)
- Docker and Docker Compose
- Public IP address
- Ports 80 and 443 open in firewall
- At least 2 CPU cores, 4GB RAM for a basic edge

## DNS and TLS

DNS and TLS are fully automatic:

- **DNS**: Foghorn tells Navigator to create an A record: `edge-{node_id}.{cluster}.frameworks.network → your IP`
- **TLS**: Cluster wildcard cert (`*.{cluster}.frameworks.network`) is pushed to Helmsman, which configures Caddy

No manual DNS or cert setup required. Cert renewal is automatic.

## Multi-Node Provisioning

For deploying multiple edges from a manifest file:

```bash
frameworks edge provision --manifest edges.yaml --parallel 4
```

## What Changes for Streamers

When a streamer sets a self-hosted cluster as their preferred cluster:

- **Ingest URLs change**: to cluster-scoped domains like `edge-ingest.{cluster}.frameworks.network`
- **Playback URLs change**: to `foghorn.{cluster}.frameworks.network/play/{playbackId}/hls/index.m3u8`
- **Existing embeds still work**: Playback IDs resolve via Commodore regardless of which cluster the stream is on

### Peering and viewer routing

- **Preferred ↔ official cluster**: always-on peering. Foghorn has fresh edge data for both and scores them on every viewer request. Seamless.
- **Other subscribed clusters**: peering is demand-driven. A PeerChannel opens when a stream triggers it (e.g., viewer on cluster C requests a stream that lives on the preferred cluster). Once open, those edges join the scoring pool. When the last stream involving that peer ends, the connection closes.
- **Unsubscribed clusters**: never peered, never scored.

So a viewer connecting to any subscribed cluster can reach the stream — it just takes one demand-driven lookup to establish the peering, after which subsequent viewers are served from cache.

If the preferred cluster differs from the official (billing-tier) cluster, streamers see ingest URLs for both clusters — they can choose based on geographic proximity.
