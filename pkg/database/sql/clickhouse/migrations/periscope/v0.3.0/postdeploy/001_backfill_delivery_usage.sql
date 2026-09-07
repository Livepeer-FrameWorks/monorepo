-- Delivery history is populated by Periscope ingest's checkpointed worker.
-- Refuse to enter contract while that worker is still catching up: otherwise
-- the synchronous rollup refresh publishes a partial retained-history view.
-- Fifteen minutes covers the worker's 5-minute cadence, 2-minute settlement
-- lag, and one 5-minute bucket truncation without flapping at a healthy phase.
SELECT throwIf(
    (
        (SELECT count() FROM (
            SELECT 1
            FROM periscope.viewer_sessions_final
            PREWHERE projection_version_ms >= toUnixTimestamp64Milli(now64(3) - INTERVAL 90 DAY)
            LIMIT 1
        ))
        +
        (SELECT count() FROM (
            SELECT 1
            FROM periscope.restream_sessions_final
            PREWHERE projection_version_ms >= toUnixTimestamp64Milli(now64(3) - INTERVAL 90 DAY)
            LIMIT 1
        ))
    ) > 0
    AND (SELECT max(last_processed_projection_ms)
         FROM periscope.ledger_rebuild_cursors_v2
         WHERE ledger_name = 'delivery_usage_5m')
            < toUnixTimestamp64Milli(now64(3) - INTERVAL 15 MINUTE),
    'delivery_usage_5m backfill is not caught up; keep Periscope ingest running and retry postdeploy'
);
