-- stream_event_log, stream_health_samples and track_list_events hold a live
-- stream's own series. A replay of a clip, DVR or DVR chapter, and an uploaded
-- VOD, runs as its own Mist stream; its lifecycle, buffer and track reports are
-- kept under its artifact_hash with the zero stream_id, and the live-stream
-- rollups read only the stream's own rows.
ALTER TABLE periscope.stream_event_log
    ADD COLUMN IF NOT EXISTS artifact_hash String DEFAULT '' AFTER stream_id;

ALTER TABLE periscope.stream_health_samples
    ADD COLUMN IF NOT EXISTS artifact_hash String DEFAULT '' AFTER stream_id;

ALTER TABLE periscope.track_list_events
    ADD COLUMN IF NOT EXISTS artifact_hash String DEFAULT '' AFTER stream_id;

ALTER TABLE periscope.stream_viewer_5m_mv MODIFY QUERY
SELECT
    toStartOfInterval(timestamp, INTERVAL 5 MINUTE) AS timestamp_5m,
    tenant_id,
    stream_id,
    max(total_viewers) AS max_viewers,
    avg(total_viewers) AS avg_viewers
FROM periscope.stream_event_log
WHERE total_viewers IS NOT NULL AND artifact_hash = ''
GROUP BY timestamp_5m, tenant_id, stream_id;

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
WHERE artifact_hash = ''
GROUP BY timestamp_5m, tenant_id, stream_id, internal_name, node_id;

ALTER TABLE periscope.rebuffering_events_mv MODIFY QUERY
SELECT
    timestamp,
    tenant_id,
    stream_id,
    internal_name,
    node_id,
    buffer_state,
    lagInFrame(buffer_state) OVER (PARTITION BY tenant_id, stream_id ORDER BY timestamp) AS prev_state,
    if(buffer_state = 'DRY' AND prev_state IN ('FULL', 'RECOVER'), 1, 0) AS rebuffer_start,
    if(buffer_state = 'RECOVER' AND prev_state = 'DRY', 1, 0) AS rebuffer_end
FROM periscope.stream_health_samples
WHERE buffer_state IN ('FULL', 'DRY', 'RECOVER') AND artifact_hash = '';

ALTER TABLE periscope.quality_tier_daily_mv MODIFY QUERY
SELECT
    toDate(timestamp) as day,
    tenant_id,
    stream_id,
    internal_name,
    countIf(primary_height >= 2160) * 5 AS tier_2160p_minutes,
    countIf(primary_height >= 1440 AND primary_height < 2160) * 5 AS tier_1440p_minutes,
    countIf(primary_height >= 1080 AND primary_height < 1440) * 5 AS tier_1080p_minutes,
    countIf(primary_height >= 720 AND primary_height < 1080) * 5 AS tier_720p_minutes,
    countIf(primary_height >= 480 AND primary_height < 720) * 5 AS tier_480p_minutes,
    countIf(primary_height < 480) * 5 AS tier_sd_minutes,
    ifNull(argMax(quality_tier, timestamp), 'Unknown') AS primary_tier,
    countIf(primary_video_codec LIKE '%264%') * 5 AS codec_h264_minutes,
    countIf(primary_video_codec LIKE '%265%' OR primary_video_codec LIKE '%HEVC%') * 5 AS codec_h265_minutes,
    countIf(lower(primary_video_codec) LIKE '%vp9%') * 5 AS codec_vp9_minutes,
    countIf(lower(primary_video_codec) LIKE '%av1%') * 5 AS codec_av1_minutes,
    ifNull(toUInt32(avg(primary_video_bitrate)), 0) AS avg_bitrate,
    ifNull(avgIf(primary_fps, primary_fps > 0), 0) AS avg_fps
FROM periscope.track_list_events
WHERE track_count > 0 AND artifact_hash = ''
GROUP BY day, tenant_id, stream_id, internal_name;
