-- Mist reports 0 FPS when the source frame rate is unknown or dynamic.
-- Rollups must ignore that sentinel so unknown FPS does not look like an
-- unhealthy zero-frame stream.
--
-- Schema source of truth: pkg/database/sql/clickhouse/periscope.sql

DROP VIEW IF EXISTS stream_health_5m_mv;

CREATE MATERIALIZED VIEW IF NOT EXISTS stream_health_5m_mv TO stream_health_5m AS
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
    ifNull(argMax(
        if(height >= 2160, '2160p',
          if(height >= 1440, '1440p',
            if(height >= 1080, '1080p',
              if(height >= 720, '720p',
                if(height >= 480, '480p', 'SD'))))), timestamp
    ), 'Unknown') AS quality_tier
FROM stream_health_samples
GROUP BY timestamp_5m, tenant_id, stream_id, internal_name, node_id;

DROP VIEW IF EXISTS quality_tier_daily_mv;

CREATE MATERIALIZED VIEW IF NOT EXISTS quality_tier_daily_mv TO quality_tier_daily AS
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
FROM track_list_events
WHERE track_count > 0
GROUP BY day, tenant_id, stream_id, internal_name;
