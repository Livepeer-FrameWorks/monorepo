# Cross-Cluster Billing Attribution

Each cluster reports its own usage per tenant. Inter-cluster DTSC bandwidth is
infrastructure cost, not a tenant-facing billing item.

## Data Flow

```
Ingest:     MistServer -> Foghorn (PUSH_REWRITE) -> ValidateStreamKey -> cache {OriginClusterID}
Viewer:     MistServer -> Foghorn (USER_NEW/END) -> cache hit: origin from cache
                                                  -> cache miss: ResolveIdentifier -> Quartermaster -> origin
Analytics:  trigger -> Decklog -> Kafka -> Periscope Ingest -> ClickHouse (cluster_id + origin_cluster_id)
                                                            -> MV rollup -> Periscope Query
                                                            -> per-cluster UsageSummary -> Purser
```

**Ingest path:** `sendTriggerToDecklog()` sets `trigger.ClusterId` from
`p.clusterID` on every event. The `OriginClusterID` is populated from
`ValidateStreamKeyResponse` (ingest) and `ResolveIdentifierResponse` (federated
viewer cache-miss path).

**Query path:** `generateTenantUsageSummary` groups `tenant_viewer_daily` by
`cluster_id`, emitting one `UsageSummary` per cluster. Non-cluster-scoped metrics
(storage, processing, API calls) are attributed to the tenant's primary cluster.

## ClickHouse Schema

| Table / MV                 | Cluster columns                                          | Engine           |
| -------------------------- | -------------------------------------------------------- | ---------------- |
| `viewer_connection_events` | `cluster_id`, `origin_cluster_id`                        | MergeTree        |
| `stream_event_log`         | `cluster_id`, `origin_cluster_id`                        | MergeTree        |
| `viewer_hours_hourly` (MV) | `cluster_id`, `origin_cluster_id` in GROUP BY            | SummingMergeTree |
| `tenant_viewer_daily` (MV) | `cluster_id`, `origin_cluster_id` in GROUP BY + ORDER BY | SummingMergeTree |

`origin_cluster_id` is included in ORDER BY keys for rollup tables to prevent
merge-collapse in AggregatingMergeTree/SummingMergeTree engines.

## Settlement Query

Cross-cluster traffic attribution from the pre-aggregated daily rollup:

```sql
SELECT cluster_id AS serving_cluster, origin_cluster_id AS content_cluster,
       tenant_id, sum(viewer_hours), sum(egress_gb)
FROM periscope.tenant_viewer_daily
WHERE origin_cluster_id != '' AND origin_cluster_id != cluster_id
  AND day BETWEEN ? AND ?
GROUP BY serving_cluster, content_cluster, tenant_id
```

## Key Files

| File                                                 | Purpose                                                                              |
| ---------------------------------------------------- | ------------------------------------------------------------------------------------ |
| `api_balancing/internal/triggers/processor.go`       | Sets `origin_cluster_id` in streamContext and triggers                               |
| `api_analytics_ingest/internal/handlers/handlers.go` | Extracts `cluster_id` + `origin_cluster_id` from MistTrigger into ClickHouse         |
| `api_analytics_query/internal/handlers/billing.go`   | Per-cluster billing summaries (`generateTenantUsageSummary`)                         |
| `pkg/database/sql/clickhouse/periscope.sql`          | Schema with cluster columns and MVs                                                  |
| `api_control/internal/grpc/server.go`                | `ResolveIdentifier` enriches with cluster context via `resolveClusterRouteForTenant` |
| `pkg/proto/ipc.proto`                                | `origin_cluster_id` field on `MistTrigger`                                           |
| `docs/architecture/billing-tier-provisioning.md`     | Account-level tier provisioning (complementary)                                      |

## Gotchas

- Legacy rows with empty `cluster_id` are attributed to the tenant's primary
  cluster (`billing.go:229-232`).
- Origin enrichment was coupled to Foghorn connectivity — `resolveFoghornForTenant`
  dialed Foghorn as a side-effect. Now decoupled via `resolveClusterRouteForTenant`
  which only contacts Quartermaster.
