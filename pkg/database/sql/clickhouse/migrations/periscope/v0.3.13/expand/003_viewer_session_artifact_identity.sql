-- A viewer of non-live content (a clip, DVR or DVR chapter replay, or an
-- uploaded VOD) ends with the content's artifact_hash. stream_id on such a row
-- names only the source stream, and an uploaded VOD has none. Daily stream
-- analytics count a live stream's own viewers and egress, so artifact views stay
-- out of the source stream's row and form no row under the zero stream_id.
ALTER TABLE periscope.viewer_sessions_final
    ADD COLUMN IF NOT EXISTS artifact_hash String DEFAULT '' AFTER stream_id;

CREATE OR REPLACE VIEW periscope.viewer_sessions_final_v AS
SELECT
    tenant_id, node_id, session_id,
    min(projection_version_ms) AS billable_at_ms,
    argMax(source_event_id,       projection_version_ms) AS source_event_id,
    argMax(cluster_id,            projection_version_ms) AS cluster_id,
    argMax(stream_id,             projection_version_ms) AS stream_id,
    argMax(artifact_hash,         projection_version_ms) AS artifact_hash,
    argMax(stream_name,           projection_version_ms) AS stream_name,
    argMax(connector,             projection_version_ms) AS connector,
    argMax(host,                  projection_version_ms) AS host,
    argMax(country_code,          projection_version_ms) AS country_code,
    argMax(city,                  projection_version_ms) AS city,
    argMax(latitude,              projection_version_ms) AS latitude,
    argMax(longitude,             projection_version_ms) AS longitude,
    argMax(tags,                  projection_version_ms) AS tags,
    argMax(duration_seconds,      projection_version_ms) AS duration_seconds,
    argMax(uploaded_bytes,        projection_version_ms) AS uploaded_bytes,
    argMax(downloaded_bytes,      projection_version_ms) AS downloaded_bytes,
    argMax(seconds_connected,     projection_version_ms) AS seconds_connected,
    argMax(source_started_at_ms,  projection_version_ms) AS source_started_at_ms,
    argMax(source_ended_at_ms,    projection_version_ms) AS source_ended_at_ms,
    argMax(edge_received_at_ms,   projection_version_ms) AS edge_received_at_ms,
    max(projection_version_ms) AS latest_projection_version_ms,
    argMax(closed_reason,         projection_version_ms) AS closed_reason,
    argMax(stream_times,          projection_version_ms) AS stream_times,
    argMax(connector_times,       projection_version_ms) AS connector_times,
    argMax(host_times,            projection_version_ms) AS host_times
FROM periscope.viewer_sessions_final
GROUP BY tenant_id, node_id, session_id;

-- The topology view selects f.*, which ClickHouse expands when the view is
-- created, so it is recreated to carry artifact_hash.
CREATE OR REPLACE VIEW periscope.viewer_sessions_topology_v AS
SELECT f.*, p.origin_cluster_id, p.control_cell_id
FROM periscope.viewer_sessions_final_v AS f
LEFT JOIN
(
    SELECT tenant_id, node_id, session_id,
           argMax(origin_cluster_id, projection_version_ms) AS origin_cluster_id,
           argMax(control_cell_id, projection_version_ms) AS control_cell_id
    FROM periscope.viewer_sessions_final
    GROUP BY tenant_id, node_id, session_id
) AS p USING (tenant_id, node_id, session_id);

ALTER TABLE periscope.stream_analytics_daily_mv MODIFY QUERY
WITH affected AS (
    SELECT day, tenant_id, stream_id FROM (
    SELECT DISTINCT toDate(window_start) AS day, tenant_id, stream_id
    FROM periscope.delivery_usage_5m
    WHERE projection_version_ms >= toUnixTimestamp64Milli(now64(3) - INTERVAL 2 DAY)
      AND window_start >= toStartOfDay(now() - INTERVAL 89 DAY)
    UNION DISTINCT
    SELECT DISTINCT toDate(window_start) AS day, tenant_id, stream_id
    FROM periscope.viewer_usage_5m
    WHERE projection_version_ms >= toUnixTimestamp64Milli(now64(3) - INTERVAL 2 DAY)
      AND window_start >= toStartOfDay(now() - INTERVAL 89 DAY)
    UNION DISTINCT
    SELECT DISTINCT toDate(toDateTime(intDiv(old.source_ended_at_ms, 1000))) AS day, old.tenant_id, old.stream_id
    FROM periscope.viewer_sessions_final AS old
    INNER JOIN (
        SELECT DISTINCT tenant_id, node_id, session_id
        FROM periscope.viewer_sessions_final
        WHERE projection_version_ms >= toUnixTimestamp64Milli(now64(3) - INTERVAL 2 DAY)
    ) AS changed USING (tenant_id, node_id, session_id)
    WHERE old.tenant_id IN (
        SELECT tenant_id FROM periscope.viewer_sessions_final
        WHERE projection_version_ms >= toUnixTimestamp64Milli(now64(3) - INTERVAL 2 DAY)
    )
      AND old.source_ended_at_ms >= toInt64(toUnixTimestamp(toStartOfDay(now() - INTERVAL 729 DAY))) * 1000
      AND old.artifact_hash = ''
    UNION DISTINCT
    SELECT DISTINCT toDate(toDateTime(intDiv(old.source_ended_at_ms, 1000))) AS day, old.tenant_id, old.stream_id
    FROM periscope.restream_sessions_final AS old
    INNER JOIN (
        SELECT DISTINCT tenant_id, node_id, source_event_id
        FROM periscope.restream_sessions_final
        WHERE projection_version_ms >= toUnixTimestamp64Milli(now64(3) - INTERVAL 2 DAY)
    ) AS changed USING (tenant_id, node_id, source_event_id)
    WHERE old.tenant_id IN (
        SELECT tenant_id FROM periscope.restream_sessions_final
        WHERE projection_version_ms >= toUnixTimestamp64Milli(now64(3) - INTERVAL 2 DAY)
    )
      AND old.source_ended_at_ms >= toInt64(toUnixTimestamp(toStartOfDay(now() - INTERVAL 729 DAY))) * 1000
    UNION DISTINCT
    SELECT toDate(bucket) AS day, tenant_id, stream_id
    FROM periscope.rollup_backfill_markers
    WHERE scope = 'stream_analytics_daily'
      AND seed_version_ms >= now64(3) - INTERVAL 1 DAY
      AND bucket >= toStartOfDay(now() - INTERVAL 729 DAY)
    )
    WHERE stream_id != toUUIDOrZero('')
)
SELECT
    affected.day AS day,
    affected.tenant_id AS tenant_id,
    affected.stream_id AS stream_id,
    audience.internal_name AS internal_name,
    audience.total_views AS total_views,
    audience.unique_viewers_state AS unique_viewers_state,
    audience.unique_countries AS unique_countries,
    audience.unique_cities AS unique_cities,
    delivery.egress_bytes AS egress_bytes,
    now64(3) AS refresh_version_ms
FROM affected
LEFT JOIN (
    SELECT day, tenant_id, stream_id, toUInt64(sum(bytes)) AS egress_bytes
    FROM (
        SELECT toDate(toDateTime(intDiv(source_ended_at_ms, 1000))) AS day, tenant_id, stream_id,
            downloaded_bytes AS bytes
        FROM periscope.viewer_sessions_final_v
        WHERE (toDate(toDateTime(intDiv(source_ended_at_ms, 1000))), tenant_id, stream_id) IN (SELECT day, tenant_id, stream_id FROM affected)
          AND closed_reason = 'final'
          AND artifact_hash = ''
        UNION ALL
        SELECT toDate(toDateTime(intDiv(source_ended_at_ms, 1000))) AS day, tenant_id, stream_id,
            bytes_sent AS bytes
        FROM periscope.restream_sessions_final_v
        WHERE (toDate(toDateTime(intDiv(source_ended_at_ms, 1000))), tenant_id, stream_id) IN (SELECT day, tenant_id, stream_id FROM affected)
          AND state IN ('idle', 'failed')
          AND source_started_at_ms > 0 AND source_ended_at_ms > source_started_at_ms
    )
    GROUP BY day, tenant_id, stream_id
) AS delivery USING (day, tenant_id, stream_id)
LEFT JOIN (
    SELECT day, tenant_id, stream_id,
        anyIf(stream_name, included = 1) AS internal_name,
        toUInt64(uniqCombinedIf(tuple(node_id, session_id), included = 1)) AS total_views,
        uniqCombinedStateIf(viewer_key, included = 1) AS unique_viewers_state,
        toUInt32(uniqCombinedIf(country_code, included = 1 AND country_code != '')) AS unique_countries,
        toUInt32(uniqCombinedIf(city, included = 1 AND city != '')) AS unique_cities
    FROM (
        SELECT day, tenant_id, stream_id, '' AS stream_name, '' AS viewer_key,
            '' AS node_id, '' AS session_id, '' AS country_code, '' AS city, toUInt8(0) AS included
        FROM affected
        UNION ALL
        SELECT toDate(toDateTime(intDiv(source_ended_at_ms, 1000))) AS day, tenant_id, stream_id,
            stream_name, if(host != '', host, concat(toString(node_id), '|', session_id)) AS viewer_key,
            node_id, session_id, country_code, city, toUInt8(1) AS included
        FROM periscope.viewer_sessions_final_v
        WHERE (toDate(toDateTime(intDiv(source_ended_at_ms, 1000))), tenant_id, stream_id) IN (SELECT day, tenant_id, stream_id FROM affected)
          AND closed_reason = 'final'
          AND artifact_hash = ''
    )
    GROUP BY day, tenant_id, stream_id
) AS audience USING (day, tenant_id, stream_id)
SETTINGS max_bytes_before_external_group_by = 268435456,
         max_bytes_before_external_sort = 268435456;
