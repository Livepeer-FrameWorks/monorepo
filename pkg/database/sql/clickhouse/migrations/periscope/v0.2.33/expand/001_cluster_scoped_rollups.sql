-- v0.2.33 expand: align existing Periscope analytics tables with the
-- cluster-scoped baseline.
--
-- Older installs created these rollup/source tables before cluster_id was part
-- of storage and processing analytics. Fresh installs already get these
-- columns from periscope.sql; this migration makes upgraded installs match the
-- expected write and materialized-view shape.

ALTER TABLE storage_snapshots
    ADD COLUMN IF NOT EXISTS cluster_id LowCardinality(String) DEFAULT '' AFTER node_id;

ALTER TABLE storage_usage_hourly
    ADD COLUMN IF NOT EXISTS cluster_id LowCardinality(String) DEFAULT '' AFTER tenant_id;

ALTER TABLE storage_usage_hourly_mv MODIFY QUERY
SELECT
    toStartOfHour(timestamp) AS hour,
    tenant_id,
    cluster_id,
    avgState(total_bytes) AS avg_total_bytes,
    avgState(clip_bytes) AS avg_clip_bytes,
    avgState(dvr_bytes) AS avg_dvr_bytes,
    avgState(vod_bytes) AS avg_vod_bytes
FROM storage_snapshots
GROUP BY hour, tenant_id, cluster_id;

ALTER TABLE processing_events
    ADD COLUMN IF NOT EXISTS cluster_id LowCardinality(String) DEFAULT '' AFTER node_id,
    ADD COLUMN IF NOT EXISTS origin_cluster_id LowCardinality(String) DEFAULT '' AFTER cluster_id;

ALTER TABLE processing_hourly
    ADD COLUMN IF NOT EXISTS cluster_id LowCardinality(String) DEFAULT '' AFTER tenant_id;

ALTER TABLE processing_hourly_mv MODIFY QUERY
SELECT
    toStartOfHour(timestamp) AS hour,
    tenant_id,
    cluster_id,
    process_type,
    lower(coalesce(output_codec, 'unknown')) AS output_codec,
    coalesce(track_type, 'video') AS track_type,
    sumState(duration_ms) AS total_duration_ms,
    countState() AS segment_count,
    uniqState(stream_id) AS unique_streams
FROM processing_events
GROUP BY hour, tenant_id, cluster_id, process_type, output_codec, track_type;

ALTER TABLE processing_daily
    ADD COLUMN IF NOT EXISTS cluster_id LowCardinality(String) DEFAULT '' AFTER tenant_id;

ALTER TABLE processing_daily_mv MODIFY QUERY
SELECT
    toDate(hour) AS day,
    tenant_id,
    cluster_id,
    sumMergeIf(total_duration_ms, process_type = 'Livepeer' AND output_codec = 'h264') / 1000.0 AS livepeer_h264_seconds,
    sumMergeIf(total_duration_ms, process_type = 'Livepeer' AND output_codec = 'vp9') / 1000.0 AS livepeer_vp9_seconds,
    sumMergeIf(total_duration_ms, process_type = 'Livepeer' AND output_codec = 'av1') / 1000.0 AS livepeer_av1_seconds,
    sumMergeIf(total_duration_ms, process_type = 'Livepeer' AND output_codec IN ('hevc', 'h265')) / 1000.0 AS livepeer_hevc_seconds,
    toUInt64(countMergeIf(segment_count, process_type = 'Livepeer')) AS livepeer_segment_count,
    toUInt32(uniqMergeIf(unique_streams, process_type = 'Livepeer')) AS livepeer_unique_streams,
    sumMergeIf(total_duration_ms, process_type = 'AV' AND output_codec = 'h264') / 1000.0 AS native_av_h264_seconds,
    sumMergeIf(total_duration_ms, process_type = 'AV' AND output_codec = 'vp9') / 1000.0 AS native_av_vp9_seconds,
    sumMergeIf(total_duration_ms, process_type = 'AV' AND output_codec = 'av1') / 1000.0 AS native_av_av1_seconds,
    sumMergeIf(total_duration_ms, process_type = 'AV' AND output_codec IN ('hevc', 'h265')) / 1000.0 AS native_av_hevc_seconds,
    sumMergeIf(total_duration_ms, process_type = 'AV' AND output_codec = 'aac') / 1000.0 AS native_av_aac_seconds,
    sumMergeIf(total_duration_ms, process_type = 'AV' AND output_codec = 'opus') / 1000.0 AS native_av_opus_seconds,
    toUInt64(countMergeIf(segment_count, process_type = 'AV')) AS native_av_segment_count,
    toUInt32(uniqMergeIf(unique_streams, process_type = 'AV')) AS native_av_unique_streams,
    sumMergeIf(total_duration_ms, track_type = 'audio') / 1000.0 AS audio_seconds,
    sumMergeIf(total_duration_ms, track_type = 'video') / 1000.0 AS video_seconds,
    sumMergeIf(total_duration_ms, process_type = 'Livepeer') / 1000.0 AS livepeer_seconds,
    sumMergeIf(total_duration_ms, process_type = 'AV') / 1000.0 AS native_av_seconds
FROM processing_hourly
GROUP BY day, tenant_id, cluster_id;
