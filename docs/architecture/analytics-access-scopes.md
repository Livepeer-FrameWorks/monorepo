# Analytics access scopes

Analytics reads are split by the question the caller is asking, not by page name.
Bridge enforces the access boundary before calling Periscope or Quartermaster.

## Scopes

| Scope              | Caller intent                                                                 | Allowed data                                                                                                                                                                                                  | Examples                                                                                |
| ------------------ | ----------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | --------------------------------------------------------------------------------------- |
| Public topology    | Show platform status, federation shape, marketplace context, and public KPIs. | Official cluster topology with node/service placement, subscribed-cluster KPI summaries, owned-cluster topology, peering context, and public orchestrator vantage points. No per-tenant load or host metrics. | `networkStatus` for unauthenticated users, marketing network map.                       |
| Tenant analytics   | Show what happened to the tenant's own streams and viewers.                   | Periscope rows filtered by the caller's `tenant_id`, including routing decisions, federation events, client QoE, viewer geography, and usage.                                                                 | Audience routing map, stream health, subscriber routing matrix on a subscribed cluster. |
| Cluster operations | Operate infrastructure the caller owns.                                       | Nodes, service instances, node metrics, node performance, enrollment tokens, cluster inspection, and system-health subscriptions for owned clusters only.                                                     | `/infrastructure`, `/infrastructure/[clusterId]`, `/nodes`, `/nodes/[id]`.              |

## Bridge enforcement

- Tenant analytics resolvers pass the context tenant id to Periscope and do not require cluster ownership. Periscope queries must keep `tenant_id = ?` predicates on the underlying ClickHouse reads.
- Cluster operations resolvers first resolve the caller's owned clusters from Quartermaster. Reads without a cluster filter fan out only across owned clusters. Reads with a node id first fetch the node and then require ownership of its cluster.
- Public topology resolvers use Quartermaster's official-cluster surface and the caller's active cluster-access rows. Authenticated tenants receive official topology plus subscribed or owned cluster rows, with basic cluster-level load counters for every visible cluster. Private node and service-instance topology is included only for owned clusters.

This allows anonymous visitors and tenants to see the public platform map while preventing a tenant subscribed to a marketplace/shared cluster from seeing a private cluster owner's node fleet, service placement, host metrics, or unrelated tenant traffic.

## Webapp contract

- `/network` and `/infrastructure/federation` can render public topology for anonymous visitors. After login, they add tenant-scoped routing/federation overlays for the caller's own streams.
- `/analytics/audience` builds routing-map node pins from tenant routing-event coordinates, not from node inventory.
- `/infrastructure`, `/infrastructure/[clusterId]`, `/nodes`, and `/nodes/[id]` are cluster-operator surfaces. They should not fetch node inventory, service instances, node metrics, node performance, or system-health subscriptions unless the current tenant owns the cluster.

When adding a new analytics endpoint, choose one of these scopes explicitly. Do not reuse an operator endpoint to satisfy a tenant analytics view, and do not broaden a tenant endpoint to include operational data.
