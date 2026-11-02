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
frameworks edge deploy \
  --enrollment-token enroll_xxx \
  --ssh ubuntu@my-edge.example.com
```

`frameworks edge provision` runs the same pipeline with finer control, and takes `--manifest` for several nodes:

1. **Preflight** — Validates SSH access, ports, DNS and OS prerequisites (Docker in container mode, systemd or launchd in native mode)
2. **Tune** — Configures OS kernel parameters for media streaming (optional)
3. **Register** — The enrollment token pre-registers the node and assigns its domain and node ID
4. **Config seed** — Foghorn delivers the TLS certificate, CA and site configuration over `ConfigSeed`
5. **Install** — Renders the edge environment and either the single-container compose file or the native service units
6. **Start** — Starts the edge stack
7. **Verify** — Checks HTTPS readiness and that Helmsman's control stream to Foghorn is connected

### What the operator provides

- **Enrollment token** — from the cluster operator or self-created via private cluster
- **SSH access** — to the target server

Everything else is cluster-assigned automatically:

- Node ID: preferred human-readable host/node label when available, otherwise a 6-byte hex fallback
- Edge domain: `{node_label}.{cluster_slug}.frameworks.network` where `node_label` is the node ID with a single `edge-` prefix
- Wildcard TLS certificate (pushed via ConfigSeed)
- Foghorn gRPC address (for Helmsman control plane)

### What gets deployed

| Component      | Role                                                                                   |
| -------------- | -------------------------------------------------------------------------------------- |
| **MistServer** | Media engine — handles WHIP ingest, HLS/DASH/WHEP playback, DTSC replication           |
| **Helmsman**   | Sidecar — connects to Foghorn (gRPC), reports health, receives stream routing commands |
| **Caddy**      | Reverse proxy — TLS termination, HTTPS for all protocols                               |

In container mode all three run inside the single `frameworks-edge` container under s6. In native mode they run as systemd services on Linux or launchd services on macOS.

After enrollment, Foghorn updates Helmsman, MistServer and Caddy through the edge's own rolling update. Re-run `frameworks edge provision` only when a new CLI renders the edge configuration differently; `--dry-run` shows the plan per node.

### Ports

The default edge template routes all traffic through Caddy on HTTPS:

| Port    | Protocol | Notes                                            |
| ------- | -------- | ------------------------------------------------ |
| 443/tcp | HTTPS    | All ingest (WHIP) and playback (WHEP, HLS, DASH) |
| 80/tcp  | HTTP     | Redirect to HTTPS                                |

MistServer listens on 8080 internally; Caddy reverse-proxies to it. For direct RTMP (1935/tcp) or SRT (8889/udp) ingest, open those MistServer ports in the host firewall.

### Server requirements

- Linux (Ubuntu 22.04+ recommended), or macOS in native mode
- Docker and Docker Compose for container mode
- Public IP address
- Ports 80 and 443 open in firewall
- At least 2 CPU cores, 4GB RAM for a basic edge

## DNS and TLS

DNS and TLS are fully automatic:

- **DNS**: Foghorn tells Navigator to create an A record: `{node_label}.{cluster}.frameworks.network → your IP`, where `node_label` is the node ID with a single `edge-` prefix
- **TLS**: Cluster wildcard cert (`*.{cluster}.frameworks.network`) is pushed to Helmsman, which configures Caddy

No manual DNS or cert setup required. Cert renewal is automatic.

## Multi-Node Provisioning

For deploying multiple edges from a manifest file:

```bash
frameworks edge provision --manifest edges.yaml --parallel 4
```

## What Changes for Streamers

When a streamer sets a self-hosted cluster as their preferred cluster:

- **Dashboard aliases change**: the self-hosted cluster's concrete ingest and playback aliases are
  presented first
- **Shared ingest stays multi-cluster**: the public front door still ranks every healthy cluster the
  stream is entitled to use
- **Existing embeds still work**: Playback IDs resolve via Commodore regardless of which cluster the stream is on

### Peering and viewer routing

- **Preferred ↔ official cluster**: always-on peering. Foghorn has fresh edge data for both and scores them on every viewer request. Seamless.
- **Other subscribed clusters**: peering is demand-driven. A PeerChannel opens when a stream triggers it (e.g., viewer on cluster C requests a stream that lives on the preferred cluster). Once open, those edges join the scoring pool. When the last stream involving that peer ends, the connection closes.
- **Unsubscribed clusters**: never peered, never scored.

So a viewer connecting to any subscribed cluster can reach the stream — it just takes one demand-driven lookup to establish the peering, after which subsequent viewers are served from cache.

If the preferred cluster differs from the official cluster, both concrete aliases remain available,
while the shared front door chooses across the authorized healthy candidates and an active placement
claim pins reconnects.
