-- Stale-close workers now suppress keys already represented in these tables.
-- The views provide a canonical read during rollout and collapse the bounded
-- race where two ingest replicas observe a new stale key simultaneously.
CREATE VIEW IF NOT EXISTS viewer_sessions_anomalous_v AS
SELECT
    tenant_id,
    node_id,
    session_id,
    argMax(cluster_id, source_projection_version_ms) AS cluster_id,
    argMax(stream_id, source_projection_version_ms) AS stream_id,
    argMax(stream_name, source_projection_version_ms) AS stream_name,
    argMax(estimated_duration_seconds, source_projection_version_ms) AS estimated_duration_seconds,
    argMax(observed_first_at_ms, source_projection_version_ms) AS observed_first_at_ms,
    argMax(observed_last_at_ms, source_projection_version_ms) AS observed_last_at_ms,
    argMax(closed_at_ms, source_projection_version_ms) AS closed_at_ms,
    argMax(closed_reason, source_projection_version_ms) AS closed_reason,
    max(source_projection_version_ms) AS projection_version_ms,
    argMax(notes, source_projection_version_ms) AS notes
FROM (
    SELECT *, projection_version_ms AS source_projection_version_ms
    FROM viewer_sessions_anomalous
)
GROUP BY tenant_id, node_id, session_id;

CREATE VIEW IF NOT EXISTS stream_sessions_anomalous_v AS
SELECT
    tenant_id,
    node_id,
    stream_id,
    argMax(cluster_id, source_projection_version_ms) AS cluster_id,
    argMax(stream_name, source_projection_version_ms) AS stream_name,
    argMax(estimated_duration_seconds, source_projection_version_ms) AS estimated_duration_seconds,
    argMax(observed_first_at_ms, source_projection_version_ms) AS observed_first_at_ms,
    argMax(observed_last_at_ms, source_projection_version_ms) AS observed_last_at_ms,
    argMax(closed_at_ms, source_projection_version_ms) AS closed_at_ms,
    argMax(closed_reason, source_projection_version_ms) AS closed_reason,
    max(source_projection_version_ms) AS projection_version_ms,
    argMax(notes, source_projection_version_ms) AS notes
FROM (
    SELECT *, projection_version_ms AS source_projection_version_ms
    FROM stream_sessions_anomalous
)
GROUP BY tenant_id, node_id, stream_id;

ALTER TABLE viewer_sessions_anomalous DELETE WHERE
    (tenant_id, node_id, session_id, projection_version_ms) NOT IN (
        SELECT tenant_id, node_id, session_id, max(projection_version_ms)
        FROM viewer_sessions_anomalous
        GROUP BY tenant_id, node_id, session_id
    )
SETTINGS mutations_sync = 2, allow_nondeterministic_mutations = 1;

ALTER TABLE stream_sessions_anomalous DELETE WHERE
    (tenant_id, node_id, stream_id, projection_version_ms) NOT IN (
        SELECT tenant_id, node_id, stream_id, max(projection_version_ms)
        FROM stream_sessions_anomalous
        GROUP BY tenant_id, node_id, stream_id
    )
SETTINGS mutations_sync = 2, allow_nondeterministic_mutations = 1;
