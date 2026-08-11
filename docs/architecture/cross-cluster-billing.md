# Cross-Cluster Billing Attribution

Each cluster reports its own usage per tenant. Inter-cluster DTSC bandwidth is
infrastructure cost, not a tenant-facing billing item.

## Data Flow

```
Ingest:     MistServer -> Foghorn (PUSH_REWRITE) -> ValidateStreamKey -> cache {OriginClusterID}
Viewer:     MistServer -> Foghorn (USER_NEW/END) -> cache hit: origin from cache
                                                  -> cache miss: ResolveIdentifier -> Quartermaster -> origin
Analytics:  trigger -> Decklog -> Kafka -> Periscope Ingest -> ClickHouse
                                                            -> finalized facts + canonical ledgers
                                                            -> Periscope Query -> per-cluster usage report -> Purser
```

**Ingest path:** `sendTriggerToDecklog()` sets `trigger.ClusterId` from
`p.clusterID` on every event. The `OriginClusterID` is populated from
`ValidateStreamKeyResponse` (ingest) and `ResolveIdentifierResponse` (federated
viewer cache-miss path).

**Query path:** `generateTenantUsageSummary` reads finalized facts and canonical
5-minute sources. Viewer minutes and network bytes come from
`viewer_sessions_final`; processing comes from `processing_segments_final`; storage
is integrated directly from canonical `storage_snapshots`; API usage comes from
`api_usage_5m_v`.
Cluster-scoped meters are emitted as one usage-report record per cluster. Operational
tenant-level gauges attach to the tenant's primary cluster.

## ClickHouse Schema

| Table / view                 | Cluster columns                                  | Engine / role                                     |
| ---------------------------- | ------------------------------------------------ | ------------------------------------------------- |
| `viewer_sessions_final`      | `cluster_id`, breakdown arrays from `USER_END`   | Append-only finalized facts                       |
| `viewer_usage_5m`            | `cluster_id`                                     | Canonical 5-minute ledger                         |
| `storage_gb_seconds_5m`      | `cluster_id`, storage provider attribution       | Canonical 5-minute ledger                         |
| `processing_segments_final`  | `cluster_id`, `process_type`, `output_codec`     | Append-only finalized processing facts            |
| `*_hourly` / `*_daily` views | cluster columns matching their canonical sources | Refreshable rollup stores with public dedup views |

Rollup tables are dashboard caches. Billing reads finalized facts or 5-minute
ledgers directly.

## Settlement Query

Customer invoice usage is grouped by the cluster that served or processed the
metered work. Customer storage billing sums provider slices into
customer-facing storage scope rows. The same billing pass persists the
provider-keyed storage slices into `purser.storage_provider_usage_records`.
Paid invoices allocate storage line revenue across those provider rows and
write `operator_credit_ledger` accruals with
`source_type='storage_provider_usage'`.

## Operator Credit Accrual

`api_billing/internal/operator/credit.go` turns paid customer invoices into
cluster-operator revenue. Only `paid` invoices accrue (no provisional revenue),
and only `third_party_marketplace`-attributed lines produce rows —
platform-official and tenant-private clusters accrue nothing. Each accrual
records `gross_cents`, a platform fee resolved from `platform_fee_policy` basis
points (per-owner row wins over the global marketplace default; missing policy
fail-softs to 0 bps), and the resulting `payable_cents`. New accruals start
`accruing` only for `approved` + `payout_eligible` owners in `cluster_owners`;
everyone else accrues as `held` — complete for audit, parked for payout.
Writers are idempotent via partial unique indexes per source
(`invoice_line_item_id`, `storage_provider_usage_record_id`,
`usage_adjustment_id`, `stripe_invoice_id`). Payment reversals
(refunds/chargebacks) write negating `entry_type='clawback'` rows referencing
the original accrual via `reverses_ledger_id`, deduplicated through
`operator_credit_clawback_reversals`. Payout execution and operator-visible
reporting are not built; the productization of this ledger into the full
attribution → accrual → payout loop is
`docs/rfcs/federated-settlement-attribution.md`.

## Key Files

| File                                             | Purpose                                                                              |
| ------------------------------------------------ | ------------------------------------------------------------------------------------ |
| `api_balancing/internal/triggers`                | Sets `origin_cluster_id` in streamContext and triggers                               |
| `api_analytics_ingest/internal/handlers`         | Extracts `cluster_id` + `origin_cluster_id` from MistTrigger into ClickHouse         |
| `api_analytics_query/internal/handlers`          | Per-cluster billing records (`generateTenantUsageSummary`)                           |
| `api_billing/internal/operator/credit.go`        | Operator credit-ledger accrual (marketplace lines, storage providers, fee bps)       |
| `pkg/database/sql/clickhouse`                    | Schema with cluster columns and MVs                                                  |
| `api_control/internal/grpc`                      | `ResolveIdentifier` enriches with cluster context via `resolveClusterRouteForTenant` |
| `pkg/proto`                                      | `origin_cluster_id` field on `MistTrigger`                                           |
| `docs/architecture/billing-tier-provisioning.md` | Account-level tier provisioning (complementary)                                      |

## Gotchas

- Empty `cluster_id` on a rated row is an ingest/enrichment bug; billing fails closed
  instead of guessing an attribution cluster.
- Origin enrichment was coupled to Foghorn connectivity — `resolveFoghornForTenant`
  dialed Foghorn as a side-effect. Now decoupled via `resolveClusterRouteForTenant`
  which only contacts Quartermaster.
