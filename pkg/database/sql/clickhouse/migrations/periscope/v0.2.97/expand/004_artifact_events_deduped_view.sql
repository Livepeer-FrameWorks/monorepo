-- Canonical read-time dedup surface for artifact_events. artifact_events is a plain
-- ReplicatedMergeTree, so an at-least-once outbox redelivery appends a byte-identical row.
-- Every reader (api_analytics_query counts/lists/summaries and the Metabase artifact cards)
-- reads this view so they share ONE identity and counts agree with the rows they list.
--
-- Invariant: only rows carrying a non-empty event_id (the stable outbox row id) are deduped.
-- The second UNION ALL branch collapses their redeliveries with LIMIT 1 BY event_id (one row
-- per event_id). Legacy rows (event_id='') have NO trustworthy dedup identity, so the first
-- branch passes them through verbatim — one view row per base row. Two legacy rows that share
-- tenant/stream/second/request_id/stage but differ in any other column (percent, message,
-- file_path, s3_url, size_bytes, …) are therefore BOTH kept; history is never collapsed away.
-- Per ClickHouse UNION ALL semantics LIMIT 1 BY binds to its own SELECT, not the whole union,
-- so it never touches the legacy branch. The first branch is SELECT * FROM artifact_events so
-- downstream column inheritance still resolves to the base table.
--
-- A plain (non-materialized) VIEW is metadata-only, so this is rolling-safe. IF NOT EXISTS
-- keeps it idempotent against a freshly-baselined cluster.
--
-- Schema source of truth: pkg/database/sql/clickhouse/periscope.sql — the same view is in the
-- baseline so a fresh init and an upgrade converge on the same schema.
CREATE VIEW IF NOT EXISTS artifact_events_deduped AS
SELECT * FROM artifact_events WHERE event_id = ''
UNION ALL
SELECT * FROM artifact_events WHERE event_id != '' LIMIT 1 BY event_id;
