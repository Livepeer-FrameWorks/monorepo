-- stream_health_5m.quality_tier comes from the latest sample with a known
-- height. Samples without video dimensions carry NULL height and rank as
-- 'Unknown', never as SD.
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
    )) AS quality_tier
FROM periscope.stream_health_samples
GROUP BY timestamp_5m, tenant_id, stream_id, internal_name, node_id;
