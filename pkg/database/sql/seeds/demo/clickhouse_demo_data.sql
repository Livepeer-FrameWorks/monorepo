-- Periscope V2 Demo Seed (ClickHouse)
-- Generates recent time-series + lifecycle data for demo tenants, streams, and nodes.

-- Constants
-- Tenant: 5eed517e-ba5e-da7a-517e-ba5eda7a0001
-- Stream ID: 5eedfeed-11fe-ca57-feed-11feca570001
-- Internal Name: demo_live_stream_001
-- Cluster: central-primary

-- =================================================================================================
-- 0. Stream Current State (stream_state_current)
-- =================================================================================================
INSERT INTO periscope.stream_state_current (
    tenant_id, stream_id, internal_name, node_id,
    status, buffer_state,
    current_viewers, total_inputs,
    uploaded_bytes, downloaded_bytes, viewer_seconds,
    has_issues, issues_description, track_count, quality_tier,
    primary_width, primary_height, primary_fps, primary_codec, primary_bitrate,
    packets_sent, packets_lost, packets_retransmitted,
    started_at, updated_at
) VALUES (
    '5eed517e-ba5e-da7a-517e-ba5eda7a0001',
    '5eedfeed-11fe-ca57-feed-11feca570001',
    'demo_live_stream_001',
    'edge-node-1',
    'offline',
    'EMPTY',
    0,
    0,
    0,
    0,
    0,
    0,
    NULL,
    0,
    NULL,
    0,
    0,
    0.0,
    NULL,
    0,
    0,
    0,
    0,
    NULL,
    now()
);

-- =================================================================================================
-- 0b. Node Current State (node_state_current)
-- =================================================================================================
INSERT INTO periscope.node_state_current (
    tenant_id, cluster_id, node_id,
    cpu_percent, ram_used_bytes, ram_total_bytes,
    disk_used_bytes, disk_total_bytes,
    up_speed, down_speed,
    active_streams, is_healthy,
    latitude, longitude, location,
    metadata, updated_at
) VALUES
(   -- Local dev node (Leiden edge)
    '5eed517e-ba5e-da7a-517e-ba5eda7a0001', 'demo-media', 'edge-node-1',
    15.2, 2100000000, 16000000000,
    45000000000, 500000000000,
    0, 0,
    0, 1,
    52.1601, 4.4970, 'Leiden',
    '{"region":"eu-west","node_name":"edge-node-1"}',
    now()
);

-- Regional nodes (offline for routing map visuals, show historical presence)
INSERT INTO periscope.node_state_current (
    tenant_id, cluster_id, node_id,
    cpu_percent, ram_used_bytes, ram_total_bytes,
    disk_used_bytes, disk_total_bytes,
    up_speed, down_speed,
    active_streams, is_healthy,
    latitude, longitude, location,
    metadata, updated_at
) VALUES
(
    '5eed517e-ba5e-da7a-517e-ba5eda7a0001', 'demo-media', 'edge-ashburn',
    0, 0, 16000000000,
    0, 500000000000,
    0, 0,
    0, 0,
    39.0438, -77.4874, 'Ashburn',
    '{"region":"us-east","node_name":"edge-ashburn"}',
    now() - INTERVAL 1 DAY
),
(
    '5eed517e-ba5e-da7a-517e-ba5eda7a0001', 'demo-media', 'edge-singapore',
    0, 0, 16000000000,
    0, 500000000000,
    0, 0,
    0, 0,
    1.3521, 103.8198, 'Singapore',
    '{"region":"apac","node_name":"edge-singapore"}',
    now() - INTERVAL 1 DAY
);

-- =================================================================================================
-- 1. Stream Event Log (stream_event_log)
-- =================================================================================================
INSERT INTO periscope.stream_event_log (
    event_id, timestamp, tenant_id, stream_id, internal_name, node_id,
    event_type, status, buffer_state,
    track_count, quality_tier, primary_width, primary_height, primary_fps, primary_codec, primary_bitrate,
    total_viewers, total_inputs, total_outputs, viewer_seconds,
    request_url, protocol, latitude, longitude, location, country_code, city,
    event_data
) VALUES
(
    generateUUIDv4(), now() - INTERVAL 3 HOUR,
    '5eed517e-ba5e-da7a-517e-ba5eda7a0001', '5eedfeed-11fe-ca57-feed-11feca570001',
    'demo_live_stream_001', 'edge-node-1',
    'stream_start', 'online', 'FULL',
    2, '1080p60', 1920, 1080, 60.0, 'H264', 4500,
    0, 1, 2, 0,
    '/live/demo_live_stream_001/index.m3u8', 'HLS', 52.1601, 4.4970, 'Leiden', 'NL', 'Leiden',
    '{"event":"stream_start","status":"online"}'
),
(
    generateUUIDv4(), now() - INTERVAL 2 HOUR,
    '5eed517e-ba5e-da7a-517e-ba5eda7a0001', '5eedfeed-11fe-ca57-feed-11feca570001',
    'demo_live_stream_001', 'edge-node-1',
    'stream_buffer', 'online', 'RECOVER',
    2, '1080p60', 1920, 1080, 60.0, 'H264', 4500,
    18, 1, 2, 7520,
    '/live/demo_live_stream_001/index.m3u8', 'HLS', 52.1601, 4.4970, 'Leiden', 'NL', 'Leiden',
    '{"event":"stream_buffer","buffer_state":"RECOVER"}'
),
(
    generateUUIDv4(), now() - INTERVAL 90 MINUTE,
    '5eed517e-ba5e-da7a-517e-ba5eda7a0001', '5eedfeed-11fe-ca57-feed-11feca570001',
    'demo_live_stream_001', 'edge-node-1',
    'track_list_update', 'online', 'FULL',
    2, '1080p60', 1920, 1080, 60.0, 'H264', 4500,
    24, 1, 2, 14500,
    '/live/demo_live_stream_001/index.m3u8', 'HLS', 52.1601, 4.4970, 'Leiden', 'NL', 'Leiden',
    '{"event":"track_list_update"}'
),
(
    generateUUIDv4(), now() - INTERVAL 30 MINUTE,
    '5eed517e-ba5e-da7a-517e-ba5eda7a0001', '5eedfeed-11fe-ca57-feed-11feca570001',
    'demo_live_stream_001', 'edge-node-1',
    'stream_end', 'offline', 'EMPTY',
    2, '1080p60', 1920, 1080, 60.0, 'H264', 4500,
    0, 0, 0, 293550,
    '/live/demo_live_stream_001/index.m3u8', 'HLS', 52.1601, 4.4970, 'Leiden', 'NL', 'Leiden',
    '{"event":"stream_end","status":"offline"}'
);

-- =================================================================================================
-- 2. Stream Health Samples (stream_health_samples) - 7 days of 1-minute samples
-- =================================================================================================
INSERT INTO periscope.stream_health_samples (
    timestamp, tenant_id, stream_id, internal_name, node_id,
    bitrate, fps, gop_size, width, height,
    buffer_size, buffer_health, buffer_state,
    codec, quality_tier, track_metadata,
    frame_ms_max, frame_ms_min, frames_max, frames_min,
    keyframe_ms_max, keyframe_ms_min,
    issues_description, has_issues, track_count,
    audio_channels, audio_sample_rate, audio_codec, audio_bitrate
)
SELECT
    toDateTime(now() - INTERVAL number MINUTE) as timestamp,
    '5eed517e-ba5e-da7a-517e-ba5eda7a0001' as tenant_id,
    '5eedfeed-11fe-ca57-feed-11feca570001' as stream_id,
    'demo_live_stream_001' as internal_name,
    'edge-node-1' as node_id,

    toUInt32(4500 + 500 * sin(number/120) + rand()%200) as bitrate,
    if(rand()%100 > 98, 55 + rand()%5, 60.0) as fps,
    60 as gop_size,
    1920 as width,
    1080 as height,

    toUInt32(2000 + rand()%400) as buffer_size,
    toFloat32(0.8 + 0.2 * abs(sin(number/200))) as buffer_health,
    multiIf(0.8 + 0.2 * abs(sin(number/200)) < 0.1, 'DRY', 0.8 + 0.2 * abs(sin(number/200)) < 0.5, 'RECOVER', 'FULL') as buffer_state,

    'H264' as codec,
    '1080p60' as quality_tier,
    '{"video":[{"codec":"H264","fps":60,"bitrate_kbps":4500,"width":1920,"height":1080}],"audio":[{"codec":"AAC"}]}' as track_metadata,

    toFloat32(25 + rand()%5) as frame_ms_max,
    toFloat32(12 + rand()%3) as frame_ms_min,
    toUInt32(120 + rand()%10) as frames_max,
    toUInt32(110 + rand()%10) as frames_min,

    toFloat32(200 + rand()%50) as keyframe_ms_max,
    toFloat32(140 + rand()%30) as keyframe_ms_min,

    if(rand()%100 > 95, 'High Packet Loss', NULL) as issues_description,
    toUInt8(if(rand()%100 > 95, 1, 0)) as has_issues,
    2 as track_count,

    2 as audio_channels,
    48000 as audio_sample_rate,
    'AAC' as audio_codec,
    192000 as audio_bitrate
FROM numbers(0, 10080);

-- =================================================================================================
-- 3. Viewer Connection Events (viewer_connection_events) - 7 days of connect + disconnect pairs
-- Geographic distribution: US (40%), NL (20%), GB (15%), DE (10%), JP (10%), SG (5%)
-- =================================================================================================
INSERT INTO periscope.viewer_connection_events (
    event_id, timestamp, tenant_id, stream_id, internal_name, session_id,
    connection_addr, connector, node_id, request_url,
    country_code, city, latitude, longitude,
    client_bucket_h3, client_bucket_res, node_bucket_h3, node_bucket_res,
    event_type, session_duration, bytes_transferred
)
SELECT
    generateUUIDv4() as event_id,
    toDateTime(now() - INTERVAL (number * 5) MINUTE) as timestamp,
    '5eed517e-ba5e-da7a-517e-ba5eda7a0001' as tenant_id,
    '5eedfeed-11fe-ca57-feed-11feca570001' as stream_id,
    'demo_live_stream_001' as internal_name,
    concat('session-', toString(number)) as session_id,
    concat(toString(10 + rand()%240), '.', toString(rand()%255), '.', toString(rand()%255), '.', toString(number%255)) as connection_addr,
    arrayElement(['HLS', 'WebRTC', 'RTMP'], 1 + rand()%3) as connector,
    arrayElement(['edge-node-1', 'edge-ashburn', 'edge-singapore'], 1 + rand()%3) as node_id,
    '/live/demo_live_stream_001/index.m3u8' as request_url,
    arrayElement(['US', 'US', 'US', 'US', 'NL', 'NL', 'GB', 'GB', 'DE', 'JP', 'JP', 'SG'], 1 + number%12) as country_code,
    multiIf(
        number%12 < 4, arrayElement(['New York', 'Los Angeles', 'Chicago', 'San Francisco'], 1 + number%4),
        number%12 < 6, arrayElement(['Amsterdam', 'Rotterdam'], 1 + number%2),
        number%12 < 8, arrayElement(['London', 'Manchester'], 1 + number%2),
        number%12 < 9, arrayElement(['Berlin', 'Munich'], 1 + number%2),
        number%12 < 11, arrayElement(['Tokyo', 'Osaka'], 1 + number%2),
        'Singapore'
    ) as city,
    multiIf(
        number%12 < 4, arrayElement([40.71, 34.05, 41.88, 37.77], 1 + number%4) + (rand()%100 - 50)/1000.0,
        number%12 < 6, 52.37 + (rand()%100 - 50)/1000.0,
        number%12 < 8, 51.51 + (rand()%100 - 50)/1000.0,
        number%12 < 9, 52.52 + (rand()%100 - 50)/1000.0,
        number%12 < 11, 35.68 + (rand()%100 - 50)/1000.0,
        1.35 + (rand()%100 - 50)/1000.0
    ) as latitude,
    multiIf(
        number%12 < 4, arrayElement([-74.00, -118.24, -87.63, -122.42], 1 + number%4) + (rand()%100 - 50)/1000.0,
        number%12 < 6, 4.90 + (rand()%100 - 50)/1000.0,
        number%12 < 8, -0.13 + (rand()%100 - 50)/1000.0,
        number%12 < 9, 13.40 + (rand()%100 - 50)/1000.0,
        number%12 < 11, 139.69 + (rand()%100 - 50)/1000.0,
        103.82 + (rand()%100 - 50)/1000.0
    ) as longitude,
    geoToH3(longitude, latitude, 5) as client_bucket_h3,
    5 as client_bucket_res,
    NULL as node_bucket_h3,
    NULL as node_bucket_res,
    'connect' as event_type,
    0 as session_duration,
    0 as bytes_transferred
FROM numbers(0, 2016);

INSERT INTO periscope.viewer_connection_events (
    event_id, timestamp, tenant_id, stream_id, internal_name, session_id,
    connection_addr, connector, node_id, request_url,
    country_code, city, latitude, longitude,
    client_bucket_h3, client_bucket_res, node_bucket_h3, node_bucket_res,
    event_type, session_duration, bytes_transferred
)
SELECT
    generateUUIDv4() as event_id,
    toDateTime(now() - INTERVAL (number * 5) MINUTE) + INTERVAL (30 + rand()%600) SECOND as timestamp,
    '5eed517e-ba5e-da7a-517e-ba5eda7a0001' as tenant_id,
    '5eedfeed-11fe-ca57-feed-11feca570001' as stream_id,
    'demo_live_stream_001' as internal_name,
    concat('session-', toString(number)) as session_id,
    concat(toString(10 + rand()%240), '.', toString(rand()%255), '.', toString(rand()%255), '.', toString(number%255)) as connection_addr,
    arrayElement(['HLS', 'WebRTC', 'RTMP'], 1 + rand()%3) as connector,
    arrayElement(['edge-node-1', 'edge-ashburn', 'edge-singapore'], 1 + rand()%3) as node_id,
    '/live/demo_live_stream_001/index.m3u8' as request_url,
    arrayElement(['US', 'US', 'US', 'US', 'NL', 'NL', 'GB', 'GB', 'DE', 'JP', 'JP', 'SG'], 1 + number%12) as country_code,
    multiIf(
        number%12 < 4, arrayElement(['New York', 'Los Angeles', 'Chicago', 'San Francisco'], 1 + number%4),
        number%12 < 6, arrayElement(['Amsterdam', 'Rotterdam'], 1 + number%2),
        number%12 < 8, arrayElement(['London', 'Manchester'], 1 + number%2),
        number%12 < 9, arrayElement(['Berlin', 'Munich'], 1 + number%2),
        number%12 < 11, arrayElement(['Tokyo', 'Osaka'], 1 + number%2),
        'Singapore'
    ) as city,
    multiIf(
        number%12 < 4, arrayElement([40.71, 34.05, 41.88, 37.77], 1 + number%4) + (rand()%100 - 50)/1000.0,
        number%12 < 6, 52.37 + (rand()%100 - 50)/1000.0,
        number%12 < 8, 51.51 + (rand()%100 - 50)/1000.0,
        number%12 < 9, 52.52 + (rand()%100 - 50)/1000.0,
        number%12 < 11, 35.68 + (rand()%100 - 50)/1000.0,
        1.35 + (rand()%100 - 50)/1000.0
    ) as latitude,
    multiIf(
        number%12 < 4, arrayElement([-74.00, -118.24, -87.63, -122.42], 1 + number%4) + (rand()%100 - 50)/1000.0,
        number%12 < 6, 4.90 + (rand()%100 - 50)/1000.0,
        number%12 < 8, -0.13 + (rand()%100 - 50)/1000.0,
        number%12 < 9, 13.40 + (rand()%100 - 50)/1000.0,
        number%12 < 11, 139.69 + (rand()%100 - 50)/1000.0,
        103.82 + (rand()%100 - 50)/1000.0
    ) as longitude,
    geoToH3(longitude, latitude, 5) as client_bucket_h3,
    5 as client_bucket_res,
    NULL as node_bucket_h3,
    NULL as node_bucket_res,
    'disconnect' as event_type,
    30 + rand()%1800 as session_duration,
    (50 + rand()%450) * 1000000 as bytes_transferred
FROM numbers(0, 2016);

-- =================================================================================================
-- 4. Routing Decisions (routing_decisions) - 7 days of 5-minute samples
-- =================================================================================================
INSERT INTO periscope.routing_decisions (
    timestamp, tenant_id, stream_id, internal_name,
    selected_node, status, details, score,
    client_ip, client_country, client_latitude, client_longitude,
    client_bucket_h3, client_bucket_res,
    node_latitude, node_longitude, node_name,
    node_bucket_h3, node_bucket_res,
    selected_node_id, routing_distance_km,
    stream_tenant_id, cluster_id, remote_cluster_id,
    latency_ms, candidates_count, event_type, source
)
SELECT
    toDateTime(now() - INTERVAL (number * 5) MINUTE) as timestamp,
    '5eed517e-ba5e-da7a-517e-ba5eda7a0001' as tenant_id,
    '5eedfeed-11fe-ca57-feed-11feca570001' as stream_id,
    'demo_live_stream_001' as internal_name,
    arrayElement(['edge-node-1', 'edge-ashburn', 'edge-singapore'], 1 + rand()%3) as selected_node,
    'success' as status,
    'geo-proximity' as details,
    800 + rand() % 200 as score,
    concat(toString(10 + rand()%240), '.', toString(rand()%255), '.', toString(rand()%255), '.', toString(number%255)) as client_ip,
    arrayElement(['US', 'NL', 'SG'], 1 + rand()%3) as client_country,
    arrayElement([40.71, 52.36, 1.35], 1 + rand()%3) + (rand()%100 - 50)/500.0 as client_latitude,
    arrayElement([-74.00, 4.90, 103.82], 1 + rand()%3) + (rand()%100 - 50)/500.0 as client_longitude,
    geoToH3(client_longitude, client_latitude, 5) as client_bucket_h3,
    5 as client_bucket_res,
    arrayElement([52.16, 39.04, 1.35], 1 + rand()%3) as node_latitude,
    arrayElement([4.49, -77.49, 103.82], 1 + rand()%3) as node_longitude,
    arrayElement(['edge-node-1', 'edge-ashburn', 'edge-singapore'], 1 + rand()%3) as node_name,
    geoToH3(node_longitude, node_latitude, 5) as node_bucket_h3,
    5 as node_bucket_res,
    NULL as selected_node_id,
    toFloat64(350 + rand()%500) as routing_distance_km,
    '5eed517e-ba5e-da7a-517e-ba5eda7a0001' as stream_tenant_id,
    'central-primary' as cluster_id,
    '' as remote_cluster_id,
    toFloat32(12 + rand()%30) as latency_ms,
    toInt32(3 + rand()%5) as candidates_count,
    'resolve_playback' as event_type,
    'foghorn' as source
FROM numbers(0, 2016);

-- 4b. Cross-cluster routing decisions (remote_redirect + cross_cluster_dtsc)
INSERT INTO periscope.routing_decisions (
    timestamp, tenant_id, stream_id, internal_name,
    selected_node, status, details, score,
    client_ip, client_country, client_latitude, client_longitude,
    client_bucket_h3, client_bucket_res,
    node_latitude, node_longitude, node_name,
    node_bucket_h3, node_bucket_res,
    routing_distance_km, stream_tenant_id,
    cluster_id, remote_cluster_id,
    latency_ms, candidates_count, event_type, source
)
SELECT
    toDateTime(now() - INTERVAL (number * 15) MINUTE) as timestamp,
    '5eed517e-ba5e-da7a-517e-ba5eda7a0001' as tenant_id,
    '5eedfeed-11fe-ca57-feed-11feca570001' as stream_id,
    'demo_live_stream_001' as internal_name,
    arrayElement(['edge-ashburn', 'edge-singapore'], 1 + number%2) as selected_node,
    arrayElement(['remote_redirect', 'cross_cluster_dtsc'], 1 + number%2) as status,
    if(number%2 = 0,
        concat('https://', arrayElement(['edge-ashburn', 'edge-singapore'], 1 + number%2), '/demo_live_stream_001'),
        concat('dtsc://', arrayElement(['edge-ashburn', 'edge-singapore'], 1 + number%2), ':4200/demo_live_stream_001')
    ) as details,
    600 + rand() % 300 as score,
    concat(toString(10 + rand()%240), '.', toString(rand()%255), '.', toString(rand()%255), '.', toString(number%255)) as client_ip,
    arrayElement(['US', 'JP', 'DE', 'GB'], 1 + rand()%4) as client_country,
    arrayElement([40.71, 35.68, 52.52, 51.50], 1 + rand()%4) as client_latitude,
    arrayElement([-74.00, 139.69, 13.40, -0.12], 1 + rand()%4) as client_longitude,
    geoToH3(client_longitude, client_latitude, 5) as client_bucket_h3,
    5 as client_bucket_res,
    arrayElement([39.04, 1.35], 1 + number%2) as node_latitude,
    arrayElement([-77.49, 103.82], 1 + number%2) as node_longitude,
    arrayElement(['edge-ashburn', 'edge-singapore'], 1 + number%2) as node_name,
    geoToH3(node_longitude, node_latitude, 5) as node_bucket_h3,
    5 as node_bucket_res,
    toFloat64(5000 + rand()%8000) as routing_distance_km,
    '5eed517e-ba5e-da7a-517e-ba5eda7a0001' as stream_tenant_id,
    'central-primary' as cluster_id,
    arrayElement(['us-east-edge', 'apac-edge'], 1 + number%2) as remote_cluster_id,
    toFloat32(45 + rand()%80) as latency_ms,
    toInt32(5 + rand()%8) as candidates_count,
    'resolve_playback' as event_type,
    'foghorn' as source
FROM numbers(0, 336);

-- 4c. Federation Events (federation_events) - 7 days of lifecycle events
INSERT INTO periscope.federation_events (
    timestamp, tenant_id, event_type, local_cluster, remote_cluster,
    stream_name, stream_id, source_node, dest_node, dtsc_url,
    latency_ms, time_to_live_ms, failure_reason,
    queried_clusters, responding_clusters, total_candidates,
    peer_cluster, role, reason,
    local_lat, local_lon, remote_lat, remote_lon
)
SELECT
    ts, tenant_id, event_type, local_cluster, remote_cluster,
    stream_name, stream_id, source_node, dest_node, dtsc_url,
    latency_ms, time_to_live_ms, failure_reason,
    queried_clusters, responding_clusters, total_candidates,
    peer_cluster, role, reason,
    local_lat, local_lon, remote_lat, remote_lon
FROM (
    -- Origin-pull arranged events (once per hour)
    SELECT
        toDateTime(now() - INTERVAL (number * 60) MINUTE) as ts,
        '5eed517e-ba5e-da7a-517e-ba5eda7a0001' as tenant_id,
        'origin_pull_arranged' as event_type,
        'central-primary' as local_cluster,
        arrayElement(['us-east-edge', 'apac-edge'], 1 + number%2) as remote_cluster,
        'demo_live_stream_001' as stream_name,
        toNullable('5eedfeed-11fe-ca57-feed-11feca570001') as stream_id,
        toNullable(arrayElement(['edge-ashburn', 'edge-singapore'], 1 + number%2)) as source_node,
        toNullable('edge-node-1') as dest_node,
        toNullable(concat('dtsc://', arrayElement(['edge-ashburn', 'edge-singapore'], 1 + number%2), ':4200/demo_live_stream_001')) as dtsc_url,
        toNullable(toFloat32(0)) as latency_ms,
        toNullable(toFloat32(0)) as time_to_live_ms,
        toNullable('') as failure_reason,
        toNullable(toUInt32(0)) as queried_clusters,
        toNullable(toUInt32(0)) as responding_clusters,
        toNullable(toUInt32(0)) as total_candidates,
        toNullable('') as peer_cluster, '' as role,
        toNullable('') as reason,
        toNullable(41.8781) as local_lat, toNullable(-87.6298) as local_lon,
        toNullable(arrayElement([40.7128, 35.6762], 1 + number%2)) as remote_lat,
        toNullable(arrayElement([-74.0060, 139.6503], 1 + number%2)) as remote_lon
    FROM numbers(0, 168)

    UNION ALL

    -- Origin-pull completed events (slightly after arranged)
    SELECT
        toDateTime(now() - INTERVAL (number * 60 - 3) MINUTE) as ts,
        '5eed517e-ba5e-da7a-517e-ba5eda7a0001',
        'origin_pull_completed',
        'central-primary',
        arrayElement(['us-east-edge', 'apac-edge'], 1 + number%2),
        'demo_live_stream_001',
        toNullable('5eedfeed-11fe-ca57-feed-11feca570001'),
        toNullable(arrayElement(['edge-ashburn', 'edge-singapore'], 1 + number%2)),
        toNullable('edge-node-1'),
        toNullable(concat('dtsc://', arrayElement(['edge-ashburn', 'edge-singapore'], 1 + number%2), ':4200/demo_live_stream_001')),
        toNullable(toFloat32(0)), toNullable(toFloat32(0)), toNullable(''),
        toNullable(toUInt32(0)), toNullable(toUInt32(0)), toNullable(toUInt32(0)),
        toNullable(''), '', toNullable(''),
        toNullable(41.8781), toNullable(-87.6298),
        toNullable(arrayElement([40.7128, 35.6762], 1 + number%2)),
        toNullable(arrayElement([-74.0060, 139.6503], 1 + number%2))
    FROM numbers(0, 168)

    UNION ALL

    -- Federation query events (every 15 minutes)
    SELECT
        toDateTime(now() - INTERVAL (number * 15) MINUTE) as ts,
        '5eed517e-ba5e-da7a-517e-ba5eda7a0001',
        'federation_query',
        'central-primary',
        '',
        'demo_live_stream_001',
        toNullable('5eedfeed-11fe-ca57-feed-11feca570001'),
        toNullable(''), toNullable(''), toNullable(''),
        toNullable(toFloat32(25 + rand()%50)),
        toNullable(toFloat32(0)), toNullable(''),
        toNullable(toUInt32(2)),
        toNullable(toUInt32(1 + rand()%2)),
        toNullable(toUInt32(2 + rand()%6)),
        toNullable(''), '', toNullable(''),
        toNullable(41.8781), toNullable(-87.6298),
        null, null
    FROM numbers(0, 672)

    UNION ALL

    -- Peer connected/disconnected events (daily cycle)
    SELECT
        toDateTime(now() - INTERVAL (number * 360) MINUTE) as ts,
        '5eed517e-ba5e-da7a-517e-ba5eda7a0001',
        arrayElement(['peer_connected', 'peer_disconnected'], 1 + number%2),
        'central-primary',
        '',
        '', toNullable(''), toNullable(''), toNullable(''), toNullable(''),
        toNullable(toFloat32(0)), toNullable(toFloat32(0)), toNullable(''),
        toNullable(toUInt32(0)), toNullable(toUInt32(0)), toNullable(toUInt32(0)),
        toNullable(arrayElement(['us-east-edge', 'apac-edge'], 1 + intDiv(number, 2) % 2)),
        '', toNullable(''),
        toNullable(41.8781), toNullable(-87.6298),
        null, null
    FROM numbers(0, 28)

    UNION ALL

    -- Leader acquired/lost events (once per day)
    SELECT
        toDateTime(now() - INTERVAL (number * 1440) MINUTE) as ts,
        '5eed517e-ba5e-da7a-517e-ba5eda7a0001',
        arrayElement(['leader_acquired', 'leader_lost'], 1 + number%2),
        'central-primary',
        '',
        '', toNullable(''), toNullable(''), toNullable(''), toNullable(''),
        toNullable(toFloat32(0)), toNullable(toFloat32(0)), toNullable(''),
        toNullable(toUInt32(0)), toNullable(toUInt32(0)), toNullable(toUInt32(0)),
        toNullable(''),
        'peer_manager',
        toNullable(''),
        toNullable(41.8781), toNullable(-87.6298),
        null, null
    FROM numbers(0, 14)

    UNION ALL

    -- Replication loop prevented events (rare, ~2 per day)
    SELECT
        toDateTime(now() - INTERVAL (number * 720 + 180) MINUTE) as ts,
        '5eed517e-ba5e-da7a-517e-ba5eda7a0001',
        'replication_loop_prevented',
        'central-primary',
        arrayElement(['us-east-edge', 'apac-edge'], 1 + number%2),
        'demo_live_stream_001',
        toNullable('5eedfeed-11fe-ca57-feed-11feca570001'),
        toNullable(''), toNullable(''), toNullable(''),
        toNullable(toFloat32(0)), toNullable(toFloat32(0)), toNullable(''),
        toNullable(toUInt32(0)), toNullable(toUInt32(0)), toNullable(toUInt32(0)),
        toNullable(''),
        '',
        toNullable(''),
        toNullable(41.8781), toNullable(-87.6298),
        toNullable(arrayElement([40.7128, 35.6762], 1 + number%2)),
        toNullable(arrayElement([-74.0060, 139.6503], 1 + number%2))
    FROM numbers(0, 14)
);

-- =================================================================================================
-- 5. Track List Events (track_list_events)
-- =================================================================================================
INSERT INTO periscope.track_list_events (
    timestamp, event_id, tenant_id, stream_id, internal_name, node_id,
    track_list, track_count, video_track_count, audio_track_count,
    primary_width, primary_height, primary_fps, primary_video_codec, primary_video_bitrate,
    quality_tier,
    primary_audio_channels, primary_audio_sample_rate, primary_audio_codec, primary_audio_bitrate
) VALUES
(   -- 1080p start
    now() - INTERVAL 3 HOUR, generateUUIDv4(),
    '5eed517e-ba5e-da7a-517e-ba5eda7a0001',
    '5eedfeed-11fe-ca57-feed-11feca570001',
    'demo_live_stream_001',
    'edge-node-1',
    '[{"trackName":"video_1","trackType":"video","codec":"H264","width":1920,"height":1080,"fps":60,"bitrateKbps":4500},{"trackName":"audio_1","trackType":"audio","codec":"AAC"}]',
    2, 1, 1,
    1920, 1080, 60.0, 'H264', 4500,
    '1080p60',
    2, 48000, 'AAC', 192000
),
(   -- 1440p upgrade
    now() - INTERVAL 2 HOUR, generateUUIDv4(),
    '5eed517e-ba5e-da7a-517e-ba5eda7a0001',
    '5eedfeed-11fe-ca57-feed-11feca570001',
    'demo_live_stream_001',
    'edge-node-1',
    '[{"trackName":"video_1","trackType":"video","codec":"H264","width":2560,"height":1440,"fps":60,"bitrateKbps":6500},{"trackName":"audio_1","trackType":"audio","codec":"AAC"}]',
    2, 1, 1,
    2560, 1440, 60.0, 'H264', 6500,
    '1440p60',
    2, 48000, 'AAC', 192000
),
(   -- 2160p peak
    now() - INTERVAL 1 HOUR, generateUUIDv4(),
    '5eed517e-ba5e-da7a-517e-ba5eda7a0001',
    '5eedfeed-11fe-ca57-feed-11feca570001',
    'demo_live_stream_001',
    'edge-node-1',
    '[{"trackName":"video_1","trackType":"video","codec":"H264","width":3840,"height":2160,"fps":60,"bitrateKbps":12000},{"trackName":"audio_1","trackType":"audio","codec":"AAC"}]',
    2, 1, 1,
    3840, 2160, 60.0, 'H264', 12000,
    '2160p60',
    2, 48000, 'AAC', 192000
);

-- =================================================================================================
-- 6. Node Metrics Samples (node_metrics_samples) - 7 days of 1-minute samples
-- =================================================================================================
INSERT INTO periscope.node_metrics_samples (
    timestamp, tenant_id, cluster_id, node_id,
    cpu_usage, ram_max, ram_current, shm_total_bytes, shm_used_bytes,
    disk_total_bytes, disk_used_bytes,
    bandwidth_in, bandwidth_out, up_speed, down_speed,
    connections_current, stream_count,
    is_healthy, operational_mode, latitude, longitude,
    metadata
)
SELECT
    toDateTime(now() - INTERVAL number MINUTE) as timestamp,
    '5eed517e-ba5e-da7a-517e-ba5eda7a0001' as tenant_id,
    'central-primary' as cluster_id,
    'edge-node-1' as node_id,
    toFloat32(30 + 20 * sin(number/120) + rand()%10) as cpu_usage,
    16000000000 as ram_max,
    4000000000 + (number * 50000) % 8000000000 as ram_current,
    2000000000 as shm_total_bytes,
    300000000 + (number * 10000) % 1500000000 as shm_used_bytes,
    500000000000 as disk_total_bytes,
    85000000000 + (number * 1000000) % 10000000000 as disk_used_bytes,
    100000000 + (rand() % 50000000) as bandwidth_in,
    500000000 + (rand() % 200000000) as bandwidth_out,
    125000000 as up_speed,
    650000000 as down_speed,
    toUInt32(50 + rand()%30) as connections_current,
    toUInt32(1 + rand()%3) as stream_count,
    1 as is_healthy,
    'normal' as operational_mode,
    52.1601 as latitude,
    4.4970 as longitude,
    '{"region":"eu-west"}' as metadata
FROM numbers(0, 10080);

-- =================================================================================================
-- 7. Artifact Events + State (clips, DVR, VOD)
-- =================================================================================================
INSERT INTO periscope.artifact_events (
    timestamp, tenant_id, stream_id, internal_name, filename, request_id,
    stage, content_type, start_unix, stop_unix,
    ingest_node_id, percent, message,
    file_path, s3_url, size_bytes, expires_at
) VALUES
(
    now() - INTERVAL 6 HOUR,
    '5eed517e-ba5e-da7a-517e-ba5eda7a0001',
    '5eedfeed-11fe-ca57-feed-11feca570001',
    'demo_live_stream_001',
    NULL,
    'a1b2c3d4e5f6789012345678901234ab',
    'done', 'clip', 1700000000, 1700003600,
    'edge-node-1', 100, 'clip complete',
    '/var/lib/mistserver/recordings/clips/demo_live_stream_001/a1b2c3d4e5f6789012345678901234ab.mp4',
    's3://frameworks/dev/clips/demo_live_stream_001/a1b2c3d4e5f6789012345678901234ab.mp4',
    107553,
    toUnixTimestamp(now() + INTERVAL 7 DAY)
),
(
    now() - INTERVAL 8 HOUR,
    '5eed517e-ba5e-da7a-517e-ba5eda7a0001',
    '5eedfeed-11fe-ca57-feed-11feca570001',
    'demo_live_stream_001',
    NULL,
    'fedcba98765432109876543210fedcba',
    'done', 'dvr', 1700001000, 1700004600,
    'edge-node-1', 100, 'dvr complete',
    '/var/lib/mistserver/recordings/dvr/5eedfeed-11fe-ca57-feed-11feca570001/fedcba98765432109876543210fedcba',
    's3://frameworks/dev/dvr/5eed517e-ba5e-da7a-517e-ba5eda7a0001/demo_live_stream_001/fedcba98765432109876543210fedcba/fedcba98765432109876543210fedcba.m3u8',
    513176,
    toUnixTimestamp(now() + INTERVAL 14 DAY)
),
(
    now() - INTERVAL 10 HOUR,
    '5eed517e-ba5e-da7a-517e-ba5eda7a0001',
    '5eedfeed-11fe-ca57-feed-11feca570001',
    'vod_int_001',
    'product_demo_2024.mp4',
    'c3d4e5f678901234567890123456abcd',
    'done', 'vod', NULL, NULL,
    'edge-node-1', 100, 'vod uploaded',
    '/var/lib/mistserver/recordings/vod/c3d4e5f678901234567890123456abcd.mp4',
    's3://frameworks/dev/vod/5eed517e-ba5e-da7a-517e-ba5eda7a0001/c3d4e5f678901234567890123456abcd/c3d4e5f678901234567890123456abcd.mp4',
    107553,
    toUnixTimestamp(now() + INTERVAL 30 DAY)
);

INSERT INTO periscope.artifact_state_current (
    tenant_id, stream_id, request_id, internal_name, filename,
    content_type, stage, progress_percent, error_message,
    requested_at, started_at, completed_at,
    clip_start_unix, clip_stop_unix,
    segment_count, manifest_path,
    file_path, s3_url, size_bytes,
    processing_node_id,
    updated_at, expires_at
) VALUES
(
    '5eed517e-ba5e-da7a-517e-ba5eda7a0001',
    '5eedfeed-11fe-ca57-feed-11feca570001',
    'a1b2c3d4e5f6789012345678901234ab',
    'demo_live_stream_001',
    NULL,
    'clip', 'done', 100, NULL,
    now() - INTERVAL 6 HOUR, now() - INTERVAL 6 HOUR, now() - INTERVAL 6 HOUR,
    1700000000, 1700003600,
    1, NULL,
    '/var/lib/mistserver/recordings/clips/demo_live_stream_001/a1b2c3d4e5f6789012345678901234ab.mp4',
    's3://frameworks/dev/clips/demo_live_stream_001/a1b2c3d4e5f6789012345678901234ab.mp4',
    107553,
    'edge-node-1',
    now(), now() + INTERVAL 7 DAY
),
(
    '5eed517e-ba5e-da7a-517e-ba5eda7a0001',
    '5eedfeed-11fe-ca57-feed-11feca570001',
    'fedcba98765432109876543210fedcba',
    'demo_live_stream_001',
    NULL,
    'dvr', 'done', 100, NULL,
    now() - INTERVAL 8 HOUR, now() - INTERVAL 8 HOUR, now() - INTERVAL 8 HOUR,
    1700001000, 1700004600,
    2, '/var/lib/mistserver/recordings/dvr/5eedfeed-11fe-ca57-feed-11feca570001/fedcba98765432109876543210fedcba',
    '/var/lib/mistserver/recordings/dvr/5eedfeed-11fe-ca57-feed-11feca570001/fedcba98765432109876543210fedcba',
    's3://frameworks/dev/dvr/5eed517e-ba5e-da7a-517e-ba5eda7a0001/demo_live_stream_001/fedcba98765432109876543210fedcba/fedcba98765432109876543210fedcba.m3u8',
    513176,
    'edge-node-1',
    now(), now() + INTERVAL 14 DAY
),
(
    '5eed517e-ba5e-da7a-517e-ba5eda7a0001',
    '5eedfeed-11fe-ca57-feed-11feca570001',
    'c3d4e5f678901234567890123456abcd',
    'vod_int_001',
    'product_demo_2024.mp4',
    'vod', 'done', 100, NULL,
    now() - INTERVAL 10 HOUR, now() - INTERVAL 10 HOUR, now() - INTERVAL 10 HOUR,
    NULL, NULL,
    NULL, NULL,
    '/var/lib/mistserver/recordings/vod/c3d4e5f678901234567890123456abcd.mp4',
    's3://frameworks/dev/vod/5eed517e-ba5e-da7a-517e-ba5eda7a0001/c3d4e5f678901234567890123456abcd/c3d4e5f678901234567890123456abcd.mp4',
    107553,
    'edge-node-1',
    now(), now() + INTERVAL 30 DAY
);

-- =================================================================================================
-- 8. Storage Snapshots + Storage Events
-- =================================================================================================
INSERT INTO periscope.storage_snapshots (
    timestamp, tenant_id, node_id, storage_scope,
    total_bytes, file_count,
    dvr_bytes, clip_bytes, vod_bytes,
    frozen_dvr_bytes, frozen_clip_bytes, frozen_vod_bytes
) VALUES
(
    now(),
    '5eed517e-ba5e-da7a-517e-ba5eda7a0001',
    'edge-node-1',
    'hot',
    160000000000,
    320,
    50000000000,
    15000000000,
    95000000000,
    0, 0, 0
),
(
    now(),
    '5eed517e-ba5e-da7a-517e-ba5eda7a0001',
    'edge-node-1',
    'cold',
    120000000000,
    280,
    60000000000,
    12000000000,
    48000000000,
    60000000000, 12000000000, 48000000000
);

INSERT INTO periscope.storage_events (
    timestamp, tenant_id, stream_id, internal_name, asset_hash,
    action, asset_type,
    size_bytes, s3_url, local_path, node_id,
    duration_ms, warm_duration_ms, error
) VALUES
(
    now() - INTERVAL 6 HOUR,
    '5eed517e-ba5e-da7a-517e-ba5eda7a0001',
    '5eedfeed-11fe-ca57-feed-11feca570001',
    'demo_live_stream_001',
    'a1b2c3d4e5f6789012345678901234ab',
    'store', 'clip',
    107553,
    's3://frameworks/dev/clips/demo_live_stream_001/a1b2c3d4e5f6789012345678901234ab.mp4',
    '/var/lib/mistserver/recordings/clips/demo_live_stream_001/a1b2c3d4e5f6789012345678901234ab.mp4',
    'edge-node-1',
    5000, 0, NULL
),
(
    now() - INTERVAL 10 HOUR,
    '5eed517e-ba5e-da7a-517e-ba5eda7a0001',
    '5eedfeed-11fe-ca57-feed-11feca570001',
    'demo_live_stream_001',
    'c3d4e5f678901234567890123456abcd',
    'store', 'vod',
    107553,
    's3://frameworks/dev/vod/5eed517e-ba5e-da7a-517e-ba5eda7a0001/c3d4e5f678901234567890123456abcd/c3d4e5f678901234567890123456abcd.mp4',
    '/var/lib/mistserver/recordings/vod/c3d4e5f678901234567890123456abcd.mp4',
    'edge-node-1',
    8000, 0, NULL
);

-- =================================================================================================
-- 9. Processing Events - 7 days of 5-minute samples
-- =================================================================================================
INSERT INTO periscope.processing_events (
    timestamp, tenant_id, node_id, cluster_id, stream_id, internal_name,
    process_type, track_type, duration_ms,
    input_codec, output_codec,
    width, height, rendition_count,
    input_bytes, output_bytes_total, output_bitrate_bps
)
SELECT
    toDateTime(now() - INTERVAL (number * 5) MINUTE) as timestamp,
    '5eed517e-ba5e-da7a-517e-ba5eda7a0001' as tenant_id,
    'edge-node-1' as node_id,
    'central-primary' as cluster_id,
    '5eedfeed-11fe-ca57-feed-11feca570001' as stream_id,
    'demo_live_stream_001' as internal_name,
    'transcode' as process_type,
    'video' as track_type,
    1500 + rand()%2000 as duration_ms,
    'H264' as input_codec,
    'H264' as output_codec,
    1920 as width,
    1080 as height,
    3 as rendition_count,
    4000000 + rand()%1000000 as input_bytes,
    9000000 + rand()%2000000 as output_bytes_total,
    4500000 as output_bitrate_bps
FROM numbers(0, 2016);

-- =================================================================================================
-- 9A. Client QoE Samples (client_qoe_samples) - 7 days of 5-minute samples
-- =================================================================================================
-- Client-side quality of experience metrics from viewers
INSERT INTO periscope.client_qoe_samples (
    timestamp, tenant_id, stream_id, internal_name, session_id, node_id,
    protocol, host, connection_time, position,
    bandwidth_in, bandwidth_out, bytes_downloaded, bytes_uploaded,
    packets_sent, packets_lost, packets_retransmitted, connection_quality
)
SELECT
    toDateTime(now() - INTERVAL (number * 5) MINUTE) as timestamp,
    '5eed517e-ba5e-da7a-517e-ba5eda7a0001' as tenant_id,
    '5eedfeed-11fe-ca57-feed-11feca570001' as stream_id,
    'demo_live_stream_001' as internal_name,
    concat('session-', toString(number % 200)) as session_id,
    arrayElement(['edge-node-1', 'edge-ashburn', 'edge-singapore'], 1 + rand()%3) as node_id,
    arrayElement(['HLS', 'WebRTC', 'DASH'], 1 + rand()%3) as protocol,
    concat(toString(10 + rand()%240), '.', toString(rand()%255), '.', toString(rand()%255), '.', toString(number%255)) as host,
    toFloat32(0.5 + rand()%100 / 100.0) as connection_time,
    toFloat32(number * 5 * 60.0 % 3600) as position,
    -- Bandwidth varies with time of day (sine wave pattern)
    toUInt64(5000000 + 3000000 * sin(number / 144.0) + rand()%1000000) as bandwidth_in,
    toUInt64(50000 + 30000 * sin(number / 144.0) + rand()%10000) as bandwidth_out,
    toUInt64((number * 5 % 1000) * 1000000 + rand()%500000) as bytes_downloaded,
    toUInt64((number * 5 % 100) * 10000 + rand()%5000) as bytes_uploaded,
    toUInt64(1000 + rand()%500) as packets_sent,
    toUInt64(rand()%20) as packets_lost,
    toUInt64(rand()%10) as packets_retransmitted,
    toFloat32(0.85 + rand()%15 / 100.0) as connection_quality
FROM numbers(0, 2016);

-- =================================================================================================
-- 9B. API Requests (api_requests) - 7 days of 5-minute aggregated batches
-- =================================================================================================
-- GraphQL API request tracking for usage analytics
INSERT INTO periscope.api_requests (
    timestamp, tenant_id, source_node, auth_type,
    operation_name, operation_type, user_hashes, token_hashes,
    request_count, error_count, total_duration_ms, total_complexity
)
SELECT
    toDateTime(now() - INTERVAL (number * 5) MINUTE) as timestamp,
    '5eed517e-ba5e-da7a-517e-ba5eda7a0001' as tenant_id,
    'gateway-1' as source_node,
    arrayElement(['jwt', 'jwt', 'jwt', 'api_token', 'api_token', 'wallet'], 1 + rand()%6) as auth_type,
    arrayElement([
        'StreamsConnection', 'Stream', 'Analytics', 'BillingStatus',
        'ClipsConnection', 'CreateClip', 'UpdateStream', 'ValidateStreamKey'
    ], 1 + rand()%8) as operation_name,
    arrayElement(['query', 'query', 'query', 'mutation'], 1 + rand()%4) as operation_type,
    if(rand()%3 = 0, [cityHash64('demo@frameworks.network')], []) as user_hashes,
    if(rand()%2 = 0, [cityHash64('fw_demo_token')], []) as token_hashes,
    toUInt32(5 + rand()%50) as request_count,
    toUInt32(rand()%3) as error_count,
    toUInt64((5 + rand()%50) * (20 + rand()%100)) as total_duration_ms,
    toUInt32((5 + rand()%50) * (1 + rand()%5)) as total_complexity
FROM numbers(0, 2016);

-- =================================================================================================
-- 9C. API Events (api_events) - Service audit log events
-- =================================================================================================
-- Cross-service audit events from the service_events Kafka topic
INSERT INTO periscope.api_events (
    tenant_id, event_type, source, user_id, resource_type, resource_id, details, timestamp
)
SELECT
    '5eed517e-ba5e-da7a-517e-ba5eda7a0001' as tenant_id,
    arrayElement([
        'stream.created', 'stream.updated', 'stream.started', 'stream.ended',
        'clip.created', 'clip.ready', 'dvr.started', 'dvr.completed',
        'user.login', 'user.logout', 'api_token.created', 'api_token.used'
    ], 1 + number % 12) as event_type,
    arrayElement(['commodore', 'commodore', 'foghorn', 'foghorn', 'api_gateway'], 1 + rand()%5) as source,
    if(rand()%3 != 0, toUUID('5eedface-5e1f-da7a-face-5e1fda7a0001'), toUUID('00000000-0000-0000-0000-000000000000')) as user_id,
    arrayElement(['stream', 'stream', 'clip', 'dvr', 'user', 'api_token'], 1 + rand()%6) as resource_type,
    if(rand()%2 = 0, '5eedfeed-11fe-ca57-feed-11feca570001', NULL) as resource_id,
    concat('{"action":"', arrayElement(['create', 'update', 'delete', 'access'], 1 + rand()%4), '","source_ip":"', toString(10 + rand()%240), '.', toString(rand()%255), '.0.1"}') as details,
    toDateTime64(now() - INTERVAL (number * 30) MINUTE, 3) as timestamp
FROM numbers(0, 336);

-- =================================================================================================
-- 10. Backfill Aggregation Tables (MVs may not process bulk-inserted historical data)
-- =================================================================================================

-- ============================================================================
-- Legacy rollup backfills DISABLED (combined-release cutover).
-- The rollup tables (tenant_viewer_daily, viewer_geo_hourly, processing_daily,
-- api_usage_hourly/daily, etc.) are Refreshable MV destinations sourced
-- from the canonical *_5m ledgers and *_final tables. Direct INSERTs into
-- them would conflict with the MV refresh cycle.
--
-- Canonical pipeline note: the live system flows
--   raw_mist_triggers → *_final → *_5m ledger → refreshable MV rollups.
-- The demo seed cannot run the parser to project raw_mist_triggers into
-- *_final, so it seeds the canonical fact tables directly below:
--   viewer_sessions_final, stream_sessions_final, processing_segments_final,
--   api_usage_5m (storage_snapshots is seeded earlier in this file).
-- The viewer_connection_events / processing_events inserts stay for
-- diagnostic event endpoints and support tooling. The rebuild workers
-- in api_analytics_ingest fire every 5 minutes after boot and populate
-- viewer_usage_5m / stream_runtime_5m / processing_5m /
-- storage_gb_seconds_5m from the seeded *_final rows; refreshable
-- rollups then pick those up on their own refresh cycles.
-- ============================================================================

-- Finalized viewer sessions matching the viewer_connection_events seed
-- above. Same session_id pattern, same geo distribution, same scale
-- (2016 sessions = 7 days × 288 5-min buckets) so canonical billing and
-- dashboards have data on first boot. duration_seconds /
-- uploaded_bytes / downloaded_bytes are synthesized from the row number
-- so the demo has variability without depending on the parser.
INSERT INTO periscope.viewer_sessions_final (
    tenant_id, node_id, session_id, source_event_id,
    cluster_id, stream_id, stream_name, connector, host,
    country_code, city, latitude, longitude, tags,
    duration_seconds, uploaded_bytes, downloaded_bytes, seconds_connected,
    source_started_at_ms, source_ended_at_ms, edge_received_at_ms, projection_version_ms,
    closed_reason, stream_times, connector_times, host_times, payload_raw
)
SELECT
    '5eed517e-ba5e-da7a-517e-ba5eda7a0001'                              AS tenant_id,
    arrayElement(['edge-node-1', 'edge-ashburn', 'edge-singapore'], 1 + number%3) AS node_id,
    concat('session-', toString(number))                                AS session_id,
    concat('sha256:', hex(murmurHash3_128(toString(number))))           AS source_event_id,
    'central-primary'                                                    AS cluster_id,
    toUUID('5eedfeed-11fe-ca57-feed-11feca570001')                       AS stream_id,
    'demo_live_stream_001'                                               AS stream_name,
    arrayElement(['HLS', 'WebRTC', 'RTMP'], 1 + number%3)                AS connector,
    'demo-host.frameworks.local'                                         AS host,
    arrayElement(['US', 'US', 'US', 'US', 'NL', 'NL', 'GB', 'GB', 'DE', 'JP', 'JP', 'SG'], 1 + number%12) AS country_code,
    multiIf(
        number%12 < 4, arrayElement(['New York', 'Los Angeles', 'Chicago', 'San Francisco'], 1 + number%4),
        number%12 < 6, arrayElement(['Amsterdam', 'Rotterdam'], 1 + number%2),
        number%12 < 8, arrayElement(['London', 'Manchester'], 1 + number%2),
        number%12 < 9, arrayElement(['Berlin', 'Munich'], 1 + number%2),
        number%12 < 11, arrayElement(['Tokyo', 'Osaka'], 1 + number%2),
        'Singapore'
    )                                                                    AS city,
    0.0                                                                  AS latitude,
    0.0                                                                  AS longitude,
    ''                                                                   AS tags,
    toUInt32(60 + number%1800)                                           AS duration_seconds,           -- 1-30 min
    toUInt64(1024*1024 * (10 + number%50))                               AS uploaded_bytes,             -- 10-60 MB
    toUInt64(1024*1024 * (50 + number%500))                              AS downloaded_bytes,           -- 50-550 MB
    toUInt64(60 + number%1800)                                           AS seconds_connected,
    toUnixTimestamp(now() - INTERVAL (number * 5) MINUTE) * 1000          AS source_started_at_ms,
    toUnixTimestamp(now() - INTERVAL (number * 5) MINUTE) * 1000 + (60 + number%1800) * 1000 AS source_ended_at_ms,
    toUnixTimestamp(now() - INTERVAL (number * 5) MINUTE) * 1000 + 100   AS edge_received_at_ms,
    toUnixTimestamp(now()) * 1000                                        AS projection_version_ms,
    'final'                                                              AS closed_reason,
    []                                                                   AS stream_times,
    []                                                                   AS connector_times,
    []                                                                   AS host_times,
    ''                                                                   AS payload_raw
FROM numbers(0, 2016);

-- Finalized stream sessions matching the viewer-session demo data, so
-- stream_runtime_5m's rebuilder has STREAM_END facts to project from.
INSERT INTO periscope.stream_sessions_final (
    tenant_id, node_id, stream_id, source_event_id,
    cluster_id, stream_name,
    downloaded_bytes, uploaded_bytes, total_viewers, total_inputs, total_outputs, viewer_seconds,
    source_started_at_ms, source_ended_at_ms, edge_received_at_ms, projection_version_ms,
    closed_reason, payload_raw
)
SELECT
    '5eed517e-ba5e-da7a-517e-ba5eda7a0001'                              AS tenant_id,
    arrayElement(['edge-node-1', 'edge-ashburn', 'edge-singapore'], 1 + number%3) AS node_id,
    toUUID('5eedfeed-11fe-ca57-feed-11feca570001')                       AS stream_id,
    concat('sha256:', hex(murmurHash3_128(concat('stream-', toString(number))))) AS source_event_id,
    'central-primary'                                                    AS cluster_id,
    'demo_live_stream_001'                                               AS stream_name,
    toUInt64(1024*1024 * (50 + number%500))                              AS downloaded_bytes,
    toUInt64(1024*1024 * (10 + number%50))                               AS uploaded_bytes,
    toUInt32(5 + number%200)                                             AS total_viewers,
    toUInt32(1)                                                          AS total_inputs,
    toUInt32(1 + number%4)                                               AS total_outputs,
    toUInt32(900 + number%2700)                                          AS viewer_seconds,
    toUnixTimestamp(now() - INTERVAL ((number + 1) * 5) MINUTE) * 1000   AS source_started_at_ms,
    toUnixTimestamp(now() - INTERVAL (number * 5) MINUTE) * 1000          AS source_ended_at_ms,
    toUnixTimestamp(now() - INTERVAL (number * 5) MINUTE) * 1000 + 100   AS edge_received_at_ms,
    toUnixTimestamp(now()) * 1000                                        AS projection_version_ms,
    'final'                                                              AS closed_reason,
    ''                                                                   AS payload_raw
FROM numbers(0, 504);  -- ~7 days of 20-min stream sessions

-- Finalized processing segments — one row per virtual segment over 7
-- days × ~12 segments/hour, split across Livepeer (h264/hevc) and AV
-- (h264/aac). source_event_id is the canonical natural key now that
-- AV virtual segments don't carry a real segment_number.
INSERT INTO periscope.processing_segments_final (
    tenant_id, node_id, stream_id, process_type, output_codec, track_type, segment_number,
    source_event_id,
    cluster_id, stream_name, input_codec, media_seconds,
    width, height, rendition_count, input_bytes, output_bytes_total, turnaround_ms, speed_factor,
    is_final,
    source_started_at_ms, source_ended_at_ms, edge_received_at_ms, projection_version_ms,
    payload_raw
)
SELECT
    '5eed517e-ba5e-da7a-517e-ba5eda7a0001'                              AS tenant_id,
    arrayElement(['edge-node-1', 'edge-ashburn', 'edge-singapore'], 1 + number%3) AS node_id,
    toUUID('5eedfeed-11fe-ca57-feed-11feca570001')                       AS stream_id,
    arrayElement(['Livepeer', 'Livepeer', 'AV', 'AV'], 1 + number%4)     AS process_type,
    arrayElement(['h264', 'hevc', 'h264', 'aac'], 1 + number%4)          AS output_codec,
    arrayElement(['video', 'video', 'video', 'audio'], 1 + number%4)     AS track_type,
    toInt32(number)                                                      AS segment_number,
    concat('sha256:', hex(murmurHash3_128(concat('seg-', toString(number))))) AS source_event_id,
    'central-primary'                                                    AS cluster_id,
    'demo_live_stream_001'                                               AS stream_name,
    'h264'                                                               AS input_codec,
    5.0                                                                  AS media_seconds,
    1920                                                                 AS width,
    1080                                                                 AS height,
    3                                                                    AS rendition_count,
    toInt64(1024*1024)                                                   AS input_bytes,
    toInt64(512*1024 * (1 + number%3))                                   AS output_bytes_total,
    toInt64(200 + number%300)                                            AS turnaround_ms,
    1.0 + 0.1 * (number%5)                                               AS speed_factor,
    toUInt8(1)                                                           AS is_final,
    toUnixTimestamp(now() - INTERVAL (number * 5) MINUTE) * 1000          AS source_started_at_ms,
    toUnixTimestamp(now() - INTERVAL (number * 5) MINUTE) * 1000 + 5000  AS source_ended_at_ms,
    toUnixTimestamp(now() - INTERVAL (number * 5) MINUTE) * 1000 + 100   AS edge_received_at_ms,
    toUnixTimestamp(now()) * 1000                                        AS projection_version_ms,
    ''                                                                   AS payload_raw
FROM numbers(0, 2016);

-- Canonical api_usage_5m so the api_usage_hourly/daily rollups have a
-- source. Each generated row is grouped by its sequence number so the
-- AggregateFunction state columns are valid one-row uniqCombined states.
INSERT INTO periscope.api_usage_5m (
    window_start, tenant_id, auth_type, operation_type, operation_name,
    requests, errors, duration_ms, complexity,
    unique_users_state, unique_tokens_state,
    projection_version_ms
)
SELECT
    toDateTime(toStartOfFiveMinute(now() - INTERVAL (number * 5) MINUTE)) AS window_start,
    '5eed517e-ba5e-da7a-517e-ba5eda7a0001'                                AS tenant_id,
    arrayElement(['api_key', 'jwt'], 1 + number%2)                        AS auth_type,
    arrayElement(['query', 'mutation'], 1 + number%2)                     AS operation_type,
    arrayElement(['GetUsage', 'GetStreamHealth', 'CreateAsset'], 1 + number%3) AS operation_name,
    toUInt64(50 + number%500)                                             AS requests,
    toUInt64(number%5)                                                    AS errors,
    toUInt64(100 + number%900)                                            AS duration_ms,
    toUInt64(10 + number%50)                                              AS complexity,
    uniqCombinedState(toUInt64(number%17))                                AS unique_users_state,
    uniqCombinedState(toUInt64(number%23))                                AS unique_tokens_state,
    toUnixTimestamp(now()) * 1000                                         AS projection_version_ms
FROM numbers(0, 2016)
GROUP BY number;
-- ============================================================================

-- =================================================================================================
-- 11. Materialized View Finalization (compact aggregated data)
-- =================================================================================================
OPTIMIZE TABLE periscope.stream_viewer_5m FINAL;
OPTIMIZE TABLE periscope.stream_health_5m FINAL;
OPTIMIZE TABLE periscope.quality_tier_daily FINAL;
OPTIMIZE TABLE periscope.client_qoe_5m FINAL;
OPTIMIZE TABLE periscope.node_performance_5m FINAL;
OPTIMIZE TABLE periscope.node_metrics_1h FINAL;
-- Rollup tables populated by refreshable MVs; OPTIMIZE happens internally
-- on refresh swap, so no manual compaction is needed here for:
--   tenant_viewer_daily, tenant_usage_hourly/daily, tenant_analytics_daily,
--   stream_analytics_daily, stream_connection_hourly, stream_runtime_*,
--   viewer_hours_hourly, viewer_geo_hourly/daily, viewer_city_hourly/daily,
--   storage_usage_hourly/daily, processing_hourly/daily, api_usage_hourly/daily.
OPTIMIZE TABLE periscope.routing_cluster_hourly FINAL;
OPTIMIZE TABLE periscope.federation_hourly FINAL;
