# Cluster Discovery and Subscriptions

## Marketplace

The marketplace lets you browse available infrastructure clusters. Each cluster represents a geographic region or operator-provided infrastructure.

**Access**: Dashboard → Infrastructure → Marketplace, or use the `browse_marketplace` MCP tool.

## Cluster Pricing Models

| Model            | Description                                   |
| ---------------- | --------------------------------------------- |
| `FREE_UNMETERED` | No cost, no usage limits (community clusters) |
| `TIER_INHERIT`   | Pricing follows your billing tier             |
| `METERED`        | Pay per usage (bandwidth, viewer hours)       |
| `MONTHLY`        | Fixed monthly fee                             |

## Subscription Flow

1. **Browse**: Find a cluster in the marketplace
2. **Connect**: Click "Connect" (instant) or "Request Access" (requires operator approval)
3. **Wait** (if approval required): Status shows as "Pending..." until the cluster operator approves
4. **Active**: Once subscribed, the cluster's edges are available for your streams

Some clusters require an invite token — enter it during the subscription request.

## Preferred Cluster

Your **preferred cluster** is the tenant's default topology and branding preference. It does not
hard-pin the shared ingest front door to one virtual media cluster.

- Set via: Dashboard → Infrastructure → My Network → "Set as Preferred", or `set_preferred_cluster` MCP tool
- Affects: which cluster-scoped aliases the dashboard presents first and which clusters maintain
  always-on peering
- Fresh front-door resolution still ranks every healthy, entitled cluster returned by Commodore
- An active publisher's placement claim pins reconnect resolution to its current cluster
- Existing playback IDs continue resolving regardless of the preference

### What is the preferred cluster vs the official cluster vs other subscribed clusters?

- **Official cluster**: Assigned by your billing tier. Always has good geographic coverage. Set automatically when your account is created or your tier changes.
- **Preferred cluster**: Your chosen cluster for DNS steering. Can be any subscribed cluster. Set via dashboard or `set_preferred_cluster`.
- **Other subscribed clusters**: Any cluster you've connected to via the marketplace. Not actively peered by default, but available for federation on demand when a stream triggers it. Once peered, their edges participate in load balancing decisions.

When preferred and official differ, the pair remains persistently peered, so Foghorn has fresh edge
data for both. The shared ingest resolver may return candidates from either cluster when both are in
the stream's authorized healthy envelope; explicit cluster-scoped aliases remain available when a
publisher intentionally needs a concrete pool. Other subscribed clusters join the scoring pool when
a stream activates peering with them.

## Private Clusters

Create your own cluster for self-hosted edge nodes:

- **Dashboard**: Infrastructure → My Network → "Create Private Cluster"
- **MCP**: Use the `create_private_cluster` tool
- **Result**: A new cluster + bootstrap token for edge enrollment

The bootstrap token is shown once — save it securely. Use it with the CLI to provision edges:

```bash
frameworks edge provision --enrollment-token <token> --ssh user@host
```

## Federation and Peering

Not all subscribed clusters behave the same way. Foghorn uses a three-tier peering model:

### 1. Official ↔ Preferred (always-on)

Your preferred cluster and your official (billing-tier) cluster maintain a persistent PeerChannel connection. They exchange `ClusterEdgeSummary` data every 30 seconds — smoothed per-edge metrics (BW, CPU, RAM, geo, viewers). This means Foghorn always has fresh edge data for both clusters and can score remote edges alongside local ones on every viewer request. No per-viewer cross-cluster RPC needed.

### 2. Other subscribed clusters (stream-scoped, demand-driven)

Subscribing to a cluster does NOT automatically open a peering connection. The PeerChannel to these clusters opens **on demand** — when a stream triggers it (e.g., a viewer requests a stream that exists on that cluster, or a `QueryStream` fan-out discovers it). Once open, the PeerChannel stays alive as long as there are active streams involving that peer. When the last stream ends, the PeerChannel closes.

### 3. Once peered, always scored

Regardless of how a PeerChannel was established (always-on or stream-scoped), once it's open, remote edge data from that peer flows into Redis. Foghorn's load balancer scores those remote edges alongside local edges on every viewer request. Remote edges get a `CrossClusterPenalty(200)` so local edges win unless the remote is meaningfully better on geo or bandwidth.

### What this means in practice

- Your preferred + official clusters: edges always visible, always scored, seamless failover
- Other subscribed clusters: invisible until a stream activates the connection, then fully scored
- Clusters you're NOT subscribed to: never peered, never scored
- The 5-minute `ListPeers` reconciliation catches topology changes but is not the primary discovery path — stream validation provides cluster peers on demand

### Can viewers on other clusters still watch?

Yes, if the clusters are subscribed. When a viewer connects to cluster B and requests a stream from cluster A, Foghorn B will discover cluster A through the stream's `cluster_peers` context (provided at stream validation time). It opens a stream-scoped PeerChannel, calls `QueryStream` to find the best source edge, and arranges a DTSC origin-pull. After the first replication, subsequent viewers are scored from local Redis cache — no per-viewer cross-cluster RPC.

## How to Pick a Cluster

Consider:

1. **Geography**: Pick a cluster close to your audience
2. **Pricing**: Free clusters have shared resources; metered/monthly may offer better capacity
3. **Self-hosting**: If you want to bring your own hardware, pick a `shared-lb` cluster in your region
4. **Availability**: Check current utilization — lower utilization means more headroom

## Multi-Cluster Ingest

One physical Foghorn can serve many virtual media clusters. The listener hostname and the process's
bootstrap `CLUSTER_ID` do not pin endpoint selection. For each stream, Commodore returns the tenant's
authorized, plan-filtered, healthy cluster envelope; Foghorn ranks eligible nodes across that envelope
using geography and load. A fresh publisher may therefore land in any eligible virtual cluster.

An existing live placement claim pins reconnect resolution to the cluster already carrying the
publisher. Public endpoint resolution does not take that claim: only the accepted `PUSH_REWRITE`
serializes placement, so asking for URLs cannot block a publisher elsewhere.

- `POST https://foghorn.frameworks.network/ingest/{streamKey}` returns a 307 to the selected WHIP node.
- `GET` on the same path returns ranked candidates, including the selected `kind` and `clusterId` plus
  RTMP and SRT URLs for protocols that cannot follow an HTTP redirect.
- GraphQL `resolveIngestEndpoint` exposes the same candidate contract.

Cluster-specific and tenant-alias endpoint kinds remain explicit handles for pinning and branding;
they do not change the shared listener into a single-cluster process.
