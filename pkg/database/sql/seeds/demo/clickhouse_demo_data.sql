-- "Big Data" Seed for Periscope (ClickHouse)
-- Generates ~24h of high-resolution data for multiple streams and nodes to simulate a busy platform.

-- Constants
-- Tenant: 00000000-0000-0000-0000-000000000001
-- Stream: demo_live_stream_001 (standardized to match Go generators)
-- Node: edge-node-1 (Leiden)

-- =================================================================================================
-- 0. Live Streams (Real-time snapshot table - used by Platform Overview)
-- =================================================================================================
INSERT INTO periscope.live_streams (
    tenant_id, internal_name, node_id,
    status, buffer_state, current_viewers, total_inputs,
    uploaded_bytes, downloaded_bytes, viewer_seconds,
    has_issues, track_count, quality_tier,
    primary_width, primary_height, primary_fps, primary_codec, primary_bitrate,
    started_at, updated_at
) VALUES (
    '00000000-0000-0000-0000-000000000001',
    'demo_live_stream_001',
    'edge-node-1',
    'offline',   -- Stream is not live (historical data only)
    NULL,        -- No buffer state when offline
    0,           -- No current viewers
    0,           -- No inputs
    52428800000, -- uploaded_bytes (~50GB historical)
    1340000000000, -- downloaded_bytes (~1.2TB historical)
    293550,      -- viewer_seconds (historical)
    0,           -- has_issues
    2,           -- track_count
    '1080p60',   -- quality_tier (last known)
    1920,        -- primary_width
    1080,        -- primary_height
    60.0,        -- primary_fps
    'H264',      -- primary_codec
    4500000,     -- primary_bitrate
    NULL,        -- Not currently started
    now()
);

-- =================================================================================================
-- 1. Stream Health Metrics (High Res - 10s intervals for 24h)
-- =================================================================================================
INSERT INTO periscope.stream_health_metrics (
    timestamp, tenant_id, internal_name, node_id,
    bitrate, fps, width, height, codec, profile,
    buffer_health, buffer_state, packets_sent, packets_lost, packets_retransmitted,
    has_issues, issues_description, audio_codec, audio_bitrate
)
SELECT
    toDateTime(now() - INTERVAL number * 10 SECOND) as timestamp,
    '00000000-0000-0000-0000-000000000001' as tenant_id,
    'demo_live_stream_001' as internal_name,
    'edge-node-1' as node_id,

    -- Organic bitrate fluctuation (base 4.5Mbps + noise)
    toUInt32(4500000 + 500000 * sin(number/100) + rand()%200000) as bitrate,

    -- Mostly stable 60fps, occasional dips
    if(rand()%100 > 98, 55 + rand()%5, 60.0) as fps,
    1920 as width,
    1080 as height,
    'H264' as codec,
    'High' as profile,

    -- Buffer health (0-1)
    toFloat32(0.8 + 0.2 * abs(sin(number/200))) as buffer_health,
    multiIf(0.8 + 0.2 * abs(sin(number/200)) < 0.1, 'DRY', 0.8 + 0.2 * abs(sin(number/200)) < 0.5, 'RECOVER', 'FULL') as buffer_state,

    -- Packets
    toUInt64(2000 + (number % 50)) as packets_sent,
    toUInt64(if(rand()%100 > 95, rand()%20, 0)) as packets_lost,
    toUInt64(if(rand()%100 > 90, rand()%50, 0)) as packets_retransmitted,

    toUInt8(if(rand()%100 > 95 AND rand()%20 > 10, 1, 0)) as has_issues,
    if(rand()%100 > 95 AND rand()%20 > 10, 'High Packet Loss', NULL) as issues_description,
    'AAC' as audio_codec,
    192000 as audio_bitrate
FROM numbers(0, 8640); -- 24 hours * 6 blocks/min

-- =================================================================================================
-- 2. Connection Events - CONNECT events (5000 sessions)
-- =================================================================================================
INSERT INTO periscope.connection_events (
    event_id, timestamp, tenant_id, internal_name, session_id,
    connection_addr, connector, node_id,
    country_code, city, latitude, longitude,
    event_type, session_duration, bytes_transferred
)
SELECT
    generateUUIDv4() as event_id,
    -- Random time in last 24h
    toDateTime(now() - rand() % 86400) as timestamp,
    '00000000-0000-0000-0000-000000000001' as tenant_id,
    'demo_live_stream_001' as internal_name,
    concat('session-', toString(number)) as session_id,

    -- Random IP
    concat(toString(rand()%255), '.', toString(rand()%255), '.', toString(rand()%255), '.', toString(rand()%255)) as connection_addr,

    arrayElement(['HLS', 'WebRTC', 'RTMP'], 1 + rand()%3) as connector,
    'edge-node-1' as node_id,

    -- Weighted Country Distribution
    transform(
        rand() % 10,
        [0, 1, 2, 3, 4, 5, 6, 7, 8, 9],
        ['US', 'US', 'US', 'NL', 'NL', 'GB', 'DE', 'FR', 'BR', 'JP'],
        'US'
    ) as country_code,

    -- City mapping (simplified)
    transform(
        transform(rand() % 10, [0, 1, 2, 3, 4, 5, 6, 7, 8, 9], ['US', 'US', 'US', 'NL', 'NL', 'GB', 'DE', 'FR', 'BR', 'JP'], 'US'),
        ['US', 'NL', 'GB', 'DE', 'FR', 'BR', 'JP'],
        ['New York', 'Amsterdam', 'London', 'Berlin', 'Paris', 'Sao Paulo', 'Tokyo'],
        'Unknown'
    ) as city,

    -- Lat/Lon centers with jitter
    transform(
        transform(rand() % 10, [0, 1, 2, 3, 4, 5, 6, 7, 8, 9], ['US', 'US', 'US', 'NL', 'NL', 'GB', 'DE', 'FR', 'BR', 'JP'], 'US'),
        ['US', 'NL', 'GB', 'DE', 'FR', 'BR', 'JP'],
        [40.71, 52.36, 51.50, 52.52, 48.85, -23.55, 35.67],
        0.0
    ) + (rand()%100 - 50)/100.0 as latitude,
    transform(
        transform(rand() % 10, [0, 1, 2, 3, 4, 5, 6, 7, 8, 9], ['US', 'US', 'US', 'NL', 'NL', 'GB', 'DE', 'FR', 'BR', 'JP'], 'US'),
        ['US', 'NL', 'GB', 'DE', 'FR', 'BR', 'JP'],
        [-74.00, 4.90, -0.12, 13.40, 2.35, -46.63, 139.65],
        0.0
    ) + (rand()%100 - 50)/100.0 as longitude,

    'connect' as event_type,
    0 as session_duration,
    0 as bytes_transferred
FROM numbers(0, 5000);

-- =================================================================================================
-- 2b. Connection Events - DISCONNECT events (required for billing MVs)
-- =================================================================================================
INSERT INTO periscope.connection_events (
    event_id, timestamp, tenant_id, internal_name, session_id,
    connection_addr, connector, node_id,
    country_code, city, latitude, longitude,
    event_type, session_duration, bytes_transferred
)
SELECT
    generateUUIDv4() as event_id,
    -- Disconnect happens 1-60 minutes after connect
    ce.timestamp + INTERVAL (60 + rand()%3540) SECOND as timestamp,
    ce.tenant_id,
    ce.internal_name,
    ce.session_id,
    ce.connection_addr,
    ce.connector,
    ce.node_id,
    ce.country_code,
    ce.city,
    ce.latitude,
    ce.longitude,
    'disconnect' as event_type,
    60 + rand()%3540 as session_duration,  -- Session duration in seconds (1-60 minutes)
    (50 + rand()%450) * 1000000 as bytes_transferred  -- 50-500 MB transferred
FROM periscope.connection_events ce
WHERE ce.event_type = 'connect'
  AND ce.internal_name = 'demo_live_stream_001';

-- =================================================================================================
-- 3. Routing Events (For Network Map Lines)
-- Includes H3 bucket indices at resolution 5 (~25km hexagons)
-- H3 indices pre-computed using scripts/h3calc (uber/h3-go)
--
-- Cities (36 total across 6 regions):
--   NA: NYC, LA, CHI, MIA, SEA, TOR
--   EU: AMS, RTM, LON, MAN, BER, MUN, FRA, PAR, LYO, MAD, BCN, ROM, MIL, STO, WAR
--   APAC: TKY, OSA, SEO, SIN, SYD, MEL, MUM, BLR
--   SA: SAO, RIO, BUE, SCL
--   MEA: DXB, JNB, CAI
--
-- Edge Nodes (6):
--   edge-leiden (NL), edge-ashburn (US-VA), edge-frankfurt (DE),
--   edge-singapore (SG), edge-tokyo (JP), edge-saopaulo (BR)
-- =================================================================================================
INSERT INTO periscope.routing_events (
    timestamp, tenant_id, internal_name, selected_node, status, details, score,
    client_ip, client_country, client_latitude, client_longitude,
    client_bucket_h3, client_bucket_res,
    node_latitude, node_longitude, node_name,
    node_bucket_h3, node_bucket_res,
    routing_distance_km
)
SELECT
    toDateTime(now() - rand() % 86400) as timestamp,
    '00000000-0000-0000-0000-000000000001' as tenant_id,
    'demo_live_stream_001' as internal_name,

    -- Select nearest edge node based on city region
    arrayElement(
        ['edge-ashburn', 'edge-ashburn', 'edge-ashburn', 'edge-ashburn', 'edge-ashburn', 'edge-ashburn',  -- NA cities -> Ashburn
         'edge-leiden', 'edge-leiden', 'edge-leiden', 'edge-leiden',  -- NL/GB -> Leiden
         'edge-frankfurt', 'edge-frankfurt', 'edge-frankfurt', 'edge-frankfurt', 'edge-frankfurt',  -- DE/FR/ES/IT -> Frankfurt
         'edge-frankfurt', 'edge-frankfurt', 'edge-frankfurt', 'edge-frankfurt', 'edge-frankfurt', 'edge-frankfurt',  -- More EU -> Frankfurt
         'edge-tokyo', 'edge-tokyo', 'edge-tokyo',  -- JP/KR -> Tokyo
         'edge-singapore', 'edge-singapore', 'edge-singapore', 'edge-singapore', 'edge-singapore',  -- SG/AU/IN -> Singapore
         'edge-saopaulo', 'edge-saopaulo', 'edge-saopaulo', 'edge-saopaulo',  -- SA -> Sao Paulo
         'edge-frankfurt', 'edge-frankfurt', 'edge-frankfurt'],  -- MEA -> Frankfurt (closest)
        1 + (number % 36)
    ) as selected_node,

    'success' as status,
    'geo-proximity' as details,
    800 + rand() % 200 as score,  -- High score (good routing)

    -- Client IP
    concat(toString(10 + rand()%240), '.', toString(rand()%255), '.', toString(rand()%255), '.', toString(number%255)) as client_ip,

    -- Client country (from city index)
    arrayElement(
        ['US', 'US', 'US', 'US', 'US', 'CA',  -- NA
         'NL', 'NL', 'GB', 'GB',  -- NL/GB
         'DE', 'DE', 'DE', 'FR', 'FR', 'ES', 'ES', 'IT', 'IT', 'SE', 'PL',  -- EU
         'JP', 'JP', 'KR',  -- East Asia
         'SG', 'AU', 'AU', 'IN', 'IN',  -- APAC
         'BR', 'BR', 'AR', 'CL',  -- SA
         'AE', 'ZA', 'EG'],  -- MEA
        1 + (number % 36)
    ) as client_country,

    -- Client latitude (with small jitter)
    arrayElement(
        [40.71, 34.05, 41.88, 25.76, 47.61, 43.65,  -- NA
         52.36, 51.92, 51.50, 53.48,  -- NL/GB
         52.52, 48.14, 50.11, 48.85, 45.76, 40.42, 41.39, 41.90, 45.46, 59.33, 52.23,  -- EU
         35.67, 34.69, 37.57,  -- East Asia
         1.35, -33.87, -37.81, 19.08, 12.97,  -- APAC
         -23.55, -22.91, -34.60, -33.45,  -- SA
         25.20, -26.20, 30.04],  -- MEA
        1 + (number % 36)
    ) + (rand() % 100 - 50) / 500.0 as client_latitude,

    -- Client longitude (with small jitter)
    arrayElement(
        [-74.01, -118.24, -87.63, -80.19, -122.33, -79.38,  -- NA
         4.90, 4.48, -0.12, -2.24,  -- NL/GB
         13.40, 11.58, 8.68, 2.35, 4.84, -3.70, 2.17, 12.50, 9.19, 18.07, 21.01,  -- EU
         139.65, 135.50, 126.98,  -- East Asia
         103.82, 151.21, 144.96, 72.88, 77.59,  -- APAC
         -46.63, -43.17, -58.38, -70.67,  -- SA
         55.27, 28.04, 31.24],  -- MEA
        1 + (number % 36)
    ) + (rand() % 100 - 50) / 500.0 as client_longitude,

    -- Client H3 bucket (pre-computed via scripts/h3calc, resolution 5)
    arrayElement(
        [599718752904282111, 599711151885910015, 599654178070986751, 600186088295759871, 599697093384208383, 599745919646171135,  -- NA
         599425793185021951, 599425957467521023, 599423697240981503, 599424170761125887,  -- NL/GB
         599526121473572863, 599533816981225471, 599536111567503359, 599536505630752767, 599534014549721087,  -- DE/FR
         599982373701943295, 599986204812771327, 599515334663208959, 599534678122168319, 599128723182059519, 599529866685054975,  -- ES/IT/SE/PL
         599811782969655295, 599794658935046143, 599838696308473855,  -- JP/KR
         600757819309817855, 602322242893774847, 602328092639231999, 600677175930126335, 600668999386136575,  -- SG/AU/IN
         601935341502332927, 601945261803044863, 602407239222820863, 602123720915419135,  -- SA
         600168505773391871, 602299504263167999, 600076239138455551],  -- MEA
        1 + (number % 36)
    ) as client_bucket_h3,
    toUInt8(5) as client_bucket_res,

    -- Node latitude (based on selected node)
    arrayElement(
        [39.04, 39.04, 39.04, 39.04, 39.04, 39.04,  -- Ashburn
         52.16, 52.16, 52.16, 52.16,  -- Leiden
         50.11, 50.11, 50.11, 50.11, 50.11, 50.11, 50.11, 50.11, 50.11, 50.11, 50.11,  -- Frankfurt
         35.69, 35.69, 35.69,  -- Tokyo
         1.35, 1.35, 1.35, 1.35, 1.35,  -- Singapore
         -23.55, -23.55, -23.55, -23.55,  -- Sao Paulo
         50.11, 50.11, 50.11],  -- Frankfurt (MEA)
        1 + (number % 36)
    ) as node_latitude,

    -- Node longitude
    arrayElement(
        [-77.49, -77.49, -77.49, -77.49, -77.49, -77.49,  -- Ashburn
         4.50, 4.50, 4.50, 4.50,  -- Leiden
         8.68, 8.68, 8.68, 8.68, 8.68, 8.68, 8.68, 8.68, 8.68, 8.68, 8.68,  -- Frankfurt
         139.69, 139.69, 139.69,  -- Tokyo
         103.82, 103.82, 103.82, 103.82, 103.82,  -- Singapore
         -46.63, -46.63, -46.63, -46.63,  -- Sao Paulo
         8.68, 8.68, 8.68],  -- Frankfurt (MEA)
        1 + (number % 36)
    ) as node_longitude,

    -- Node name
    arrayElement(
        ['edge-ashburn', 'edge-ashburn', 'edge-ashburn', 'edge-ashburn', 'edge-ashburn', 'edge-ashburn',
         'edge-leiden', 'edge-leiden', 'edge-leiden', 'edge-leiden',
         'edge-frankfurt', 'edge-frankfurt', 'edge-frankfurt', 'edge-frankfurt', 'edge-frankfurt',
         'edge-frankfurt', 'edge-frankfurt', 'edge-frankfurt', 'edge-frankfurt', 'edge-frankfurt', 'edge-frankfurt',
         'edge-tokyo', 'edge-tokyo', 'edge-tokyo',
         'edge-singapore', 'edge-singapore', 'edge-singapore', 'edge-singapore', 'edge-singapore',
         'edge-saopaulo', 'edge-saopaulo', 'edge-saopaulo', 'edge-saopaulo',
         'edge-frankfurt', 'edge-frankfurt', 'edge-frankfurt'],
        1 + (number % 36)
    ) as node_name,

    -- Node H3 bucket (pre-computed via scripts/h3calc)
    arrayElement(
        [599729341072408575, 599729341072408575, 599729341072408575, 599729341072408575, 599729341072408575, 599729341072408575,  -- Ashburn
         599425791037538303, 599425791037538303, 599425791037538303, 599425791037538303,  -- Leiden
         599536111567503359, 599536111567503359, 599536111567503359, 599536111567503359, 599536111567503359,  -- Frankfurt
         599536111567503359, 599536111567503359, 599536111567503359, 599536111567503359, 599536111567503359, 599536111567503359,
         599811782969655295, 599811782969655295, 599811782969655295,  -- Tokyo
         600757819309817855, 600757819309817855, 600757819309817855, 600757819309817855, 600757819309817855,  -- Singapore
         601935341502332927, 601935341502332927, 601935341502332927, 601935341502332927,  -- Sao Paulo
         599536111567503359, 599536111567503359, 599536111567503359],  -- Frankfurt (MEA)
        1 + (number % 36)
    ) as node_bucket_h3,
    toUInt8(5) as node_bucket_res,

    -- Approximate routing distance (km) based on city-to-node
    toFloat64(arrayElement(
        [350, 3900, 1100, 1600, 3900, 550,  -- NA to Ashburn
         170, 80, 350, 500,  -- NL/GB to Leiden
         400, 300, 0, 450, 500, 1500, 1200, 1000, 600, 1600, 1000,  -- EU to Frankfurt
         0, 400, 1200,  -- JP/KR to Tokyo
         0, 6300, 6000, 4100, 3900,  -- APAC to Singapore
         0, 350, 2100, 2900,  -- SA to Sao Paulo
         5200, 8500, 2900],  -- MEA to Frankfurt
        1 + (number % 36)
    ) + rand() % 50) as routing_distance_km

FROM numbers(0, 3600);  -- 100 events per city = 3600 total

-- =================================================================================================
-- 4. Track List Events (Stream Health Tab)
-- =================================================================================================
INSERT INTO periscope.track_list_events (
    timestamp, event_id, tenant_id, internal_name, node_id,
    track_list, track_count, video_track_count, audio_track_count,
    primary_width, primary_height, primary_fps, primary_video_codec, primary_video_bitrate,
    quality_tier
)
VALUES
(now(), generateUUIDv4(), '00000000-0000-0000-0000-000000000001', 'demo_live_stream_001', 'edge-node-1',
 '[{"trackName":"video_1","trackType":"video","codec":"H264","width":1920,"height":1080,"fps":60,"bitrateKbps":4500},{"trackName":"audio_1","trackType":"audio","codec":"AAC"}]',
 2, 1, 1, 1920, 1080, 60.0, 'H264', 4500000, '1080p60');

-- =================================================================================================
-- 5. Node Metrics (Infrastructure Dashboard)
-- =================================================================================================
INSERT INTO periscope.node_metrics (
    timestamp, tenant_id, node_id,
    cpu_usage, ram_max, ram_current,
    bandwidth_in, bandwidth_out, is_healthy,
    latitude, longitude
)
SELECT
    toDateTime(now() - INTERVAL number MINUTE) as timestamp,
    '00000000-0000-0000-0000-000000000001' as tenant_id,
    'edge-node-1' as node_id,

    -- Realistic load patterns
    toFloat32(30 + 20 * sin(number/120) + rand()%10) as cpu_usage,
    16000000000 as ram_max,
    4000000000 + (number * 50000) % 8000000000 as ram_current,

    -- Bandwidth traffic
    100000000 + (rand() % 50000000) as bandwidth_in,
    500000000 + (rand() % 200000000) as bandwidth_out,

    1 as is_healthy,
    52.1601 as latitude,
    4.4970 as longitude
FROM numbers(0, 1440);

-- =================================================================================================
-- 6. Stream Events (Lifecycle Events for Stream Timeline + Viewer Snapshots)
-- =================================================================================================
-- 6a. Push start/end events
INSERT INTO periscope.stream_events (
    timestamp, event_id, tenant_id, internal_name, node_id,
    event_type, event_data
)
SELECT
    toDateTime(now() - INTERVAL number * 4 HOUR) as timestamp,
    generateUUIDv4() as event_id,
    '00000000-0000-0000-0000-000000000001' as tenant_id,
    'demo_live_stream_001' as internal_name,
    'edge-node-1' as node_id,
    if(number % 2 = 0, 'PUSH_START', 'PUSH_END') as event_type,
    if(number % 2 = 0, '{"codec":"H264","resolution":"1920x1080"}', '{"duration_seconds":14400}') as event_data
FROM numbers(0, 12);

-- 6b. Viewer snapshot events (for peak concurrent and usage metrics)
-- These record the current viewer count at each interval
INSERT INTO periscope.stream_events (
    timestamp, event_id, tenant_id, internal_name, node_id,
    event_type, total_viewers
)
SELECT
    toDateTime(now() - INTERVAL number * 5 MINUTE) as timestamp,
    generateUUIDv4() as event_id,
    '00000000-0000-0000-0000-000000000001' as tenant_id,
    'demo_live_stream_001' as internal_name,
    'edge-node-1' as node_id,
    'STREAM_BUFFER' as event_type,
    -- Simulate realistic viewer patterns: base 30-50 with peaks up to 89
    toUInt32(30 + abs(50 * sin(number / 20.0)) + rand() % 10) as total_viewers
FROM numbers(0, 288);

-- =================================================================================================
-- 7. Client Metrics (5-minute aggregates for client-side QoE)
-- =================================================================================================
INSERT INTO periscope.client_metrics (
    timestamp, tenant_id, internal_name, node_id, session_id,
    protocol, host, connection_time,
    bandwidth_in, bandwidth_out, bytes_downloaded, bytes_uploaded,
    packets_sent, packets_lost, packets_retransmitted, connection_quality
)
SELECT
    toDateTime(now() - INTERVAL number * 5 MINUTE) as timestamp,
    '00000000-0000-0000-0000-000000000001' as tenant_id,
    'demo_live_stream_001' as internal_name,
    'edge-node-1' as node_id,
    concat('session-', toString(rand() % 5000)) as session_id,
    arrayElement(['HLS', 'WebRTC', 'DASH'], 1 + rand()%3) as protocol,
    'edge-node-1.demo.frameworks.network' as host,
    toFloat32(0.5 + rand() % 200 / 100.0) as connection_time,
    toUInt64(1000000 + rand() % 5000000) as bandwidth_in,
    toUInt64(50000 + rand() % 500000) as bandwidth_out,
    toUInt64(50000000 + rand() % 500000000) as bytes_downloaded,
    toUInt64(1000000 + rand() % 10000000) as bytes_uploaded,
    toUInt64(2000 + rand() % 500) as packets_sent,
    toUInt64(if(rand() % 100 > 95, rand() % 20, 0)) as packets_lost,
    toUInt64(if(rand() % 100 > 90, rand() % 50, 0)) as packets_retransmitted,
    toFloat32(0.85 + 0.15 * rand() / 4294967295) as connection_quality
FROM numbers(0, 288); -- 24h at 5min intervals

-- =================================================================================================
-- 8. Clip Events (For Clips/DVR pages)
-- Note: Separate INSERTs because ClickHouse doesn't allow comments between VALUES rows
-- =================================================================================================

-- Clip 1: Completed
INSERT INTO periscope.clip_events (
    timestamp, tenant_id, internal_name, request_id,
    stage, content_type, start_unix, stop_unix,
    ingest_node_id, percent, message, file_path, s3_url, size_bytes
) VALUES (
    now() - INTERVAL 2 HOUR,
    '00000000-0000-0000-0000-000000000001',
    'demo_live_stream_001',
    'clip_demo_001',
    'completed',
    'clip',
    toUnixTimestamp(now() - INTERVAL 3 HOUR),
    toUnixTimestamp(now() - INTERVAL 2 HOUR - INTERVAL 55 MINUTE),
    'edge-node-1',
    100,
    'Clip creation completed successfully',
    '/clips/demo_clip_001.mp4',
    's3://demo-bucket/clips/demo_clip_001.mp4',
    125000000
);

-- Clip 2: Processing
INSERT INTO periscope.clip_events (
    timestamp, tenant_id, internal_name, request_id,
    stage, content_type, start_unix, stop_unix,
    ingest_node_id, percent, message, file_path, s3_url, size_bytes
) VALUES (
    now() - INTERVAL 30 MINUTE,
    '00000000-0000-0000-0000-000000000001',
    'demo_live_stream_001',
    'clip_demo_002',
    'processing',
    'clip',
    toUnixTimestamp(now() - INTERVAL 1 HOUR),
    toUnixTimestamp(now() - INTERVAL 55 MINUTE),
    'edge-node-1',
    65,
    'Encoding video track...',
    NULL,
    NULL,
    NULL
);

-- DVR Recording: In Progress
INSERT INTO periscope.clip_events (
    timestamp, tenant_id, internal_name, request_id,
    stage, content_type, start_unix, stop_unix,
    ingest_node_id, percent, message, file_path, s3_url, size_bytes
) VALUES (
    now() - INTERVAL 10 MINUTE,
    '00000000-0000-0000-0000-000000000001',
    'demo_live_stream_001',
    'dvr_demo_001',
    'recording',
    'dvr',
    toUnixTimestamp(now() - INTERVAL 2 HOUR),
    NULL,
    'edge-node-1',
    NULL,
    'DVR recording in progress',
    '/dvr/demo_live_stream_001/',
    NULL,
    450000000
);

-- =================================================================================================
-- 9. Storage Snapshots (For Storage Usage page)
-- =================================================================================================
INSERT INTO periscope.storage_snapshots (
    timestamp, tenant_id, node_id,
    total_bytes, file_count, dvr_bytes, clip_bytes, recording_bytes
)
SELECT
    toDateTime(now() - INTERVAL number HOUR) as timestamp,
    '00000000-0000-0000-0000-000000000001' as tenant_id,
    'edge-node-1' as node_id,
    -- Growing storage over time
    40000000000 + number * 500000000 as total_bytes,
    140 + number * 2 as file_count,
    20000000000 + number * 250000000 as dvr_bytes,
    8000000000 + number * 100000000 as clip_bytes,
    12000000000 + number * 150000000 as recording_bytes
FROM numbers(0, 24);

-- =================================================================================================
-- 10. Quality Tier Daily (Direct insert for aggregation table - MVs don't backfill)
-- =================================================================================================
INSERT INTO periscope.quality_tier_daily (
    day, tenant_id, internal_name,
    tier_1080p_minutes, tier_720p_minutes, tier_480p_minutes, tier_sd_minutes,
    primary_tier, codec_h264_minutes, codec_h265_minutes, avg_bitrate, avg_fps
)
SELECT
    toDate(now() - INTERVAL number DAY) as day,
    '00000000-0000-0000-0000-000000000001' as tenant_id,
    'demo_live_stream_001' as internal_name,
    -- Primarily 1080p streaming
    toUInt32(180 + rand() % 60) as tier_1080p_minutes,
    toUInt32(30 + rand() % 30) as tier_720p_minutes,
    toUInt32(10 + rand() % 15) as tier_480p_minutes,
    toUInt32(5 + rand() % 10) as tier_sd_minutes,
    '1080p60' as primary_tier,
    toUInt32(200 + rand() % 40) as codec_h264_minutes,
    toUInt32(20 + rand() % 20) as codec_h265_minutes,
    4500000 as avg_bitrate,
    60.0 as avg_fps
FROM numbers(0, 7);

-- =================================================================================================
-- 11. Tenant Viewer Daily (Direct insert for platform overview)
-- =================================================================================================
INSERT INTO periscope.tenant_viewer_daily (
    day, tenant_id,
    egress_gb, viewer_hours, unique_viewers, total_sessions
)
SELECT
    toDate(now() - INTERVAL number DAY) as day,
    '00000000-0000-0000-0000-000000000001' as tenant_id,
    -- ~180GB egress per day
    toFloat64(150 + rand() % 60) as egress_gb,
    -- ~700 viewer hours per day
    toFloat64(600 + rand() % 200) as viewer_hours,
    -- ~350 unique viewers per day
    toUInt32(300 + rand() % 100) as unique_viewers,
    -- ~500 sessions per day
    toUInt32(400 + rand() % 200) as total_sessions
FROM numbers(0, 30);

-- =================================================================================================
-- Trigger Materialized View Aggregations
-- =================================================================================================
OPTIMIZE TABLE periscope.stream_health_5m FINAL;
OPTIMIZE TABLE periscope.client_metrics_5m FINAL;
OPTIMIZE TABLE periscope.node_metrics_1h FINAL;
OPTIMIZE TABLE periscope.stream_connection_hourly FINAL;
OPTIMIZE TABLE periscope.quality_tier_daily FINAL;
OPTIMIZE TABLE periscope.tenant_connection_daily FINAL;
OPTIMIZE TABLE periscope.clip_events_1h FINAL;
OPTIMIZE TABLE periscope.viewer_hours_hourly FINAL;
OPTIMIZE TABLE periscope.tenant_viewer_daily FINAL;
OPTIMIZE TABLE periscope.viewer_geo_hourly FINAL;
OPTIMIZE TABLE periscope.live_streams FINAL;
