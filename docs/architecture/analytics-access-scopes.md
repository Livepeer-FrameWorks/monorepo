# Analytics access scopes

Analytics reads are split by the question the caller is asking, not by page name.
Bridge enforces the access boundary before calling Periscope or Quartermaster.

## Scopes

| Scope              | Caller intent                                                                 | Allowed data                                                                                                                                                                                                                | Examples                                                                           |
| ------------------ | ----------------------------------------------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ---------------------------------------------------------------------------------- |
| Public topology    | Show platform status, federation shape, marketplace context, and public KPIs. | Official cluster-level topology and public orchestrator vantage points. Authentication may add accessible-cluster summaries, but never per-tenant load, private node inventory, host metrics, session identifiers, or URLs. | `networkStatus` for unauthenticated users, marketing network map.                  |
| Tenant analytics   | Show what happened to the tenant's own streams and viewers.                   | Content-scoped viewer delivery, routing, ingest, processing, storage, and stream federation. Provider-owned federation rows are included only when their `stream_tenant_id` matches the caller.                             | Audience routing map, per-stream media topology, processing and storage placement. |
| Cluster operations | Operate infrastructure the caller owns.                                       | Nodes, service instances, node metrics, node performance, enrollment tokens, cluster inspection, system-health subscriptions, and redacted workload aggregates for owned clusters only.                                     | `/infrastructure`, `/infrastructure/[clusterId]`, `/nodes`, `/nodes/[id]`.         |

## Bridge enforcement

- Tenant analytics resolvers derive the subject tenant from authentication. They never accept subscription-provider tenant IDs as content scope. Routing reads require an exact `stream_tenant_id` match and fail closed for unattributed rows; infrastructure `tenant_id` is never used as a subject-owner fallback. Unattributed routing decisions are TTL-bound diagnostic telemetry and are allowed to expire without a speculative ownership backfill. Other families use their authoritative content-owner column or an explicit content/operator union for federation.
- Cluster operations resolvers first resolve the caller's owned clusters from Quartermaster. Reads without a cluster filter fan out only across owned clusters. Reads with a node id first fetch the node and then require ownership of its cluster.
- Public topology resolvers use Quartermaster's official-cluster surface and the caller's active cluster-access rows. Authenticated tenants receive official topology plus subscribed or owned cluster rows, with basic cluster-level load counters for every visible cluster. Private node and service-instance topology is included only for owned clusters.

This allows anonymous visitors and tenants to see the public platform map while preventing a tenant subscribed to a marketplace/shared cluster from seeing a private cluster owner's node fleet, service placement, host metrics, or unrelated tenant traffic.

Cluster access is not content ownership. A marketplace subscription may reveal the cluster's public/aggregate status through `networkStatus`; it never grants raw routing rows, viewer sessions, stream identifiers, or another tenant's processing and storage facts. Conversely, a tenant can see where its own media was served or replicated even when the serving infrastructure belongs to another tenant.

`clusterWorkload` is the operator bridge between placement and privacy. It groups viewer, ingest, processing, storage, and federation work by owned cluster, node, and work kind, with counts/bytes/media-seconds/errors. `measurementKind=window` is interval activity; `measurementKind=current` is a freshness-bounded observation with `observedAt`. Current viewer and ingest placement comes from the authenticated media event stored in the current-state row; it is never inferred by joining a content tenant's node name to infrastructure inventory. Current storage retains `storageScope`, so hot and cold resident bytes stay separate and are never added to storage-event byte flow. The query removes tenant, content, stream, session, URL, and client identity before returning rows. It is a service-only Periscope RPC: Bridge supplies the Quartermaster-derived owned cluster list and the Periscope client deliberately uses its service credential even when the outer request carries a user JWT. Raw federation rows also suppress the stream tenant, stream name, and stream ID unless `stream_tenant_id` matches the caller, and never return the internal DTSC URL. Operator-owned partitions and cross-provider content ownership are queried as disjoint branches so operator reads retain partition pruning and same-tenant rows cannot duplicate. `networkStatus` remains the smaller public/marketplace summary.

Detailed content-owner routing and federation history is exact-attribution only. Both ClickHouse event tables have a `stream_tenant_id` data-skipping index for this cross-infrastructure access path. The v0.3.0 migration materializes the index for retained attributed parts; rows with missing `stream_tenant_id` remain hidden rather than being guessed from infrastructure ownership.

## Placement field semantics

| Field                                | Question answered                                          | Trust source                                                            |
| ------------------------------------ | ---------------------------------------------------------- | ----------------------------------------------------------------------- |
| `node_id`                            | Which machine performed the work?                          | Authenticated node session or recorded worker identity                  |
| `cluster_id` / `selected_cluster_id` | Which cluster contains that machine?                       | Node registry/session; never the stream origin                          |
| `origin_cluster_id`                  | Where did the content originate?                           | Stream/artifact routing context                                         |
| `control_cell_id`                    | Which Foghorn/media-authority cell decided or observed it? | Process configuration                                                   |
| `tenant_id`                          | Who owns the row's primary resource?                       | Authenticated content or infrastructure context, depending on the table |
| `stream_tenant_id`                   | Who owns content inside an infrastructure-owned event?     | Resolved stream authority                                               |

## Webapp contract

- `/network` and `/infrastructure/federation` can render public topology for anonymous visitors. After login, they add tenant-scoped routing/federation overlays for the caller's own streams.
- `/analytics/audience` builds routing-map node pins from tenant routing-event coordinates, not from node inventory.
- Traffic-matrix and owner-only workload fields use separate GraphQL operations so an expected operator authorization error cannot null generally available analytics.
- `/infrastructure`, `/infrastructure/[clusterId]`, `/nodes`, and `/nodes/[id]` are cluster-operator surfaces. They should not fetch node inventory, service instances, node metrics, node performance, or system-health subscriptions unless the current tenant owns the cluster.

When adding a new analytics endpoint, choose one of these scopes explicitly. Do not reuse an operator endpoint to satisfy a tenant analytics view, and do not broaden a tenant endpoint to include operational data.
