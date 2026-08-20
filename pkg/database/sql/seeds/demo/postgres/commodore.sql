-- Deterministic local demo fixture owned by commodore.
-- Applied explicitly by `make seed-demo`; never loaded by database first boot.

-- Demo user
INSERT INTO commodore.users (id, tenant_id, email, password_hash, first_name, last_name, role, permissions)
VALUES ('5eedface-5e1f-da7a-face-5e1fda7a0001', '5eed517e-ba5e-da7a-517e-ba5eda7a0001', 'demo@frameworks.network', '$2a$10$MJAqE.2jQ/tbbkhQs68VHOm50iIEoq4tQIiF7PUfSJfzGuCKVsAla', 'Demo', 'User', 'owner', ARRAY['streams:read','streams:write','analytics:read','users:read','users:write','settings:write'])
ON CONFLICT DO NOTHING;

UPDATE commodore.users SET verified = TRUE WHERE email = 'demo@frameworks.network' AND tenant_id = '5eed517e-ba5e-da7a-517e-ba5eda7a0001';

-- Service account
INSERT INTO commodore.users (id, tenant_id, email, password_hash, first_name, last_name, role, permissions, is_active, verified)
VALUES ('5eeddeaf-dead-beef-deaf-deadbeef0000', '5eed517e-ba5e-da7a-517e-ba5eda7a0001', 'service@internal', 'no-login', 'Service', 'Account', 'service', ARRAY['*'], TRUE, TRUE)
ON CONFLICT DO NOTHING;

-- Demo API token for programmatic access testing
-- Input token format: "fw_" + 64 hex chars (matching developer_tokens package format)
-- DEMO INPUT TOKEN: fw_0000000000000000000000000000000000000000000000000000000000demo01
-- Use this token value in API requests for local development testing
INSERT INTO commodore.api_tokens (
    id, tenant_id, user_id, token_value, token_name,
    permissions, is_active, expires_at, last_used_at, created_at
) VALUES (
    '5eed5a17-da7a-5a17-da7a-5a17da7a0001',
    '5eed517e-ba5e-da7a-517e-ba5eda7a0001',
    '5eedface-5e1f-da7a-face-5e1fda7a0001',
    -- SHA-256 hash of: fw_0000000000000000000000000000000000000000000000000000000000demo01
    '807a534c30fd84d3544bd6ee5f8b1c4426596a9c8c360b92caf7b667c25db8d8',
    'Demo API Token',
    ARRAY['streams:read', 'streams:write', 'analytics:read'],
    TRUE,
    NOW() + INTERVAL '1 year',
    NOW() - INTERVAL '1 hour',
    NOW() - INTERVAL '7 days'
) ON CONFLICT (token_value) DO NOTHING;

-- Create demo stream with fixed internal_name to match ClickHouse seed data
INSERT INTO commodore.streams (id, tenant_id, user_id, stream_key, playback_id, internal_name, title, description)
VALUES (
    '5eedfeed-11fe-ca57-feed-11feca570001',  -- Fixed demo stream UUID
    '5eed517e-ba5e-da7a-517e-ba5eda7a0001',  -- Demo tenant
    '5eedface-5e1f-da7a-face-5e1fda7a0001',  -- Demo user
    'sk_demo_live_stream_primary_key',       -- Fixed stream key
    'pb_demo_live_001',                      -- Fixed playback ID
    'demo_live_stream_001',                  -- MUST match ClickHouse seed data
    'Demo Stream',
    'Demo stream for development and testing'
) ON CONFLICT (internal_name) DO NOTHING;

-- Create primary stream key for demo stream
INSERT INTO commodore.stream_keys (tenant_id, user_id, stream_id, key_value, key_name, is_active)
VALUES (
    '5eed517e-ba5e-da7a-517e-ba5eda7a0001',
    '5eedface-5e1f-da7a-face-5e1fda7a0001',
    '5eedfeed-11fe-ca57-feed-11feca570001',
    'sk_demo_live_stream_primary_key',
    'Primary Key',
    TRUE
) ON CONFLICT (key_value) DO NOTHING;

-- ============================================================================
-- COMMODORE: Demo Clips (Business Registry)
-- ============================================================================
-- Clip business metadata owned by control plane
-- These correspond to foghorn.artifacts entries for lifecycle state

INSERT INTO commodore.clips (
    id, tenant_id, user_id, stream_id, clip_hash, internal_name, playback_id,
    title, description,
    start_time, duration, clip_mode,
    origin_cluster_id, retention_until, created_at, updated_at
) VALUES
-- Demo clip (ready) - matches foghorn.artifacts entry
(
    '5eedb17e-da7a-b17e-da7a-b17eda7a0001',
    '5eed517e-ba5e-da7a-517e-ba5eda7a0001',  -- Demo tenant
    '5eedface-5e1f-da7a-face-5e1fda7a0001',  -- Demo user
    '5eedfeed-11fe-ca57-feed-11feca570001',  -- Demo stream
    'a1b2c3d4e5f6789012345678901234ab',      -- Must match foghorn.artifacts + on-disk filename
    'clip_int_001',
    'clp1a2b3c4d5e6fg',
    'Demo Highlight Reel',
    'Amazing gameplay highlights from the demo stream',
    1640995200000,  -- Unix timestamp (ms): Jan 1, 2022 00:00:00 UTC
    5000,           -- Duration (ms): fixture is 5 seconds
    'absolute',
    'demo-media',
    NOW() + INTERVAL '7 days',   -- 7-day rolling retention for demo fixtures
    NOW() - INTERVAL '2 hours',
    NOW() - INTERVAL '2 hours'
),
-- Demo clip (deleted) - matches foghorn.artifacts entry
(
    '5eedb17e-da7a-b17e-da7a-b17eda7a0002',
    '5eed517e-ba5e-da7a-517e-ba5eda7a0001',
    '5eedface-5e1f-da7a-face-5e1fda7a0001',
    '5eedfeed-11fe-ca57-feed-11feca570001',
    '20240101120100b2c3d4e5f6789012',        -- Must match foghorn.artifacts (30-char: timestamp+hex)
    'clip_int_002',
    'clp2a2b3c4d5e6fh',
    'Old Demo Clip',
    'This clip was deleted',
    1641081600000,  -- Jan 2, 2022 00:00:00 UTC
    300000,         -- 5 minutes
    'absolute',
    'demo-media',
    NOW() - INTERVAL '1 day',   -- Already expired (retention passed)
    NOW() - INTERVAL '2 days',
    NOW() - INTERVAL '1 day'
)
ON CONFLICT (clip_hash) DO UPDATE SET
    title = EXCLUDED.title,
    origin_cluster_id = EXCLUDED.origin_cluster_id,
    updated_at = NOW();

-- ============================================================================
-- COMMODORE: Demo DVR Recordings (Business Registry)
-- ============================================================================
-- DVR recording business metadata owned by control plane
-- These correspond to foghorn.artifacts entries for lifecycle state

INSERT INTO commodore.dvr_recordings (
    id, tenant_id, user_id, stream_id, dvr_hash, internal_name, playback_id,
    stream_internal_name,
    origin_cluster_id, retention_until, created_at, updated_at
) VALUES
-- Demo DVR recording (completed) - matches foghorn.artifacts entry
(
    '5eedf11e-5afe-da7a-f11e-5afeda7a0001',
    '5eed517e-ba5e-da7a-517e-ba5eda7a0001',  -- Demo tenant
    '5eedface-5e1f-da7a-face-5e1fda7a0001',  -- Demo user
    '5eedfeed-11fe-ca57-feed-11feca570001',  -- Demo stream
    'fedcba98765432109876543210fedcba',      -- Must match foghorn.artifacts + on-disk filename
    'dvr_int_001',
    'dvr1a2b3c4d5e6fg',
    'demo_live_stream_001',
    'demo-media',
    NOW() + INTERVAL '7 days',   -- 7-day rolling retention for demo fixtures
    NOW() - INTERVAL '4 hours',
    NOW() - INTERVAL '4 hours'
),
-- Demo DVR recording (deleted) - matches foghorn.artifacts entry
(
    '5eedf11e-5afe-da7a-f11e-5afeda7a0002',
    '5eed517e-ba5e-da7a-517e-ba5eda7a0001',
    '5eedface-5e1f-da7a-face-5e1fda7a0001',
    '5eedfeed-11fe-ca57-feed-11feca570001',
    '20240101120300fedcba9876543211',        -- Must match foghorn.artifacts (30-char: timestamp+hex)
    'dvr_int_002',
    'dvr2a2b3c4d5e6fh',
    'demo_live_stream_001',
    'demo-media',
    NOW() - INTERVAL '1 day',   -- Already expired (retention passed)
    NOW() - INTERVAL '2 days',
    NOW() - INTERVAL '1 day'
)
ON CONFLICT (dvr_hash) DO UPDATE SET
    internal_name = EXCLUDED.internal_name,
    origin_cluster_id = EXCLUDED.origin_cluster_id,
    updated_at = NOW();

INSERT INTO commodore.dvr_chapter_playback (
    chapter_id, tenant_id, playback_id, artifact_hash, created_at, updated_at
) VALUES (
    '34d74b7acd7ec8cf78f6cc8c9f031a8a',
    '5eed517e-ba5e-da7a-517e-ba5eda7a0001',
    'chp_demo_recording_001',
    '34d74b7acd7ec8cf78f6cc8c9f031a8a',
    NOW() - INTERVAL '4 hours',
    NOW() - INTERVAL '4 hours'
)
ON CONFLICT (chapter_id) DO UPDATE SET
    playback_id = EXCLUDED.playback_id,
    artifact_hash = EXCLUDED.artifact_hash,
    updated_at = NOW();

-- ============================================================================
-- COMMODORE: Demo VOD Assets (Business Registry)
-- ============================================================================
-- VOD business metadata owned by control plane
-- These correspond to foghorn.artifacts + foghorn.vod_metadata entries for lifecycle state

INSERT INTO commodore.vod_assets (
    id, tenant_id, user_id, vod_hash, internal_name, playback_id,
    title, description, filename, content_type,
    size_bytes, origin_cluster_id, retention_until, library_visible, created_at, updated_at
) VALUES
-- Demo VOD (ready) - HLS-compatible MP4 sample
(
    '5eedb0d5-1e55-da7a-b0d5-1e55da7a0001',
    '5eed517e-ba5e-da7a-517e-ba5eda7a0001',  -- Demo tenant
    '5eedface-5e1f-da7a-face-5e1fda7a0001',  -- Demo user
    'c3d4e5f678901234567890123456abcd',      -- Must match foghorn.artifacts + on-disk filename
    'vod_int_001',
    'vod1a2b3c4d5e6fg',
    'Product Demo 2024',
    'Annual product demonstration showcasing new streaming features',
    'product_demo_2024.mp4',
    'video/mp4',
    107553,
    'demo-media',
    NOW() + INTERVAL '30 days',
    TRUE,
    NOW() - INTERVAL '1 day',
    NOW() - INTERVAL '1 day'
),
-- Hidden VOD artifact backing the seeded DVR chapter playback ID.
(
    '5eedb0d5-1e55-da7a-b0d5-1e55da7a0004',
    '5eed517e-ba5e-da7a-517e-ba5eda7a0001',
    '5eedface-5e1f-da7a-face-5e1fda7a0001',
    '34d74b7acd7ec8cf78f6cc8c9f031a8a',
    '34d74b7acd7ec8cf78f6cc8c9f031a8a',
    'chp_demo_recording_001',
    'Demo Stream Chapter',
    'Finalized chapter artifact for the seeded DVR recording',
    'demo_recording_chapter.mp4',
    'video/mp4',
    336471,
    'demo-media',
    NOW() + INTERVAL '7 days',
    FALSE,
    NOW() - INTERVAL '4 hours',
    NOW() - INTERVAL '4 hours'
),
-- Demo VOD (processing) - Still being validated
(
    '5eedb0d5-1e55-da7a-b0d5-1e55da7a0002',
    '5eed517e-ba5e-da7a-517e-ba5eda7a0001',
    '5eedface-5e1f-da7a-face-5e1fda7a0001',
    '20240101120500d4e5f6789012345a',        -- Must match foghorn.artifacts (30-char: timestamp+hex)
    'vod_int_002',
    'vod2a2b3c4d5e6fh',
    'Live Streaming Webinar',
    'Educational webinar about low-latency streaming',
    'webinar_recording.mp4',
    'video/mp4',
    104857600,
    'demo-media',
    NOW() + INTERVAL '30 days',
    FALSE,
    NOW() - INTERVAL '30 minutes',
    NOW() - INTERVAL '30 minutes'
),
-- Demo VOD (failed) - Invalid format
(
    '5eedb0d5-1e55-da7a-b0d5-1e55da7a0003',
    '5eed517e-ba5e-da7a-517e-ba5eda7a0001',
    '5eedface-5e1f-da7a-face-5e1fda7a0001',
    '20240101120600e5f6789012345678',        -- Must match foghorn.artifacts (30-char: timestamp+hex)
    'vod_int_003',
    'vod3a2b3c4d5e6fi',
    'Failed Upload',
    'This file failed validation due to unsupported format',
    'corrupted_file.avi',
    'video/x-msvideo',
    15728640,
    'demo-media',
    NOW() - INTERVAL '1 day',
    FALSE,
    NOW() - INTERVAL '2 days',
    NOW() - INTERVAL '2 days'
)
ON CONFLICT (vod_hash) DO UPDATE SET
    title = EXCLUDED.title,
    origin_cluster_id = EXCLUDED.origin_cluster_id,
    size_bytes = EXCLUDED.size_bytes,
    library_visible = EXCLUDED.library_visible,
    updated_at = NOW();
