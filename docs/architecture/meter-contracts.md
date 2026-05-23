# Meter contracts

The contract every rated and operational meter conforms to, end-to-end. Billing reads finalized facts and canonical storage snapshots; dashboards read canonical ledgers and refreshable rollups. Changes to a row here are coordinated changes — proto, ClickHouse DDL, parser, rebuild worker, rating engine, UI.

The columns

- **Meter** — canonical name (the `usage_type` in `purser.usage_records` for rated meters; the field name in `models.UsageSummary` for operational meters).
- **Unit** — the physical unit Purser stores and rating multiplies by `unit_price_per_<unit>`. Display unit on invoices/dashboards may differ (e.g. GiB-seconds internally, GiB-hours displayed).
- **Default rated** — `yes` means the default tier catalog attaches a pricing rule and the meter contributes to invoice line items without custom pricing. `no (priceable)` means the meter is persisted in canonical form and can be priced by cluster/custom rules.
- **Source event** — the immutable upstream fact the meter is derived from. For Mist triggers this is the canonical trigger type from `pkg/mist/triggers.go`.
- **Final fact table** — ClickHouse table whose rows are 1-to-1 with the source event at the _logical-fact_ level. Physically append-only `MergeTree`: each parser pass appends a new projection row; readers materialize the logical fact via `min/argMax` on `projection_version_ms` (see Projection model below).
- **Billing cursor time field** — the column the Periscope billing scheduler walks. Must be monotonic with respect to cursor advance — i.e. once the cursor passes `T`, no new row can land with this column `< T`. This is what makes the cursor invariant auditable.
- **Analytics window time field** — the column used to slice usage across 5-minute windows for the canonical ledger (and for dashboards that show "what happened during this hour"). May span multiple windows; may be retroactive (a USER_END finalized at 14:03 producing analytics rows back to 12:03 is normal).
- **Anomaly behavior** — what happens for facts that fail validation, time out, or land in the anomaly table.

## Rated meters

| Meter                     | Unit                                                      | Rated          | Source event                                                                        | Final fact table                                                                                                                                                                                                                                                        | Billing cursor time                                                                                                                | Analytics window time                                                                                                                                                                                                                                 | Anomaly behavior                                                                                                                                                                                                                                                                             |
| ------------------------- | --------------------------------------------------------- | -------------- | ----------------------------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `delivered_minutes`       | minutes                                                   | yes            | Mist `USER_END` trigger                                                             | `periscope.viewer_sessions_final` (append-only `MergeTree`; natural key `(tenant_id, node_id, session_id)`). Read view `viewer_sessions_final_v` materializes via `min/argMax` on `projection_version_ms`                                                               | `billable_at_ms` (= `min(projection_version_ms)`, derived; cursor uses settlement lag default 2 min)                               | `source_started_at_ms` .. `source_ended_at_ms` (half-open). Local MistServer emits `hostTimes`, `connectorTimes`, and `streamTimes` after `tags`; parser/proto changes must preserve those arrays before we rely on per-host or per-stream settlement | Session without `USER_END` → `viewer_sessions_anomalous` after `stale_close_timeout` (default 4h), excluded from rated reads; magnitude exposed via `stale_session_minutes` operational meter                                                                                                |
| `egress_gb`               | GiB (`downloaded_bytes`)                                  | no (priceable) | Mist `USER_END` trigger                                                             | `periscope.viewer_sessions_final`                                                                                                                                                                                                                                       | `billable_at_ms`                                                                                                                   | `source_started_at_ms` .. `source_ended_at_ms`                                                                                                                                                                                                        | Same as `delivered_minutes`; custom/marketplace pricing can opt in without changing the usage shape.                                                                                                                                                                                         |
| `ingress_gb`              | GiB (`uploaded_bytes`)                                    | no (priceable) | Mist `USER_END` trigger                                                             | `periscope.viewer_sessions_final`                                                                                                                                                                                                                                       | `billable_at_ms`                                                                                                                   | `source_started_at_ms` .. `source_ended_at_ms`                                                                                                                                                                                                        | Priceable but unrated by default; custom/marketplace pricing can opt in without changing the usage shape.                                                                                                                                                                                    |
| `storage_gb_seconds_cold` | GiB-seconds (displayed as GiB-hours: `gb_seconds / 3600`) | yes            | Foghorn `StorageSnapshot` event with `scope='cold'`                                 | Storage snapshots are the immutable facts; no separate `*_final` table. `periscope.storage_gb_seconds_5m` is the dashboard ledger with the same natural key `(tenant_id, cluster_id, scope, provider_tenant_id, provider_cluster_id, backend, window_start)`            | Billing integrates canonical `storage_snapshots` over each 5-minute cursor slice using snapshots with `ingested_at_ms < slice_end` | `timestamp` from the storage snapshot; the billing slice is half-open `[period_start, period_end)`                                                                                                                                                    | Billing uses hold-constant integration: each slice is seeded with the latest at-or-before snapshot, applies in-window snapshots in order, and closes the slice with the last-known value. Storage that goes silent bills at its last-known size until a zero-size or updated snapshot lands. |
| `media_seconds`           | seconds of input media                                    | no (priceable) | Mist `LIVEPEER_SEGMENT_COMPLETE` and `PROCESS_AV_VIRTUAL_SEGMENT_COMPLETE` triggers | `periscope.processing_segments_final`. Natural key `(tenant_id, node_id, stream_id, source_event_id)`; `source_event_id = sha256(node_id \|\| NUL \|\| trigger_type \|\| NUL \|\| payload_raw)`. Read view `processing_segments_final_v` materializes via `min/argMax`. | `billable_at_ms`                                                                                                                   | `source_started_at_ms` .. `source_ended_at_ms` (per-segment window)                                                                                                                                                                                   | Empty `output_codec` is quarantined. Livepeer uses Mist `segment_duration_ms`; AV uses Mist `source_advanced_ms`. `usage_details.codec_seconds` carries plain codec and joint process/codec keys for `codec_multiplier` pricing.                                                             |

> Hardware-shaped AI meters are deliberately not in the canonical ledger. Future processing meters should be product-shaped (media seconds, rendition seconds, etc.), not hardware-unit-shaped.

## Operational and default-unrated meters

These meters are emitted in the same canonical `minute_5` delta envelope as rated meters. Default catalog tiers do not price them, but custom/marketplace pricing can opt in by adding explicit rules.

| Meter                                                                  | Unit           | Source event                                 | Final fact table                                                                                   | Analytics window time                          | Anomaly behavior                                                                                                                         |
| ---------------------------------------------------------------------- | -------------- | -------------------------------------------- | -------------------------------------------------------------------------------------------------- | ---------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------- |
| `stream_runtime_seconds` (replaces broken `stream_hours` bucket-count) | seconds        | Mist `STREAM_END` trigger                    | `periscope.stream_sessions_final`                                                                  | `source_started_at_ms` .. `source_ended_at_ms` | Stream that lingers past `stale_close_timeout` without `STREAM_END` → `stream_sessions_anomalous`                                        |
| `max_viewers`                                                          | int            | Foghorn `StreamLifecycleUpdate` polled state | `periscope.stream_state_current` (existing)                                                        | sample timestamp                               | n/a — sampled state, no anomaly model                                                                                                    |
| `peak_bandwidth_mbps`                                                  | Mbps           | Mist `CLIENT_LIFECYCLE_UPDATE` poll          | `periscope.client_qoe_5m` (existing)                                                               | `timestamp_5m`                                 | n/a — sampled                                                                                                                            |
| `api_requests`, `api_errors`, `api_duration_ms`, `api_complexity`      | count / ms     | `pkg/clients/decklog` API request events     | `periscope.api_usage_5m`                                                                           | `window_start`                                 | API event with no `tenant_id` rejected at Decklog; never reaches the ledger                                                              |
| `unique_users`, `unique_tokens` (per window)                           | distinct count | API request events                           | `periscope.api_usage_5m` (`AggregateFunction(uniqCombined, UInt64)` states plus display estimates) | `window_start`                                 | n/a — same as above                                                                                                                      |
| `storage_gb_seconds_hot`                                               | GiB-seconds    | Foghorn `StorageSnapshot` event, hot scope   | `periscope.storage_gb_seconds_5m`                                                                  | `window_start`                                 | Hot storage is an edge/cache speed optimization. Operational by default; marketplace tiers can opt-in to pricing it via `priceable=true` |
| `stale_session_minutes`                                                | minutes        | derived from `viewer_sessions_anomalous`     | `viewer_sessions_anomalous`                                                                        | `closed_at_ms`                                 | n/a — itself an anomaly meter                                                                                                            |

## Projection model

This model applies to every `*_final` and 5-minute ledger table — viewer sessions, stream sessions, processing segments, and the five 5-min ledgers. The architecture is identical; only the field shapes differ.

`*_final` and ledger tables are **append-only `MergeTree`**. They are not `ReplacingMergeTree`, they are not upserted, and the parser never does SELECT-before-insert. Each parser/rebuilder pass writes a new row; multiple projection rows per logical fact coexist on disk. Readers materialize the logical fact on the fly.

### Storage shape

- Engine: `MergeTree` (append-only).
- `ORDER BY` starts with the access pattern of the hot billing path, not just the natural key. For `viewer_sessions_final`: `(tenant_id, projection_version_ms, node_id, session_id)`. Same shape for every other projection table — tenant first for per-tenant pruning, then `projection_version_ms` so the cursor's time-range predicate hits the sort index, then the natural-key columns to localize prior-projection lookups.
- `PARTITION BY toYYYYMM(toDateTime(projection_version_ms / 1000))` — calendar-month partitions on the projection time.

### Two read shapes

**1. Audit / dashboard / rebuild — the `_v` view.** A non-materialized SQL `VIEW` next to each table, e.g. `viewer_sessions_final_v`:

```sql
CREATE VIEW periscope.viewer_sessions_final_v AS
SELECT
    tenant_id, node_id, session_id,
    min(projection_version_ms) AS billable_at_ms,
    argMax(source_started_at_ms, projection_version_ms) AS source_started_at_ms,
    argMax(source_ended_at_ms,   projection_version_ms) AS source_ended_at_ms,
    argMax(edge_received_at_ms,  projection_version_ms) AS edge_received_at_ms,
    argMax(duration_seconds,     projection_version_ms) AS duration_seconds,
    argMax(uploaded_bytes,       projection_version_ms) AS uploaded_bytes,
    argMax(downloaded_bytes,     projection_version_ms) AS downloaded_bytes,
    argMax(closed_reason,        projection_version_ms) AS closed_reason
    -- … remaining fields …
FROM periscope.viewer_sessions_final
GROUP BY tenant_id, node_id, session_id;
```

This view is the canonical surface for dashboards, ledger rebuilders, and audit queries. It is **not** the surface the billing cursor uses — see below.

**2. Hot billing path — purpose-built query.** the billing cursor query is hand-written because `WHERE billable_at_ms BETWEEN …` is a post-aggregation predicate on the view and ClickHouse cannot reliably push it through to partition pruning. Instead the cursor query exploits the table's `ORDER BY` directly:

```sql
WITH window_candidates AS (
    -- Step 1: rows with at least one projection in the cursor window.
    -- Partition pruning + sort skip-index hit projection_version_ms cleanly.
    SELECT
        tenant_id, node_id, session_id,
        min(projection_version_ms)   AS proj_first_in_window,
        argMax(duration_seconds,     projection_version_ms) AS duration_seconds,
        argMax(uploaded_bytes,       projection_version_ms) AS uploaded_bytes,
        argMax(downloaded_bytes,     projection_version_ms) AS downloaded_bytes,
        argMax(closed_reason,        projection_version_ms) AS closed_reason
        -- … rated fields only — view does the full set …
    FROM periscope.viewer_sessions_final
    WHERE projection_version_ms >= ? -- cursor_start
      AND projection_version_ms <  ? -- cursor_end
    GROUP BY tenant_id, node_id, session_id
)
SELECT c.*
FROM window_candidates c
LEFT ANTI JOIN (
    -- Step 2: anti-join — exclude keys whose first projection is older
    -- than the cursor window. Bounded to the candidate natural keys.
    -- Partition pruning: projection_version_ms < cursor_start.
    SELECT DISTINCT tenant_id, node_id, session_id
    FROM periscope.viewer_sessions_final
    WHERE projection_version_ms < ? -- cursor_start
      AND (tenant_id, node_id, session_id) IN (
          SELECT tenant_id, node_id, session_id FROM window_candidates
      )
) prior
USING (tenant_id, node_id, session_id);
```

The result set is exactly the logical facts whose `billable_at_ms` (= `min(projection_version_ms)`) lies in the cursor window. The candidates scan touches the partitions overlapping the window plus settlement lag; the anti-join touches older partitions but only for the candidate natural keys.

Each rated meter ships this query as a typed Go helper in `api_analytics_query` per rated meter, not as a generic `SELECT FROM *_final_v WHERE billable_at_ms`. Cursor performance is the helper's responsibility; the view is for everything else.

### Why the asymmetry

Two read shapes look like duplicated effort, but they're enforcing two different contracts:

- The `_v` view is the **semantic contract** — "given this table, this is the logical fact." It's the source of truth for what a row means; Readers should not have to re-derive `min/argMax` themselves.
- The billing helper is the **performance contract** — "given a cursor window, find new facts in O(window-size + candidate-anti-join), not O(table-size)." It's hand-shaped to the table's ordering and partitioning so the cursor stays cheap as retention grows.

If a future ClickHouse version pushes post-GROUP-BY predicates through to partition pruning automatically, the helper collapses back to a view query. Until then, the asymmetry is deliberate.

## Rollup contract

Dashboard rollups are caches over canonical ledgers, not billing inputs. The public rollup names (`tenant_usage_hourly`, `viewer_geo_hourly`, `processing_daily`, etc.) must be deduped read surfaces. A refreshable materialized view may `APPEND` into an internal store table, but callers must not query that raw store directly. The public table or view with the historical name must collapse refresh versions by natural key before returning rows.

Rules:

- Billing never reads dashboard rollups.
- Dashboards never read raw append targets.
- Refreshable MV filters are projection-version catch-up windows, not query-retention windows. APPEND refreshes rewrite affected source-time buckets into a raw store; the public surface dedupes by refresh version.
- Unique counts are stored as `uniqCombined` aggregate states and merged with `uniqCombinedMerge` / `uniqCombinedMergeState`. They are never summed as scalars across windows.
- Rollups keep the public names consumers already query. Temporary `_v2` tables are not part of the release shape; if a scratch table is needed during migration it is dropped before release.

### Steady-state row counts

Most natural keys carry exactly one projection row (parser ran once, no reproject). `argMax` over a one-row group is trivial; the anti-join is a no-op. Re-projection produces additional rows; ClickHouse collapses them on read. If a natural key accumulates many projections over a long retention window, we can either:

- accept the read cost (small per-key cost, amortized across the query),
- run `OPTIMIZE TABLE … FINAL` during a maintenance window to compact, or
- evolve the table to an `AggregatingMergeTree` with `argMaxState` columns. Out of scope.

## Time-field semantics

Four time concepts per fact. **Edge-time and billable-time are different concepts and never the same column.**

- `source_started_at_ms` — when the underlying activity began, in the source system's frame of reference. For `USER_END` this is `edge_received_at_ms - duration_seconds * 1000`. For `STREAM_END` the parser looks up the current stream start from `stream_state_current` when available and otherwise records a zero-duration interval at `source_ended_at_ms` rather than inventing runtime from viewer counters. For storage snapshots it's the snapshot wall-clock. For API requests it's `request_received_at_ms`. Used for analytics window-slicing; never for the cursor.
- `source_ended_at_ms` — when the underlying activity ended, in the source system's frame. For `USER_END` this is approximately `edge_received_at_ms`. For storage snapshots = `source_started_at_ms` (instantaneous). For API requests = `request_received_at_ms + duration_ms` if available, else equal to start. Used for analytics window-slicing; never for the cursor.
- `edge_received_at_ms` — when Helmsman / the collector accepted the trigger at the edge. **Audit-only. Never used for cursoring.** It can be hours older than `projection_version_ms` because the edge WAL durably retains triggers across edge outages.
- `projection_version_ms` — when the parser wrote this projection row. **Stored as a column on every row.** Determines field-value freshness via `argMax(field, projection_version_ms)`. Also determines `billable_at_ms` via `min(projection_version_ms)` across all projection rows for a given natural key.

`billable_at_ms` is therefore **derived, not stored**. It is `min(projection_version_ms)` across the projection rows for one logical fact — the first time Periscope saw it. It is deterministic given the table contents, never set explicitly by the parser, never overwritten. The billing cursor walks this derived value over the read view.

For derived 5-min ledger rows (`viewer_usage_5m`, `processing_5m`, `api_usage_5m`, etc.) the same model holds: the rebuild worker appends a projection row each time it computes the window; readers materialize via `min/argMax`. Storage billing is the exception: invoices integrate `storage_snapshots` directly so a tenant with no fresh snapshot activity still bills from the last-known size. The storage ledger remains the dashboard/rollup source.

## Corrections

The pipeline supports **pure replay/reprojection only**. A parser pass that produces the same logical fact for an existing `source_event_id` is permitted and expected — multiple projection rows accumulate; readers see the same logical fact via `argMax`. `billable_at_ms` is unchanged because `min(projection_version_ms)` is unchanged.

Material billing corrections — a parser pass that produces a different billable value (different `duration_seconds`, different bytes, different scope) for a logical fact that has already been cursored past — do not mutate the original usage row. The cursor will not re-visit the row because `min(projection_version_ms)` is unchanged. Instead, the divergence produces an additive correction delta in Purser (`purser.usage_adjustments`) keyed by the divergence source id. Invoice aggregation unions applied adjustments with canonical `minute_5` delta rows.

### Operational guardrail

On a guarded projection insert:

1. Read the prior `argMax`-materialized value of every rated field for this natural key from the table (a small lookup; ClickHouse handles it as a partial GROUP BY over the key's existing projection rows).
2. Compare the new projection's rated-field values to the prior values.
3. If any rated field's value differs by more than a per-meter epsilon (`duration_seconds` ≥ 1, uploaded/downloaded bytes ≥ 1 KiB, scope changes, codec changes — defined per meter in the parser):
   - For finalized-fact tables, record the divergence before writing the new projection. If the divergence row cannot be written, the projection fails and the Kafka message retries.
   - For derived storage ledger rows, record the divergence and append the latest projection. Billing receives an explicit correction for already-cursored source windows, while dashboard rollups keep the latest storage truth.
   - Increment `periscope_projection_divergence_total{table, meter, field}` Prometheus counter.
   - Write an audit row to `periscope.projection_divergences` : `(observed_at_ms, table_name, natural_key_json, prior_value_json, new_value_json, source_event_id)`.

This gives us:

- A counter on dashboards we can alert on (any non-zero divergence rate is "investigate").
- A queryable audit trail of which rows diverged.
- A durable correction row in Purser for supported divergence types, without mutating already-billed facts.

The lookup is a per-insert cost. We accept it because divergence is expected to be rare (parser code changes, schema migrations). If it becomes a hot-path bottleneck, batch the lookups.

### Correction handling

The production invariant is: **corrections happen through explicit additive adjustment rows, never through silent mutation of billable ledgers.** The divergence table is the source of truth for the adjustment payload: it carries the natural key, prior value, new value, and source event. The billing summarizer converts supported divergence rows into `UsageSummary.usage_adjustments`; Purser persists them in `purser.usage_adjustments` with `value_kind='correction_delta'` and `status='applied'`. Invoice aggregation includes those deltas alongside canonical `usage_records`.

The cursor walk over `billable_at_ms` is therefore monotonic and never replays an already-billed row.

## Cursor invariant

Every rated meter walks `billable_at_ms` — the derived "first projection" column on the materialize-on-read view. Cursor never walks `edge_received_at_ms`, never walks `source_*_at_ms`, never walks `window_start`. The invariant:

```
For every cursor advance [T_old, T_new):
  every logical fact whose billable_at_ms ∈ [T_old, T_new) is billed exactly once;
  no logical fact's billable_at_ms can decrease over time, because billable_at_ms
    is min(projection_version_ms) and projection rows are append-only;
  re-projections of an already-cursored fact do not re-bill, because they only
    add later projection rows (raising max(projection_version_ms), never the min).
```

The first clause holds because the WAL absorbs edge-side outages and Periscope stamps `projection_version_ms` when the projection row is written — a `USER_END` accepted at the edge at 10:00 and stuck in the WAL until 10:45 produces a projection row at 10:45 with `min(projection_version_ms) = billable_at_ms = 10:45`. The cursor sees it on the 10:45+lag tick. **Edge outages defer billing; they never lose it.**

The third clause is the corrections guardrail. A pure replay/reprojection only adds projection rows with higher `projection_version_ms`. `billable_at_ms` (the min) is unchanged. The cursor does not re-visit the row. If a parser pass changes a field value, the divergence guardrail records the difference and the billing summarizer emits an additive correction where the divergence type is supported.

Settlement lag (`targetEnd = now - settlement_lag`, default 2 min) absorbs in-Kafka reorderings between parser instances. Anything older than the lag is assumed durably visible.

## Anomaly invariant

For every rated meter, exactly one of two outcomes per source event:

- The event passes parser validation → row lands in the `*_final` table → billed once.
- The event fails or never arrives → row lands in `*_anomalous` (or doesn't appear at all) → **never billed**, exposed as an operational anomaly meter for visibility.

There is no third path. Parser errors, quarantines, and stale closes are all visible — none of them result in silently dropped revenue or silently inflated revenue.

## Storage scope split

Cold storage is the default rated product meter: objects persisted to S3/object storage are what customers pay for on platform tiers. Hot storage on the edge is a FrameWorks performance/cache optimization and remains operational-only by default. We still track hot GB-seconds for capacity, eviction, cache-efficiency dashboards, and marketplace/custom pricing.

The rated default `usage_type` is `storage_gb_seconds_cold`. The operational hot meter may use `storage_gb_seconds_hot` in dashboard records; platform tiers do not attach a pricing rule to it by default, while marketplace or custom cluster pricing may opt in. The existing `UNIQUE (tenant_id, cluster_id, usage_type, period_start, period_end)` unique constraint keeps hot and cold rows distinct.

`purser.usage_records` intentionally does not carry storage-provider columns. Provider attribution (storage_provider_tenant_id, storage_provider_cluster_id, storage_backend) is canonical in `storage_snapshots` and mirrored into `periscope.storage_gb_seconds_5m` for dashboard rollups. The customer-invoice path sums providers per `(cluster, scope)` before emitting to Purser, because customers are billed by where their content sits (cluster), not who hosted it. The same billing summarizer also emits provider-keyed storage rows into `purser.storage_provider_usage_records`, preserving usage tenant, customer cluster, provider tenant/cluster, backend, scope, meter, and GB-seconds. Paid invoice finalization allocates storage line revenue across those provider rows and provider-attributed storage correction rows, then writes `operator_credit_ledger` accruals sourced by `storage_provider_usage_record_id` or `usage_adjustment_id`.

## Display units

| Internal                 | Displayed                                                                     |
| ------------------------ | ----------------------------------------------------------------------------- |
| `delivered_minutes`      | "minutes" or "hours" depending on magnitude                                   |
| `ingress_gb`             | "Ingress (GiB)"                                                               |
| `egress_gb`              | "Egress (GiB)"                                                                |
| `storage_gb_seconds_*`   | "GiB-hours" (`gb_seconds / 3600`)                                             |
| `media_seconds`          | total media seconds, with codec and process/codec detail from `usage_details` |
| `stream_runtime_seconds` | "hours" (`/ 3600`) for dashboards                                             |

Display conversion lives in the rating engine (for invoices) and the GraphQL resolver (for dashboards). Internal storage stays in the unit named in this table — derived display values are computed on read, never stored.

## Release review gates

Before the metering release ships, reviewers must be able to prove each gate from code, migrations, tests, and generated artifacts:

- No rated query reads cumulative daily/hourly rollups. Rated meters come from `*_final` tables or canonical ledgers only.
- Billing cursor windows are exact, half-open, 5-minute slices for `value_kind='delta'` rows. A catch-up run must emit multiple 5-minute records, not one wide record.
- Purser rejects malformed meter keys, non-delta billed rows, and misaligned periods into quarantine instead of inserting billable records. Meter names are syntactic data so custom/marketplace pricing can add new meters without a schema release.
- Metered dashboard rollups expose deduped public names; no rated or metered aggregate resolver reads a raw append target or a bounded replacement view that silently drops 7d/30d history. Lifecycle/diagnostic event counters may read event-history tables directly, but those reads must fail closed rather than return partial zeros.
- `storage_gb_seconds_cold` is the default rated storage product. `storage_gb_seconds_hot` is tracked and priceable for marketplace/custom pricing, but default-unrated.
- Storage facts carry provider attribution for marketplace analysis: usage tenant, storage provider tenant/cluster, backend, and scope remain separate dimensions.
- `media_seconds` uses input media duration. Livepeer uses segment duration; AV uses `source_advanced_ms`. Livepeer `LIVEPEER_SEGMENT_COMPLETE` currently does not carry output codec, so Livepeer segments are classified as H.264 until that Mist trigger payload is extended. Wall-clock `seconds_since_last`, turnaround, RTF, frame counts, and bytes are operational detail only.
- AV virtual segments dedupe on `source_event_id`, not `segment_number`; distinct triggers must not collapse just because AV writes `segment_number=0`.
- USER_END parser/proto behavior is checked against local MistServer payloads, including `hostTimes`, `connectorTimes`, and `streamTimes`. If those arrays are not preserved yet, per-host/per-stream settlement must not be claimed as supported.
- Hardware-shaped marketing or billing terms such as `gpu_hours` do not appear in public pricing, docs, API schemas, or default meter registries.
- `average_storage_gb` appears only in explicit historical inventory or migration notes, not in active APIs, seed data, pricing catalogs, or generated demo data.
- `make graphql` is run outside the sandbox after GraphQL schema changes; generated gateway and Houdini artifacts must match the checked-in schema before deploy.

## Changing this contract

A meter that needs to change unit, source event, or anomaly behavior requires:

1. A row update here.
2. Coordinated change to:
   - `pkg/database/sql/clickhouse/periscope.sql` (schema)
   - `api_analytics_ingest/internal/handlers/handlers.go` (parser + rebuild)
   - `api_billing/internal/rating/types.go` (known defaults and unit conversion, if needed)
   - `api_billing/internal/handlers/cluster_rating.go` (aggregation rules)
   - `pkg/graphql/schema.graphql` (if dashboard-visible)
3. A migration plan if existing `purser.usage_records` rows carry the old shape.
4. Pricing-row migration if the unit changed (e.g. GiB-month → GiB-hours).

## Related

- `docs/architecture/trigger-durability.md` — the WAL contract feeding Mist triggers into `raw_mist_triggers`
- `docs/architecture/analytics-pipeline.md` — the end-to-end flow
- `docs/architecture/finalized-fact-tables.md` — the table-model details for `*_final` and `*_anomalous`
- `pkg/database/sql/clickhouse/periscope.sql` — DDL
- `api_billing/internal/rating/types.go` — `Meter` constants
