DROP VIEW IF EXISTS processing_5m_v;

CREATE VIEW IF NOT EXISTS processing_5m_v AS
SELECT
    window_start, tenant_id,
    argMax(cluster_id, projection_version_ms) AS cluster_id,
    argMax(stream_id, projection_version_ms) AS stream_id,
    argMax(process_type, projection_version_ms) AS process_type,
    argMax(output_codec, projection_version_ms) AS output_codec,
    argMax(track_type, projection_version_ms) AS track_type,
    source_event_id,
    min(projection_version_ms) AS billable_at_ms,
    argMax(media_seconds, projection_version_ms) AS media_seconds,
    max(projection_version_ms) AS latest_projection_version_ms
FROM processing_5m
GROUP BY window_start, tenant_id, source_event_id;
