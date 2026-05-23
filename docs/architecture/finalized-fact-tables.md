# Finalized-fact tables

Companion to [meter-contracts.md](meter-contracts.md). The contracts doc says _what_ each meter promises; this doc says _how_ the underlying `*_final` and 5-min ledger tables are shaped, why the projection model is append-only, and how readers consume them.

## What "finalized" means

A row in a `*_final` table is one logical immutable fact derived from a single upstream event. For Mist viewer sessions that event is `USER_END`; for stream sessions it's `STREAM_END`; for processing it's `LIVEPEER_SEGMENT_COMPLETE` or `PROCESS_AV_VIRTUAL_SEGMENT_COMPLETE`. The finalized fact is what billing reads. Anything that is _not_ a real final event from the source — heartbeats, polls, in-flight estimates, stale-close synthetics — lives in either provisional tables (`*_anomalous`, `*_current`) or stays in the raw audit journal (`raw_mist_triggers`), but never reaches `*_final`.

This separation is what makes billing auditable. The cursor invariant in [meter-contracts.md](meter-contracts.md) only works because `*_final` has exactly one logical row per actual finalized event.

## The projection model in concrete terms

Every `*_final` and 5-min ledger table follows the same physical shape:

- **Engine**: `MergeTree` (append-only). Not `ReplacingMergeTree`. Not upserted.
- **Identity**: natural key columns explicit in the table — for example `(tenant_id, node_id, session_id)` for `viewer_sessions_final`. Stable cross-pipeline identity (`source_event_id` from the edge WAL) is preserved as a column for traceback.
- **Time**: four columns. `source_started_at_ms` and `source_ended_at_ms` mark the activity window in the source system's frame. `edge_received_at_ms` marks when Helmsman accepted the trigger at the edge. `projection_version_ms` marks when this projection row was written.
- **Ordering**: `ORDER BY (tenant_id, projection_version_ms, …natural-key columns…)` — tenant for pruning, projection time for cursor range scans, then the natural key for prior-projection lookups.
- **Partitioning**: `PARTITION BY toYYYYMM(toDateTime(projection_version_ms / 1000))` — monthly partitions on projection time.

Multiple rows for the same logical fact coexist on disk. The parser may write more than one — re-projection after a code change, a Kafka retry, a divergent payload. Readers materialize the logical fact on read via `min/argMax`, never via FINAL on the hot path.

### Two read shapes, again

`*_final_v` views (defined alongside the tables in `pkg/database/sql/clickhouse/periscope.sql`) are the **semantic surface**:

```sql
SELECT … FROM viewer_sessions_final_v WHERE tenant_id = ? AND session_id = ?
```

This returns the logical fact via `min(projection_version_ms) AS billable_at_ms` and `argMax(field, projection_version_ms)` per column. Use for audits, dashboard rollup rebuilders, and any query that needs "the truth about this session right now."

The **billing cursor** uses a purpose-built helper that hits the table directly, not the view, because `WHERE billable_at_ms BETWEEN …` is a post-aggregation predicate and ClickHouse cannot prune partitions cleanly through it. The cursor instead expresses its time range as `WHERE projection_version_ms ∈ [start, end)` on a CTE that groups by natural key, then `LEFT ANTI JOIN`s against prior projections of the same natural keys. Full query in [meter-contracts.md](meter-contracts.md).

### Why append-only

The earlier sketch of this work used `ReplacingMergeTree(projection_version_ms)` and a SELECT-before-insert in the parser to preserve `billable_at_ms`. That has two problems:

1. **Race.** Two parser instances handling Kafka partitions for the same `source_event_id` (admittedly rare given partition keying, but not impossible during a rebalance) would each see "no existing row" and each write a fresh `billable_at_ms`. The later parts merge wins arbitrarily.
2. **Eventual collapse.** `ReplacingMergeTree` collapses duplicates only during background merges. Until merge, both rows are visible to SELECT. So a SELECT-before-insert reads from a non-collapsed state and can pick the wrong "prior" value.

Append-only `MergeTree` plus `min/argMax` on read sidesteps both. The parser never reads; it only appends. The materialize-on-read pattern is ClickHouse's idiomatic answer to "what is the current truth across N projection versions."

The cost is read overhead: every read of a `*_final` row scans all projection rows for the natural key and aggregates. In steady state most natural keys have exactly one row (parser ran once, no reproject) so the aggregate is trivial. Worst case is a long-retention table with many reprojections per key, which we'd handle with periodic `OPTIMIZE TABLE … FINAL` or migration to `AggregatingMergeTree` with `argMaxState` columns.

## Anomaly tables

`viewer_sessions_anomalous` and `stream_sessions_anomalous` are physically separate tables. They are written by the stale-close worker in `api_analytics_ingest/internal/handlers/stale_close.go` (the original plan put it in `api_sidecar` but the live-state source `viewer_sessions_current` already lives in Periscope's ClickHouse — duplicating that state in Helmsman would be a step backward).

The worker scans `viewer_sessions_current` for sessions whose `last_updated` is older than `stale_close_timeout` (default 4h) and that have no row in `viewer_sessions_final`. Each hit becomes one anomaly row with `closed_reason='stale'`. Same shape for streams from `stream_state_current`.

**The rated billing read path never touches these tables.** That's the entire purpose of the physical separation: an operator who joins the wrong tables cannot accidentally bill anomalous minutes. The operational meter `stale_session_minutes` exists for visibility, but it lives on a separate dashboard query.

## Divergence guardrail

`projection_divergences` is an append-only audit table written when the parser detects that a new projection of an already-seen logical fact carries a different rated-field value beyond a per-meter epsilon. The new row is **still written** to the `*_final` table (append invariant — append always, never refuse), and the divergence is logged with:

- a Prometheus counter `periscope_projection_divergence_total{table, meter, field}`,
- an audit row in `projection_divergences` with the prior value, new value, and `source_event_id` for replay.

This makes "silent corrections" impossible: every divergence is observable on a dashboard, and operators have a queryable backlog for explicit credit/debit adjustments.

The implementation lives in `api_analytics_ingest/internal/handlers/final_fact_parser.go`: `checkViewerSessionDivergence`, `checkStreamSessionDivergence`, and `checkProcessingSegmentDivergence`. All three `*_final` projection paths run the guarded check. The lookup is one short `argMax`-grouped SELECT per insert. We accept that per-insert cost because material divergence is expected to be rare; if it becomes a bottleneck the rebuilder can batch the lookups. Per-meter epsilons:

- viewer/stream `duration_seconds` / `viewer_seconds`: 1 second
- `uploaded_bytes` / `downloaded_bytes`: 1 KiB
- processing `media_seconds`: 50 ms
- `cluster_id`: any change is a divergence (attribution drift)

## Layout in `periscope.sql`

Finalized-fact and canonical-ledger tables live in one section, following this layout — table, view (where applicable), in natural reading order:

- `viewer_sessions_final` + `viewer_sessions_final_v`
- `stream_sessions_final` + `stream_sessions_final_v`
- `processing_segments_final` + `processing_segments_final_v`
- `viewer_sessions_anomalous`
- `stream_sessions_anomalous`
- `ledger_rebuild_cursors`
- `viewer_usage_5m` + `viewer_usage_5m_v`
- `stream_runtime_5m` + `stream_runtime_5m_v`
- `storage_gb_seconds_5m` + `storage_gb_seconds_5m_v`
- `processing_5m` + `processing_5m_v`
- `api_usage_5m` + `api_usage_5m_v`
- `projection_divergences`

When the file outgrows a single domain we split, not before.

## Related

- [meter-contracts.md](meter-contracts.md) — meter-by-meter contract, cursor invariant, corrections section
- [trigger-durability.md](trigger-durability.md) — how triggers reach `raw_mist_triggers` in the first place
- [analytics-pipeline.md](analytics-pipeline.md) — end-to-end shape of the analytics path
- `pkg/database/sql/clickhouse/periscope.sql` — DDL
- `api_analytics_ingest/internal/handlers/final_fact_parser.go` — projection writers
- `api_analytics_ingest/internal/handlers/ledger_rebuilders.go` — 5-min ledger workers
- `api_analytics_ingest/internal/handlers/stale_close.go` — anomaly writer
