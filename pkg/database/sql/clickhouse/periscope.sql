-- ============================================================================
-- PERISCOPE V2 CLICKHOUSE SCHEMA (GraphQL-first)
-- Clean, normalized analytics schema designed to align with public GraphQL API.
-- ============================================================================

CREATE DATABASE IF NOT EXISTS periscope;
USE periscope;

-- ============================================================================
-- RAW MIST TRIGGER JOURNAL
-- ----------------------------------------------------------------------------
-- Durable record of every Mist trigger received at the edge (api_sidecar
-- Helmsman). Helmsman persists a local WAL entry before responding 200 OK
-- to Mist, then drains the WAL via the existing HelmsmanControl bidi stream
-- and waits for a MistTriggerAck before truncating. This table is the
-- analytics-side projection of that journal — re-deliveries collide on
-- source_request_id (sha256(node_id || NUL || trigger_type || NUL || payload_raw)), so
-- ReplacingMergeTree(ingested_at_ms) + argMax-on-read collapses duplicates.
--
-- Currently scoped to the seven final-event triggers (USER_END,
-- STREAM_END, PUSH_END, RECORDING_END, RECORDING_SEGMENT,
-- LIVEPEER_SEGMENT_COMPLETE, PROCESS_AV_VIRTUAL_SEGMENT_COMPLETE). The
-- table schema accepts any trigger_type so the WAL can be extended later
-- without DDL changes.
-- ============================================================================

CREATE TABLE IF NOT EXISTS raw_mist_triggers (
    node_id LowCardinality(String),
    trigger_type LowCardinality(String),
    source_request_id String,        -- sha256 hex of (node_id || NUL || trigger_type || NUL || payload_raw)
    payload String CODEC(ZSTD(3)),   -- raw protobuf MistTrigger envelope from Decklog
    tenant_id String DEFAULT '',     -- enriched by Foghorn before Decklog publish
    cluster_id LowCardinality(String) DEFAULT '',
    received_at_ms Int64,            -- Helmsman-side wall clock at WAL append
    forwarded_at_ms Int64,           -- Helmsman-side wall clock at first successful send
    ingested_at_ms Int64,            -- Periscope-side wall clock at INSERT (version)
    schema_version Int32 DEFAULT 0
) ENGINE = ReplacingMergeTree(ingested_at_ms)
PARTITION BY toYYYYMM(toDateTime(received_at_ms / 1000))
ORDER BY (node_id, trigger_type, source_request_id)
TTL toDateTime(received_at_ms / 1000) + INTERVAL 30 DAY;

-- ============================================================================
-- CORE STREAM EVENT LOG (normalized lifecycle + notable events)
-- ============================================================================

CREATE TABLE IF NOT EXISTS stream_event_log (
    event_id UUID,
    timestamp DateTime,
    tenant_id UUID,
    stream_id UUID,
    internal_name String,
    node_id LowCardinality(String),
    cluster_id LowCardinality(String) DEFAULT '',

    event_type LowCardinality(String),      -- canonical event type (stream_lifecycle, stream_buffer, stream_end, etc.)
    status Nullable(String),                -- stream status at time of event

    -- Optional stream metrics captured at event time
    buffer_state Nullable(String),
    has_issues Nullable(UInt8),
    issues_description Nullable(String),
    track_count Nullable(UInt16),
    quality_tier Nullable(String),
    primary_width Nullable(UInt16),
    primary_height Nullable(UInt16),
    primary_fps Nullable(Float32),
    primary_codec Nullable(String),
    primary_bitrate Nullable(UInt32),

    downloaded_bytes Nullable(UInt64),
    uploaded_bytes Nullable(UInt64),
    total_viewers Nullable(UInt32),
    total_inputs Nullable(UInt16),
    total_outputs Nullable(UInt16),
    viewer_seconds Nullable(UInt64),

    -- Ingest / routing details (where present)
    stream_key Nullable(String),
    user_id Nullable(String),
    request_url Nullable(String),
    protocol Nullable(String),

    -- Geo metadata (when present)
    latitude Nullable(Float64),
    longitude Nullable(Float64),
    location Nullable(String),
    country_code Nullable(FixedString(2)),
    city Nullable(String),

    -- Full event payload (typed JSON)
    event_data String,
    source_region LowCardinality(String) DEFAULT '',
    stream_origin_region LowCardinality(String) DEFAULT '',
    stream_origin_cluster_id LowCardinality(String) DEFAULT '',
    schema_version UInt8 DEFAULT 0
) ENGINE = MergeTree()
PARTITION BY (toYYYYMM(timestamp), tenant_id)
ORDER BY (tenant_id, stream_id, timestamp, event_id)
TTL timestamp + INTERVAL 90 DAY;

-- ============================================================================
-- STREAM VIEWER ROLLUPS (from stream_event_log.total_viewers)
-- ============================================================================

CREATE TABLE IF NOT EXISTS stream_viewer_5m (
    timestamp_5m DateTime,
    tenant_id UUID,
    stream_id UUID,
    max_viewers UInt32,
    avg_viewers Float32
) ENGINE = MergeTree()
PARTITION BY toYYYYMM(timestamp_5m)
ORDER BY (timestamp_5m, tenant_id, stream_id)
TTL timestamp_5m + INTERVAL 180 DAY;

CREATE MATERIALIZED VIEW IF NOT EXISTS stream_viewer_5m_mv TO stream_viewer_5m AS
SELECT
    toStartOfInterval(timestamp, INTERVAL 5 MINUTE) AS timestamp_5m,
    tenant_id,
    stream_id,
    max(total_viewers) AS max_viewers,
    avg(total_viewers) AS avg_viewers
FROM stream_event_log
WHERE total_viewers IS NOT NULL
GROUP BY timestamp_5m, tenant_id, stream_id;

-- ============================================================================
-- STREAM CURRENT STATE (live snapshot)
-- ============================================================================

CREATE TABLE IF NOT EXISTS stream_state_current (
    tenant_id UUID,
    stream_id UUID,
    internal_name String,
    node_id LowCardinality(String),

    status LowCardinality(String),
    buffer_state LowCardinality(String),

    current_viewers UInt32,
    total_inputs UInt16,

    uploaded_bytes UInt64,
    downloaded_bytes UInt64,
    viewer_seconds UInt64,

    has_issues Nullable(UInt8),
    issues_description Nullable(String),
    track_count Nullable(UInt16),
    quality_tier Nullable(String),
    primary_width Nullable(UInt16),
    primary_height Nullable(UInt16),
    primary_fps Nullable(Float32),
    primary_codec Nullable(String),
    primary_bitrate Nullable(UInt32),

    packets_sent Nullable(UInt64),
    packets_lost Nullable(UInt64),
    packets_retransmitted Nullable(UInt64),

    started_at Nullable(DateTime),
    updated_at DateTime
) ENGINE = ReplacingMergeTree(updated_at)
ORDER BY (tenant_id, stream_id);

-- ============================================================================
-- STREAM HEALTH SAMPLES (detailed QoE)
-- ============================================================================

CREATE TABLE IF NOT EXISTS stream_health_samples (
    timestamp DateTime,
    tenant_id UUID,
    stream_id UUID,
    internal_name String,
    node_id LowCardinality(String),

    bitrate Nullable(UInt32),
    fps Nullable(Float32),
    gop_size Nullable(UInt16),
    width Nullable(UInt16),
    height Nullable(UInt16),

    buffer_size Nullable(UInt32),
    buffer_health Nullable(Float32),
    buffer_state LowCardinality(String),

    codec Nullable(String),
    quality_tier Nullable(String),
    track_metadata JSON,

    frame_ms_max Nullable(Float32),
    frame_ms_min Nullable(Float32),
    frames_max Nullable(UInt32),
    frames_min Nullable(UInt32),
    keyframe_ms_max Nullable(Float32),
    keyframe_ms_min Nullable(Float32),
    frame_jitter_ms Nullable(Float32),

    issues_description Nullable(String),
    has_issues Nullable(UInt8),
    track_count Nullable(UInt16),

    audio_channels Nullable(UInt8),
    audio_sample_rate Nullable(UInt32),
    audio_codec Nullable(String),
    audio_bitrate Nullable(UInt32),
    source_region LowCardinality(String) DEFAULT '',
    stream_origin_region LowCardinality(String) DEFAULT '',
    stream_origin_cluster_id LowCardinality(String) DEFAULT '',
    schema_version UInt8 DEFAULT 0
) ENGINE = MergeTree()
PARTITION BY (toYYYYMM(timestamp), tenant_id)
ORDER BY (tenant_id, stream_id, timestamp)
TTL timestamp + INTERVAL 30 DAY;

-- 5m rollups for health
CREATE TABLE IF NOT EXISTS stream_health_5m (
    timestamp_5m DateTime,
    tenant_id UUID,
    stream_id UUID,
    internal_name String,
    node_id LowCardinality(String),
    rebuffer_count UInt32,
    issue_count UInt32,
    sample_issues Nullable(String),
    avg_bitrate Float32,
    avg_fps Float32,
    avg_buffer_health Float32,
    avg_frame_jitter_ms Nullable(Float32),
    max_frame_jitter_ms Nullable(Float32),
    buffer_dry_count UInt32,
    quality_tier LowCardinality(String)
) ENGINE = MergeTree()
PARTITION BY toYYYYMM(timestamp_5m)
ORDER BY (timestamp_5m, tenant_id, stream_id, node_id)
TTL timestamp_5m + INTERVAL 180 DAY;

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

-- Rebuffering events derived from health samples
CREATE TABLE IF NOT EXISTS rebuffering_events (
    timestamp DateTime,
    tenant_id UUID,
    stream_id UUID,
    internal_name String,
    node_id LowCardinality(String),
    buffer_state LowCardinality(String),
    prev_state LowCardinality(String),
    rebuffer_start UInt8,
    rebuffer_end UInt8,
    source_region LowCardinality(String) DEFAULT '',
    stream_origin_region LowCardinality(String) DEFAULT '',
    stream_origin_cluster_id LowCardinality(String) DEFAULT '',
    schema_version UInt8 DEFAULT 0
) ENGINE = MergeTree()
PARTITION BY (toYYYYMM(timestamp), tenant_id)
ORDER BY (tenant_id, stream_id, timestamp)
TTL timestamp + INTERVAL 90 DAY;

CREATE MATERIALIZED VIEW IF NOT EXISTS rebuffering_events_mv TO rebuffering_events AS
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
FROM stream_health_samples
WHERE buffer_state IN ('FULL', 'DRY', 'RECOVER');

-- ============================================================================
-- TRACK LIST EVENTS + QUALITY ROLLUPS
-- ============================================================================

CREATE TABLE IF NOT EXISTS track_list_events (
    timestamp DateTime,
    event_id UUID,
    tenant_id UUID,
    stream_id UUID,
    internal_name String,
    node_id LowCardinality(String),

    track_list String,
    track_count UInt16,
    video_track_count UInt16,
    audio_track_count UInt16,

    primary_width Nullable(UInt16),
    primary_height Nullable(UInt16),
    primary_fps Nullable(Float32),
    primary_video_codec Nullable(String),
    primary_video_bitrate Nullable(UInt32),
    quality_tier Nullable(String),

    primary_audio_channels Nullable(UInt8),
    primary_audio_sample_rate Nullable(UInt32),
    primary_audio_codec Nullable(String),
    primary_audio_bitrate Nullable(UInt32),
    source_region LowCardinality(String) DEFAULT '',
    stream_origin_region LowCardinality(String) DEFAULT '',
    stream_origin_cluster_id LowCardinality(String) DEFAULT '',
    schema_version UInt8 DEFAULT 0
) ENGINE = MergeTree()
PARTITION BY (toYYYYMM(timestamp), tenant_id)
ORDER BY (tenant_id, stream_id, timestamp, event_id)
TTL timestamp + INTERVAL 90 DAY;

CREATE TABLE IF NOT EXISTS quality_tier_daily (
    day Date,
    tenant_id UUID,
    stream_id UUID,
    internal_name String,
    tier_2160p_minutes UInt32,
    tier_1440p_minutes UInt32,
    tier_1080p_minutes UInt32,
    tier_720p_minutes UInt32,
    tier_480p_minutes UInt32,
    tier_sd_minutes UInt32,
    primary_tier LowCardinality(String),
    codec_h264_minutes UInt32,
    codec_h265_minutes UInt32,
    codec_vp9_minutes UInt32,
    codec_av1_minutes UInt32,
    avg_bitrate UInt32,
    avg_fps Float32
) ENGINE = SummingMergeTree()
PARTITION BY toYYYYMM(day)
ORDER BY (day, tenant_id, stream_id)
TTL day + INTERVAL 730 DAY;

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

-- ============================================================================
-- VIEWER CONNECTIONS + SESSIONS
-- ============================================================================

CREATE TABLE IF NOT EXISTS viewer_connection_events (
    event_id UUID,
    timestamp DateTime,
    tenant_id UUID,
    stream_id UUID,
    internal_name String,
    session_id String,
    connection_addr String,
    connector LowCardinality(String),
    node_id LowCardinality(String),
    cluster_id LowCardinality(String) DEFAULT '',
    origin_cluster_id LowCardinality(String) DEFAULT '',
    request_url Nullable(String),

    country_code FixedString(2),
    city LowCardinality(String),
    latitude Float64,
    longitude Float64,
    client_bucket_h3 Nullable(UInt64),
    client_bucket_res Nullable(UInt8),
    node_bucket_h3 Nullable(UInt64),
    node_bucket_res Nullable(UInt8),

    event_type LowCardinality(String),
    session_duration UInt32,
    bytes_transferred UInt64,
    source_region LowCardinality(String) DEFAULT '',
    stream_origin_region LowCardinality(String) DEFAULT '',
    stream_origin_cluster_id LowCardinality(String) DEFAULT '',
    schema_version UInt8 DEFAULT 0
) ENGINE = MergeTree()
PARTITION BY (toYYYYMM(timestamp), tenant_id)
ORDER BY (tenant_id, stream_id, timestamp, event_id)
TTL timestamp + INTERVAL 90 DAY;

CREATE TABLE IF NOT EXISTS viewer_sessions_current (
    tenant_id UUID,
    stream_id UUID,
    internal_name LowCardinality(String),
    session_id String,
    node_id LowCardinality(String),

    connected_at SimpleAggregateFunction(min, Nullable(DateTime)),
    disconnected_at SimpleAggregateFunction(max, Nullable(DateTime)),

    connector SimpleAggregateFunction(any, LowCardinality(String)),

    country_code SimpleAggregateFunction(any, FixedString(2)),
    city SimpleAggregateFunction(any, LowCardinality(String)),
    latitude SimpleAggregateFunction(any, Float64),
    longitude SimpleAggregateFunction(any, Float64),

    bytes_transferred SimpleAggregateFunction(max, UInt64),
    session_duration SimpleAggregateFunction(max, UInt32),

    last_updated SimpleAggregateFunction(max, DateTime)
) ENGINE = AggregatingMergeTree()
ORDER BY (tenant_id, stream_id, node_id, session_id);

CREATE MATERIALIZED VIEW IF NOT EXISTS viewer_sessions_connect_mv TO viewer_sessions_current AS
SELECT
    tenant_id,
    stream_id,
    internal_name,
    session_id,
    node_id,
    timestamp AS connected_at,
    CAST(NULL AS Nullable(DateTime)) AS disconnected_at,
    connector,
    country_code,
    city,
    latitude,
    longitude,
    bytes_transferred,
    session_duration,
    timestamp AS last_updated
FROM viewer_connection_events
WHERE event_type = 'connect' AND session_id != '';

CREATE MATERIALIZED VIEW IF NOT EXISTS viewer_sessions_disconnect_mv TO viewer_sessions_current AS
SELECT
    tenant_id,
    stream_id,
    internal_name,
    session_id,
    node_id,
    CAST(NULL AS Nullable(DateTime)) AS connected_at,
    timestamp AS disconnected_at,
    connector,
    country_code,
    city,
    latitude,
    longitude,
    bytes_transferred,
    session_duration,
    timestamp AS last_updated
FROM viewer_connection_events
WHERE event_type = 'disconnect' AND session_id != '';

-- Client QoE samples
-- event_id is populated per-sample by Helmsman and propagated through Foghorn's
-- ClientLifecycleBatch. Nullable + DEFAULT NULL on purpose: a server-side
-- generateUUIDv4() default would defeat replay dedup by minting a fresh UUID
-- for every replayed row.
CREATE TABLE IF NOT EXISTS client_qoe_samples (
    timestamp DateTime,
    event_id Nullable(UUID) DEFAULT NULL,
    tenant_id UUID,
    stream_id UUID,
    internal_name String,
    session_id String,
    node_id LowCardinality(String),

    protocol LowCardinality(String),
    host String,
    connection_time Float32,
    position Nullable(Float32),

    bandwidth_in UInt64,
    bandwidth_out UInt64,
    bytes_downloaded UInt64,
    bytes_uploaded UInt64,

    packets_sent UInt64,
    packets_lost UInt64,
    packets_retransmitted UInt64,
    connection_quality Nullable(Float32),
    source_region LowCardinality(String) DEFAULT '',
    stream_origin_region LowCardinality(String) DEFAULT '',
    stream_origin_cluster_id LowCardinality(String) DEFAULT '',
    schema_version UInt8 DEFAULT 0
) ENGINE = MergeTree()
PARTITION BY (toYYYYMM(timestamp), tenant_id)
ORDER BY (tenant_id, stream_id, timestamp)
TTL timestamp + INTERVAL 90 DAY;

CREATE TABLE IF NOT EXISTS client_qoe_5m (
    timestamp_5m DateTime,
    tenant_id UUID,
    stream_id UUID,
    internal_name String,
    node_id LowCardinality(String),
    active_sessions UInt32,
    avg_bw_in Float64,
    avg_bw_out Float64,
    avg_connection_time Float32,
    pkt_loss_rate Nullable(Float32)
) ENGINE = MergeTree()
PARTITION BY toYYYYMM(timestamp_5m)
ORDER BY (timestamp_5m, tenant_id, stream_id, node_id)
TTL timestamp_5m + INTERVAL 180 DAY;

CREATE MATERIALIZED VIEW IF NOT EXISTS client_qoe_5m_mv TO client_qoe_5m AS
SELECT
    toStartOfInterval(timestamp, INTERVAL 5 MINUTE) AS timestamp_5m,
    tenant_id,
    stream_id,
    internal_name,
    node_id,
    count(DISTINCT session_id) as active_sessions,
    avg(bandwidth_in) AS avg_bw_in,
    avg(bandwidth_out) AS avg_bw_out,
    avg(connection_time) AS avg_connection_time,
    if(sum(packets_sent) > 0, sum(packets_lost) / sum(packets_sent), NULL) AS pkt_loss_rate
FROM client_qoe_samples
GROUP BY timestamp_5m, tenant_id, stream_id, internal_name, node_id;


-- ============================================================================
-- PLAYER BOOT TELEMETRY (browser-originated startup waterfall)
-- ============================================================================
-- One row per player boot attempt (browser sendBeacon -> Bridge -> Decklog).
-- Diagnostic/lossy, never a viewer-count or billing source. Attribution is
-- server-derived: Bridge stamps tenant_id/stream_id/artifact_hash from Commodore
-- and mints event_id; node_id/serving_cluster_id are trusted only when a
-- telemetry token supplied them (cluster_attributed = 1), which gates the
-- cluster-ops read surface. Percentiles are computed at read time over this raw
-- table — there is deliberately no rollup MV (quantile() is not mergeable in a
-- plain MergeTree; add an AggregatingMergeTree rollup only if volume demands).
CREATE TABLE IF NOT EXISTS player_boot_samples (
    timestamp DateTime,
    event_id UUID,
    tenant_id UUID,
    stream_id Nullable(UUID),
    artifact_hash String DEFAULT '',
    internal_name String DEFAULT '',
    session_id String DEFAULT '',
    trace_id String DEFAULT '',

    node_id LowCardinality(String) DEFAULT '',
    serving_cluster_id LowCardinality(String) DEFAULT '',
    origin_cluster_id LowCardinality(String) DEFAULT '',
    cluster_attributed UInt8 DEFAULT 0,

    total_ttf_ms UInt32 DEFAULT 0,
    gateway_resolve_ms UInt32 DEFAULT 0,
    mist_hydrate_ms UInt32 DEFAULT 0,
    player_select_ms UInt32 DEFAULT 0,
    connect_ms UInt32 DEFAULT 0,
    prebuffer_ms UInt32 DEFAULT 0,

    outcome LowCardinality(String) DEFAULT '',
    error_code LowCardinality(String) DEFAULT '',
    player_type LowCardinality(String) DEFAULT '',
    protocol LowCardinality(String) DEFAULT '',
    content_type LowCardinality(String) DEFAULT '',
    is_live UInt8 DEFAULT 0,
    connection_type LowCardinality(String) DEFAULT '',
    player_version LowCardinality(String) DEFAULT '',

    manifest_url String DEFAULT '',
    manifest_ms UInt32 DEFAULT 0,
    manifest_transfer_size UInt64 DEFAULT 0,
    first_segment_url String DEFAULT '',
    first_segment_ms UInt32 DEFAULT 0,
    first_segment_transfer_size UInt64 DEFAULT 0,
    cdn_cache_status LowCardinality(String) DEFAULT '',
    age_seconds Nullable(UInt32) DEFAULT NULL,
    resources String DEFAULT '',

    source_region LowCardinality(String) DEFAULT '',
    stream_origin_region LowCardinality(String) DEFAULT '',
    stream_origin_cluster_id LowCardinality(String) DEFAULT '',
    schema_version UInt8 DEFAULT 0
) ENGINE = MergeTree()
PARTITION BY (toYYYYMM(timestamp), tenant_id)
-- stream_id is Nullable (null for VOD), so it cannot appear raw in the sorting
-- key without allow_nullable_key; ifNull() makes the key non-nullable while
-- keeping per-stream locality.
ORDER BY (tenant_id, ifNull(stream_id, toUUID('00000000-0000-0000-0000-000000000000')), timestamp)
TTL timestamp + INTERVAL 90 DAY;


-- ============================================================================
-- VIEWER-EXPERIENCED QoE (browser-originated session deltas)
-- ============================================================================
-- One row per QoE beacon (heartbeat or final) from the player. Unlike the polled
-- client_qoe_samples (Mist connection stats, can't see HLS/DASH viewer stalls),
-- this is what the viewer actually experienced. Every counter is an ADDITIVE
-- DELTA for the window since the previous beacon, so sum() reconstructs session
-- totals even when the final beacon is lost. Diagnostic/lossy — never a
-- viewer-count or billing source.
--
-- Dedupe has two distinct modes with different keys:
--   * MistTrigger event_id (minted per received beacon by Bridge) → Kafka replay.
--   * client-stable (tenant_id, content_id, session_id, beacon_seq) → a
--     double-fired client beacon. Bridge mints a FRESH event_id per HTTP request,
--     so event_id CANNOT catch this; the ReplacingMergeTree ORDER BY does.
-- Dedup is merge-time (eventual): any reader must consume a deduped surface — read
-- with FINAL / GROUP BY — and never sum the raw table before replacement, or
-- duplicates inflate the sums. Ratios (rebuffer_ms/played_ms, dropped/decoded) are
-- computed at read time over the deduped rows.
CREATE TABLE IF NOT EXISTS client_qoe_session_deltas (
    timestamp DateTime,
    event_id UUID,                       -- Bridge-minted; Kafka-replay idempotency only, NOT the client dedupe key
    tenant_id UUID,
    stream_id Nullable(UUID),            -- null for VOD/standalone artifacts
    artifact_hash String DEFAULT '',
    internal_name String DEFAULT '',
    content_id String DEFAULT '',
    session_id String DEFAULT '',

    beacon_seq UInt32 DEFAULT 0,
    is_final UInt8 DEFAULT 0,
    flush_reason LowCardinality(String) DEFAULT '',

    node_id LowCardinality(String) DEFAULT '',
    serving_cluster_id LowCardinality(String) DEFAULT '',
    origin_cluster_id LowCardinality(String) DEFAULT '',
    cluster_attributed UInt8 DEFAULT 0,  -- 1 only when a telemetry token supplied node/cluster

    player_type LowCardinality(String) DEFAULT '',
    protocol LowCardinality(String) DEFAULT '',
    content_type LowCardinality(String) DEFAULT '',
    is_live UInt8 DEFAULT 0,
    connection_type LowCardinality(String) DEFAULT '',
    player_version LowCardinality(String) DEFAULT '',

    -- Additive QoE deltas for the window since the previous beacon.
    -- played_ms is the genuine-watch-time denominator (union of video.played
    -- growth), never wall-clock. rebuffer_ms excludes initial buffering, seeks,
    -- and pauses; seek_wait_ms is kept separate for diagnostics.
    played_ms UInt64 DEFAULT 0,
    rebuffer_ms UInt64 DEFAULT 0,
    rebuffer_count UInt32 DEFAULT 0,
    seek_wait_ms UInt64 DEFAULT 0,

    -- Rendering quality. Browser frame counters are cumulative/non-resettable, so
    -- these are per-window deltas. frame_stats_supported = 0 means the platform
    -- never reports frame stats (0/0 must not read as "perfect").
    frame_stats_supported UInt8 DEFAULT 0,
    frames_decoded UInt64 DEFAULT 0,
    frames_dropped UInt64 DEFAULT 0,
    frames_corrupted UInt64 DEFAULT 0,

    -- Session-terminal failure. fatal_error = 1 only for an unrecoverable error
    -- after first frame; the client flushes that transition immediately so a
    -- lost final beacon does not hide it.
    first_frame UInt8 DEFAULT 0,
    fatal_error UInt8 DEFAULT 0,
    error_code LowCardinality(String) DEFAULT '',

    bitrate_bps_seconds UInt64 DEFAULT 0,
    abr_upswitch_count UInt32 DEFAULT 0,
    abr_downswitch_count UInt32 DEFAULT 0,
    play_intent UInt8 DEFAULT 0,
    live_edge_latency_ms UInt32 DEFAULT 0,

    -- VOD reach + timeline geometry. bucket_width_s > 0 is the presence bit for a
    -- real VOD reach sample (live/no-duration beacons leave it 0 and are excluded
    -- from the retention denominator). Geometry is persisted here too so a seek-only
    -- session (reached far, watched nothing → no vod_retention_buckets rows) still
    -- carries the timeline. max_bucket_reached = furthest bucket the playhead reached
    -- (per-session max at read time); the audience-retention input, distinct from
    -- watch density.
    bucket_width_s UInt32 DEFAULT 0,
    asset_duration_s UInt32 DEFAULT 0,
    max_bucket_reached UInt32 DEFAULT 0,

    source_region LowCardinality(String) DEFAULT '',
    stream_origin_region LowCardinality(String) DEFAULT '',
    stream_origin_cluster_id LowCardinality(String) DEFAULT '',
    schema_version UInt8 DEFAULT 0
) ENGINE = ReplacingMergeTree(timestamp)
PARTITION BY (toYYYYMM(timestamp), tenant_id)
-- ORDER BY tuple IS the ReplacingMergeTree dedupe key: the client-stable identity
-- of a beacon, which collapses double-fires regardless of the per-request event_id.
ORDER BY (tenant_id, content_id, session_id, beacon_seq)
TTL timestamp + INTERVAL 90 DAY;


-- VOD retention heatmap: one row per (session, timeline bucket, beacon). Folded
-- out of the same session QoE beacon (VOD content only) by the ingest handler.
-- seconds_watched is the per-bucket watched-seconds DELTA for the beacon window,
-- so it is additive across a session's beacons and across sessions. This table is
-- the "most replayed" WATCH-DENSITY source (sum(seconds_watched) per bucket). The
-- separate AUDIENCE-RETENTION curve (sessions reaching ≥ bucket ÷ total) is derived
-- from per-session max_bucket_reached on client_qoe_session_deltas, NOT from this
-- table — a seek-to-end advances reach without adding density here. Buckets are
-- fixed-WIDTH (bucket_width_s, chosen client-side by duration tier) so the
-- bucket→timestamp mapping is asset-independent.
--
-- Dedup mirrors client_qoe_session_deltas: ReplacingMergeTree on the client-stable
-- (tenant_id, artifact_hash, session_id, bucket_index, beacon_seq) collapses a
-- double-fired beacon; event_id carries Kafka-replay idempotency. Curves are
-- computed at read time over the deduped rows. Any rollup over this table must be
-- built from a deduped surface (read FINAL or a deduped view), never summed off the
-- raw ReplacingMergeTree before merge-time replacement runs.
CREATE TABLE IF NOT EXISTS vod_retention_buckets (
    timestamp DateTime,
    event_id UUID,
    tenant_id UUID,
    artifact_hash String DEFAULT '',
    internal_name String DEFAULT '',
    content_id String DEFAULT '',
    session_id String DEFAULT '',

    beacon_seq UInt32 DEFAULT 0,
    bucket_width_s UInt32 DEFAULT 0,
    asset_duration_s UInt32 DEFAULT 0,
    bucket_index UInt32,
    seconds_watched Float32 DEFAULT 0,

    source_region LowCardinality(String) DEFAULT '',
    schema_version UInt8 DEFAULT 0
) ENGINE = ReplacingMergeTree(timestamp)
PARTITION BY (toYYYYMM(timestamp), tenant_id)
ORDER BY (tenant_id, artifact_hash, session_id, bucket_index, beacon_seq)
TTL timestamp + INTERVAL 90 DAY;


-- ============================================================================
-- ROUTING DECISIONS
-- ============================================================================

CREATE TABLE IF NOT EXISTS routing_decisions (
    timestamp DateTime,
    tenant_id UUID,
    stream_id UUID,
    internal_name String,

    selected_node LowCardinality(String),
    status LowCardinality(String),
    details String,
    score Int64,

    client_ip String,
    client_country FixedString(2),
    client_latitude Float64,
    client_longitude Float64,
    client_bucket_h3 Nullable(UInt64),
    client_bucket_res Nullable(UInt8),

    node_latitude Float64,
    node_longitude Float64,
    node_name LowCardinality(String),
    node_bucket_h3 Nullable(UInt64),
    node_bucket_res Nullable(UInt8),
    selected_node_id Nullable(String),
    routing_distance_km Nullable(Float64),

    stream_tenant_id Nullable(UUID),
    cluster_id LowCardinality(String) DEFAULT '',
    remote_cluster_id LowCardinality(String) DEFAULT '',

    latency_ms Nullable(Float32),
    candidates_count Nullable(Int32),
    event_type Nullable(String),
    source Nullable(String),
    source_region LowCardinality(String) DEFAULT '',
    stream_origin_region LowCardinality(String) DEFAULT '',
    stream_origin_cluster_id LowCardinality(String) DEFAULT '',
    schema_version UInt8 DEFAULT 0
) ENGINE = MergeTree()
PARTITION BY (toYYYYMM(timestamp), tenant_id)
ORDER BY (tenant_id, stream_id, timestamp)
TTL timestamp + INTERVAL 90 DAY;

-- Hourly routing by cluster pair (for traffic matrix and dashboards)
CREATE TABLE IF NOT EXISTS routing_cluster_hourly (
    hour DateTime,
    tenant_id UUID,
    cluster_id LowCardinality(String),
    remote_cluster_id LowCardinality(String),
    status LowCardinality(String),
    event_count UInt32,
    success_count UInt32,
    sum_latency_ms Float32,
    sum_distance_km Float64,
    max_latency_ms Float32,
    avg_score Float64
) ENGINE = SummingMergeTree((event_count, success_count, sum_latency_ms, sum_distance_km))
PARTITION BY toYYYYMM(hour)
ORDER BY (hour, tenant_id, cluster_id, remote_cluster_id, status)
TTL hour + INTERVAL 365 DAY;

CREATE MATERIALIZED VIEW IF NOT EXISTS routing_cluster_hourly_mv TO routing_cluster_hourly AS
SELECT
    toStartOfHour(timestamp) AS hour,
    tenant_id,
    cluster_id,
    remote_cluster_id,
    status,
    toUInt32(count()) AS event_count,
    toUInt32(countIf(status = 'success')) AS success_count,
    toFloat32(sum(if(isNull(latency_ms), 0., assumeNotNull(latency_ms)))) AS sum_latency_ms,
    toFloat64(sum(if(isNull(routing_distance_km), 0., assumeNotNull(routing_distance_km)))) AS sum_distance_km,
    toFloat32(max(if(isNull(latency_ms), 0., assumeNotNull(latency_ms)))) AS max_latency_ms,
    toFloat64(avg(score)) AS avg_score
FROM routing_decisions
GROUP BY hour, tenant_id, cluster_id, remote_cluster_id, status;

-- ============================================================================
-- FEDERATION EVENTS
-- ============================================================================

CREATE TABLE IF NOT EXISTS federation_events (
    timestamp DateTime,
    tenant_id UUID,
    event_type LowCardinality(String),
    local_cluster LowCardinality(String),
    remote_cluster LowCardinality(String),
    stream_name String DEFAULT '',
    stream_id Nullable(UUID),
    source_node Nullable(String),
    dest_node Nullable(String),
    dtsc_url Nullable(String),
    latency_ms Nullable(Float32),
    time_to_live_ms Nullable(Float32),
    failure_reason Nullable(String),
    queried_clusters Nullable(UInt32),
    responding_clusters Nullable(UInt32),
    total_candidates Nullable(UInt32),
    best_remote_score Nullable(UInt64),
    peer_cluster Nullable(String),
    role LowCardinality(String) DEFAULT '',
    reason Nullable(String),
    blocked_cluster Nullable(String),
    existing_replication_cluster Nullable(String),
    local_lat Nullable(Float64),
    local_lon Nullable(Float64),
    remote_lat Nullable(Float64),
    remote_lon Nullable(Float64),
    source_region LowCardinality(String) DEFAULT '',
    stream_origin_region LowCardinality(String) DEFAULT '',
    stream_origin_cluster_id LowCardinality(String) DEFAULT '',
    schema_version UInt8 DEFAULT 0
) ENGINE = MergeTree()
PARTITION BY (toYYYYMM(timestamp), tenant_id)
ORDER BY (tenant_id, local_cluster, event_type, timestamp)
TTL timestamp + INTERVAL 90 DAY;

-- Hourly rollup for federation summary dashboards
CREATE TABLE IF NOT EXISTS federation_hourly (
    hour DateTime,
    tenant_id UUID,
    local_cluster LowCardinality(String),
    remote_cluster LowCardinality(String),
    event_type LowCardinality(String),
    event_count UInt32,
    sum_latency_ms Float32,
    sum_time_to_live_ms Float32,
    failure_count UInt32
) ENGINE = SummingMergeTree((event_count, sum_latency_ms, sum_time_to_live_ms, failure_count))
PARTITION BY toYYYYMM(hour)
ORDER BY (hour, tenant_id, local_cluster, remote_cluster, event_type)
TTL hour + INTERVAL 365 DAY;

CREATE MATERIALIZED VIEW IF NOT EXISTS federation_hourly_mv TO federation_hourly AS
SELECT
    toStartOfHour(timestamp) AS hour,
    tenant_id,
    local_cluster,
    remote_cluster,
    event_type,
    count() AS event_count,
    sum(ifNull(latency_ms, 0)) AS sum_latency_ms,
    sum(ifNull(time_to_live_ms, 0)) AS sum_time_to_live_ms,
    countIf(failure_reason != '' AND failure_reason IS NOT NULL) AS failure_count
FROM federation_events
GROUP BY hour, tenant_id, local_cluster, remote_cluster, event_type;

-- ============================================================================
-- NODE STATE + METRICS
-- ============================================================================

CREATE TABLE IF NOT EXISTS node_state_current (
    tenant_id UUID,
    cluster_id LowCardinality(String),
    node_id String,

    cpu_percent Float32,
    ram_used_bytes UInt64,
    ram_total_bytes UInt64,
    disk_used_bytes UInt64,
    disk_total_bytes UInt64,

    up_speed UInt64,
    down_speed UInt64,
    bw_limit UInt64 DEFAULT 0,

    active_streams UInt32,
    is_healthy UInt8,
    operational_mode LowCardinality(String) DEFAULT 'normal',

    latitude Float64,
    longitude Float64,
    location String,

    metadata JSON,
    updated_at DateTime
) ENGINE = ReplacingMergeTree(updated_at)
ORDER BY (tenant_id, cluster_id, node_id);

CREATE TABLE IF NOT EXISTS node_metrics_samples (
    timestamp DateTime,
    tenant_id UUID,
    cluster_id LowCardinality(String),
    node_id LowCardinality(String),

    cpu_usage Float32,
    ram_max UInt64,
    ram_current UInt64,
    shm_total_bytes UInt64,
    shm_used_bytes UInt64,
    disk_total_bytes UInt64,
    disk_used_bytes UInt64,

    bandwidth_in UInt64,
    bandwidth_out UInt64,
    up_speed UInt64,
    down_speed UInt64,
    connections_current UInt32,
    stream_count UInt32,

    is_healthy UInt8,
    operational_mode LowCardinality(String) DEFAULT 'normal',
    latitude Float64,
    longitude Float64,

    metadata JSON,
    source_region LowCardinality(String) DEFAULT '',
    stream_origin_region LowCardinality(String) DEFAULT '',
    stream_origin_cluster_id LowCardinality(String) DEFAULT '',
    schema_version UInt8 DEFAULT 0
) ENGINE = MergeTree()
PARTITION BY (toYYYYMM(timestamp), tenant_id)
ORDER BY (tenant_id, cluster_id, node_id, timestamp)
TTL timestamp + INTERVAL 30 DAY;

CREATE TABLE IF NOT EXISTS node_performance_5m (
    timestamp_5m DateTime,
    tenant_id UUID,
    cluster_id LowCardinality(String),
    node_id LowCardinality(String),
    cpu_sum SimpleAggregateFunction(sum, Float64),
    cpu_count SimpleAggregateFunction(sum, UInt64),
    max_cpu SimpleAggregateFunction(max, Float32),
    memory_sum SimpleAggregateFunction(sum, Float64),
    memory_count SimpleAggregateFunction(sum, UInt64),
    max_memory SimpleAggregateFunction(max, Float32),
    bw_in_max SimpleAggregateFunction(max, UInt64),
    bw_in_min SimpleAggregateFunction(min, UInt64),
    bw_out_max SimpleAggregateFunction(max, UInt64),
    bw_out_min SimpleAggregateFunction(min, UInt64),
    streams_sum SimpleAggregateFunction(sum, Float64),
    streams_count SimpleAggregateFunction(sum, UInt64),
    max_streams SimpleAggregateFunction(max, UInt32)
) ENGINE = AggregatingMergeTree()
PARTITION BY (toYYYYMM(timestamp_5m), tenant_id)
ORDER BY (tenant_id, cluster_id, node_id, timestamp_5m)
TTL timestamp_5m + INTERVAL 180 DAY;

CREATE MATERIALIZED VIEW IF NOT EXISTS node_performance_5m_mv TO node_performance_5m AS
SELECT
    toStartOfInterval(timestamp, INTERVAL 5 MINUTE) as timestamp_5m,
    tenant_id, cluster_id, node_id,
    sum(cpu_usage) as cpu_sum,
    count() as cpu_count,
    max(cpu_usage) as max_cpu,
    sum(if(ram_max > 0, ram_current / ram_max * 100, 0)) as memory_sum,
    count() as memory_count,
    max(if(ram_max > 0, ram_current / ram_max * 100, 0)) as max_memory,
    max(bandwidth_in) as bw_in_max,
    min(bandwidth_in) as bw_in_min,
    max(bandwidth_out) as bw_out_max,
    min(bandwidth_out) as bw_out_min,
    sum(stream_count) as streams_sum,
    count() as streams_count,
    max(stream_count) as max_streams
FROM node_metrics_samples
GROUP BY timestamp_5m, tenant_id, cluster_id, node_id;

CREATE TABLE IF NOT EXISTS node_metrics_1h (
    timestamp_1h DateTime,
    tenant_id UUID,
    cluster_id LowCardinality(String),
    node_id LowCardinality(String),
    cpu_sum SimpleAggregateFunction(sum, Float64),
    cpu_count SimpleAggregateFunction(sum, UInt64),
    peak_cpu SimpleAggregateFunction(max, Float32),
    memory_sum SimpleAggregateFunction(sum, Float64),
    memory_count SimpleAggregateFunction(sum, UInt64),
    peak_memory SimpleAggregateFunction(max, Float32),
    disk_sum SimpleAggregateFunction(sum, Float64),
    disk_count SimpleAggregateFunction(sum, UInt64),
    peak_disk SimpleAggregateFunction(max, Float32),
    shm_sum SimpleAggregateFunction(sum, Float64),
    shm_count SimpleAggregateFunction(sum, UInt64),
    peak_shm SimpleAggregateFunction(max, Float32),
    bw_in_max SimpleAggregateFunction(max, UInt64),
    bw_in_min SimpleAggregateFunction(min, UInt64),
    bw_out_max SimpleAggregateFunction(max, UInt64),
    bw_out_min SimpleAggregateFunction(min, UInt64),
    healthy_sum SimpleAggregateFunction(sum, UInt64),
    healthy_count SimpleAggregateFunction(sum, UInt64)
) ENGINE = AggregatingMergeTree()
PARTITION BY (toYYYYMM(timestamp_1h), tenant_id)
ORDER BY (tenant_id, cluster_id, node_id, timestamp_1h)
TTL timestamp_1h + INTERVAL 365 DAY;

CREATE MATERIALIZED VIEW IF NOT EXISTS node_metrics_1h_mv TO node_metrics_1h AS
SELECT
    toStartOfHour(timestamp) AS timestamp_1h,
    tenant_id, cluster_id, node_id,
    sum(cpu_usage) AS cpu_sum, count() AS cpu_count, max(cpu_usage) AS peak_cpu,
    sum(if(ram_max > 0, ram_current / ram_max * 100, 0)) AS memory_sum,
    count() AS memory_count,
    max(if(ram_max > 0, ram_current / ram_max * 100, 0)) AS peak_memory,
    sum(if(disk_total_bytes > 0, disk_used_bytes / disk_total_bytes * 100, 0)) AS disk_sum,
    count() AS disk_count,
    max(if(disk_total_bytes > 0, disk_used_bytes / disk_total_bytes * 100, 0)) AS peak_disk,
    sum(if(shm_total_bytes > 0, shm_used_bytes / shm_total_bytes * 100, 0)) AS shm_sum,
    count() AS shm_count,
    max(if(shm_total_bytes > 0, shm_used_bytes / shm_total_bytes * 100, 0)) AS peak_shm,
    max(bandwidth_in) AS bw_in_max, min(bandwidth_in) AS bw_in_min,
    max(bandwidth_out) AS bw_out_max, min(bandwidth_out) AS bw_out_min,
    sum(is_healthy) AS healthy_sum, count() AS healthy_count
FROM node_metrics_samples
GROUP BY timestamp_1h, tenant_id, cluster_id, node_id;

-- ============================================================================
-- ARTIFACT EVENTS + STATE (clips, DVR, VOD)
-- ============================================================================

CREATE TABLE IF NOT EXISTS artifact_events (
    timestamp DateTime,
    tenant_id UUID,
    stream_id UUID,
    internal_name String,
    cluster_id LowCardinality(String) DEFAULT '',
    origin_cluster_id LowCardinality(String) DEFAULT '',
    filename Nullable(String),
    request_id String,

    stage LowCardinality(String),
    content_type LowCardinality(String),

    start_unix Nullable(Int64),
    stop_unix Nullable(Int64),

    ingest_node_id Nullable(String),

    percent Nullable(UInt32),
    message Nullable(String),

    file_path Nullable(String),
    s3_url Nullable(String),
    size_bytes Nullable(UInt64),
    expires_at Nullable(Int64),
    source_region LowCardinality(String) DEFAULT '',
    stream_origin_region LowCardinality(String) DEFAULT '',
    stream_origin_cluster_id LowCardinality(String) DEFAULT '',
    schema_version UInt8 DEFAULT 0
) ENGINE = MergeTree()
PARTITION BY (toYYYYMM(timestamp), tenant_id)
ORDER BY (tenant_id, stream_id, timestamp, request_id)
TTL timestamp + INTERVAL 90 DAY;


CREATE TABLE IF NOT EXISTS artifact_state_current (
    tenant_id UUID,
    stream_id UUID,
    request_id String,
    internal_name String,
    filename Nullable(String),

    content_type LowCardinality(String),
    stage LowCardinality(String),
    progress_percent UInt8,
    error_message Nullable(String),

    requested_at DateTime,
    started_at Nullable(DateTime),
    completed_at Nullable(DateTime),

    clip_start_unix Nullable(Int64),
    clip_stop_unix Nullable(Int64),

    segment_count Nullable(UInt32),
    manifest_path Nullable(String),

    file_path Nullable(String),
    s3_url Nullable(String),
    size_bytes Nullable(UInt64),

    processing_node_id Nullable(String),

    updated_at DateTime,
    expires_at Nullable(DateTime),

    storage_location Nullable(String),
    sync_status Nullable(String),
    is_hot Nullable(Bool),
    is_synced Nullable(Bool),
    is_finalized Nullable(Bool),
    is_frozen Nullable(Bool)
) ENGINE = ReplacingMergeTree(updated_at)
ORDER BY (tenant_id, request_id);

-- ============================================================================
-- STORAGE SNAPSHOTS + LIFECYCLE
-- ============================================================================

CREATE TABLE IF NOT EXISTS storage_snapshots (
    timestamp DateTime,
    tenant_id UUID,
    node_id LowCardinality(String),
    cluster_id LowCardinality(String) DEFAULT '',
    storage_scope LowCardinality(String) DEFAULT 'hot',

    -- Provider attribution. The `tenant_id` above is the usage tenant
    -- (the customer); these three columns identify who owns the
    -- underlying storage capacity. Marketplace settlement keys off
    -- them to route payouts to capacity owners. For cold snapshots
    -- emitted by Foghorn this is the platform/cluster operator; for
    -- hot edge snapshots from Helmsman it's the node's owning tenant.
    storage_provider_tenant_id LowCardinality(String) DEFAULT '',
    storage_provider_cluster_id LowCardinality(String) DEFAULT '',
    storage_backend LowCardinality(String) DEFAULT 'unknown',

    total_bytes UInt64,
    file_count UInt32,

    dvr_bytes UInt64,
    clip_bytes UInt64,
    vod_bytes UInt64,

    frozen_dvr_bytes UInt64 DEFAULT 0,
    frozen_clip_bytes UInt64 DEFAULT 0,
    frozen_vod_bytes UInt64 DEFAULT 0,

    -- Ingest wall-clock when periscope wrote this row, in ms since
    -- epoch. The storage rebuilder cursors on this column (not on the
    -- source `timestamp`) so a snapshot that lands minutes or hours
    -- after its recorded `timestamp` is still picked up by the next
    -- rebuilder pass instead of being permanently skipped.
    ingested_at_ms Int64 DEFAULT toUnixTimestamp64Milli(now64(3))
) ENGINE = MergeTree()
PARTITION BY (toYYYYMM(timestamp), tenant_id)
ORDER BY (timestamp, tenant_id, node_id)
TTL timestamp + INTERVAL 90 DAY;


CREATE TABLE IF NOT EXISTS storage_events (
    timestamp DateTime,
    tenant_id UUID,
    stream_id UUID,
    internal_name String,
    asset_hash String,

    action LowCardinality(String),
    asset_type LowCardinality(String),

    size_bytes UInt64,
    s3_url Nullable(String),
    local_path Nullable(String),
    node_id LowCardinality(String),
    duration_ms Nullable(Int64),
    warm_duration_ms Nullable(Int64),
    error Nullable(String),
    cluster_id LowCardinality(String) DEFAULT '',
    origin_cluster_id LowCardinality(String) DEFAULT '',
    source_region LowCardinality(String) DEFAULT '',
    stream_origin_region LowCardinality(String) DEFAULT '',
    stream_origin_cluster_id LowCardinality(String) DEFAULT '',
    schema_version UInt8 DEFAULT 0
) ENGINE = MergeTree()
PARTITION BY (toYYYYMM(timestamp), tenant_id)
ORDER BY (tenant_id, stream_id, timestamp, asset_hash)
TTL timestamp + INTERVAL 90 DAY;

-- ============================================================================
-- PROCESSING EVENTS + DAILY ROLLUPS
-- ============================================================================

CREATE TABLE IF NOT EXISTS processing_events (
    timestamp DateTime,
    tenant_id UUID,
    node_id LowCardinality(String),
    cluster_id LowCardinality(String) DEFAULT '',
    origin_cluster_id LowCardinality(String) DEFAULT '',
    stream_id UUID,
    internal_name String,

    process_type LowCardinality(String),
    track_type LowCardinality(String),

    duration_ms Int64,

    input_codec Nullable(String),
    output_codec Nullable(String),

    segment_number Nullable(Int32),
    width Nullable(Int32),
    height Nullable(Int32),
    rendition_count Nullable(Int32),
    broadcaster_url Nullable(String),
    upload_time_us Nullable(Int64),
    livepeer_session_id Nullable(String),
    segment_start_ms Nullable(Int64),
    input_bytes Nullable(Int64),
    output_bytes_total Nullable(Int64),
    attempt_count Nullable(Int32),
    turnaround_ms Nullable(Int64),
    speed_factor Nullable(Float64),
    renditions_json Nullable(String),

    input_frames Nullable(Int64),
    output_frames Nullable(Int64),
    decode_us_per_frame Nullable(Int64),
    transform_us_per_frame Nullable(Int64),
    encode_us_per_frame Nullable(Int64),
    is_final Nullable(UInt8),
    input_frames_delta Nullable(Int64),
    output_frames_delta Nullable(Int64),
    input_bytes_delta Nullable(Int64),
    output_bytes_delta Nullable(Int64),
    input_width Nullable(Int32),
    input_height Nullable(Int32),
    output_width Nullable(Int32),
    output_height Nullable(Int32),
    input_fpks Nullable(Int32),
    output_fps_measured Nullable(Float64),
    sample_rate Nullable(Int32),
    channels Nullable(Int32),
    source_timestamp_ms Nullable(Int64),
    sink_timestamp_ms Nullable(Int64),
    source_advanced_ms Nullable(Int64),
    sink_advanced_ms Nullable(Int64),
    rtf_in Nullable(Float64),
    rtf_out Nullable(Float64),
    pipeline_lag_ms Nullable(Int64),
    output_bitrate_bps Nullable(Int64),
    source_region LowCardinality(String) DEFAULT '',
    stream_origin_region LowCardinality(String) DEFAULT '',
    stream_origin_cluster_id LowCardinality(String) DEFAULT '',
    schema_version UInt8 DEFAULT 0
) ENGINE = MergeTree()
PARTITION BY (toYYYYMM(timestamp), tenant_id)
ORDER BY (tenant_id, stream_id, timestamp)
TTL timestamp + INTERVAL 90 DAY;


-- ============================================================================
-- INGEST ERRORS (DLQ)
-- ============================================================================

CREATE TABLE IF NOT EXISTS ingest_errors (
    received_at DateTime,
    event_id String,
    event_type LowCardinality(String),
    source LowCardinality(String),
    tenant_id String,
    stream_id String,
    error String,
    payload_json String,
    source_region LowCardinality(String) DEFAULT '',
    stream_origin_region LowCardinality(String) DEFAULT '',
    stream_origin_cluster_id LowCardinality(String) DEFAULT '',
    schema_version UInt8 DEFAULT 0
) ENGINE = MergeTree()
PARTITION BY toYYYYMM(received_at)
ORDER BY (received_at, event_type, event_id)
TTL received_at + INTERVAL 30 DAY;

-- ============================================================================
-- API REQUEST TRACKING (Rate Limiting & Agent Analytics)
-- ============================================================================
-- Tracks GraphQL API requests for usage analytics and billing.
-- Enables tracking of agent vs. human usage patterns.
-- See: docs/rfcs/agent-access.md
-- ============================================================================

CREATE TABLE IF NOT EXISTS api_requests (
    timestamp DateTime,
    tenant_id UUID,
    source_node Nullable(String),                     -- Gateway instance ID for aggregate batches

    -- ===== AUTH TRACKING =====
    auth_type LowCardinality(String) DEFAULT 'jwt',  -- 'jwt', 'api_token', 'wallet', 'anonymous'

    -- ===== REQUEST DETAILS =====
    operation_name Nullable(String),
    operation_type LowCardinality(String),           -- 'query', 'mutation', 'subscription'
    user_hashes Array(UInt64) DEFAULT [],
    token_hashes Array(UInt64) DEFAULT [],

    -- ===== AGGREGATE COUNTS (for batch writes) =====
    -- For per-request writes: request_count=1, error_count=0 or 1
    -- For aggregate writes: request_count=N, error_count=M
    request_count UInt32 DEFAULT 1,
    error_count UInt32 DEFAULT 0,
    total_duration_ms UInt64 DEFAULT 0,              -- Sum of all request durations in aggregate
    total_complexity UInt32 DEFAULT 0,               -- Sum of all complexity scores in aggregate
    source_region LowCardinality(String) DEFAULT '',
    stream_origin_region LowCardinality(String) DEFAULT '',
    stream_origin_cluster_id LowCardinality(String) DEFAULT '',
    schema_version UInt8 DEFAULT 0
) ENGINE = MergeTree()
PARTITION BY (toYYYYMM(timestamp), tenant_id)
ORDER BY (tenant_id, timestamp)
TTL timestamp + INTERVAL 90 DAY;


-- ============================================================================
-- TENANT ACQUISITION EVENTS (signup attribution)
-- ============================================================================

CREATE TABLE IF NOT EXISTS tenant_acquisition_events (
    timestamp DateTime,
    tenant_id UUID,
    user_id Nullable(UUID),
    signup_channel LowCardinality(String),
    signup_method LowCardinality(String),
    utm_source Nullable(String),
    utm_medium Nullable(String),
    utm_campaign Nullable(String),
    utm_content Nullable(String),
    utm_term Nullable(String),
    http_referer Nullable(String),
    landing_page Nullable(String),
    referral_code Nullable(String),
    is_agent UInt8,
    event_data String,
    source_region LowCardinality(String) DEFAULT '',
    stream_origin_region LowCardinality(String) DEFAULT '',
    stream_origin_cluster_id LowCardinality(String) DEFAULT '',
    schema_version UInt8 DEFAULT 0
) ENGINE = MergeTree()
PARTITION BY toYYYYMM(timestamp)
ORDER BY (signup_channel, timestamp, tenant_id)
TTL timestamp + INTERVAL 730 DAY;

-- ============================================================================
-- SERVICE EVENT AUDIT LOG (service_events topic)
-- ============================================================================

CREATE TABLE IF NOT EXISTS api_events (
    event_id UUID DEFAULT generateUUIDv4(),
    tenant_id UUID,
    event_type LowCardinality(String),
    source LowCardinality(String),
    user_id Nullable(UUID),
    resource_type LowCardinality(String),
    resource_id Nullable(String),
    details String,
    timestamp DateTime64(3),
    cluster_id LowCardinality(String) DEFAULT '',
    source_region LowCardinality(String) DEFAULT '',
    stream_origin_region LowCardinality(String) DEFAULT '',
    stream_origin_cluster_id LowCardinality(String) DEFAULT '',
    schema_version UInt8 DEFAULT 0,

    INDEX idx_event_type event_type TYPE bloom_filter GRANULARITY 4,
    INDEX idx_resource_type resource_type TYPE bloom_filter GRANULARITY 4
) ENGINE = MergeTree()
PARTITION BY toYYYYMM(timestamp)
ORDER BY (tenant_id, event_type, timestamp)
TTL timestamp + INTERVAL 1 YEAR;

-- ============================================================================
-- ORCHESTRATOR VISIBILITY
-- Per-orchestrator state, discovery observations, and outcome telemetry from
-- Livepeer gateways. Multi-IP / multi-vantage observations are first-class —
-- one row per (gateway, orch, resolved_ip) so DNS round-robin / geo-anycast
-- is preserved. See docs/architecture/orchestrator-visibility.md.
-- ============================================================================

-- Orchestrator identity row — the eth-address-keyed fact that "we know about
-- this orch under this cluster owner". Pricing, capabilities, hardware,
-- advertised sub-nodes are NOT here; they're per-instance (an orch eth address
-- typically fronts N load-balanced go-livepeer processes, each with its own
-- config — usually consistent, NOT guaranteed). See
-- orchestrator_instance_state_current.
CREATE TABLE IF NOT EXISTS orchestrator_state_current (
    tenant_id UUID,
    orch_addr String,

    last_seen DateTime,
    metadata JSON,
    updated_at DateTime
) ENGINE = ReplacingMergeTree(updated_at)
ORDER BY (tenant_id, orch_addr);

-- Per-instance state. One row per (cluster owner, orch_addr, resolved_ip).
-- Instances behind a balanced DNS hostname can have independent price,
-- capabilities, and hardware — this table preserves that. The federation
-- map's side panel reads from here for the per-instance config breakdown.
CREATE TABLE IF NOT EXISTS orchestrator_instance_state_current (
    tenant_id UUID,
    orch_addr String,
    resolved_ip String,

    canonical_url String,
    advertised_node_urls Array(String),
    capabilities Array(String),

    price_per_unit Int64,
    pixels_per_unit Int64,
    capability_price_capabilities Array(String),
    capability_price_positions Array(UInt32),
    capability_price_price_per_units Array(Int64),
    capability_price_pixels_per_units Array(Int64),

    hardware String,
    source LowCardinality(String) DEFAULT 'gateway_pool',

    last_seen DateTime,
    metadata JSON,
    updated_at DateTime
) ENGINE = ReplacingMergeTree(updated_at)
ORDER BY (tenant_id, orch_addr, resolved_ip);

-- Per-vantage observation: one row per (cluster owner, gateway, orch_addr,
-- resolved_ip). This is the authoritative "where is this orch reachable from
-- this gateway, and what does that path look like" table. The federation map
-- and side-panel multi-vantage table read from here. NOT collapsed by
-- orch_addr alone — multi-IP and multi-region are intentional dimensions.
CREATE TABLE IF NOT EXISTS orchestrator_vantage_current (
    tenant_id UUID,
    gateway_id LowCardinality(String),
    gateway_region LowCardinality(String),
    orch_addr String,
    resolved_ip String,

    latitude Float64,
    longitude Float64,
    city String,
    country_code LowCardinality(String),
    geo_source LowCardinality(String) DEFAULT 'unknown',
    geo_resolved_at DateTime,

    latest_latency_ms UInt32,
    score Float32,
    dialed_recently UInt8,
    last_seen DateTime,
    updated_at DateTime
) ENGINE = ReplacingMergeTree(updated_at)
ORDER BY (tenant_id, gateway_id, orch_addr, resolved_ip);

-- Raw discovery observations. One row per (gateway attempt, resolved IP).
-- Failures are durable here; reachable=0 + failure_kind capture the why.
CREATE TABLE IF NOT EXISTS orchestrator_discovery_samples (
    timestamp DateTime,
    tenant_id UUID,
    gateway_id LowCardinality(String),
    gateway_region LowCardinality(String),
    orch_addr String,
    orch_url String,
    resolved_ip String,
    advertised_node_url String,

    discovery_latency_ms UInt32,
    reachable UInt8,
    compatible UInt8,
    score Float32,
    dialed UInt8,
    failure_reason String,
    failure_kind LowCardinality(String),

    latitude Float64,
    longitude Float64,
    country_code LowCardinality(String),
    geo_source LowCardinality(String) DEFAULT 'unknown',
    source_region LowCardinality(String) DEFAULT '',
    stream_origin_region LowCardinality(String) DEFAULT '',
    stream_origin_cluster_id LowCardinality(String) DEFAULT '',
    schema_version UInt8 DEFAULT 0,

    INDEX idx_orch_addr orch_addr TYPE bloom_filter GRANULARITY 4
) ENGINE = MergeTree()
PARTITION BY (toYYYYMM(timestamp), tenant_id)
ORDER BY (tenant_id, gateway_id, orch_addr, resolved_ip, timestamp)
TTL timestamp + INTERVAL 30 DAY;

CREATE TABLE IF NOT EXISTS orchestrator_discovery_5m (
    timestamp_5m DateTime,
    tenant_id UUID,
    gateway_id LowCardinality(String),
    gateway_region LowCardinality(String),
    orch_addr String,
    resolved_ip String,
    attempts SimpleAggregateFunction(sum, UInt64),
    successes SimpleAggregateFunction(sum, UInt64),
    failures SimpleAggregateFunction(sum, UInt64),
    latency_sum SimpleAggregateFunction(sum, UInt64),
    latency_count SimpleAggregateFunction(sum, UInt64),
    max_latency SimpleAggregateFunction(max, UInt32)
) ENGINE = AggregatingMergeTree()
PARTITION BY (toYYYYMM(timestamp_5m), tenant_id)
ORDER BY (tenant_id, gateway_id, orch_addr, resolved_ip, timestamp_5m)
TTL timestamp_5m + INTERVAL 180 DAY;

-- Only dialed rows count as attempts: sibling-IP rows from a multi-A-record
-- DNS response are observation context, not extra attempts. Counting them as
-- attempts would underreport success rate by N-1 per cycle for an orch with
-- N IPs.
CREATE MATERIALIZED VIEW IF NOT EXISTS orchestrator_discovery_5m_mv TO orchestrator_discovery_5m AS
SELECT
    toStartOfInterval(timestamp, INTERVAL 5 MINUTE) AS timestamp_5m,
    tenant_id, gateway_id, gateway_region, orch_addr, resolved_ip,
    sumIf(1, dialed = 1) AS attempts,
    sumIf(1, dialed = 1 AND reachable = 1) AS successes,
    sumIf(1, dialed = 1 AND reachable = 0) AS failures,
    sumIf(toUInt64(discovery_latency_ms), dialed = 1 AND reachable = 1) AS latency_sum,
    sumIf(1, dialed = 1 AND reachable = 1) AS latency_count,
    maxIf(discovery_latency_ms, dialed = 1) AS max_latency
FROM orchestrator_discovery_samples
WHERE dialed = 1
GROUP BY timestamp_5m, tenant_id, gateway_id, gateway_region, orch_addr, resolved_ip;

CREATE TABLE IF NOT EXISTS orchestrator_discovery_1h (
    timestamp_1h DateTime,
    tenant_id UUID,
    gateway_id LowCardinality(String),
    gateway_region LowCardinality(String),
    orch_addr String,
    resolved_ip String,
    attempts SimpleAggregateFunction(sum, UInt64),
    successes SimpleAggregateFunction(sum, UInt64),
    failures SimpleAggregateFunction(sum, UInt64),
    latency_sum SimpleAggregateFunction(sum, UInt64),
    latency_count SimpleAggregateFunction(sum, UInt64),
    max_latency SimpleAggregateFunction(max, UInt32)
) ENGINE = AggregatingMergeTree()
PARTITION BY (toYYYYMM(timestamp_1h), tenant_id)
ORDER BY (tenant_id, gateway_id, orch_addr, resolved_ip, timestamp_1h)
TTL timestamp_1h + INTERVAL 1 YEAR;

CREATE MATERIALIZED VIEW IF NOT EXISTS orchestrator_discovery_1h_mv TO orchestrator_discovery_1h AS
SELECT
    toStartOfHour(timestamp) AS timestamp_1h,
    tenant_id, gateway_id, gateway_region, orch_addr, resolved_ip,
    sumIf(1, dialed = 1) AS attempts,
    sumIf(1, dialed = 1 AND reachable = 1) AS successes,
    sumIf(1, dialed = 1 AND reachable = 0) AS failures,
    sumIf(toUInt64(discovery_latency_ms), dialed = 1 AND reachable = 1) AS latency_sum,
    sumIf(1, dialed = 1 AND reachable = 1) AS latency_count,
    maxIf(discovery_latency_ms, dialed = 1) AS max_latency
FROM orchestrator_discovery_samples
WHERE dialed = 1
GROUP BY timestamp_1h, tenant_id, gateway_id, gateway_region, orch_addr, resolved_ip;

-- Per-segment / per-session transcode outcomes. tenant_id is the stream
-- tenant; cluster_owner_tenant_id is the gateway cluster owner.
CREATE TABLE IF NOT EXISTS orchestrator_transcode_outcomes (
    timestamp DateTime,
    tenant_id UUID,
    cluster_owner_tenant_id UUID,
    gateway_id LowCardinality(String),
    gateway_region LowCardinality(String),
    cluster_id LowCardinality(String),
    orch_addr String,
    orch_url String,
    resolved_ip String,

    session_id String,
    manifest_id_hash String,
    seq_no UInt64,
    success UInt8,
    latency_score Float32,
    upload_ms UInt32,
    transcode_ms UInt32,
    overall_ms UInt32,
    pixels UInt64,
    profiles Array(String),
    error_code String,
    error_kind LowCardinality(String),
    source_region LowCardinality(String) DEFAULT '',
    stream_origin_region LowCardinality(String) DEFAULT '',
    stream_origin_cluster_id LowCardinality(String) DEFAULT '',
    schema_version UInt8 DEFAULT 0,

    INDEX idx_orch_addr orch_addr TYPE bloom_filter GRANULARITY 4,
    INDEX idx_session_id session_id TYPE bloom_filter GRANULARITY 4
) ENGINE = MergeTree()
PARTITION BY (toYYYYMM(timestamp), tenant_id)
ORDER BY (tenant_id, orch_addr, resolved_ip, timestamp)
TTL timestamp + INTERVAL 30 DAY;

CREATE TABLE IF NOT EXISTS orchestrator_transcode_hourly (
    timestamp_1h DateTime,
    tenant_id UUID,
    cluster_owner_tenant_id UUID,
    gateway_id LowCardinality(String),
    gateway_region LowCardinality(String),
    orch_addr String,
    resolved_ip String,
    attempts SimpleAggregateFunction(sum, UInt64),
    successes SimpleAggregateFunction(sum, UInt64),
    failures SimpleAggregateFunction(sum, UInt64),
    overall_ms_sum SimpleAggregateFunction(sum, UInt64),
    overall_ms_count SimpleAggregateFunction(sum, UInt64),
    max_overall_ms SimpleAggregateFunction(max, UInt32),
    pixels_sum SimpleAggregateFunction(sum, UInt64)
) ENGINE = AggregatingMergeTree()
PARTITION BY (toYYYYMM(timestamp_1h), tenant_id)
ORDER BY (tenant_id, orch_addr, resolved_ip, gateway_id, timestamp_1h)
TTL timestamp_1h + INTERVAL 2 YEAR;

CREATE MATERIALIZED VIEW IF NOT EXISTS orchestrator_transcode_hourly_mv TO orchestrator_transcode_hourly AS
SELECT
    toStartOfHour(timestamp) AS timestamp_1h,
    tenant_id, cluster_owner_tenant_id, gateway_id, gateway_region, orch_addr, resolved_ip,
    count() AS attempts,
    sumIf(1, success = 1) AS successes,
    sumIf(1, success = 0) AS failures,
    sumIf(toUInt64(overall_ms), success = 1) AS overall_ms_sum,
    sumIf(1, success = 1) AS overall_ms_count,
    maxIf(overall_ms, success = 1) AS max_overall_ms,
    sumIf(pixels, success = 1) AS pixels_sum
FROM orchestrator_transcode_outcomes
GROUP BY timestamp_1h, tenant_id, cluster_owner_tenant_id, gateway_id, gateway_region, orch_addr, resolved_ip;

-- AI outcomes use a separate table because pricing meters differ from
-- transcode outcomes.
CREATE TABLE IF NOT EXISTS orchestrator_ai_outcomes (
    timestamp DateTime,
    tenant_id UUID,
    cluster_owner_tenant_id UUID,
    gateway_id LowCardinality(String),
    gateway_region LowCardinality(String),
    cluster_id LowCardinality(String),
    orch_addr String,
    orch_url String,
    resolved_ip String,

    session_id String,
    pipeline LowCardinality(String),
    model String,
    latency_score Float32,
    price_per_unit Int64,
    latency_ms UInt32,
    success UInt8,
    error_code String,
    error_kind LowCardinality(String),

    INDEX idx_orch_addr orch_addr TYPE bloom_filter GRANULARITY 4,
    INDEX idx_pipeline pipeline TYPE bloom_filter GRANULARITY 4
) ENGINE = MergeTree()
PARTITION BY (toYYYYMM(timestamp), tenant_id)
ORDER BY (tenant_id, orch_addr, resolved_ip, timestamp)
TTL timestamp + INTERVAL 30 DAY;

CREATE TABLE IF NOT EXISTS orchestrator_ai_hourly (
    timestamp_1h DateTime,
    tenant_id UUID,
    cluster_owner_tenant_id UUID,
    gateway_id LowCardinality(String),
    gateway_region LowCardinality(String),
    orch_addr String,
    resolved_ip String,
    attempts SimpleAggregateFunction(sum, UInt64),
    successes SimpleAggregateFunction(sum, UInt64),
    failures SimpleAggregateFunction(sum, UInt64),
    latency_ms_sum SimpleAggregateFunction(sum, UInt64),
    latency_ms_count SimpleAggregateFunction(sum, UInt64),
    max_latency_ms SimpleAggregateFunction(max, UInt32)
) ENGINE = AggregatingMergeTree()
PARTITION BY (toYYYYMM(timestamp_1h), tenant_id)
ORDER BY (tenant_id, orch_addr, resolved_ip, gateway_id, timestamp_1h)
TTL timestamp_1h + INTERVAL 2 YEAR;

CREATE MATERIALIZED VIEW IF NOT EXISTS orchestrator_ai_hourly_mv TO orchestrator_ai_hourly AS
SELECT
    toStartOfHour(timestamp) AS timestamp_1h,
    tenant_id, cluster_owner_tenant_id, gateway_id, gateway_region, orch_addr, resolved_ip,
    count() AS attempts,
    sumIf(1, success = 1) AS successes,
    sumIf(1, success = 0) AS failures,
    sumIf(toUInt64(latency_ms), success = 1) AS latency_ms_sum,
    sumIf(1, success = 1) AS latency_ms_count,
    maxIf(latency_ms, success = 1) AS max_latency_ms
FROM orchestrator_ai_outcomes
GROUP BY timestamp_1h, tenant_id, cluster_owner_tenant_id, gateway_id, gateway_region, orch_addr, resolved_ip;

-- ============================================================================
-- FINALIZED FACT TABLES + 5-MIN CANONICAL LEDGERS
-- ----------------------------------------------------------------------------
-- Projection model — applies to every *_final and 5-min ledger table below.
--
-- 1. Physically append-only MergeTree. Each parser/rebuilder pass writes a
--    new row; multiple projection rows per logical fact coexist on disk.
-- 2. Readers materialize the logical fact via min/argMax on
--    projection_version_ms. Each table has a *_v view next to it that wraps
--    the GROUP BY natural_key + min/argMax read pattern.
-- 3. billable_at_ms is DERIVED, not stored. It is min(projection_version_ms)
--    over the projection rows of a logical fact. the billing cursor walks
--    this value.
-- 4. ORDER BY puts (tenant_id, projection_version_ms) first so the hot
--    billing cursor's WHERE projection_version_ms ∈ [start, end) hits the
--    sort index after per-tenant pruning. Natural-key columns trail.
-- 5. PARTITION BY toYYYYMM(toDateTime(projection_version_ms / 1000)) — the
--    billing cursor's time predicate prunes to overlapping calendar months.
-- 6. Supports pure replay (same logical fact, repeated projections)
--    through the normal parser path. Material billing corrections — a
--    parser pass that changes a rated field's value after the row has
--    been cursored past — are emitted as additive Purser adjustments
--    from projection_divergences, never by mutating the original row.
--
-- See docs/architecture/meter-contracts.md for the full contract.
-- ============================================================================

-- viewer_sessions_final: one logical row per Mist USER_END accepted by
-- Periscope. Natural key (tenant_id, node_id, session_id). Append-only
-- projections; viewer_sessions_final_v materializes the logical fact.
--
-- Source proto: pkg/proto/ipc.proto ViewerDisconnectTrigger. Multi-stream
-- sessions land with parallel (name, seconds) tuples in stream_times /
-- connector_times / host_times — MistServer src/session.cpp already emits
-- these arrays for sessions that touched multiple streams/connectors/hosts.
CREATE TABLE IF NOT EXISTS viewer_sessions_final (
    -- Natural key
    tenant_id UUID,
    node_id LowCardinality(String),
    session_id String,

    -- Stable cross-pipeline identity
    source_event_id String,            -- sha256(node_id || NUL || trigger_type || NUL || payload_raw); same as raw_mist_triggers

    -- Enrichment
    cluster_id LowCardinality(String) DEFAULT '',
    stream_id UUID DEFAULT toUUIDOrZero(''),
    stream_name String DEFAULT '',
    connector LowCardinality(String) DEFAULT '',
    host String DEFAULT '',
    country_code FixedString(2) DEFAULT '\0\0',
    city LowCardinality(String) DEFAULT '',
    latitude Float64 DEFAULT 0,
    longitude Float64 DEFAULT 0,
    tags String DEFAULT '',

    -- Counters
    duration_seconds UInt32 DEFAULT 0,
    uploaded_bytes UInt64 DEFAULT 0,
    downloaded_bytes UInt64 DEFAULT 0,
    seconds_connected UInt64 DEFAULT 0, -- enrichment override of duration when present

    -- Time model (see projection-model note at top of section)
    source_started_at_ms Int64,
    source_ended_at_ms Int64,
    edge_received_at_ms Int64,         -- Helmsman WAL accept; audit only
    projection_version_ms Int64,       -- parser-pass wall clock; cursor uses min() of this

    closed_reason LowCardinality(String) DEFAULT 'final', -- 'final' for USER_END; anomalous closes live in viewer_sessions_anomalous

    -- USER_END breakdown arrays from MistServer src/session.cpp. Multi-stream
    -- sessions land here as parallel arrays (name + seconds) so per-stream /
    -- per-host / per-connector attribution can split the session correctly
    -- instead of dumping all minutes on the first entry of the comma-joined
    -- summary string. Empty for single-element sessions.
    stream_times Array(Tuple(name LowCardinality(String), seconds UInt32)) DEFAULT [],
    connector_times Array(Tuple(name LowCardinality(String), seconds UInt32)) DEFAULT [],
    host_times Array(Tuple(name LowCardinality(String), seconds UInt32)) DEFAULT [],

    payload_raw String CODEC(ZSTD(3))
) ENGINE = MergeTree()
PARTITION BY toYYYYMM(toDateTime(projection_version_ms / 1000))
ORDER BY (tenant_id, projection_version_ms, node_id, session_id)
TTL toDateTime(projection_version_ms / 1000) + INTERVAL 730 DAY;

CREATE VIEW IF NOT EXISTS viewer_sessions_final_v AS
SELECT
    tenant_id, node_id, session_id,
    min(projection_version_ms) AS billable_at_ms,
    argMax(source_event_id,       projection_version_ms) AS source_event_id,
    argMax(cluster_id,            projection_version_ms) AS cluster_id,
    argMax(stream_id,             projection_version_ms) AS stream_id,
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
    -- USER_END breakdown arrays from MistServer src/session.cpp. Multi-stream
    -- sessions land here as parallel (name, seconds) tuples so per-stream /
    -- per-connector / per-host attribution can split a single session across
    -- the elements it touched. Empty array for single-element sessions.
    argMax(stream_times,          projection_version_ms) AS stream_times,
    argMax(connector_times,       projection_version_ms) AS connector_times,
    argMax(host_times,            projection_version_ms) AS host_times
FROM viewer_sessions_final
GROUP BY tenant_id, node_id, session_id;

-- stream_sessions_final: one logical row per Mist STREAM_END accepted by
-- Periscope. Source proto: StreamEndTrigger.
CREATE TABLE IF NOT EXISTS stream_sessions_final (
    tenant_id UUID,
    node_id LowCardinality(String),
    stream_id UUID,                    -- enriched by Foghorn; natural-key column
    source_event_id String,

    cluster_id LowCardinality(String) DEFAULT '',
    stream_name String DEFAULT '',

    -- Stream-end counters
    downloaded_bytes Int64 DEFAULT 0,
    uploaded_bytes Int64 DEFAULT 0,
    total_viewers Int64 DEFAULT 0,
    total_inputs Int64 DEFAULT 0,
    total_outputs Int64 DEFAULT 0,
    viewer_seconds Int64 DEFAULT 0,

    source_started_at_ms Int64,
    source_ended_at_ms Int64,
    edge_received_at_ms Int64,
    projection_version_ms Int64,

    closed_reason LowCardinality(String) DEFAULT 'final',
    payload_raw String CODEC(ZSTD(3))
) ENGINE = MergeTree()
PARTITION BY toYYYYMM(toDateTime(projection_version_ms / 1000))
ORDER BY (tenant_id, projection_version_ms, node_id, stream_id, source_event_id)
TTL toDateTime(projection_version_ms / 1000) + INTERVAL 730 DAY;

CREATE VIEW IF NOT EXISTS stream_sessions_final_v AS
SELECT
    tenant_id, node_id, stream_id, source_event_id,
    min(projection_version_ms) AS billable_at_ms,
    argMax(cluster_id,            projection_version_ms) AS cluster_id,
    argMax(stream_name,           projection_version_ms) AS stream_name,
    argMax(downloaded_bytes,      projection_version_ms) AS downloaded_bytes,
    argMax(uploaded_bytes,        projection_version_ms) AS uploaded_bytes,
    argMax(total_viewers,         projection_version_ms) AS total_viewers,
    argMax(total_inputs,          projection_version_ms) AS total_inputs,
    argMax(total_outputs,         projection_version_ms) AS total_outputs,
    argMax(viewer_seconds,        projection_version_ms) AS viewer_seconds,
    argMax(source_started_at_ms,  projection_version_ms) AS source_started_at_ms,
    argMax(source_ended_at_ms,    projection_version_ms) AS source_ended_at_ms,
    argMax(edge_received_at_ms,   projection_version_ms) AS edge_received_at_ms,
    max(projection_version_ms) AS latest_projection_version_ms,
    argMax(closed_reason,         projection_version_ms) AS closed_reason
FROM stream_sessions_final
GROUP BY tenant_id, node_id, stream_id, source_event_id;

-- processing_segments_final: one logical row per LIVEPEER_SEGMENT_COMPLETE
-- or PROCESS_AV_VIRTUAL_SEGMENT_COMPLETE event. Explicit natural key — the
-- segment_dedupe_key column is a debug-convenience hash, NOT the dedupe
-- identity. Future debugging should not need to reverse-engineer a hash.
--
CREATE TABLE IF NOT EXISTS processing_segments_final (
    tenant_id UUID,
    node_id LowCardinality(String),
    stream_id UUID,
    process_type LowCardinality(String),    -- 'Livepeer' | 'AV' | 'FFmpeg'
    output_codec LowCardinality(String),
    track_type LowCardinality(String),      -- 'audio' | 'video'
    segment_number Int32,

    -- Identity. source_event_id = sha256(node_id || NUL || trigger_type || NUL || payload_raw)
    -- from raw_mist_triggers; unique per Mist trigger and therefore per
    -- logical segment for both Livepeer and AV-virtual-segment events. AV
    -- triggers carry no real segment_number, so dedupe MUST key on
    -- source_event_id. segment_dedupe_key stays as a debug-convenience
    -- compact hash; segment_number stays informational.
    source_event_id String,
    segment_dedupe_key UInt64 DEFAULT cityHash64(source_event_id),

    -- Common
    cluster_id LowCardinality(String) DEFAULT '',
    stream_name String DEFAULT '',
    input_codec LowCardinality(String) DEFAULT '',
    media_seconds Float64 DEFAULT 0,

    -- Livepeer-specific
    width Int32 DEFAULT 0,
    height Int32 DEFAULT 0,
    rendition_count Int32 DEFAULT 0,
    input_bytes Int64 DEFAULT 0,
    output_bytes_total Int64 DEFAULT 0,
    turnaround_ms Int64 DEFAULT 0,
    speed_factor Float64 DEFAULT 0,
    livepeer_session_id String DEFAULT '',

    -- MistProcAV-specific
    input_frames Int64 DEFAULT 0,
    output_frames Int64 DEFAULT 0,
    input_frames_delta Int64 DEFAULT 0,
    output_frames_delta Int64 DEFAULT 0,
    input_bytes_delta Int64 DEFAULT 0,
    output_bytes_delta Int64 DEFAULT 0,
    rtf_in Float64 DEFAULT 0,
    rtf_out Float64 DEFAULT 0,
    is_final UInt8 DEFAULT 0,

    source_started_at_ms Int64,
    source_ended_at_ms Int64,
    edge_received_at_ms Int64,
    projection_version_ms Int64,

    payload_raw String CODEC(ZSTD(3))
) ENGINE = MergeTree()
PARTITION BY toYYYYMM(toDateTime(projection_version_ms / 1000))
ORDER BY (tenant_id, projection_version_ms, node_id, stream_id, source_event_id, process_type, output_codec, track_type)
TTL toDateTime(projection_version_ms / 1000) + INTERVAL 730 DAY;

CREATE VIEW IF NOT EXISTS processing_segments_final_v AS
SELECT
    tenant_id, node_id, stream_id,
    argMax(process_type, projection_version_ms) AS process_type,
    argMax(output_codec, projection_version_ms) AS output_codec,
    argMax(track_type, projection_version_ms) AS track_type,
    source_event_id,
    min(projection_version_ms) AS billable_at_ms,
    argMax(segment_number,        projection_version_ms) AS segment_number,
    argMax(segment_dedupe_key,    projection_version_ms) AS segment_dedupe_key,
    argMax(cluster_id,            projection_version_ms) AS cluster_id,
    argMax(stream_name,           projection_version_ms) AS stream_name,
    argMax(input_codec,           projection_version_ms) AS input_codec,
    argMax(media_seconds,         projection_version_ms) AS media_seconds,
    argMax(width,                 projection_version_ms) AS width,
    argMax(height,                projection_version_ms) AS height,
    argMax(rendition_count,       projection_version_ms) AS rendition_count,
    argMax(input_bytes,           projection_version_ms) AS input_bytes,
    argMax(output_bytes_total,    projection_version_ms) AS output_bytes_total,
    argMax(turnaround_ms,         projection_version_ms) AS turnaround_ms,
    argMax(speed_factor,          projection_version_ms) AS speed_factor,
    argMax(livepeer_session_id,   projection_version_ms) AS livepeer_session_id,
    argMax(input_frames,          projection_version_ms) AS input_frames,
    argMax(output_frames,         projection_version_ms) AS output_frames,
    argMax(input_frames_delta,    projection_version_ms) AS input_frames_delta,
    argMax(output_frames_delta,   projection_version_ms) AS output_frames_delta,
    argMax(input_bytes_delta,     projection_version_ms) AS input_bytes_delta,
    argMax(output_bytes_delta,    projection_version_ms) AS output_bytes_delta,
    argMax(rtf_in,                projection_version_ms) AS rtf_in,
    argMax(rtf_out,               projection_version_ms) AS rtf_out,
    argMax(is_final,              projection_version_ms) AS is_final,
    argMax(source_started_at_ms,  projection_version_ms) AS source_started_at_ms,
    argMax(source_ended_at_ms,    projection_version_ms) AS source_ended_at_ms,
    argMax(edge_received_at_ms,   projection_version_ms) AS edge_received_at_ms,
    max(projection_version_ms) AS latest_projection_version_ms
FROM processing_segments_final
GROUP BY tenant_id, node_id, stream_id, source_event_id;

-- ============================================================================
-- ANOMALY TABLES — stale closes and operator-visible non-billable facts.
-- ----------------------------------------------------------------------------
-- Physically separate from *_final so rated billing reads cannot reach them.
-- The stale-close worker in api_sidecar writes here when a session/stream
-- lingers past stale_close_timeout without a real USER_END/STREAM_END.
-- Operational meters (e.g. stale_session_minutes) read from these tables.
-- ============================================================================

CREATE TABLE IF NOT EXISTS viewer_sessions_anomalous (
    tenant_id UUID,
    node_id LowCardinality(String),
    session_id String,

    cluster_id LowCardinality(String) DEFAULT '',
    stream_id UUID DEFAULT toUUIDOrZero(''),
    stream_name String DEFAULT '',

    estimated_duration_seconds UInt32 DEFAULT 0, -- helmsman best-effort guess
    observed_first_at_ms Int64 DEFAULT 0,
    observed_last_at_ms Int64 DEFAULT 0,
    closed_at_ms Int64,
    closed_reason LowCardinality(String) DEFAULT 'stale', -- 'stale' | 'orphan' | 'parser_error'
    projection_version_ms Int64,

    notes String DEFAULT ''
) ENGINE = MergeTree()
PARTITION BY toYYYYMM(toDateTime(closed_at_ms / 1000))
ORDER BY (tenant_id, closed_at_ms, node_id, session_id)
TTL toDateTime(closed_at_ms / 1000) + INTERVAL 365 DAY;

CREATE TABLE IF NOT EXISTS stream_sessions_anomalous (
    tenant_id UUID,
    node_id LowCardinality(String),
    stream_id UUID,

    cluster_id LowCardinality(String) DEFAULT '',
    stream_name String DEFAULT '',

    estimated_duration_seconds UInt32 DEFAULT 0,
    observed_first_at_ms Int64 DEFAULT 0,
    observed_last_at_ms Int64 DEFAULT 0,
    closed_at_ms Int64,
    closed_reason LowCardinality(String) DEFAULT 'stale',
    projection_version_ms Int64,

    notes String DEFAULT ''
) ENGINE = MergeTree()
PARTITION BY toYYYYMM(toDateTime(closed_at_ms / 1000))
ORDER BY (tenant_id, closed_at_ms, node_id, stream_id)
TTL toDateTime(closed_at_ms / 1000) + INTERVAL 365 DAY;

-- ============================================================================
-- 5-MIN CANONICAL LEDGERS — analytics-side projection of final facts.
-- ----------------------------------------------------------------------------
-- Same projection model as the *_final tables. Each ledger row is a
-- 5-minute-bucketed aggregate computed by a rebuild worker; reruns append
-- new projection rows. The *_v view materializes the logical row.
--
-- The schema and rebuild workers ship together;
-- see api_analytics_ingest for the rebuilders.
-- handles dashboards.
-- ============================================================================

CREATE TABLE IF NOT EXISTS ledger_rebuild_cursors (
    ledger_name LowCardinality(String),
    last_processed_projection_ms Int64,
    updated_at_ms Int64
) ENGINE = ReplacingMergeTree(updated_at_ms)
ORDER BY ledger_name
TTL toDateTime(updated_at_ms / 1000) + INTERVAL 365 DAY;

CREATE TABLE IF NOT EXISTS viewer_usage_5m (
    -- Natural key
    window_start DateTime,             -- toStartOfFiveMinute boundary
    tenant_id UUID,
    cluster_id LowCardinality(String) DEFAULT '',
    stream_id UUID DEFAULT toUUIDOrZero(''),
    node_id LowCardinality(String),
    session_id String,

    -- Allocated counters: overlap of session's [source_started_at_ms,
    -- source_ended_at_ms) with this window, in seconds and bytes.
    seconds_observed UInt32 DEFAULT 0,
    up_bytes_observed UInt64 DEFAULT 0,
    down_bytes_observed UInt64 DEFAULT 0,

    -- Traceback
    source_event_id String,             -- USER_END source_event_id that produced this row

    projection_version_ms Int64
) ENGINE = MergeTree()
PARTITION BY toYYYYMM(toDateTime(projection_version_ms / 1000))
ORDER BY (tenant_id, projection_version_ms, cluster_id, stream_id, node_id, session_id, window_start)
TTL toDateTime(projection_version_ms / 1000) + INTERVAL 90 DAY;

CREATE VIEW IF NOT EXISTS viewer_usage_5m_v AS
SELECT
    window_start, tenant_id, cluster_id, stream_id, node_id, session_id,
    billable_at_ms, seconds_observed, up_bytes_observed, down_bytes_observed,
    source_event_id, latest_projection_version_ms
FROM (
    SELECT
        window_start, tenant_id, cluster_id, stream_id, node_id, session_id,
        min(projection_version_ms) AS billable_at_ms,
        argMax(seconds_observed,    projection_version_ms) AS seconds_observed,
        argMax(up_bytes_observed,   projection_version_ms) AS up_bytes_observed,
        argMax(down_bytes_observed, projection_version_ms) AS down_bytes_observed,
        argMax(source_event_id,     projection_version_ms) AS source_event_id,
        max(projection_version_ms) AS latest_projection_version_ms
    FROM viewer_usage_5m
    GROUP BY window_start, tenant_id, cluster_id, stream_id, node_id, session_id
)
WHERE seconds_observed > 0 OR up_bytes_observed > 0 OR down_bytes_observed > 0;

CREATE TABLE IF NOT EXISTS stream_runtime_5m (
    window_start DateTime,
    tenant_id UUID,
    cluster_id LowCardinality(String) DEFAULT '',
    stream_id UUID,
    source_event_id String,             -- STREAM_END source_event_id

    active_seconds UInt32 DEFAULT 0,
    peak_viewers UInt32 DEFAULT 0,

    projection_version_ms Int64
) ENGINE = MergeTree()
PARTITION BY toYYYYMM(toDateTime(projection_version_ms / 1000))
ORDER BY (tenant_id, projection_version_ms, cluster_id, stream_id, source_event_id, window_start)
TTL toDateTime(projection_version_ms / 1000) + INTERVAL 90 DAY;

CREATE VIEW IF NOT EXISTS stream_runtime_5m_v AS
SELECT
    window_start, tenant_id, cluster_id, stream_id, source_event_id,
    min(projection_version_ms) AS billable_at_ms,
    argMax(active_seconds,  projection_version_ms) AS active_seconds,
    argMax(peak_viewers,    projection_version_ms) AS peak_viewers,
    max(projection_version_ms) AS latest_projection_version_ms
FROM stream_runtime_5m
GROUP BY window_start, tenant_id, cluster_id, stream_id, source_event_id;

-- storage_gb_seconds_5m: time-weighted integration of storage_snapshots
-- per (tenant, cluster, storage_scope, provider attribution) into
-- 5-minute windows. Storage snapshots are themselves the immutable
-- facts — no separate *_final table. The rebuild worker writes here
-- directly. Provider attribution columns enable marketplace settlement
-- routing without a second pass over the ledger.
CREATE TABLE IF NOT EXISTS storage_gb_seconds_5m (
    window_start DateTime,
    tenant_id UUID,                                        -- usage tenant
    cluster_id LowCardinality(String) DEFAULT '',
    storage_scope LowCardinality(String) DEFAULT 'hot',    -- 'hot' | 'cold'
    storage_provider_tenant_id LowCardinality(String) DEFAULT '',
    storage_provider_cluster_id LowCardinality(String) DEFAULT '',
    storage_backend LowCardinality(String) DEFAULT 'unknown',

    gb_seconds Float64 DEFAULT 0,       -- ∫(total_bytes/GiB) dt across the 5-min window
    file_count UInt64 DEFAULT 0,        -- argMax of snapshots within the window

    projection_version_ms Int64
) ENGINE = MergeTree()
PARTITION BY toYYYYMM(toDateTime(projection_version_ms / 1000))
ORDER BY (tenant_id, projection_version_ms, cluster_id, storage_provider_tenant_id, storage_provider_cluster_id, storage_scope, storage_backend, window_start)
TTL toDateTime(projection_version_ms / 1000) + INTERVAL 90 DAY;

CREATE VIEW IF NOT EXISTS storage_gb_seconds_5m_v AS
SELECT
    window_start, tenant_id, cluster_id, storage_scope,
    storage_provider_tenant_id, storage_provider_cluster_id, storage_backend,
    min(projection_version_ms) AS billable_at_ms,
    argMax(gb_seconds, projection_version_ms) AS gb_seconds,
    argMax(file_count, projection_version_ms) AS file_count,
    max(projection_version_ms) AS latest_projection_version_ms
FROM storage_gb_seconds_5m
GROUP BY window_start, tenant_id, cluster_id, storage_scope, storage_provider_tenant_id, storage_provider_cluster_id, storage_backend;

-- processing_5m: 5-min bucketed aggregation of processing_segments_final.
-- source_event_id is the canonical dedupe identity. Process, codec, track,
-- and cluster are materialized fields so replayed attribution changes
-- replace the prior shape in processing_5m_v.
CREATE TABLE IF NOT EXISTS processing_5m (
    window_start DateTime,
    tenant_id UUID,
    cluster_id LowCardinality(String) DEFAULT '',
    stream_id UUID DEFAULT toUUIDOrZero(''),
    process_type LowCardinality(String),
    output_codec LowCardinality(String),
    track_type LowCardinality(String),
    source_event_id String,

    media_seconds Float64 DEFAULT 0,

    projection_version_ms Int64
) ENGINE = MergeTree()
PARTITION BY toYYYYMM(toDateTime(projection_version_ms / 1000))
ORDER BY (tenant_id, projection_version_ms, cluster_id, stream_id, process_type, output_codec, track_type, source_event_id, window_start)
TTL toDateTime(projection_version_ms / 1000) + INTERVAL 90 DAY;

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

-- api_usage_5m: per-window scalar aggregates. Per-window unique-user/token
-- counts are stored as exact UInt64 estimates from the rebuild worker's
-- uniqCombined() over api_requests for that window stored as
-- AggregateFunction(uniqCombined, UInt64) state columns so dashboards
-- can merge across windows via uniqCombinedMerge. argMax on the state
-- column picks the latest projection's state without re-finalizing it.
CREATE TABLE IF NOT EXISTS api_usage_5m (
    window_start DateTime,
    tenant_id UUID,
    auth_type LowCardinality(String),
    operation_type LowCardinality(String),
    operation_name LowCardinality(String) DEFAULT '',
    requests UInt64 DEFAULT 0,
    errors UInt64 DEFAULT 0,
    duration_ms UInt64 DEFAULT 0,
    complexity UInt64 DEFAULT 0,
    unique_users_state AggregateFunction(uniqCombined, UInt64),
    unique_tokens_state AggregateFunction(uniqCombined, UInt64),
    projection_version_ms Int64
) ENGINE = MergeTree()
PARTITION BY toYYYYMM(toDateTime(projection_version_ms / 1000))
ORDER BY (tenant_id, projection_version_ms, auth_type, operation_type, operation_name, window_start)
TTL toDateTime(projection_version_ms / 1000) + INTERVAL 365 DAY;

-- unique_*_state columns are AggregateFunction(uniqCombined, …) — not
-- argMaxState — so we pick the latest projection's state with argMax(value,
-- key), which works on any column type including aggregate states.
CREATE VIEW IF NOT EXISTS api_usage_5m_v AS
SELECT
    window_start, tenant_id, auth_type, operation_type, operation_name,
    min(projection_version_ms) AS billable_at_ms,
    argMax(requests,            projection_version_ms) AS requests,
    argMax(errors,              projection_version_ms) AS errors,
    argMax(duration_ms,         projection_version_ms) AS duration_ms,
    argMax(complexity,          projection_version_ms) AS complexity,
    argMax(unique_users_state,  projection_version_ms) AS unique_users_state,
    argMax(unique_tokens_state, projection_version_ms) AS unique_tokens_state,
    max(projection_version_ms) AS latest_projection_version_ms
FROM api_usage_5m
GROUP BY window_start, tenant_id, auth_type, operation_type, operation_name;

-- ============================================================================
-- OPERATIONAL GUARDRAIL — projection divergence audit
-- ----------------------------------------------------------------------------
-- This guardrail runs on every finalized rated fact projection
-- (viewer_sessions_final, stream_sessions_final, processing_segments_final).
-- If a rated field's new value differs beyond a per-meter epsilon, the
-- parser STILL writes the projection row (append invariant) but ALSO bumps
-- a Prometheus counter and writes an audit row here.
--
-- This table is the source feed for explicit additive corrections outside
-- the normal append-only parser path. The billing summarizer converts
-- supported divergence types into Purser usage_adjustments.
-- ============================================================================

CREATE TABLE IF NOT EXISTS projection_divergences (
    observed_at_ms Int64,
    table_name LowCardinality(String),
    meter LowCardinality(String),
    field LowCardinality(String),
    natural_key_json String CODEC(ZSTD(3)),
    prior_value_json String CODEC(ZSTD(3)),
    new_value_json String CODEC(ZSTD(3)),
    source_event_id String
) ENGINE = MergeTree()
PARTITION BY toYYYYMM(toDateTime(observed_at_ms / 1000))
ORDER BY (table_name, observed_at_ms, source_event_id)
TTL toDateTime(observed_at_ms / 1000) + INTERVAL 90 DAY;
-- Canonical-meter rollup tier. Each rollup is a *_store ReplacingMergeTree
-- that the refreshable MV APPENDs into, plus a dedup VIEW under the
-- public name that does argMax(col, refresh_version_ms) GROUP BY
-- natural_key. Resolvers read the public-name VIEW; nothing reads
-- *_store directly. Each MV scans recent projection-version changes and
-- rewrites the affected source-time buckets into the append store.
--
-- Migration from earlier MV/table shapes is handled by
-- pkg/database/sql/clickhouse/migrations/periscope/v0.2.64/contract/001.
-- Greenfield init via this file installs the canonical shape directly.

CREATE TABLE IF NOT EXISTS tenant_usage_5m_store (
    window_start DateTime,
    tenant_id UUID,
    cluster_id LowCardinality(String) DEFAULT '',
    seconds_observed UInt64 DEFAULT 0,
    up_bytes UInt64 DEFAULT 0,
    down_bytes UInt64 DEFAULT 0,
    unique_sessions_state AggregateFunction(uniqCombined, String),
    unique_streams_state AggregateFunction(uniqCombined, UUID),
    refresh_version_ms DateTime64(3)
) ENGINE = ReplacingMergeTree(refresh_version_ms)
PARTITION BY toYYYYMM(window_start)
ORDER BY (tenant_id, window_start, cluster_id)
TTL window_start + INTERVAL 30 DAY;

CREATE MATERIALIZED VIEW IF NOT EXISTS tenant_usage_5m_mv
REFRESH EVERY 1 MINUTE APPEND TO tenant_usage_5m_store AS
SELECT
    window_start,
    tenant_id,
    cluster_id,
    toUInt64(sum(seconds_observed))    AS seconds_observed,
    toUInt64(sum(up_bytes_observed))   AS up_bytes,
    toUInt64(sum(down_bytes_observed)) AS down_bytes,
    uniqCombinedState(session_id)      AS unique_sessions_state,
    uniqCombinedState(stream_id)       AS unique_streams_state,
    now64(3)                           AS refresh_version_ms
FROM viewer_usage_5m_v
WHERE (window_start, tenant_id, cluster_id) IN (
    SELECT DISTINCT window_start, tenant_id, cluster_id
    FROM viewer_usage_5m_v
    WHERE latest_projection_version_ms >= toUnixTimestamp64Milli(now64(3) - INTERVAL 2 HOUR)
)
GROUP BY window_start, tenant_id, cluster_id;

CREATE VIEW IF NOT EXISTS tenant_usage_5m AS
SELECT
    window_start, tenant_id, cluster_id,
    argMax(seconds_observed,      refresh_version_ms) AS seconds_observed,
    argMax(up_bytes,              refresh_version_ms) AS up_bytes,
    argMax(down_bytes,            refresh_version_ms) AS down_bytes,
    argMax(unique_sessions_state, refresh_version_ms) AS unique_sessions_state,
    argMax(unique_streams_state,  refresh_version_ms) AS unique_streams_state,
    max(refresh_version_ms) AS latest_refresh_version_ms
FROM tenant_usage_5m_store
GROUP BY window_start, tenant_id, cluster_id;

-- ----------------------------------------------------------------------------
-- Hourly tier (24h/7d dashboards)
-- ----------------------------------------------------------------------------

CREATE TABLE IF NOT EXISTS tenant_usage_hourly_store (
    hour DateTime,
    tenant_id UUID,
    cluster_id LowCardinality(String) DEFAULT '',
    seconds_observed UInt64 DEFAULT 0,
    up_bytes UInt64 DEFAULT 0,
    down_bytes UInt64 DEFAULT 0,
    unique_sessions_state AggregateFunction(uniqCombined, String),
    unique_streams_state AggregateFunction(uniqCombined, UUID),
    refresh_version_ms DateTime64(3)
) ENGINE = ReplacingMergeTree(refresh_version_ms)
PARTITION BY toYYYYMM(hour)
ORDER BY (tenant_id, hour, cluster_id)
TTL hour + INTERVAL 730 DAY;

CREATE MATERIALIZED VIEW IF NOT EXISTS tenant_usage_hourly_mv
REFRESH EVERY 5 MINUTE APPEND TO tenant_usage_hourly_store AS
SELECT
    toStartOfHour(window_start) AS hour,
    tenant_id,
    cluster_id,
    toUInt64(sum(seconds_observed))    AS seconds_observed,
    toUInt64(sum(up_bytes_observed))   AS up_bytes,
    toUInt64(sum(down_bytes_observed)) AS down_bytes,
    uniqCombinedState(session_id)      AS unique_sessions_state,
    uniqCombinedState(stream_id)       AS unique_streams_state,
    now64(3)                           AS refresh_version_ms
FROM viewer_usage_5m_v
WHERE (hour, tenant_id, cluster_id) IN (
    SELECT DISTINCT toStartOfHour(window_start) AS hour, tenant_id, cluster_id
    FROM viewer_usage_5m_v
    WHERE latest_projection_version_ms >= toUnixTimestamp64Milli(now64(3) - INTERVAL 2 DAY)
)
GROUP BY hour, tenant_id, cluster_id;

CREATE VIEW IF NOT EXISTS tenant_usage_hourly AS
SELECT
    hour, tenant_id, cluster_id,
    argMax(seconds_observed,      refresh_version_ms) AS seconds_observed,
    argMax(up_bytes,              refresh_version_ms) AS up_bytes,
    argMax(down_bytes,            refresh_version_ms) AS down_bytes,
    argMax(unique_sessions_state, refresh_version_ms) AS unique_sessions_state,
    argMax(unique_streams_state,  refresh_version_ms) AS unique_streams_state,
    max(refresh_version_ms) AS latest_refresh_version_ms
FROM tenant_usage_hourly_store
GROUP BY hour, tenant_id, cluster_id;

CREATE TABLE IF NOT EXISTS viewer_hours_hourly_store (
    hour DateTime,
    tenant_id UUID,
    cluster_id LowCardinality(String) DEFAULT '',
    stream_id UUID DEFAULT toUUIDOrZero(''),
    country_code FixedString(2) DEFAULT '\0\0',
    total_session_seconds UInt64 DEFAULT 0,
    total_bytes UInt64 DEFAULT 0,
    egress_bytes UInt64 DEFAULT 0,
    unique_viewers_state AggregateFunction(uniqCombined, String),
    refresh_version_ms DateTime64(3)
) ENGINE = ReplacingMergeTree(refresh_version_ms)
PARTITION BY toYYYYMM(hour)
ORDER BY (tenant_id, hour, cluster_id, stream_id, country_code)
TTL hour + INTERVAL 365 DAY;

CREATE MATERIALIZED VIEW IF NOT EXISTS viewer_hours_hourly_mv
REFRESH EVERY 5 MINUTE APPEND TO viewer_hours_hourly_store AS
SELECT
    toStartOfHour(u.window_start) AS hour,
    u.tenant_id,
    u.cluster_id,
    u.stream_id,
    s.country_code,
    toUInt64(sum(u.seconds_observed))                          AS total_session_seconds,
    toUInt64(sum(u.up_bytes_observed + u.down_bytes_observed)) AS total_bytes,
    toUInt64(sum(u.down_bytes_observed))                        AS egress_bytes,
    uniqCombinedState(if(s.host != '', s.host, concat(toString(u.node_id), '|', u.session_id))) AS unique_viewers_state,
    now64(3)                                                   AS refresh_version_ms
FROM viewer_usage_5m_v u
LEFT JOIN viewer_sessions_final_v s USING (tenant_id, node_id, session_id)
WHERE (hour, u.tenant_id, u.cluster_id, u.stream_id, s.country_code) IN (
    SELECT DISTINCT toStartOfHour(u.window_start) AS hour, u.tenant_id, u.cluster_id, u.stream_id, s.country_code
    FROM viewer_usage_5m_v u
    LEFT JOIN viewer_sessions_final_v s USING (tenant_id, node_id, session_id)
    WHERE u.latest_projection_version_ms >= toUnixTimestamp64Milli(now64(3) - INTERVAL 2 DAY)
)
GROUP BY hour, u.tenant_id, u.cluster_id, u.stream_id, s.country_code;

CREATE VIEW IF NOT EXISTS viewer_hours_hourly AS
SELECT
    hour, tenant_id, cluster_id, stream_id, country_code,
    argMax(total_session_seconds, refresh_version_ms) AS total_session_seconds,
    argMax(total_bytes,           refresh_version_ms) AS total_bytes,
    argMax(egress_bytes,          refresh_version_ms) AS egress_bytes,
    argMax(unique_viewers_state,  refresh_version_ms) AS unique_viewers_state,
    max(refresh_version_ms) AS latest_refresh_version_ms
FROM viewer_hours_hourly_store
GROUP BY hour, tenant_id, cluster_id, stream_id, country_code;

CREATE TABLE IF NOT EXISTS viewer_geo_hourly_store (
    hour DateTime,
    tenant_id UUID,
    country_code FixedString(2),
    viewer_count UInt64 DEFAULT 0,
    viewer_hours Float64 DEFAULT 0,
    egress_gb Float64 DEFAULT 0,
    unique_viewers_state AggregateFunction(uniqCombined, String),
    refresh_version_ms DateTime64(3)
) ENGINE = ReplacingMergeTree(refresh_version_ms)
PARTITION BY toYYYYMM(hour)
ORDER BY (tenant_id, hour, country_code)
TTL hour + INTERVAL 365 DAY;

CREATE MATERIALIZED VIEW IF NOT EXISTS viewer_geo_hourly_mv
REFRESH EVERY 5 MINUTE APPEND TO viewer_geo_hourly_store AS
SELECT
    toStartOfHour(u.window_start) AS hour,
    u.tenant_id,
    s.country_code,
    toUInt64(uniqCombined(if(s.host != '', s.host, concat(toString(u.node_id), '|', u.session_id)))) AS viewer_count,
    sum(u.seconds_observed) / 3600.0                                AS viewer_hours,
    sum(u.down_bytes_observed) / pow(1024, 3) AS egress_gb,
    uniqCombinedState(if(s.host != '', s.host, concat(toString(u.node_id), '|', u.session_id))) AS unique_viewers_state,
    now64(3)                                                        AS refresh_version_ms
FROM viewer_usage_5m_v u
LEFT JOIN viewer_sessions_final_v s USING (tenant_id, node_id, session_id)
WHERE (hour, u.tenant_id, s.country_code) IN (
    SELECT DISTINCT toStartOfHour(u.window_start) AS hour, u.tenant_id, s.country_code
    FROM viewer_usage_5m_v u
    LEFT JOIN viewer_sessions_final_v s USING (tenant_id, node_id, session_id)
    WHERE u.latest_projection_version_ms >= toUnixTimestamp64Milli(now64(3) - INTERVAL 2 DAY)
)
GROUP BY hour, u.tenant_id, s.country_code;

CREATE VIEW IF NOT EXISTS viewer_geo_hourly AS
SELECT
    hour, tenant_id, country_code,
    argMax(viewer_count,         refresh_version_ms) AS viewer_count,
    argMax(viewer_hours,         refresh_version_ms) AS viewer_hours,
    argMax(egress_gb,            refresh_version_ms) AS egress_gb,
    argMax(unique_viewers_state, refresh_version_ms) AS unique_viewers_state,
    max(refresh_version_ms) AS latest_refresh_version_ms
FROM viewer_geo_hourly_store
GROUP BY hour, tenant_id, country_code;

CREATE TABLE IF NOT EXISTS viewer_city_hourly_store (
    hour DateTime,
    tenant_id UUID,
    stream_id UUID,
    country_code FixedString(2),
    city LowCardinality(String),
    latitude Float64 DEFAULT 0,
    longitude Float64 DEFAULT 0,
    viewer_count UInt64 DEFAULT 0,
    viewer_hours Float64 DEFAULT 0,
    egress_gb Float64 DEFAULT 0,
    unique_viewers_state AggregateFunction(uniqCombined, String),
    refresh_version_ms DateTime64(3)
) ENGINE = ReplacingMergeTree(refresh_version_ms)
PARTITION BY toYYYYMM(hour)
ORDER BY (tenant_id, hour, stream_id, country_code, city)
TTL hour + INTERVAL 365 DAY;

CREATE MATERIALIZED VIEW IF NOT EXISTS viewer_city_hourly_mv
REFRESH EVERY 5 MINUTE APPEND TO viewer_city_hourly_store AS
SELECT
    toStartOfHour(u.window_start) AS hour,
    u.tenant_id,
    u.stream_id,
    s.country_code,
    s.city,
    any(s.latitude)                                                 AS latitude,
    any(s.longitude)                                                AS longitude,
    toUInt64(uniqCombined(if(s.host != '', s.host, concat(toString(u.node_id), '|', u.session_id)))) AS viewer_count,
    sum(u.seconds_observed) / 3600.0                                AS viewer_hours,
    sum(u.down_bytes_observed) / pow(1024, 3) AS egress_gb,
    uniqCombinedState(if(s.host != '', s.host, concat(toString(u.node_id), '|', u.session_id))) AS unique_viewers_state,
    now64(3)                                                        AS refresh_version_ms
FROM viewer_usage_5m_v u
INNER JOIN viewer_sessions_final_v s USING (tenant_id, node_id, session_id)
WHERE (hour, u.tenant_id, u.stream_id, s.country_code, s.city) IN (
    SELECT DISTINCT toStartOfHour(u.window_start) AS hour, u.tenant_id, u.stream_id, s.country_code, s.city
    FROM viewer_usage_5m_v u
    INNER JOIN viewer_sessions_final_v s USING (tenant_id, node_id, session_id)
    WHERE s.city != ''
      AND u.latest_projection_version_ms >= toUnixTimestamp64Milli(now64(3) - INTERVAL 2 DAY)
)
GROUP BY hour, u.tenant_id, u.stream_id, s.country_code, s.city;

CREATE VIEW IF NOT EXISTS viewer_city_hourly AS
SELECT
    hour, tenant_id, stream_id, country_code, city,
    argMax(latitude,             refresh_version_ms) AS latitude,
    argMax(longitude,            refresh_version_ms) AS longitude,
    argMax(viewer_count,         refresh_version_ms) AS viewer_count,
    argMax(viewer_hours,         refresh_version_ms) AS viewer_hours,
    argMax(egress_gb,            refresh_version_ms) AS egress_gb,
    argMax(unique_viewers_state, refresh_version_ms) AS unique_viewers_state,
    max(refresh_version_ms) AS latest_refresh_version_ms
FROM viewer_city_hourly_store
GROUP BY hour, tenant_id, stream_id, country_code, city;

CREATE TABLE IF NOT EXISTS stream_connection_hourly_store (
    hour DateTime,
    tenant_id UUID,
    stream_id UUID,
    internal_name String,
    total_bytes UInt64 DEFAULT 0,
    total_sessions UInt64 DEFAULT 0,
    unique_viewers_state AggregateFunction(uniqCombined, String),
    refresh_version_ms DateTime64(3)
) ENGINE = ReplacingMergeTree(refresh_version_ms)
PARTITION BY toYYYYMM(hour)
ORDER BY (tenant_id, hour, stream_id)
TTL hour + INTERVAL 365 DAY;

CREATE MATERIALIZED VIEW IF NOT EXISTS stream_connection_hourly_mv
REFRESH EVERY 5 MINUTE APPEND TO stream_connection_hourly_store AS
SELECT
    toStartOfHour(u.window_start) AS hour,
    u.tenant_id,
    u.stream_id,
    any(s.stream_name)                                         AS internal_name,
    toUInt64(sum(u.up_bytes_observed + u.down_bytes_observed)) AS total_bytes,
    toUInt64(uniqCombined(u.session_id))                       AS total_sessions,
    uniqCombinedState(if(s.host != '', s.host, concat(toString(u.node_id), '|', u.session_id))) AS unique_viewers_state,
    now64(3)                                                   AS refresh_version_ms
FROM viewer_usage_5m_v u
LEFT JOIN viewer_sessions_final_v s USING (tenant_id, node_id, session_id)
WHERE (hour, u.tenant_id, u.stream_id) IN (
    SELECT DISTINCT toStartOfHour(u.window_start) AS hour, u.tenant_id, u.stream_id
    FROM viewer_usage_5m_v u
    LEFT JOIN viewer_sessions_final_v s USING (tenant_id, node_id, session_id)
    WHERE u.latest_projection_version_ms >= toUnixTimestamp64Milli(now64(3) - INTERVAL 2 DAY)
)
GROUP BY hour, u.tenant_id, u.stream_id;

CREATE VIEW IF NOT EXISTS stream_connection_hourly AS
SELECT
    hour, tenant_id, stream_id,
    argMax(internal_name,        refresh_version_ms) AS internal_name,
    argMax(total_bytes,          refresh_version_ms) AS total_bytes,
    argMax(total_sessions,       refresh_version_ms) AS total_sessions,
    argMax(unique_viewers_state, refresh_version_ms) AS unique_viewers_state,
    max(refresh_version_ms) AS latest_refresh_version_ms
FROM stream_connection_hourly_store
GROUP BY hour, tenant_id, stream_id;

CREATE TABLE IF NOT EXISTS stream_runtime_hourly_store (
    hour DateTime,
    tenant_id UUID,
    cluster_id LowCardinality(String) DEFAULT '',
    stream_id UUID,
    runtime_seconds UInt64 DEFAULT 0,
    peak_viewers UInt32 DEFAULT 0,
    refresh_version_ms DateTime64(3)
) ENGINE = ReplacingMergeTree(refresh_version_ms)
PARTITION BY toYYYYMM(hour)
ORDER BY (tenant_id, hour, cluster_id, stream_id)
TTL hour + INTERVAL 365 DAY;

CREATE MATERIALIZED VIEW IF NOT EXISTS stream_runtime_hourly_mv
REFRESH EVERY 5 MINUTE APPEND TO stream_runtime_hourly_store AS
SELECT
    toStartOfHour(window_start) AS hour,
    tenant_id,
    cluster_id,
    stream_id,
    toUInt64(sum(active_seconds)) AS runtime_seconds,
    toUInt32(max(peak_viewers))   AS peak_viewers,
    now64(3)                      AS refresh_version_ms
FROM stream_runtime_5m_v
WHERE (hour, tenant_id, cluster_id, stream_id) IN (
    SELECT DISTINCT toStartOfHour(window_start) AS hour, tenant_id, cluster_id, stream_id
    FROM stream_runtime_5m_v
    WHERE latest_projection_version_ms >= toUnixTimestamp64Milli(now64(3) - INTERVAL 2 DAY)
)
GROUP BY hour, tenant_id, cluster_id, stream_id;

CREATE VIEW IF NOT EXISTS stream_runtime_hourly AS
SELECT
    hour, tenant_id, cluster_id, stream_id,
    argMax(runtime_seconds, refresh_version_ms) AS runtime_seconds,
    argMax(peak_viewers,    refresh_version_ms) AS peak_viewers,
    max(refresh_version_ms) AS latest_refresh_version_ms
FROM stream_runtime_hourly_store
GROUP BY hour, tenant_id, cluster_id, stream_id;

-- storage_usage_hourly/daily are defined below in the storage-provider
-- attribution block so the rollup columns include
-- (storage_provider_tenant_id, storage_provider_cluster_id, storage_backend)
-- alongside the usage tenant.

CREATE TABLE IF NOT EXISTS processing_hourly_store (
    hour DateTime,
    tenant_id UUID,
    cluster_id LowCardinality(String) DEFAULT '',
    process_type LowCardinality(String),
    output_codec LowCardinality(String),
    track_type LowCardinality(String),
    media_seconds Float64 DEFAULT 0,
    segment_count UInt64 DEFAULT 0,
    refresh_version_ms DateTime64(3)
) ENGINE = ReplacingMergeTree(refresh_version_ms)
PARTITION BY toYYYYMM(hour)
ORDER BY (tenant_id, hour, cluster_id, process_type, output_codec, track_type)
TTL hour + INTERVAL 730 DAY;

CREATE MATERIALIZED VIEW IF NOT EXISTS processing_hourly_mv
REFRESH EVERY 5 MINUTE APPEND TO processing_hourly_store AS
SELECT
    toStartOfHour(window_start) AS hour,
    tenant_id,
    cluster_id,
    process_type,
    output_codec,
    track_type,
    sum(p5.media_seconds)                    AS media_seconds,
    toUInt64(uniqCombined(p5.source_event_id)) AS segment_count,
    now64(3)                                 AS refresh_version_ms
FROM processing_5m_v AS p5
WHERE (hour, tenant_id, cluster_id, process_type, output_codec, track_type) IN (
    SELECT DISTINCT toStartOfHour(window_start) AS hour, tenant_id, cluster_id, process_type, output_codec, track_type
    FROM processing_5m_v
    WHERE latest_projection_version_ms >= toUnixTimestamp64Milli(now64(3) - INTERVAL 2 DAY)
)
GROUP BY hour, tenant_id, cluster_id, process_type, output_codec, track_type;

CREATE VIEW IF NOT EXISTS processing_hourly AS
SELECT
    hour, tenant_id, cluster_id, process_type, output_codec, track_type,
    argMax(media_seconds, refresh_version_ms) AS media_seconds,
    argMax(segment_count, refresh_version_ms) AS segment_count,
    max(refresh_version_ms) AS latest_refresh_version_ms
FROM processing_hourly_store
GROUP BY hour, tenant_id, cluster_id, process_type, output_codec, track_type;

CREATE TABLE IF NOT EXISTS processing_daily_store (
    day Date,
    tenant_id UUID,
    cluster_id LowCardinality(String) DEFAULT '',
    process_type LowCardinality(String),
    output_codec LowCardinality(String),
    track_type LowCardinality(String),
    media_seconds Float64 DEFAULT 0,
    segment_count UInt64 DEFAULT 0,
    refresh_version_ms DateTime64(3)
) ENGINE = ReplacingMergeTree(refresh_version_ms)
PARTITION BY toYYYYMM(day)
ORDER BY (tenant_id, day, cluster_id, process_type, output_codec, track_type)
TTL day + INTERVAL 1825 DAY;

CREATE MATERIALIZED VIEW IF NOT EXISTS processing_daily_mv
REFRESH EVERY 1 HOUR APPEND TO processing_daily_store AS
SELECT
    toDate(hour) AS day,
    tenant_id,
    cluster_id,
    process_type,
    output_codec,
    track_type,
    sum(ph.media_seconds) AS media_seconds,
    sum(ph.segment_count) AS segment_count,
    now64(3)           AS refresh_version_ms
FROM processing_hourly AS ph
WHERE (day, tenant_id, cluster_id, process_type, output_codec, track_type) IN (
    SELECT DISTINCT toDate(hour) AS day, tenant_id, cluster_id, process_type, output_codec, track_type
    FROM processing_hourly
    WHERE latest_refresh_version_ms >= now64(3) - INTERVAL 7 DAY
)
GROUP BY day, tenant_id, cluster_id, process_type, output_codec, track_type;

CREATE VIEW IF NOT EXISTS processing_daily AS
SELECT
    day, tenant_id, cluster_id, process_type, output_codec, track_type,
    argMax(media_seconds, refresh_version_ms) AS media_seconds,
    argMax(segment_count, refresh_version_ms) AS segment_count,
    max(refresh_version_ms) AS latest_refresh_version_ms
FROM processing_daily_store
GROUP BY day, tenant_id, cluster_id, process_type, output_codec, track_type;

CREATE TABLE IF NOT EXISTS api_usage_hourly_store (
    hour DateTime,
    tenant_id UUID,
    auth_type LowCardinality(String),
    operation_type LowCardinality(String),
    operation_name LowCardinality(String) DEFAULT '',
    requests UInt64 DEFAULT 0,
    errors UInt64 DEFAULT 0,
    duration_ms UInt64 DEFAULT 0,
    complexity UInt64 DEFAULT 0,
    unique_users_state AggregateFunction(uniqCombined, UInt64),
    unique_tokens_state AggregateFunction(uniqCombined, UInt64),
    refresh_version_ms DateTime64(3)
) ENGINE = ReplacingMergeTree(refresh_version_ms)
PARTITION BY toYYYYMM(hour)
ORDER BY (tenant_id, hour, auth_type, operation_type, operation_name)
TTL hour + INTERVAL 365 DAY;

CREATE MATERIALIZED VIEW IF NOT EXISTS api_usage_hourly_mv
REFRESH EVERY 5 MINUTE APPEND TO api_usage_hourly_store AS
SELECT
    toStartOfHour(window_start) AS hour,
    tenant_id,
    auth_type,
    operation_type,
    operation_name,
    sum(api5.requests)    AS requests,
    sum(api5.errors)      AS errors,
    sum(api5.duration_ms) AS duration_ms,
    sum(api5.complexity)  AS complexity,
    uniqCombinedMergeState(api5.unique_users_state)  AS unique_users_state,
    uniqCombinedMergeState(api5.unique_tokens_state) AS unique_tokens_state,
    now64(3)                                    AS refresh_version_ms
FROM api_usage_5m_v AS api5
WHERE (hour, tenant_id, auth_type, operation_type, operation_name) IN (
    SELECT DISTINCT toStartOfHour(window_start) AS hour, tenant_id, auth_type, operation_type, operation_name
    FROM api_usage_5m_v
    WHERE latest_projection_version_ms >= toUnixTimestamp64Milli(now64(3) - INTERVAL 2 DAY)
)
GROUP BY hour, tenant_id, auth_type, operation_type, operation_name;

CREATE VIEW IF NOT EXISTS api_usage_hourly AS
SELECT
    hour, tenant_id, auth_type, operation_type, operation_name,
    argMax(requests,            refresh_version_ms) AS requests,
    argMax(errors,              refresh_version_ms) AS errors,
    argMax(duration_ms,         refresh_version_ms) AS duration_ms,
    argMax(complexity,          refresh_version_ms) AS complexity,
    argMax(unique_users_state,  refresh_version_ms) AS unique_users_state,
    argMax(unique_tokens_state, refresh_version_ms) AS unique_tokens_state,
    max(refresh_version_ms) AS latest_refresh_version_ms
FROM api_usage_hourly_store
GROUP BY hour, tenant_id, auth_type, operation_type, operation_name;

-- ----------------------------------------------------------------------------
-- Daily tier (30d+ dashboards)
-- ----------------------------------------------------------------------------

-- ----------------------------------------------------------------------------
-- Daily tier (30d+ dashboards). Each daily MV reads from the dedup VIEW of
-- its hourly counterpart so the daily rollup sees a stable per-hour value
-- regardless of how many refresh versions exist in the hourly _store.
-- ----------------------------------------------------------------------------

CREATE TABLE IF NOT EXISTS tenant_usage_daily_store (
    day Date,
    tenant_id UUID,
    cluster_id LowCardinality(String) DEFAULT '',
    seconds_observed UInt64 DEFAULT 0,
    up_bytes UInt64 DEFAULT 0,
    down_bytes UInt64 DEFAULT 0,
    unique_sessions_state AggregateFunction(uniqCombined, String),
    unique_streams_state AggregateFunction(uniqCombined, UUID),
    refresh_version_ms DateTime64(3)
) ENGINE = ReplacingMergeTree(refresh_version_ms)
PARTITION BY toYYYYMM(day)
ORDER BY (tenant_id, day, cluster_id)
TTL day + INTERVAL 1825 DAY;

CREATE MATERIALIZED VIEW IF NOT EXISTS tenant_usage_daily_mv
REFRESH EVERY 1 HOUR APPEND TO tenant_usage_daily_store AS
SELECT
    toDate(hour) AS day,
    tenant_id,
    cluster_id,
    sum(tuh.seconds_observed) AS seconds_observed,
    sum(tuh.up_bytes)         AS up_bytes,
    sum(tuh.down_bytes)       AS down_bytes,
    uniqCombinedMergeState(tuh.unique_sessions_state) AS unique_sessions_state,
    uniqCombinedMergeState(tuh.unique_streams_state)  AS unique_streams_state,
    now64(3)                                      AS refresh_version_ms
FROM tenant_usage_hourly AS tuh
WHERE (day, tenant_id, cluster_id) IN (
    SELECT DISTINCT toDate(hour) AS day, tenant_id, cluster_id
    FROM tenant_usage_hourly
    WHERE latest_refresh_version_ms >= now64(3) - INTERVAL 7 DAY
)
GROUP BY day, tenant_id, cluster_id;

CREATE VIEW IF NOT EXISTS tenant_usage_daily AS
SELECT
    day, tenant_id, cluster_id,
    argMax(seconds_observed,      refresh_version_ms) AS seconds_observed,
    argMax(up_bytes,              refresh_version_ms) AS up_bytes,
    argMax(down_bytes,            refresh_version_ms) AS down_bytes,
    argMax(unique_sessions_state, refresh_version_ms) AS unique_sessions_state,
    argMax(unique_streams_state,  refresh_version_ms) AS unique_streams_state,
    max(refresh_version_ms) AS latest_refresh_version_ms
FROM tenant_usage_daily_store
GROUP BY day, tenant_id, cluster_id;

CREATE TABLE IF NOT EXISTS tenant_viewer_daily_store (
    day Date,
    tenant_id UUID,
    cluster_id LowCardinality(String) DEFAULT '',
    viewer_hours Float64 DEFAULT 0,
    egress_gb Float64 DEFAULT 0,
    unique_viewers_state AggregateFunction(uniqCombined, String),
    total_sessions UInt64 DEFAULT 0,
    refresh_version_ms DateTime64(3)
) ENGINE = ReplacingMergeTree(refresh_version_ms)
PARTITION BY toYYYYMM(day)
ORDER BY (tenant_id, day, cluster_id)
TTL day + INTERVAL 1825 DAY;

CREATE MATERIALIZED VIEW IF NOT EXISTS tenant_viewer_daily_mv
REFRESH EVERY 1 HOUR APPEND TO tenant_viewer_daily_store AS
SELECT
    toDate(u.window_start) AS day,
    u.tenant_id,
    u.cluster_id,
    sum(u.seconds_observed) / 3600.0 AS viewer_hours,
    sum(u.down_bytes_observed) / pow(1024, 3) AS egress_gb,
    uniqCombinedState(if(s.host != '', s.host, concat(toString(u.node_id), '|', u.session_id))) AS unique_viewers_state,
    toUInt64(uniqCombined(u.session_id)) AS total_sessions,
    now64(3) AS refresh_version_ms
FROM viewer_usage_5m_v u
LEFT JOIN viewer_sessions_final_v s USING (tenant_id, node_id, session_id)
WHERE (toDate(u.window_start), u.tenant_id, u.cluster_id) IN (
    SELECT DISTINCT toDate(window_start) AS day, tenant_id, cluster_id
    FROM viewer_usage_5m_v
    WHERE latest_projection_version_ms >= toUnixTimestamp64Milli(now64(3) - INTERVAL 7 DAY)
)
GROUP BY day, u.tenant_id, u.cluster_id;

CREATE VIEW IF NOT EXISTS tenant_viewer_daily AS
SELECT
    day, tenant_id, cluster_id,
    viewer_hours,
    egress_gb,
    unique_viewers_state,
    toUInt64(finalizeAggregation(unique_viewers_state)) AS unique_viewers,
    total_sessions,
    latest_refresh_version_ms
FROM (
    SELECT
        day, tenant_id, cluster_id,
        argMax(viewer_hours,          refresh_version_ms) AS viewer_hours,
        argMax(egress_gb,             refresh_version_ms) AS egress_gb,
        argMax(unique_viewers_state,  refresh_version_ms) AS unique_viewers_state,
        argMax(total_sessions,        refresh_version_ms) AS total_sessions,
        max(refresh_version_ms) AS latest_refresh_version_ms
    FROM tenant_viewer_daily_store
    GROUP BY day, tenant_id, cluster_id
);

CREATE TABLE IF NOT EXISTS tenant_analytics_daily_store (
    day Date,
    tenant_id UUID,
    total_streams UInt64 DEFAULT 0,
    total_views UInt64 DEFAULT 0,
    unique_viewers_state AggregateFunction(uniqCombined, String),
    egress_bytes UInt64 DEFAULT 0,
    refresh_version_ms DateTime64(3)
) ENGINE = ReplacingMergeTree(refresh_version_ms)
PARTITION BY toYYYYMM(day)
ORDER BY (tenant_id, day)
TTL day + INTERVAL 1825 DAY;

CREATE MATERIALIZED VIEW IF NOT EXISTS tenant_analytics_daily_mv
REFRESH EVERY 1 HOUR APPEND TO tenant_analytics_daily_store AS
SELECT
    toDate(u.window_start) AS day,
    u.tenant_id,
    toUInt64(uniqCombined(u.stream_id)) AS total_streams,
    toUInt64(uniqCombined(u.session_id)) AS total_views,
    uniqCombinedState(if(s.host != '', s.host, concat(toString(u.node_id), '|', u.session_id))) AS unique_viewers_state,
    toUInt64(sum(u.down_bytes_observed)) AS egress_bytes,
    now64(3) AS refresh_version_ms
FROM viewer_usage_5m_v u
LEFT JOIN viewer_sessions_final_v s USING (tenant_id, node_id, session_id)
WHERE (toDate(u.window_start), u.tenant_id) IN (
    SELECT DISTINCT toDate(window_start) AS day, tenant_id
    FROM viewer_usage_5m_v
    WHERE latest_projection_version_ms >= toUnixTimestamp64Milli(now64(3) - INTERVAL 7 DAY)
)
GROUP BY day, u.tenant_id;

CREATE VIEW IF NOT EXISTS tenant_analytics_daily AS
SELECT
    day, tenant_id,
    total_streams,
    total_views,
    unique_viewers_state,
    toUInt64(finalizeAggregation(unique_viewers_state)) AS unique_viewers,
    egress_bytes,
    latest_refresh_version_ms
FROM (
    SELECT
        day, tenant_id,
        argMax(total_streams,         refresh_version_ms) AS total_streams,
        argMax(total_views,           refresh_version_ms) AS total_views,
        argMax(unique_viewers_state,  refresh_version_ms) AS unique_viewers_state,
        argMax(egress_bytes,          refresh_version_ms) AS egress_bytes,
        max(refresh_version_ms) AS latest_refresh_version_ms
    FROM tenant_analytics_daily_store
    GROUP BY day, tenant_id
);

CREATE TABLE IF NOT EXISTS stream_analytics_daily_store (
    day Date,
    tenant_id UUID,
    stream_id UUID,
    internal_name String DEFAULT '',
    total_views UInt64 DEFAULT 0,
    unique_viewers_state AggregateFunction(uniqCombined, String),
    unique_countries UInt32 DEFAULT 0,
    unique_cities UInt32 DEFAULT 0,
    egress_bytes UInt64 DEFAULT 0,
    refresh_version_ms DateTime64(3)
) ENGINE = ReplacingMergeTree(refresh_version_ms)
PARTITION BY toYYYYMM(day)
ORDER BY (tenant_id, day, stream_id)
TTL day + INTERVAL 1825 DAY;

CREATE MATERIALIZED VIEW IF NOT EXISTS stream_analytics_daily_mv
REFRESH EVERY 1 HOUR APPEND TO stream_analytics_daily_store AS
SELECT
    toDate(u.window_start) AS day,
    u.tenant_id,
    u.stream_id,
    any(s.stream_name)                                         AS internal_name,
    toUInt64(uniqCombined(u.session_id))                       AS total_views,
    uniqCombinedState(if(s.host != '', s.host, concat(toString(u.node_id), '|', u.session_id))) AS unique_viewers_state,
    toUInt32(uniqCombined(s.country_code))                     AS unique_countries,
    toUInt32(uniqCombined(s.city))                             AS unique_cities,
    toUInt64(sum(u.down_bytes_observed)) AS egress_bytes,
    now64(3)                                                   AS refresh_version_ms
FROM viewer_usage_5m_v u
LEFT JOIN viewer_sessions_final_v s USING (tenant_id, node_id, session_id)
WHERE (day, u.tenant_id, u.stream_id) IN (
    SELECT DISTINCT toDate(u.window_start) AS day, u.tenant_id, u.stream_id
    FROM viewer_usage_5m_v u
    LEFT JOIN viewer_sessions_final_v s USING (tenant_id, node_id, session_id)
    WHERE u.latest_projection_version_ms >= toUnixTimestamp64Milli(now64(3) - INTERVAL 2 DAY)
)
GROUP BY day, u.tenant_id, u.stream_id;

CREATE VIEW IF NOT EXISTS stream_analytics_daily AS
SELECT
    day, tenant_id, stream_id,
    internal_name,
    total_views,
    unique_viewers_state,
    toUInt64(finalizeAggregation(unique_viewers_state)) AS unique_viewers,
    unique_countries,
    unique_cities,
    egress_bytes,
    latest_refresh_version_ms
FROM (
    SELECT
        day, tenant_id, stream_id,
        argMax(internal_name,         refresh_version_ms) AS internal_name,
        argMax(total_views,           refresh_version_ms) AS total_views,
        argMax(unique_viewers_state,  refresh_version_ms) AS unique_viewers_state,
        argMax(unique_countries,      refresh_version_ms) AS unique_countries,
        argMax(unique_cities,         refresh_version_ms) AS unique_cities,
        argMax(egress_bytes,          refresh_version_ms) AS egress_bytes,
        max(refresh_version_ms) AS latest_refresh_version_ms
    FROM stream_analytics_daily_store
    GROUP BY day, tenant_id, stream_id
);

CREATE TABLE IF NOT EXISTS viewer_geo_daily_store (
    day Date,
    tenant_id UUID,
    country_code FixedString(2),
    viewer_count UInt64 DEFAULT 0,
    viewer_hours Float64 DEFAULT 0,
    egress_gb Float64 DEFAULT 0,
    unique_viewers_state AggregateFunction(uniqCombined, String),
    refresh_version_ms DateTime64(3)
) ENGINE = ReplacingMergeTree(refresh_version_ms)
PARTITION BY toYYYYMM(day)
ORDER BY (tenant_id, day, country_code)
TTL day + INTERVAL 1825 DAY;

CREATE MATERIALIZED VIEW IF NOT EXISTS viewer_geo_daily_mv
REFRESH EVERY 1 HOUR APPEND TO viewer_geo_daily_store AS
SELECT
    toDate(hour) AS day,
    tenant_id,
    country_code,
    uniqCombinedMerge(vgh.unique_viewers_state)      AS viewer_count,
    sum(vgh.viewer_hours)                        AS viewer_hours,
    sum(vgh.egress_gb)                           AS egress_gb,
    uniqCombinedMergeState(vgh.unique_viewers_state) AS unique_viewers_state,
    now64(3)                                     AS refresh_version_ms
FROM viewer_geo_hourly AS vgh
WHERE (day, tenant_id, country_code) IN (
    SELECT DISTINCT toDate(hour) AS day, tenant_id, country_code
    FROM viewer_geo_hourly
    WHERE latest_refresh_version_ms >= now64(3) - INTERVAL 7 DAY
)
GROUP BY day, tenant_id, country_code;

CREATE VIEW IF NOT EXISTS viewer_geo_daily AS
SELECT
    day, tenant_id, country_code,
    argMax(viewer_count,         refresh_version_ms) AS viewer_count,
    argMax(viewer_hours,         refresh_version_ms) AS viewer_hours,
    argMax(egress_gb,            refresh_version_ms) AS egress_gb,
    argMax(unique_viewers_state, refresh_version_ms) AS unique_viewers_state,
    max(refresh_version_ms) AS latest_refresh_version_ms
FROM viewer_geo_daily_store
GROUP BY day, tenant_id, country_code;

CREATE TABLE IF NOT EXISTS viewer_city_daily_store (
    day Date,
    tenant_id UUID,
    stream_id UUID,
    country_code FixedString(2),
    city LowCardinality(String),
    viewer_count UInt64 DEFAULT 0,
    viewer_hours Float64 DEFAULT 0,
    egress_gb Float64 DEFAULT 0,
    unique_viewers_state AggregateFunction(uniqCombined, String),
    refresh_version_ms DateTime64(3)
) ENGINE = ReplacingMergeTree(refresh_version_ms)
PARTITION BY toYYYYMM(day)
ORDER BY (tenant_id, day, stream_id, country_code, city)
TTL day + INTERVAL 1825 DAY;

CREATE MATERIALIZED VIEW IF NOT EXISTS viewer_city_daily_mv
REFRESH EVERY 1 HOUR APPEND TO viewer_city_daily_store AS
SELECT
    toDate(hour) AS day,
    tenant_id,
    stream_id,
    country_code,
    city,
    uniqCombinedMerge(vch.unique_viewers_state)      AS viewer_count,
    sum(vch.viewer_hours)                        AS viewer_hours,
    sum(vch.egress_gb)                           AS egress_gb,
    uniqCombinedMergeState(vch.unique_viewers_state) AS unique_viewers_state,
    now64(3)                                     AS refresh_version_ms
FROM viewer_city_hourly AS vch
WHERE (day, tenant_id, stream_id, country_code, city) IN (
    SELECT DISTINCT toDate(hour) AS day, tenant_id, stream_id, country_code, city
    FROM viewer_city_hourly
    WHERE latest_refresh_version_ms >= now64(3) - INTERVAL 7 DAY
)
GROUP BY day, tenant_id, stream_id, country_code, city;

CREATE VIEW IF NOT EXISTS viewer_city_daily AS
SELECT
    day, tenant_id, stream_id, country_code, city,
    argMax(viewer_count,         refresh_version_ms) AS viewer_count,
    argMax(viewer_hours,         refresh_version_ms) AS viewer_hours,
    argMax(egress_gb,            refresh_version_ms) AS egress_gb,
    argMax(unique_viewers_state, refresh_version_ms) AS unique_viewers_state,
    max(refresh_version_ms) AS latest_refresh_version_ms
FROM viewer_city_daily_store
GROUP BY day, tenant_id, stream_id, country_code, city;

CREATE TABLE IF NOT EXISTS stream_runtime_daily_store (
    day Date,
    tenant_id UUID,
    cluster_id LowCardinality(String) DEFAULT '',
    stream_id UUID,
    runtime_seconds UInt64 DEFAULT 0,
    peak_viewers UInt32 DEFAULT 0,
    refresh_version_ms DateTime64(3)
) ENGINE = ReplacingMergeTree(refresh_version_ms)
PARTITION BY toYYYYMM(day)
ORDER BY (tenant_id, day, cluster_id, stream_id)
TTL day + INTERVAL 1825 DAY;

CREATE MATERIALIZED VIEW IF NOT EXISTS stream_runtime_daily_mv
REFRESH EVERY 1 HOUR APPEND TO stream_runtime_daily_store AS
SELECT
    toDate(hour) AS day,
    tenant_id,
    cluster_id,
    stream_id,
    sum(srh.runtime_seconds) AS runtime_seconds,
    max(srh.peak_viewers)    AS peak_viewers,
    now64(3)             AS refresh_version_ms
FROM stream_runtime_hourly AS srh
WHERE (day, tenant_id, cluster_id, stream_id) IN (
    SELECT DISTINCT toDate(hour) AS day, tenant_id, cluster_id, stream_id
    FROM stream_runtime_hourly
    WHERE latest_refresh_version_ms >= now64(3) - INTERVAL 7 DAY
)
GROUP BY day, tenant_id, cluster_id, stream_id;

CREATE VIEW IF NOT EXISTS stream_runtime_daily AS
SELECT
    day, tenant_id, cluster_id, stream_id,
    argMax(runtime_seconds, refresh_version_ms) AS runtime_seconds,
    argMax(peak_viewers,    refresh_version_ms) AS peak_viewers,
    max(refresh_version_ms) AS latest_refresh_version_ms
FROM stream_runtime_daily_store
GROUP BY day, tenant_id, cluster_id, stream_id;

-- storage_usage_daily is defined below in the storage-provider attribution
-- block so the rollup columns include provider attribution.

-- processing_daily is defined alongside processing_hourly above; nothing else here.

CREATE TABLE IF NOT EXISTS api_usage_daily_store (
    day Date,
    tenant_id UUID,
    auth_type LowCardinality(String),
    operation_type LowCardinality(String),
    operation_name LowCardinality(String) DEFAULT '',
    requests UInt64 DEFAULT 0,
    errors UInt64 DEFAULT 0,
    duration_ms UInt64 DEFAULT 0,
    complexity UInt64 DEFAULT 0,
    unique_users_state AggregateFunction(uniqCombined, UInt64),
    unique_tokens_state AggregateFunction(uniqCombined, UInt64),
    refresh_version_ms DateTime64(3)
) ENGINE = ReplacingMergeTree(refresh_version_ms)
PARTITION BY toYYYYMM(day)
ORDER BY (tenant_id, day, auth_type, operation_type, operation_name)
TTL day + INTERVAL 1825 DAY;

CREATE MATERIALIZED VIEW IF NOT EXISTS api_usage_daily_mv
REFRESH EVERY 1 HOUR APPEND TO api_usage_daily_store AS
SELECT
    toDate(hour) AS day,
    tenant_id,
    auth_type,
    operation_type,
    operation_name,
    sum(auh.requests)    AS requests,
    sum(auh.errors)      AS errors,
    sum(auh.duration_ms) AS duration_ms,
    sum(auh.complexity)  AS complexity,
    uniqCombinedMergeState(auh.unique_users_state)  AS unique_users_state,
    uniqCombinedMergeState(auh.unique_tokens_state) AS unique_tokens_state,
    now64(3)                                    AS refresh_version_ms
FROM api_usage_hourly AS auh
WHERE (day, tenant_id, auth_type, operation_type, operation_name) IN (
    SELECT DISTINCT toDate(hour) AS day, tenant_id, auth_type, operation_type, operation_name
    FROM api_usage_hourly
    WHERE latest_refresh_version_ms >= now64(3) - INTERVAL 7 DAY
)
GROUP BY day, tenant_id, auth_type, operation_type, operation_name;

CREATE VIEW IF NOT EXISTS api_usage_daily AS
SELECT
    day, tenant_id, auth_type, operation_type, operation_name,
    argMax(requests,            refresh_version_ms) AS requests,
    argMax(errors,              refresh_version_ms) AS errors,
    argMax(duration_ms,         refresh_version_ms) AS duration_ms,
    argMax(complexity,          refresh_version_ms) AS complexity,
    argMax(unique_users_state,  refresh_version_ms) AS unique_users_state,
    argMax(unique_tokens_state, refresh_version_ms) AS unique_tokens_state,
    max(refresh_version_ms) AS latest_refresh_version_ms
FROM api_usage_daily_store
GROUP BY day, tenant_id, auth_type, operation_type, operation_name;

-- ============================================================================
-- STORAGE PROVIDER ATTRIBUTION (combined release, late-stage swap)
-- ----------------------------------------------------------------------------
-- Adds storage_provider_tenant_id, storage_provider_cluster_id, and
-- storage_backend to the 5m ledger and hourly/daily rollups. Customer
-- invoices rate by usage tenant and serving cluster; settlement analytics
-- can route by provider using the same canonical storage facts.
-- Default behavior unchanged: customer invoice rates by usage tenant_id;
-- cold is rated, hot is operational-but-priceable. See
-- docs/architecture/meter-contracts.md.
-- ============================================================================

CREATE TABLE IF NOT EXISTS storage_usage_hourly_store (
    hour DateTime,
    tenant_id UUID,
    cluster_id LowCardinality(String) DEFAULT '',
    storage_scope LowCardinality(String) DEFAULT 'hot',
    storage_provider_tenant_id LowCardinality(String) DEFAULT '',
    storage_provider_cluster_id LowCardinality(String) DEFAULT '',
    storage_backend LowCardinality(String) DEFAULT 'unknown',
    gb_seconds Float64 DEFAULT 0,
    gb_hours Float64 DEFAULT 0,
    avg_gb Float64 DEFAULT 0,
    refresh_version_ms DateTime64(3)
) ENGINE = ReplacingMergeTree(refresh_version_ms)
PARTITION BY toYYYYMM(hour)
ORDER BY (tenant_id, hour, cluster_id, storage_provider_tenant_id, storage_provider_cluster_id, storage_scope, storage_backend)
TTL hour + INTERVAL 365 DAY;

CREATE MATERIALIZED VIEW IF NOT EXISTS storage_usage_hourly_mv
REFRESH EVERY 5 MINUTE APPEND TO storage_usage_hourly_store AS
SELECT
    toStartOfHour(window_start) AS hour,
    tenant_id,
    cluster_id,
    storage_scope,
    storage_provider_tenant_id,
    storage_provider_cluster_id,
    storage_backend,
    sum(sg5.gb_seconds)        AS gb_seconds,
    sum(sg5.gb_seconds) / 3600.0 AS gb_hours,
    sum(sg5.gb_seconds) / 3600.0 AS avg_gb,
    now64(3)                   AS refresh_version_ms
FROM storage_gb_seconds_5m_v AS sg5
WHERE (hour, tenant_id, cluster_id, storage_scope, storage_provider_tenant_id, storage_provider_cluster_id, storage_backend) IN (
    SELECT DISTINCT toStartOfHour(window_start) AS hour, tenant_id, cluster_id, storage_scope, storage_provider_tenant_id, storage_provider_cluster_id, storage_backend
    FROM storage_gb_seconds_5m_v
    WHERE latest_projection_version_ms >= toUnixTimestamp64Milli(now64(3) - INTERVAL 2 DAY)
)
GROUP BY hour, tenant_id, cluster_id, storage_scope, storage_provider_tenant_id, storage_provider_cluster_id, storage_backend;

CREATE VIEW IF NOT EXISTS storage_usage_hourly AS
SELECT
    hour, tenant_id, cluster_id, storage_scope,
    storage_provider_tenant_id, storage_provider_cluster_id, storage_backend,
    argMax(gb_seconds, refresh_version_ms) AS gb_seconds,
    argMax(gb_hours,   refresh_version_ms) AS gb_hours,
    argMax(avg_gb,     refresh_version_ms) AS avg_gb,
    max(refresh_version_ms) AS latest_refresh_version_ms
FROM storage_usage_hourly_store
GROUP BY hour, tenant_id, cluster_id, storage_scope,
         storage_provider_tenant_id, storage_provider_cluster_id, storage_backend;

CREATE TABLE IF NOT EXISTS storage_usage_daily_store (
    day Date,
    tenant_id UUID,
    cluster_id LowCardinality(String) DEFAULT '',
    storage_scope LowCardinality(String) DEFAULT 'hot',
    storage_provider_tenant_id LowCardinality(String) DEFAULT '',
    storage_provider_cluster_id LowCardinality(String) DEFAULT '',
    storage_backend LowCardinality(String) DEFAULT 'unknown',
    gb_seconds Float64 DEFAULT 0,
    gb_hours Float64 DEFAULT 0,
    refresh_version_ms DateTime64(3)
) ENGINE = ReplacingMergeTree(refresh_version_ms)
PARTITION BY toYYYYMM(day)
ORDER BY (tenant_id, day, cluster_id, storage_provider_tenant_id, storage_provider_cluster_id, storage_scope, storage_backend)
TTL day + INTERVAL 1825 DAY;

CREATE MATERIALIZED VIEW IF NOT EXISTS storage_usage_daily_mv
REFRESH EVERY 1 HOUR APPEND TO storage_usage_daily_store AS
SELECT
    toDate(hour) AS day,
    tenant_id,
    cluster_id,
    storage_scope,
    storage_provider_tenant_id,
    storage_provider_cluster_id,
    storage_backend,
    sum(suh.gb_seconds)          AS gb_seconds,
    sum(suh.gb_seconds) / 3600.0 AS gb_hours,
    now64(3)                 AS refresh_version_ms
FROM storage_usage_hourly AS suh
WHERE (day, tenant_id, cluster_id, storage_scope, storage_provider_tenant_id, storage_provider_cluster_id, storage_backend) IN (
    SELECT DISTINCT toDate(hour) AS day, tenant_id, cluster_id, storage_scope, storage_provider_tenant_id, storage_provider_cluster_id, storage_backend
    FROM storage_usage_hourly
    WHERE latest_refresh_version_ms >= now64(3) - INTERVAL 7 DAY
)
GROUP BY day, tenant_id, cluster_id, storage_scope, storage_provider_tenant_id, storage_provider_cluster_id, storage_backend;

CREATE VIEW IF NOT EXISTS storage_usage_daily AS
SELECT
    day, tenant_id, cluster_id, storage_scope,
    storage_provider_tenant_id, storage_provider_cluster_id, storage_backend,
    argMax(gb_seconds, refresh_version_ms) AS gb_seconds,
    argMax(gb_hours,   refresh_version_ms) AS gb_hours,
    max(refresh_version_ms) AS latest_refresh_version_ms
FROM storage_usage_daily_store
GROUP BY day, tenant_id, cluster_id, storage_scope,
         storage_provider_tenant_id, storage_provider_cluster_id, storage_backend;
