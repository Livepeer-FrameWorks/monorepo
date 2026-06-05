-- Finalized fact projections and canonical 5-minute ledgers.
--
-- Schema source of truth: pkg/database/sql/clickhouse/periscope.sql

CREATE TABLE IF NOT EXISTS viewer_sessions_final (
    tenant_id UUID,
    node_id LowCardinality(String),
    session_id String,
    source_event_id String,
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
    duration_seconds UInt32 DEFAULT 0,
    uploaded_bytes UInt64 DEFAULT 0,
    downloaded_bytes UInt64 DEFAULT 0,
    seconds_connected UInt64 DEFAULT 0,
    source_started_at_ms Int64,
    source_ended_at_ms Int64,
    edge_received_at_ms Int64,
    projection_version_ms Int64,
    closed_reason LowCardinality(String) DEFAULT 'final',
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
    argMax(closed_reason,         projection_version_ms) AS closed_reason
FROM viewer_sessions_final
GROUP BY tenant_id, node_id, session_id;

CREATE TABLE IF NOT EXISTS stream_sessions_final (
    tenant_id UUID,
    node_id LowCardinality(String),
    stream_id UUID,
    source_event_id String,
    cluster_id LowCardinality(String) DEFAULT '',
    stream_name String DEFAULT '',
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

CREATE TABLE IF NOT EXISTS processing_segments_final (
    tenant_id UUID,
    node_id LowCardinality(String),
    stream_id UUID,
    process_type LowCardinality(String),
    output_codec LowCardinality(String),
    track_type LowCardinality(String),
    segment_number Int32,
    -- Identity. source_event_id = sha256(node_id || NUL || trigger_type || NUL || payload_raw)
    -- from raw_mist_triggers; unique per Mist trigger and therefore per
    -- logical segment for both Livepeer and AV-virtual-segment events. AV
    -- triggers carry no real segment_number, so dedupe MUST key on
    -- source_event_id. segment_dedupe_key stays as a debug-convenience
    -- compact hash; segment_number stays informational.
    source_event_id String,
    segment_dedupe_key UInt64 DEFAULT cityHash64(source_event_id),
    cluster_id LowCardinality(String) DEFAULT '',
    stream_name String DEFAULT '',
    input_codec LowCardinality(String) DEFAULT '',
    media_seconds Float64 DEFAULT 0,
    width Int32 DEFAULT 0,
    height Int32 DEFAULT 0,
    rendition_count Int32 DEFAULT 0,
    input_bytes Int64 DEFAULT 0,
    output_bytes_total Int64 DEFAULT 0,
    turnaround_ms Int64 DEFAULT 0,
    speed_factor Float64 DEFAULT 0,
    livepeer_session_id String DEFAULT '',
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
    argMax(media_seconds,  projection_version_ms) AS media_seconds,
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

CREATE TABLE IF NOT EXISTS viewer_sessions_anomalous (
    tenant_id UUID,
    node_id LowCardinality(String),
    session_id String,
    cluster_id LowCardinality(String) DEFAULT '',
    stream_id UUID DEFAULT toUUIDOrZero(''),
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

CREATE TABLE IF NOT EXISTS ledger_rebuild_cursors (
    ledger_name LowCardinality(String),
    last_processed_projection_ms Int64,
    updated_at_ms Int64
) ENGINE = ReplacingMergeTree(updated_at_ms)
ORDER BY ledger_name
TTL toDateTime(updated_at_ms / 1000) + INTERVAL 365 DAY;

CREATE TABLE IF NOT EXISTS viewer_usage_5m (
    window_start DateTime,
    tenant_id UUID,
    cluster_id LowCardinality(String) DEFAULT '',
    stream_id UUID DEFAULT toUUIDOrZero(''),
    node_id LowCardinality(String),
    session_id String,
    seconds_observed UInt32 DEFAULT 0,
    up_bytes_observed UInt64 DEFAULT 0,
    down_bytes_observed UInt64 DEFAULT 0,
    source_event_id String,
    projection_version_ms Int64
) ENGINE = MergeTree()
PARTITION BY toYYYYMM(toDateTime(projection_version_ms / 1000))
ORDER BY (tenant_id, projection_version_ms, cluster_id, stream_id, node_id, session_id, window_start)
TTL toDateTime(projection_version_ms / 1000) + INTERVAL 90 DAY;

CREATE VIEW IF NOT EXISTS viewer_usage_5m_v AS
SELECT
    window_start, tenant_id, cluster_id, stream_id, node_id, session_id,
    min(projection_version_ms) AS billable_at_ms,
    argMax(seconds_observed,    projection_version_ms) AS seconds_observed,
    argMax(up_bytes_observed,   projection_version_ms) AS up_bytes_observed,
    argMax(down_bytes_observed, projection_version_ms) AS down_bytes_observed,
    argMax(source_event_id,     projection_version_ms) AS source_event_id,
    max(projection_version_ms) AS latest_projection_version_ms
FROM viewer_usage_5m
GROUP BY window_start, tenant_id, cluster_id, stream_id, node_id, session_id;

CREATE TABLE IF NOT EXISTS stream_runtime_5m (
    window_start DateTime,
    tenant_id UUID,
    cluster_id LowCardinality(String) DEFAULT '',
    stream_id UUID,
    source_event_id String,
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

CREATE TABLE IF NOT EXISTS storage_gb_seconds_5m (
    window_start DateTime,
    tenant_id UUID,
    cluster_id LowCardinality(String) DEFAULT '',
    storage_scope LowCardinality(String) DEFAULT 'hot',
    storage_provider_tenant_id LowCardinality(String) DEFAULT '',
    storage_provider_cluster_id LowCardinality(String) DEFAULT '',
    storage_backend LowCardinality(String) DEFAULT 'unknown',
    gb_seconds Float64 DEFAULT 0,
    file_count UInt64 DEFAULT 0,
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

CREATE TABLE IF NOT EXISTS processing_5m (
    window_start DateTime,
    tenant_id UUID,
    cluster_id LowCardinality(String) DEFAULT '',
    process_type LowCardinality(String),
    output_codec LowCardinality(String),
    track_type LowCardinality(String),
    source_event_id String,
    media_seconds Float64 DEFAULT 0,
    projection_version_ms Int64
) ENGINE = MergeTree()
PARTITION BY toYYYYMM(toDateTime(projection_version_ms / 1000))
ORDER BY (tenant_id, projection_version_ms, cluster_id, process_type, output_codec, track_type, source_event_id, window_start)
TTL toDateTime(projection_version_ms / 1000) + INTERVAL 90 DAY;

CREATE VIEW IF NOT EXISTS processing_5m_v AS
SELECT
    window_start, tenant_id,
    argMax(cluster_id, projection_version_ms) AS cluster_id,
    argMax(process_type, projection_version_ms) AS process_type,
    argMax(output_codec, projection_version_ms) AS output_codec,
    argMax(track_type, projection_version_ms) AS track_type,
    source_event_id,
    min(projection_version_ms) AS billable_at_ms,
    argMax(media_seconds, projection_version_ms) AS media_seconds,
    max(projection_version_ms) AS latest_projection_version_ms
FROM processing_5m
GROUP BY window_start, tenant_id, source_event_id;

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
-- key).
CREATE VIEW IF NOT EXISTS api_usage_5m_v AS
SELECT
    window_start, tenant_id, auth_type, operation_type, operation_name,
    min(projection_version_ms) AS billable_at_ms,
    argMax(requests,    projection_version_ms) AS requests,
    argMax(errors,      projection_version_ms) AS errors,
    argMax(duration_ms, projection_version_ms) AS duration_ms,
    argMax(complexity,  projection_version_ms) AS complexity,
    argMax(unique_users_state,  projection_version_ms) AS unique_users_state,
    argMax(unique_tokens_state, projection_version_ms) AS unique_tokens_state,
    max(projection_version_ms) AS latest_projection_version_ms
FROM api_usage_5m
GROUP BY window_start, tenant_id, auth_type, operation_type, operation_name;

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

-- Storage provider attribution. storage_snapshots already exists in
-- production; add the new columns idempotently so existing deployments
-- can carry marketplace settlement data. Defaults match meter-contracts.md.
ALTER TABLE storage_snapshots
    ADD COLUMN IF NOT EXISTS storage_provider_tenant_id LowCardinality(String) DEFAULT '';
ALTER TABLE storage_snapshots
    ADD COLUMN IF NOT EXISTS storage_provider_cluster_id LowCardinality(String) DEFAULT '';
ALTER TABLE storage_snapshots
    ADD COLUMN IF NOT EXISTS storage_backend LowCardinality(String) DEFAULT 'unknown';

-- Ingest wall-clock for the storage rebuilder's late-arrival cursor.
-- See storage_snapshots declaration in periscope.sql for the rationale.
ALTER TABLE storage_snapshots
    ADD COLUMN IF NOT EXISTS ingested_at_ms Int64 DEFAULT toUnixTimestamp64Milli(now64(3));
