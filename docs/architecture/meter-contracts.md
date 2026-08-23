# Meter contracts

The contract every rated and operational meter conforms to, end-to-end. Billing reads finalized facts and canonical storage snapshots; dashboards read canonical ledgers and refreshable rollups. Changes to a row here are coordinated changes — proto, ClickHouse DDL, parser, rebuild worker, rating engine, UI.

Regional reports use one stable envelope, `models.UsageSummary`, containing
`report_id`, source/region/sequence, tenant, work cluster, period,
completeness, and a list of `{meter, unit, quantity, dimensions}` values. Meter
names are data, not struct fields. Purser validates every meter against
`purser.meter_definitions`, persists zero-priced quantities, and prices them
with selector rules when a tier or cluster opts in. There is no second billing
API or fixed-field compatibility envelope. Source-level `window_complete`
reports use the runtime system tenant and the reserved `_source` cluster, carry
no meters, and exist only to gate invoice completeness.

The columns

- **Meter** — canonical name (the `usage_type` in `purser.usage_records` for rated meters; the field name in `models.UsageSummary` for operational meters).
- **Unit** — the physical unit Purser stores and rating multiplies by `unit_price_per_<unit>`. Display unit on invoices/dashboards may differ (e.g. GiB-seconds internally, GiB-hours displayed).
- **Default rated** — `yes` means the default tier catalog attaches a pricing rule and the meter contributes to invoice line items without custom pricing. `no (priceable)` means the meter is persisted in canonical form and can be priced by cluster/custom rules.
- **Source event** — the immutable upstream fact the meter is derived from. For Mist triggers this is the canonical trigger type from `pkg/mist/triggers.go`.
- **Final fact table** — ClickHouse table whose rows are 1-to-1 with the source event at the _logical-fact_ level. Physically append-only `MergeTree`: each parser pass appends a new projection row; readers materialize the logical fact via `min/argMax` on `projection_version_ms` (see Projection model below).
- **Billing cursor time field** — the column the regional metering worker walks. Must be monotonic with respect to cursor advance — i.e. once the cursor passes `T`, no new row can land with this column `< T`. This is what makes the cursor invariant auditable.
- **Analytics window time field** — the column used to slice usage across 5-minute windows for the canonical ledger (and for dashboards that show "what happened during this hour"). May span multiple windows; may be retroactive (a USER_END finalized at 14:03 producing analytics rows back to 12:03 is normal).
- **Anomaly behavior** — what happens for facts that fail validation, time out, or land in the anomaly table.

## Rated meters

| Meter                                                                                                                                      | Unit                                                      | Rated                                   | Source event                                                                                        | Final fact table                                                                                                                                                                                                                                             | Billing cursor time                                                                                                                | Analytics window time                                                                                                                                                                                                                                 | Anomaly behavior                                                                                                                                                                                                                                                                             |
| ------------------------------------------------------------------------------------------------------------------------------------------ | --------------------------------------------------------- | --------------------------------------- | --------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ | ---------------------------------------------------------------------------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `delivered_minutes`                                                                                                                        | minutes                                                   | yes                                     | Mist `USER_END` trigger                                                                             | `periscope.viewer_sessions_final` (append-only `MergeTree`; natural key `(tenant_id, node_id, session_id)`). Read view `viewer_sessions_final_v` materializes via `min/argMax` on `projection_version_ms`                                                    | `billable_at_ms` (= `min(projection_version_ms)`, derived; cursor uses settlement lag default 2 min)                               | `source_started_at_ms` .. `source_ended_at_ms` (half-open). Local MistServer emits `hostTimes`, `connectorTimes`, and `streamTimes` after `tags`; parser/proto changes must preserve those arrays before we rely on per-host or per-stream settlement | Session without `USER_END` → `viewer_sessions_anomalous` after `stale_close_timeout` (default 4h), excluded from rated reads; magnitude exposed via `stale_session_minutes` operational meter                                                                                                |
| `egress_gb`                                                                                                                                | GiB (`downloaded_bytes`)                                  | no (priceable)                          | Mist `USER_END` trigger                                                                             | `periscope.viewer_sessions_final`                                                                                                                                                                                                                            | `billable_at_ms`                                                                                                                   | `source_started_at_ms` .. `source_ended_at_ms`                                                                                                                                                                                                        | Same as `delivered_minutes`; custom/marketplace pricing can opt in without changing the usage shape.                                                                                                                                                                                         |
| `ingress_gb`                                                                                                                               | GiB (`uploaded_bytes`)                                    | no (priceable)                          | Mist `USER_END` trigger                                                                             | `periscope.viewer_sessions_final`                                                                                                                                                                                                                            | `billable_at_ms`                                                                                                                   | `source_started_at_ms` .. `source_ended_at_ms`                                                                                                                                                                                                        | Priceable but unrated by default; custom/marketplace pricing can opt in without changing the usage shape.                                                                                                                                                                                    |
| `storage_gb_seconds_cold`                                                                                                                  | GiB-seconds (displayed as GiB-hours: `gb_seconds / 3600`) | yes                                     | Foghorn `StorageSnapshot` event with `scope='cold'`                                                 | Storage snapshots are the immutable facts; no separate `*_final` table. `periscope.storage_gb_seconds_5m` is the dashboard ledger with the same natural key `(tenant_id, cluster_id, scope, provider_tenant_id, provider_cluster_id, backend, window_start)` | Billing integrates canonical `storage_snapshots` over each 5-minute cursor slice using snapshots with `ingested_at_ms < slice_end` | `timestamp` from the storage snapshot; the billing slice is half-open `[period_start, period_end)`                                                                                                                                                    | Billing uses hold-constant integration: each slice is seeded with the latest at-or-before snapshot, applies in-window snapshots in order, and closes the slice with the last-known value. Storage that goes silent bills at its last-known size until a zero-size or updated snapshot lands. |
| `transcode_input_seconds`, `transcode_rendition_seconds`                                                                                   | media seconds                                             | rendition seconds are default-priceable | Mist `LIVEPEER_SEGMENT_COMPLETE` and `PROCESS_AV_VIRTUAL_SEGMENT_COMPLETE` triggers                 | `periscope.processing_segments_final`; dimensions include execution backend, codecs, track type, rendition profile, and resolution class                                                                                                                     | `billable_at_ms`                                                                                                                   | source segment interval                                                                                                                                                                                                                               | Missing work cluster or unusable processing identity fails the slice; malformed dimensions are quarantined by Purser.                                                                                                                                                                        |
| `remux_seconds`, `transcription_seconds`, `inference_frames`, `inference_input_tokens`, `inference_output_tokens`, `inference_invocations` | seconds / frames / tokens / invocations                   | no (priceable)                          | processing completion facts from native, Livepeer, ONNX, transcription, or later execution backends | dimensioned processing facts/ledgers; execution backend and model are dimensions, not new meter code paths                                                                                                                                                   | first accepted projection                                                                                                          | source work interval                                                                                                                                                                                                                                  | Unknown meters/dimensions are quarantined until registered; registered zero-priced meters remain itemized.                                                                                                                                                                                   |

Processing is first-class. Customer-facing meters are product/work shaped, but
the contract is deliberately broad enough for ONNX inference, transcription,
LLM tokens, Livepeer execution, and future model backends. Hardware utilization
may be retained as an operational meter when useful; it is not used as a
substitute for the work quantity the customer can understand.

## Operational and default-unrated meters

These meters are emitted in the same canonical `minute_5` delta envelope as rated meters. Default catalog tiers do not price them, but custom/marketplace pricing can opt in by adding explicit rules.

| Meter                                                                                                | Unit              | Source event                                                | Final fact table                                                                                             | Analytics window time                                   | Anomaly behavior                                                                                                                                      |
| ---------------------------------------------------------------------------------------------------- | ----------------- | ----------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------ | ------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------- |
| `stream_runtime_seconds` (replaces broken `stream_hours` bucket-count)                               | seconds           | Mist `STREAM_END` trigger                                   | `periscope.stream_sessions_final`                                                                            | `source_started_at_ms` .. `source_ended_at_ms`          | Stream that lingers past `stale_close_timeout` without `STREAM_END` → `stream_sessions_anomalous`                                                     |
| `max_viewers`                                                                                        | int               | Foghorn `StreamLifecycleUpdate` polled state                | `periscope.stream_state_current` (existing)                                                                  | sample timestamp                                        | n/a — sampled state, no anomaly model                                                                                                                 |
| `peak_bandwidth_mbps`                                                                                | Mbps              | Mist `CLIENT_LIFECYCLE_UPDATE` poll                         | `periscope.client_qoe_5m` (existing)                                                                         | `timestamp_5m`                                          | n/a — sampled                                                                                                                                         |
| `api_requests`, `api_errors`, `api_duration_ms`, `api_complexity`                                    | count / ms        | `pkg/clients/decklog` API request events                    | `periscope.api_requests` with stable per-envelope aggregate identity; `api_usage_5m` is the dashboard ledger | source `timestamp`; billing cursors on `ingested_at_ms` | API event with no `tenant_id` rejected at Decklog; delayed delivery bills in its ingestion slice and Kafka replay is anti-joined by `source_event_id` |
| `llm_input_tokens`, `llm_output_tokens`, `embedding_tokens`, `embedding_requests`, `search_requests` | tokens / requests | Skipper's durable `skipper_usage` publisher through Decklog | same `api_requests` fact contract; `api_usage_5m` remains a source-time dashboard projection                 | source `timestamp`; billing cursors on `ingested_at_ms` | Persisted and itemized even when the price is zero. Provider/model/service are bounded dimensions.                                                    |
| `unique_users`, `unique_tokens` (per billing slice)                                                  | distinct count    | API request events                                          | deduplicated `periscope.api_requests`; `api_usage_5m` retains mergeable dashboard estimates                  | billing cursors on `ingested_at_ms`                     | Same replay handling as the API counters above                                                                                                        |
| `storage_gb_seconds_hot`                                                                             | GiB-seconds       | Foghorn `StorageSnapshot` event, hot scope                  | `periscope.storage_gb_seconds_5m`                                                                            | `window_start`                                          | Hot storage is an edge/cache speed optimization. Operational by default; marketplace tiers can opt-in to pricing it via `priceable=true`              |
| `stale_session_minutes`                                                                              | minutes           | derived from `viewer_sessions_anomalous`                    | `viewer_sessions_anomalous`                                                                                  | `closed_at_ms`                                          | n/a — itself an anomaly meter                                                                                                                         |

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

For derived 5-min ledger rows (`viewer_usage_5m`, `processing_5m`, `api_usage_5m`, `storage_gb_seconds_5m`, etc.) the same model holds: the rebuild worker appends a projection row each time it computes the window; readers materialize via `min/argMax`. Storage snapshots are the raw facts, but billing reads the closed 5-minute storage ledger by first projection time so late snapshots bill once instead of being inferred independently from raw snapshots.

## Corrections

The pipeline supports **pure replay/reprojection only**. A parser pass that produces the same logical fact for an existing `source_event_id` is permitted and expected — multiple projection rows accumulate; readers see the same logical fact via `argMax`. `billable_at_ms` is unchanged because `min(projection_version_ms)` is unchanged.

Material billing corrections — a parser pass that produces a different billable value (different `duration_seconds`, different bytes, different scope) for a logical fact that has already been cursored past — do not mutate the original usage row. The cursor will not re-visit the row because `min(projection_version_ms)` is unchanged. Instead, the divergence produces an additive correction delta in Purser (`purser.usage_adjustments`) keyed by the divergence source id. Invoice aggregation unions applied adjustments with canonical `minute_5` delta rows.

### Operational guardrail

On a guarded projection insert:

1. Read the prior `argMax`-materialized value of every rated field for this natural key from the table (a small lookup; ClickHouse handles it as a partial GROUP BY over the key's existing projection rows).
2. Compare the new projection's rated-field values to the prior values.
3. If any rated field's value differs by more than a per-meter epsilon (`duration_seconds` ≥ 1, uploaded/downloaded bytes ≥ 1 KiB, scope changes, codec changes — defined per meter in the parser):
   - For finalized-fact tables, record the divergence before writing the new projection. If the divergence row cannot be written, the projection fails and the Kafka message retries.

- For derived storage ledger rows, the first projection is canonical usage for the projection slice. Later projections for the same natural key record a divergence before appending the latest projection. Billing receives an explicit correction only for already-cursored projections, while dashboard rollups keep the latest storage truth.
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

## Billing summarizer

The component that executes the cursor walk is the standalone regional `periscope-metering` binary (`api_analytics_query/cmd/periscope-metering`). It is deployed beside one logical ClickHouse source; Periscope Query has no scheduler or billing dependency. Multiple worker replicas share a source ID and use PostgreSQL fenced leases, while different regional ClickHouse deployments use different source IDs.

1. **Tenant set.** Active tenants are discovered from the canonical billing surfaces themselves (finalized-fact tables, storage snapshots, canonical ledgers, projection divergences over the last 7 days) — sourcing from these tables, not event logs, guarantees any tenant the rated meters can see is a tenant the cursor walks. That set is unioned with every tenant that already has a cursor row, so a tenant that goes quiet still gets its trailing slices closed.
2. **Cursor.** Each tenant has one row in `periscope.billing_cursors` — a **Postgres** (Yugabyte) table, not ClickHouse. `periscope.metering_sources.activated_at` is the no-history-replay boundary. A first-seen tenant starts at the later of that boundary and its earliest canonical fact. The walk target is `targetEnd = now − 2 min settlement lag`, truncated to the 5-minute grid.
3. **Slices.** The walk emits exact, half-open, 5-minute slices from the cursor to the target — a catch-up after downtime produces many 5-minute records, never one wide record. Per slice the summarizer runs first-projection/ingestion cursor queries against viewer, stream, processing, and API facts plus the closed storage ledger. Source-time 5-minute ledgers remain dashboard projections and are not used as delivery-time cursors. The summarizer ships the resulting `UsageSummary` rows to Kafka `billing.usage_reports` and only then advances the cursor. Empty slices still advance it; a mid-walk failure stops advancement so nothing is skipped, and the next tick resumes from the same slice.
4. **Completion.** Only after every tenant cursor reaches the common target does the worker emit one `window_complete` report per source/window. Finalized tenant reports and the marker use the same Kafka partition key, so Purser observes every certified report before the marker. Purser records source completeness from that marker—not from an individual tenant report—and invoice finalization fails closed if any required regional window is absent.

**Per-cluster, fail-closed attribution.** One report is emitted per `(tenant, work cluster)` with canonical usage in the slice. Viewer delivery always uses the serving `cluster_id`; `origin_cluster_id` remains audit context and never steals delivery revenue from the cluster that did the work. Processing uses its executing cluster and storage uses placement/provider attribution. A rated fact with no work cluster fails the tenant slice rather than guessing. Tenant-wide API/AI counters attach to the tenant's primary cluster through Quartermaster.

**Active reservations.** Every minute the worker derives an absolute provisional
viewer hold from open `USER_NEW` state plus the latest Mist client poll. The
poll refreshes liveness and cumulative bytes; it never becomes invoice usage.
Purser prices the active quantity against remaining period allowances, exposes
settled/reserved/available prepaid balances, and expires stale reservations
after three minutes if a worker disappears. `USER_END` remains the only final
delivery fact and replaces the need for the hold when the zero reservation
arrives.

**Provider usage split.** From canonical work ledgers the summarizer produces
customer-facing per-work-cluster meter quantities and provider-keyed
`ProviderUsage` rows. The latter preserve provider tenant/cluster, canonical
meter, unit, dimensions, source, and report identity in
`purser.provider_usage_records` for marketplace revenue allocation. Storage is
currently the first producer of this generic surface; processing and future
provider-backed meters use the same contract. Supported
`projection_divergences` rows are converted into additive `usage_adjustments`
on the same summaries (see Corrections).

## Anomaly invariant

For every rated meter, exactly one of two outcomes per source event:

- The event passes parser validation → row lands in the `*_final` table → billed once.
- The event fails or never arrives → row lands in `*_anomalous` (or doesn't appear at all) → **never billed**, exposed as an operational anomaly meter for visibility.

There is no third path. Parser errors, quarantines, and stale closes are all visible — none of them result in silently dropped revenue or silently inflated revenue.

## Storage scope split

Cold storage is the default rated product meter: objects persisted to S3/object storage are what customers pay for on platform tiers. Hot storage on the edge is a FrameWorks performance/cache optimization and remains operational-only by default. We still track hot GB-seconds for capacity, eviction, cache-efficiency dashboards, and marketplace/custom pricing.

The rated default `usage_type` is `storage_gb_seconds_cold`. The operational hot meter may use `storage_gb_seconds_hot` in dashboard records; platform tiers do not attach a pricing rule to it by default, while marketplace or custom cluster pricing may opt in. Purser's dimensioned window key includes tenant, work cluster, source, meter, dimension hash, and half-open period, so hot/cold scopes and provider reports remain distinct and replay-idempotent.

`purser.usage_records` intentionally does not carry provider ownership columns.
Provider attribution is canonical in the source fact and emitted separately as
`ProviderUsage`. The customer-invoice path sums providers into work-cluster
dimension buckets, because customers are billed for the work performed; the
parallel `purser.provider_usage_records` rows preserve who performed it. Paid
invoice finalization allocates line revenue across provider usage and explicit
correction rows, then writes `operator_credit_ledger` accruals sourced by
`provider_usage_record_id` or `usage_adjustment_id`.

## Display units

| Internal                                                  | Displayed                                                                          |
| --------------------------------------------------------- | ---------------------------------------------------------------------------------- |
| `delivered_minutes`                                       | "minutes" or "hours" depending on magnitude                                        |
| `ingress_gb`                                              | "Ingress (GiB)"                                                                    |
| `egress_gb`                                               | "Egress (GiB)"                                                                     |
| `storage_gb_seconds_*`                                    | "GiB-hours" (`gb_seconds / 3600`)                                                  |
| `transcode_input_seconds` / `transcode_rendition_seconds` | seconds plus execution backend, codec, track, rendition, and resolution dimensions |
| `remux_seconds` / `transcription_seconds`                 | seconds plus container, backend, model, and language dimensions as applicable      |
| `inference_*`                                             | frames, tokens, or invocations plus execution backend and model dimensions         |
| `stream_runtime_seconds`                                  | "hours" (`/ 3600`) for dashboards                                                  |

Display conversion lives in the rating engine (for invoices) and the GraphQL resolver (for dashboards). Internal storage stays in the unit named in this table — derived display values are computed on read, never stored.

## Release review gates

Before the metering release ships, reviewers must be able to prove each gate from code, migrations, tests, and generated artifacts:

- No rated query reads cumulative daily/hourly rollups. Rated meters come from `*_final` tables or canonical ledgers only.
- Billing cursor windows are exact, half-open, 5-minute slices for `value_kind='delta'` rows. A catch-up run must emit multiple 5-minute records, not one wide record.
- Purser rejects malformed meter keys, non-delta billed rows, and misaligned periods into quarantine instead of inserting billable records. Meter names are syntactic data so custom/marketplace pricing can add new meters without a schema release.
- Metered dashboard rollups expose deduped public names; no rated or metered aggregate resolver reads a raw append target or a bounded replacement view that silently drops 7d/30d history. Lifecycle/diagnostic event counters may read event-history tables directly, but those reads must fail closed rather than return partial zeros.
- `storage_gb_seconds_cold` is the default rated storage product. `storage_gb_seconds_hot` is tracked and priceable for marketplace/custom pricing, but default-unrated.
- Storage facts carry provider attribution for marketplace analysis: usage tenant, storage provider tenant/cluster, backend, and scope remain separate dimensions.
- Processing facts emit explicit input and rendition work with backend/codec/model dimensions. Livepeer uses segment duration; AV uses `source_advanced_ms`; ONNX/transcription/model work uses its natural frames, seconds, tokens, or invocation count.
- AV virtual segments dedupe on `source_event_id`, not `segment_number`; distinct triggers must not collapse just because AV writes `segment_number=0`.
- USER_END parser/proto behavior is checked against local MistServer payloads, including `hostTimes`, `connectorTimes`, and `streamTimes`. If those arrays are not preserved yet, per-host/per-stream settlement must not be claimed as supported.
- New processing backends reuse the dimensioned meter contract; they do not require a parallel billing publisher or versioned usage envelope.
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
- `docs/architecture/clickhouse-conventions.md` — schema-wide engine/rollup/versioning conventions
- `pkg/database/sql/clickhouse/periscope.sql` — DDL
- `api_billing/internal/rating/types.go` — `Meter` constants
