# ClickHouse conventions (Periscope schema)

Cross-cutting engine, rollup, and versioning conventions for `pkg/database/sql/clickhouse/periscope.sql`. This page explains why every table family is shaped the way it is; it deliberately does not restate the table-model details for `*_final` / `*_anomalous` ([finalized-fact-tables.md](finalized-fact-tables.md)) or the meter-by-meter billing contract ([meter-contracts.md](meter-contracts.md)).

## Database engine

The `periscope` database uses the `Replicated` database engine: DDL auto-propagates across replicas via Keeper, every table gets a shared replica path, and refreshable-MV refresh coordination (one replica refreshes) requires it. The same DDL runs in dev (single node + embedded Keeper) and prod; `{shard}`/`{replica}` come from server macros. See [database-ha.md](database-ha.md).

## Engine selection

Every table is one of four Replicated engines, chosen by the table's role — not by taste:

| Engine                             | Role                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                   | Examples                                                                                                                       |
| ---------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------ |
| `ReplicatedMergeTree`              | Append-only event/sample/fact streams; nothing collapses at the engine level                                                                                                                                                                                                                                                                                                                                                                                                                                           | `stream_event_log`, `*_samples`, `viewer_connection_events`, `*_final`, the 5-min ledgers, `projection_divergences`            |
| `ReplicatedReplacingMergeTree(v)`  | Latest-row-wins by natural key: current-state snapshots (versioned on `updated_at`/`version`), the replay-dedup log `raw_mist_triggers` (versioned on ingest time, keyed on the stable event id), the client-beacon dedup log `client_qoe_session_deltas` (`ReplacingMergeTree(timestamp)` keyed on the client-stable tuple `(tenant_id, content_id, session_id, beacon_seq)` — `event_id` is Bridge-minted per HTTP request and explicitly NOT the dedupe key), and rollup stores (versioned on `refresh_version_ms`) | `stream_state_current`, `artifact_state_current`, `raw_mist_triggers`, `client_qoe_session_deltas`, `*_store` rollup targets   |
| `ReplicatedAggregatingMergeTree`   | Partial-aggregate state rollups: columns are `AggregateFunction` states merged at read time                                                                                                                                                                                                                                                                                                                                                                                                                            | `viewer_sessions_current` (connect/disconnect merge), `node_performance_5m`, `node_metrics_1h`, `orchestrator_discovery_5m/1h` |
| `ReplicatedSummingMergeTree(cols)` | Purely additive counters folded by insert-time MVs                                                                                                                                                                                                                                                                                                                                                                                                                                                                     | `quality_tier_daily`, `routing_cluster_hourly`, `federation_hourly`                                                            |

Partitioning is monthly (`PARTITION BY toYYYYMM(...)`) on the column the hot read path range-scans — projection time for billing-cursor tables, source time for dashboard tables.

The load-bearing caveat for both Replacing and Aggregating engines: **collapse/merge is eventual**, happening during background merges. Reads must never depend on merges having run — they go through a deduping view (`argMax` + `GROUP BY` natural key), an aggregate-merge function, or `FINAL` on low-volume surfaces. This is what the dedup invariant below enforces.

## `*_final` tables and deduped `_v` views

Finalized facts and canonical 5-min ledgers are **append-only `MergeTree`** with a paired non-materialized `_v` view (`viewer_sessions_final` + `viewer_sessions_final_v`, `viewer_usage_5m` + `viewer_usage_5m_v`, …). The parser never upserts and never SELECTs-before-insert; re-projections append rows and the `_v` view materializes the logical fact via `min/argMax` on `projection_version_ms`. `billable_at_ms` is derived (`min(projection_version_ms)`), never stored. The full rationale (race-freedom, cursor monotonicity, the hand-written billing-cursor query) is in [finalized-fact-tables.md](finalized-fact-tables.md) and [meter-contracts.md](meter-contracts.md).

`ledger_rebuild_cursors` (one row per ledger, `ReplicatedReplacingMergeTree(updated_at_ms)`, `ORDER BY ledger_name`) tracks each rebuild worker's position: the workers in `api_analytics_ingest/internal/handlers/ledger_rebuilders.go` walk the source facts by `projection_version_ms` and recompute every affected 5-minute window, appending new projection rows to the ledger. Restarts resume from the cursor; recomputation is idempotent because ledgers use the same append + `argMax`-on-read projection model — a rerun adds later projection rows, it never mutates or double-counts.

## The versioned rollup pattern: store + `_mv` + public deduped name

Dashboard rollups (`tenant_usage_5m/hourly/daily`, `viewer_geo_hourly/daily`, `processing_hourly/daily`, `api_usage_hourly/daily`, …) are refreshable materialized views, and every one follows the same triple:

1. `<rollup>_store` — `ReplicatedReplacingMergeTree(refresh_version_ms)` append target. Nothing reads it directly.
2. `<rollup>_mv` — `REFRESH EVERY … APPEND TO <rollup>_store`. Each refresh selects the source-time buckets touched by recent projection activity (a projection-version **catch-up window**, e.g. "buckets with projections in the last 2 hours/2 days"), recomputes them from a deduped canonical surface (e.g. `viewer_usage_5m_v`), and appends them stamped with `refresh_version_ms = now64(3)`.
3. `<rollup>` — the public name is a plain `VIEW` that collapses refresh versions: `argMax(col, refresh_version_ms) … GROUP BY natural key`. Resolvers query only this name.

Why the triple instead of something simpler:

- **A bounded replacement refresh truncates history.** A refreshable MV that _replaces_ its target publishes only what its query window selected — a 2-hour catch-up filter would leave a 2-hour table and silently drop the 7d/30d history dashboards paginate over. Replacement is only acceptable when the refresh recomputes the full published retention, which is exactly what the catch-up optimization avoids.
- **An unversioned append replay double-counts.** With plain `APPEND` into a `MergeTree`, every refresh (and every refresh retry after a replica hiccup) adds the same buckets again, and a `sum()` over the store counts them twice. Versioning each refresh and deduping under the public name makes refreshes idempotent: the latest refresh of each bucket wins, older refresh generations are collapsed on read (and eventually by background merges).

Unique counts inside rollups are stored as `uniqCombined` aggregate states and merged with `uniqCombinedMerge` — never summed as scalars across windows or refresh versions. The billing-side rules (billing never reads rollups; rollups are caches over canonical ledgers) are in the Rollup contract of [meter-contracts.md](meter-contracts.md).

## Ratio metrics: num/denom decomposition

A rollup row never stores a ratio, average, or percentage. It stores the **additive numerator and denominator separately** and the read layer divides:

- `routing_cluster_hourly` / `federation_hourly` store `sum_latency_ms` + `event_count` (and `success_count` / `failure_count`), not an average latency or success rate.
- Session QoE stores `rebuffer_ms` + `played_ms` deltas; the rebuffering ratio is computed at read time over deduped rows.
- The deferred session-QoE 5-minute rollup must keep this shape — num/denom columns, grouped so capability flags (`frame_stats_supported`-style) can exclude non-reporting sources from the denominator ([analytics-pipeline.md](analytics-pipeline.md), `docs/rfcs/external-player-qoe.md`).

The reasons: pre-computed ratios cannot be merged across buckets, partitions, or refresh versions without weighting errors (a 99% ratio over 10 s and a 50% ratio over 10 h don't average to 74.5%); additive counters survive `SummingMergeTree` folds and partial-window beacons under plain `sum()`; and 0/0 stays distinguishable as "no data" instead of reading as perfect.

## The dedup invariant

**Aggregates are computed only from deduped surfaces, never from raw append streams.** A deduped surface is one of: a `_v` projection view, a public rollup name, an aggregate-state merge, or an engine-collapsed read (`FINAL`) on a low-volume table. Concretely:

- Rollup MVs source from `viewer_usage_5m_v` (deduped ledger view), not `viewer_usage_5m` (raw append table).
- Resolvers read public rollup names, never `*_store` append targets.
- Session-QoE and VOD-retention ratios are computed over `FINAL`-read `ReplacingMergeTree` tables; any future rollup over them must also start from a deduped surface.

Summing a raw append table counts every re-projection and every refresh generation; summing a `ReplacingMergeTree` before merges run counts duplicate client beacons and replayed events. Both are silent inflation — the release gates in [meter-contracts.md](meter-contracts.md) reject either shape.

## Related

- [finalized-fact-tables.md](finalized-fact-tables.md) - table model for `*_final`, `*_anomalous`, and the 5-min ledgers
- [meter-contracts.md](meter-contracts.md) - meter contract, cursor invariant, rollup contract, release gates
- [analytics-pipeline.md](analytics-pipeline.md) - end-to-end pipeline and table glossary
- [database-ha.md](database-ha.md) - ClickHouse replication/Keeper topology
- `pkg/database/sql/clickhouse/periscope.sql` - DDL (conventions in section headers)
