# Cross-Cluster Billing Attribution

Each cluster reports its own usage per tenant. Inter-cluster DTSC bandwidth is
infrastructure cost, not a tenant-facing billing item.

## Data Flow

```
Ingest:     MistServer -> Foghorn (PUSH_REWRITE) -> signed local authority / connected claim -> serving cluster
Viewer:     MistServer -> Foghorn (USER_NEW/END) -> cluster handling this viewer is serving cluster
Analytics:  trigger -> Decklog -> Kafka -> Periscope Ingest -> ClickHouse
                                                            -> finalized facts + canonical ledgers
                                                            -> Periscope Metering -> Kafka -> Purser
```

**Ingest path:** the publishing node's authenticated local session supplies the
serving cluster. Ready signed authority validates the credential, tenant, and
outage owner locally; normal connected operation still takes the distributed
placement claim through Commodore. `sendTriggerToDecklog()` carries that
serving cluster in `trigger.ClusterId`. `OriginClusterID` is also preserved from
stream resolution, but it is lineage/audit metadata rather than the work
attribution key.

**Metering path:** `generateTenantUsageSummary` reads finalized facts and canonical
5-minute sources. Viewer minutes and network bytes come from
`viewer_sessions_final`; processing comes from `processing_segments_final`; storage
is integrated directly from canonical `storage_snapshots`; API usage comes from
`api_usage_5m_v`.
Cluster-scoped meters are emitted as one usage report per work cluster:
delivery → serving cluster, processing → executing cluster, storage → placement
cluster. Tenant-level API/AI meters attach to the tenant's primary cluster.

## ClickHouse Schema

| Table / view                 | Cluster columns                                  | Engine / role                                     |
| ---------------------------- | ------------------------------------------------ | ------------------------------------------------- |
| `viewer_sessions_final`      | `cluster_id`, breakdown arrays from `USER_END`   | Append-only finalized facts                       |
| `viewer_usage_5m`            | `cluster_id`                                     | Canonical 5-minute ledger                         |
| `storage_gb_seconds_5m`      | `cluster_id`, storage provider attribution       | Dashboard ledger; billing integrates snapshots    |
| `processing_segments_final`  | `cluster_id`, `process_type`, `output_codec`     | Append-only finalized processing facts            |
| `*_hourly` / `*_daily` views | cluster columns matching their canonical sources | Refreshable rollup stores with public dedup views |

Hourly/daily rollup tables are dashboard caches. Billing reads finalized facts,
the canonical API ledger, and storage snapshots directly according to each
meter's contract; it never reads dashboard rollups.

## Settlement Query

Customer invoice usage is grouped by the cluster that actually served,
processed, or stored the metered work. Origin cluster remains available for
audit and routing analysis but does not determine the delivery charge. Customer storage billing sums provider slices into
customer-facing storage scope rows. The same billing pass persists generic
provider-attributed work into `purser.provider_usage_records`; storage is one
meter family on that surface, alongside processing and future provider-backed
work. Paid invoices allocate line revenue across those provider rows and write
`operator_credit_ledger` accruals with `source_type='provider_usage'`.

Current storage-provider attribution is not destination attestation. The
storage-owning Foghorn stamps the single-emitter `durable_backend_local` fact at
claim/completion time and later reports provider-observed bytes. That is sound
for the current platform-operated, official-only storage boundary, but it is not
settlement-grade proof from an independent destination. Remote provider storage
must use the verified assignment and destination attestation defined by
[`cross-cluster-durable-replication-v1.md`](../rfcs/cross-cluster-durable-replication-v1.md).

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
(`invoice_line_item_id`, `provider_usage_record_id`,
`usage_adjustment_id`, `stripe_invoice_id`). Payment reversals
(refunds/chargebacks) write negating `entry_type='clawback'` rows referencing
the original accrual via `reverses_ledger_id`, deduplicated through
`operator_credit_clawback_reversals`. Payout execution and operator-visible
reporting are not built; the productization of this ledger into the full
attribution → accrual → payout loop is
`docs/rfcs/federated-settlement-attribution.md`.

## Permanent Invoice Delivery

Draft invoices are live Purser projections and may be refreshed in place while
the billing period remains open. They never trigger customer email, payment,
Stripe meter export, operator credit, or period advancement. A complete period
is re-rated and the same invoice row plus its `invoice_line_items` snapshot is
made permanent in one transaction; `manual_review` remains a rerunnable hold,
not a finalized invoice.

That finalization transaction also writes two delivery surfaces. Stripe meter
events are keyed by permanent invoice line, so two codec/model/backend
dimension buckets of the same meter cannot collapse; their bounded dimensions
travel in the Stripe payload. Customer invoice email is queued in
`purser.invoice_email_outbox`. Horizontally scaled Purser workers lease and
retry those rows, then render only the permanent invoice header and itemized
line snapshot. Missing line items or unavailable SMTP defer delivery instead
of sending a partial summary. `payment_report_invoice_email_outbox_stuck`
exposes repeatedly failing notifications to operators.

## Key Files

| File                                             | Purpose                                                                              |
| ------------------------------------------------ | ------------------------------------------------------------------------------------ |
| `api_balancing/internal/triggers`                | Sets `origin_cluster_id` in streamContext and triggers                               |
| `api_analytics_ingest/internal/handlers`         | Extracts `cluster_id` + `origin_cluster_id` from MistTrigger into ClickHouse         |
| `api_analytics_query/cmd/periscope-metering`     | Regional scheduler and HA lease holder                                               |
| `api_analytics_query/internal/handlers`          | Per-work-cluster reports (`generateTenantUsageSummary`)                              |
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
