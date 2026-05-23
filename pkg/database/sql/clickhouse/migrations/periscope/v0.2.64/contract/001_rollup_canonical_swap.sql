-- Canonical-meter rollup swap. Drops legacy rollups + their MVs and
-- recreates the same public table names as dedup VIEWs over per-rollup
-- *_store ReplacingMergeTree tables that the refreshable MVs APPEND
-- into. This avoids the EXCHANGE-TABLES retention truncation that
-- comes with REFRESH EVERY ... TO and never exposes raw ReplacingMergeTree
-- dedup hazards to resolvers. Each refresh scans recent projection-version
-- changes and rewrites the affected source-time buckets into the append store.
--
-- Also fixes the api_usage_5m_v argMax bug on
-- AggregateFunction(uniqCombined, …) state columns.
--
-- Idempotent: DROPs are IF EXISTS, CREATEs are IF NOT EXISTS where
-- safe. The public-name surface flips from TABLE to VIEW so resolvers
-- keep using the same names; nothing reads *_store directly.
--
-- Schema source of truth: pkg/database/sql/clickhouse/periscope.sql

-- 1. Drop existing MVs (both REFRESH-EVERY-TO and any earlier APPEND attempts).
DROP VIEW IF EXISTS tenant_usage_5m_mv;
DROP VIEW IF EXISTS tenant_usage_hourly_mv;
DROP VIEW IF EXISTS stream_connection_hourly_mv;
DROP VIEW IF EXISTS viewer_hours_hourly_mv;
DROP VIEW IF EXISTS tenant_viewer_daily_mv;
DROP VIEW IF EXISTS viewer_geo_hourly_mv;
DROP VIEW IF EXISTS viewer_city_hourly_mv;
DROP VIEW IF EXISTS viewer_geo_daily_mv;
DROP VIEW IF EXISTS viewer_city_daily_mv;
DROP VIEW IF EXISTS tenant_analytics_daily_mv;
DROP VIEW IF EXISTS stream_analytics_daily_mv;
DROP VIEW IF EXISTS storage_usage_hourly_mv;
DROP VIEW IF EXISTS storage_usage_daily_mv;
DROP VIEW IF EXISTS stream_runtime_hourly_mv;
DROP VIEW IF EXISTS stream_runtime_daily_mv;
DROP VIEW IF EXISTS processing_hourly_mv;
DROP VIEW IF EXISTS processing_daily_mv;
DROP VIEW IF EXISTS api_usage_hourly_mv;
DROP VIEW IF EXISTS api_usage_daily_mv;
DROP VIEW IF EXISTS tenant_usage_daily_mv;

-- 2. Drop existing public-name TABLEs (from the legacy MergeTree shape).
--    These are recreated below as VIEWs over *_store.
DROP TABLE IF EXISTS tenant_usage_5m;
DROP TABLE IF EXISTS tenant_usage_hourly;
DROP TABLE IF EXISTS tenant_usage_daily;
DROP TABLE IF EXISTS stream_connection_hourly;
DROP TABLE IF EXISTS viewer_hours_hourly;
DROP TABLE IF EXISTS tenant_viewer_daily;
DROP TABLE IF EXISTS viewer_geo_hourly;
DROP TABLE IF EXISTS viewer_city_hourly;
DROP TABLE IF EXISTS viewer_geo_daily;
DROP TABLE IF EXISTS viewer_city_daily;
DROP TABLE IF EXISTS tenant_analytics_daily;
DROP TABLE IF EXISTS stream_analytics_daily;
DROP TABLE IF EXISTS storage_usage_hourly;
DROP TABLE IF EXISTS storage_usage_daily;
DROP TABLE IF EXISTS stream_runtime_hourly;
DROP TABLE IF EXISTS stream_runtime_daily;
DROP TABLE IF EXISTS processing_hourly;
DROP TABLE IF EXISTS processing_daily;
DROP TABLE IF EXISTS api_usage_hourly;
DROP TABLE IF EXISTS api_usage_daily;

-- 3. Drop any stale *_store and dedup VIEW remnants from a partial run.
DROP VIEW  IF EXISTS tenant_usage_5m;
DROP VIEW  IF EXISTS tenant_usage_hourly;
DROP VIEW  IF EXISTS tenant_usage_daily;
DROP VIEW  IF EXISTS stream_connection_hourly;
DROP VIEW  IF EXISTS viewer_hours_hourly;
DROP VIEW  IF EXISTS tenant_viewer_daily;
DROP VIEW  IF EXISTS viewer_geo_hourly;
DROP VIEW  IF EXISTS viewer_city_hourly;
DROP VIEW  IF EXISTS viewer_geo_daily;
DROP VIEW  IF EXISTS viewer_city_daily;
DROP VIEW  IF EXISTS tenant_analytics_daily;
DROP VIEW  IF EXISTS stream_analytics_daily;
DROP VIEW  IF EXISTS storage_usage_hourly;
DROP VIEW  IF EXISTS storage_usage_daily;
DROP VIEW  IF EXISTS stream_runtime_hourly;
DROP VIEW  IF EXISTS stream_runtime_daily;
DROP VIEW  IF EXISTS processing_hourly;
DROP VIEW  IF EXISTS processing_daily;
DROP VIEW  IF EXISTS api_usage_hourly;
DROP VIEW  IF EXISTS api_usage_daily;
DROP TABLE IF EXISTS tenant_usage_5m_store;
DROP TABLE IF EXISTS tenant_usage_hourly_store;
DROP TABLE IF EXISTS tenant_usage_daily_store;
DROP TABLE IF EXISTS stream_connection_hourly_store;
DROP TABLE IF EXISTS viewer_hours_hourly_store;
DROP TABLE IF EXISTS tenant_viewer_daily_store;
DROP TABLE IF EXISTS viewer_geo_hourly_store;
DROP TABLE IF EXISTS viewer_city_hourly_store;
DROP TABLE IF EXISTS viewer_geo_daily_store;
DROP TABLE IF EXISTS viewer_city_daily_store;
DROP TABLE IF EXISTS tenant_analytics_daily_store;
DROP TABLE IF EXISTS stream_analytics_daily_store;
DROP TABLE IF EXISTS storage_usage_hourly_store;
DROP TABLE IF EXISTS storage_usage_daily_store;
DROP TABLE IF EXISTS stream_runtime_hourly_store;
DROP TABLE IF EXISTS stream_runtime_daily_store;
DROP TABLE IF EXISTS processing_hourly_store;
DROP TABLE IF EXISTS processing_daily_store;
DROP TABLE IF EXISTS api_usage_hourly_store;
DROP TABLE IF EXISTS api_usage_daily_store;


-- ----------------------------------------------------------------------------
-- 5-minute live tier (sub-hour dashboards). Same shape as tenant_usage_hourly
-- so resolvers can switch grain by just swapping table + time column.
-- Short TTL: the canonical 5-min ledger (viewer_usage_5m) is the long-term
-- source; this rollup is a query-side cache only.
-- ----------------------------------------------------------------------------

-- ----------------------------------------------------------------------------
-- 5-minute live tier (sub-hour dashboards).
-- ----------------------------------------------------------------------------

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
WHERE latest_projection_version_ms >= toUnixTimestamp64Milli(now64(3) - INTERVAL 2 HOUR)
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
    uniqCombinedState(u.session_id)                            AS unique_viewers_state,
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
    toUInt64(uniqCombined(u.session_id))                            AS viewer_count,
    sum(u.seconds_observed) / 3600.0                                AS viewer_hours,
    sum(u.down_bytes_observed) / pow(1024, 3) AS egress_gb,
    uniqCombinedState(u.session_id)                                 AS unique_viewers_state,
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
    toUInt64(uniqCombined(u.session_id))                            AS viewer_count,
    sum(u.seconds_observed) / 3600.0                                AS viewer_hours,
    sum(u.down_bytes_observed) / pow(1024, 3) AS egress_gb,
    uniqCombinedState(u.session_id)                                 AS unique_viewers_state,
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
    uniqCombinedState(u.session_id)                            AS unique_viewers_state,
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
    sum(media_seconds)                          AS media_seconds,
    toUInt64(uniqCombined(source_event_id))  AS segment_count,
    now64(3)                                    AS refresh_version_ms
FROM processing_5m_v
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
    sum(media_seconds) AS media_seconds,
    sum(segment_count) AS segment_count,
    now64(3)           AS refresh_version_ms
FROM processing_hourly
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
    sum(requests)    AS requests,
    sum(errors)      AS errors,
    sum(duration_ms) AS duration_ms,
    sum(complexity)  AS complexity,
    uniqCombinedMergeState(unique_users_state)  AS unique_users_state,
    uniqCombinedMergeState(unique_tokens_state) AS unique_tokens_state,
    now64(3)                                    AS refresh_version_ms
FROM api_usage_5m_v
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
    sum(seconds_observed) AS seconds_observed,
    sum(up_bytes)         AS up_bytes,
    sum(down_bytes)       AS down_bytes,
    uniqCombinedMergeState(unique_sessions_state) AS unique_sessions_state,
    uniqCombinedMergeState(unique_streams_state)  AS unique_streams_state,
    now64(3)                                      AS refresh_version_ms
FROM tenant_usage_hourly
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
    day,
    tenant_id,
    cluster_id,
    sum(seconds_observed) / 3600.0            AS viewer_hours,
    sum(down_bytes) / pow(1024, 3) AS egress_gb,
    uniqCombinedMergeState(unique_sessions_state) AS unique_viewers_state,
    uniqCombinedMerge(unique_sessions_state)  AS total_sessions,
    now64(3)                                  AS refresh_version_ms
FROM tenant_usage_daily
WHERE (day, tenant_id, cluster_id) IN (
    SELECT DISTINCT day, tenant_id, cluster_id
    FROM tenant_usage_daily
    WHERE latest_refresh_version_ms >= now64(3) - INTERVAL 7 DAY
)
GROUP BY day, tenant_id, cluster_id;

CREATE VIEW IF NOT EXISTS tenant_viewer_daily AS
SELECT
    day, tenant_id, cluster_id,
    argMax(viewer_hours,          refresh_version_ms) AS viewer_hours,
    argMax(egress_gb,             refresh_version_ms) AS egress_gb,
    argMax(unique_viewers_state,  refresh_version_ms) AS unique_viewers_state,
    toUInt64(finalizeAggregation(argMax(unique_viewers_state, refresh_version_ms))) AS unique_viewers,
    argMax(total_sessions,        refresh_version_ms) AS total_sessions,
    max(refresh_version_ms) AS latest_refresh_version_ms
FROM tenant_viewer_daily_store
GROUP BY day, tenant_id, cluster_id;

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
    day,
    tenant_id,
    uniqCombinedMerge(unique_streams_state)  AS total_streams,
    uniqCombinedMerge(unique_sessions_state) AS total_views,
    uniqCombinedMergeState(unique_sessions_state) AS unique_viewers_state,
    sum(down_bytes)                          AS egress_bytes,
    now64(3)                                 AS refresh_version_ms
FROM tenant_usage_daily
WHERE (day, tenant_id) IN (
    SELECT DISTINCT day, tenant_id
    FROM tenant_usage_daily
    WHERE latest_refresh_version_ms >= now64(3) - INTERVAL 7 DAY
)
GROUP BY day, tenant_id;

CREATE VIEW IF NOT EXISTS tenant_analytics_daily AS
SELECT
    day, tenant_id,
    argMax(total_streams,         refresh_version_ms) AS total_streams,
    argMax(total_views,           refresh_version_ms) AS total_views,
    argMax(unique_viewers_state,  refresh_version_ms) AS unique_viewers_state,
    toUInt64(finalizeAggregation(argMax(unique_viewers_state, refresh_version_ms))) AS unique_viewers,
    argMax(egress_bytes,          refresh_version_ms) AS egress_bytes,
    max(refresh_version_ms) AS latest_refresh_version_ms
FROM tenant_analytics_daily_store
GROUP BY day, tenant_id;

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
    uniqCombinedState(u.session_id)                            AS unique_viewers_state,
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
    argMax(internal_name,         refresh_version_ms) AS internal_name,
    argMax(total_views,           refresh_version_ms) AS total_views,
    argMax(unique_viewers_state,  refresh_version_ms) AS unique_viewers_state,
    toUInt64(finalizeAggregation(argMax(unique_viewers_state, refresh_version_ms))) AS unique_viewers,
    argMax(unique_countries,      refresh_version_ms) AS unique_countries,
    argMax(unique_cities,         refresh_version_ms) AS unique_cities,
    argMax(egress_bytes,          refresh_version_ms) AS egress_bytes,
    max(refresh_version_ms) AS latest_refresh_version_ms
FROM stream_analytics_daily_store
GROUP BY day, tenant_id, stream_id;

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
    uniqCombinedMerge(unique_viewers_state)      AS viewer_count,
    sum(viewer_hours)                            AS viewer_hours,
    sum(egress_gb)                               AS egress_gb,
    uniqCombinedMergeState(unique_viewers_state) AS unique_viewers_state,
    now64(3)                                     AS refresh_version_ms
FROM viewer_geo_hourly
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
    uniqCombinedMerge(unique_viewers_state)      AS viewer_count,
    sum(viewer_hours)                            AS viewer_hours,
    sum(egress_gb)                               AS egress_gb,
    uniqCombinedMergeState(unique_viewers_state) AS unique_viewers_state,
    now64(3)                                     AS refresh_version_ms
FROM viewer_city_hourly
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
    sum(runtime_seconds) AS runtime_seconds,
    max(peak_viewers)    AS peak_viewers,
    now64(3)             AS refresh_version_ms
FROM stream_runtime_hourly
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
    sum(requests)    AS requests,
    sum(errors)      AS errors,
    sum(duration_ms) AS duration_ms,
    sum(complexity)  AS complexity,
    uniqCombinedMergeState(unique_users_state)  AS unique_users_state,
    uniqCombinedMergeState(unique_tokens_state) AS unique_tokens_state,
    now64(3)                                    AS refresh_version_ms
FROM api_usage_hourly
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

-- storage_gb_seconds_5m + storage_gb_seconds_5m_v are created with
-- provider attribution in expand/002. The storage_usage_hourly /
-- storage_usage_daily rollups below are net-new in this contract pass
-- and have no legacy shape to swap from.

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
    sum(gb_seconds)            AS gb_seconds,
    sum(gb_seconds) / 3600.0   AS gb_hours,
    sum(gb_seconds) / 3600.0   AS avg_gb,
    now64(3)                   AS refresh_version_ms
FROM storage_gb_seconds_5m_v
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
    sum(gb_seconds)          AS gb_seconds,
    sum(gb_seconds) / 3600.0 AS gb_hours,
    now64(3)                 AS refresh_version_ms
FROM storage_usage_hourly
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
