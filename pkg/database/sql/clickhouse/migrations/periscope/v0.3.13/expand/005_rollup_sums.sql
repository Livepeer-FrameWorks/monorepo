-- The 5-minute rollups are written by insert-triggered views, one row per
-- insert block and bucket, so their averages and distinct counts described a
-- block and depended on how samples were batched into inserts. Each rollup
-- also stores the sums, sample counts and mergeable states its readers
-- combine over a bucket's rows. Rows written before these columns read as one
-- sample of their block values.
ALTER TABLE periscope.stream_viewer_5m
    ADD COLUMN IF NOT EXISTS viewers_sum Float64 DEFAULT avg_viewers,
    ADD COLUMN IF NOT EXISTS viewers_samples UInt64 DEFAULT 1;

ALTER TABLE periscope.stream_viewer_5m_mv MODIFY QUERY
SELECT
    toStartOfInterval(timestamp, INTERVAL 5 MINUTE) AS timestamp_5m,
    tenant_id,
    stream_id,
    max(total_viewers) AS max_viewers,
    avg(total_viewers) AS avg_viewers,
    ifNull(sum(total_viewers), 0) AS viewers_sum,
    count(total_viewers) AS viewers_samples
FROM periscope.stream_event_log
WHERE total_viewers IS NOT NULL AND artifact_hash = ''
GROUP BY timestamp_5m, tenant_id, stream_id;

ALTER TABLE periscope.stream_health_5m
    ADD COLUMN IF NOT EXISTS bitrate_sum Float64 DEFAULT avg_bitrate,
    ADD COLUMN IF NOT EXISTS bitrate_samples UInt64 DEFAULT 1,
    ADD COLUMN IF NOT EXISTS fps_sum Float64 DEFAULT avg_fps,
    ADD COLUMN IF NOT EXISTS fps_samples UInt64 DEFAULT 1,
    ADD COLUMN IF NOT EXISTS buffer_health_sum Float64 DEFAULT avg_buffer_health,
    ADD COLUMN IF NOT EXISTS buffer_health_samples UInt64 DEFAULT 1,
    ADD COLUMN IF NOT EXISTS frame_jitter_sum Float64 DEFAULT ifNull(avg_frame_jitter_ms, 0),
    ADD COLUMN IF NOT EXISTS frame_jitter_samples UInt64 DEFAULT if(isNull(avg_frame_jitter_ms), 0, 1),
    ADD COLUMN IF NOT EXISTS quality_tier_at DateTime DEFAULT timestamp_5m;

ALTER TABLE periscope.stream_health_5m_mv MODIFY QUERY
SELECT
    toStartOfInterval(timestamp, INTERVAL 5 MINUTE) AS timestamp_5m,
    tenant_id,
    stream_id,
    internal_name,
    node_id,
    countIf(buffer_state = 'DRY') AS rebuffer_count,
    countIf(has_issues = 1) AS issue_count,
    any(issues_description) AS sample_issues,
    ifNull(avg(bitrate), 0) AS avg_bitrate,
    ifNull(avgIf(fps, fps > 0), 0) AS avg_fps,
    ifNull(avg(buffer_health), 0) AS avg_buffer_health,
    avg(frame_jitter_ms) AS avg_frame_jitter_ms,
    max(frame_jitter_ms) AS max_frame_jitter_ms,
    countIf(buffer_state = 'DRY') AS buffer_dry_count,
    if(countIf(height > 0) = 0, 'Unknown', argMaxIf(
        multiIf(assumeNotNull(height) >= 2160, '2160p',
                assumeNotNull(height) >= 1440, '1440p',
                assumeNotNull(height) >= 1080, '1080p',
                assumeNotNull(height) >= 720, '720p',
                assumeNotNull(height) >= 480, '480p', 'SD'),
        timestamp, height > 0
    )) AS quality_tier,
    ifNull(sum(bitrate), 0) AS bitrate_sum,
    count(bitrate) AS bitrate_samples,
    ifNull(sumIf(fps, fps > 0), 0) AS fps_sum,
    countIf(fps > 0) AS fps_samples,
    ifNull(sum(buffer_health), 0) AS buffer_health_sum,
    count(buffer_health) AS buffer_health_samples,
    ifNull(sum(frame_jitter_ms), 0) AS frame_jitter_sum,
    count(frame_jitter_ms) AS frame_jitter_samples,
    if(countIf(height > 0) = 0, toDateTime(0), maxIf(timestamp, height > 0)) AS quality_tier_at
FROM periscope.stream_health_samples
WHERE artifact_hash = ''
GROUP BY timestamp_5m, tenant_id, stream_id, internal_name, node_id;

ALTER TABLE periscope.client_qoe_5m
    ADD COLUMN IF NOT EXISTS bw_in_sum Float64 DEFAULT avg_bw_in,
    ADD COLUMN IF NOT EXISTS bw_out_sum Float64 DEFAULT avg_bw_out,
    ADD COLUMN IF NOT EXISTS connection_time_sum Float64 DEFAULT avg_connection_time,
    ADD COLUMN IF NOT EXISTS qoe_samples UInt64 DEFAULT 1,
    ADD COLUMN IF NOT EXISTS packets_sent_sum UInt64 DEFAULT 0,
    ADD COLUMN IF NOT EXISTS packets_lost_sum UInt64 DEFAULT 0,
    ADD COLUMN IF NOT EXISTS sessions_state AggregateFunction(uniq, String),
    ADD COLUMN IF NOT EXISTS rollup_version UInt8 DEFAULT 0;

ALTER TABLE periscope.client_qoe_5m_mv MODIFY QUERY
SELECT
    toStartOfInterval(timestamp, INTERVAL 5 MINUTE) AS timestamp_5m,
    tenant_id,
    stream_id,
    artifact_hash,
    internal_name,
    node_id,
    count(DISTINCT session_id) as active_sessions,
    avg(bandwidth_in) AS avg_bw_in,
    avg(bandwidth_out) AS avg_bw_out,
    avg(connection_time) AS avg_connection_time,
    if(sum(packets_sent) > 0, sum(packets_lost) / sum(packets_sent), NULL) AS pkt_loss_rate,
    sum(bandwidth_in) AS bw_in_sum,
    sum(bandwidth_out) AS bw_out_sum,
    sum(connection_time) AS connection_time_sum,
    count() AS qoe_samples,
    sum(packets_sent) AS packets_sent_sum,
    sum(packets_lost) AS packets_lost_sum,
    uniqState(session_id) AS sessions_state,
    toUInt8(1) AS rollup_version
FROM periscope.client_qoe_samples
GROUP BY timestamp_5m, tenant_id, stream_id, artifact_hash, internal_name, node_id;
