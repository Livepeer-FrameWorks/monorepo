-- Deterministic local demo fixture owned by foghorn.
-- Applied explicitly by `make seed-demo`; never loaded by database first boot.

-- ============================================================================
-- FOGHORN: Demo Artifacts (Unified Clip/DVR Lifecycle Table)
-- ============================================================================
-- Demo artifacts for development and testing
-- Note: Business metadata (tenant_id, title, description) is in Commodore
-- Foghorn only stores lifecycle state here
-- The artifact_hash values MUST match the clip_hash/dvr_hash in commodore.clips/dvr_recordings above

INSERT INTO foghorn.artifacts (
    artifact_hash, artifact_type, stream_internal_name, internal_name, tenant_id,
    origin_cluster_id, status, size_bytes, manifest_path, format,
    storage_location, sync_status, retention_until, library_visible,
    created_at, updated_at
) VALUES
-- Demo clip (ready)
(
    'a1b2c3d4e5f6789012345678901234ab',      -- Must match on-disk filename
    'clip',
    'demo_live_stream_001',
    'clip_int_001',
    '5eed517e-ba5e-da7a-517e-ba5eda7a0001',  -- Demo tenant (denormalized for fallback)
    'demo-media',
    'ready',
    107553,         -- Browser-safe H.264/AAC fixture
    '/var/lib/mistserver/recordings/clips/demo_live_stream_001/a1b2c3d4e5f6789012345678901234ab.mp4',
    'mp4',
    'local',
    'pending',
    NOW() + INTERVAL '7 days',   -- 7-day rolling retention for demo fixtures
    TRUE,
    NOW() - INTERVAL '2 hours',
    NOW() - INTERVAL '2 hours'
),
-- Demo clip (deleted, for testing cleanup flows)
(
    '20240101120100b2c3d4e5f6789012',        -- 30-char: timestamp(14) + hex(16)
    'clip',
    'demo_live_stream_001',
    'clip_int_002',
    '5eed517e-ba5e-da7a-517e-ba5eda7a0001',  -- Demo tenant
    'demo-media',
    'deleted',
    140795,
    '/var/lib/mistserver/recordings/clips/demo_live_stream_001/20240101120100b2c3d4e5f6789012.mp4',
    'mp4',
    'local',
    'pending',
    NOW() - INTERVAL '1 day',    -- Already expired (past retention)
    TRUE,
    NOW() - INTERVAL '2 days',
    NOW() - INTERVAL '1 day'
),
-- Demo DVR recording (completed)
(
    'fedcba98765432109876543210fedcba',      -- Must match on-disk directory/filename
    'dvr',
    'demo_live_stream_001',
    'dvr_int_001',
    '5eed517e-ba5e-da7a-517e-ba5eda7a0001',  -- Demo tenant
    'demo-media',
    'completed',
    513176,         -- Actual total size: ~501KB (2 segments + manifest)
    '/var/lib/mistserver/recordings/dvr/5eedfeed-11fe-ca57-feed-11feca570001/fedcba98765432109876543210fedcba',
    'm3u8',
    'local',
    'pending',
    NOW() + INTERVAL '7 days',   -- 7-day rolling retention for demo fixtures
    TRUE,
    NOW() - INTERVAL '4 hours',
    NOW() - INTERVAL '4 hours'
),
-- Demo DVR recording (deleted, for testing cleanup flows)
(
    '20240101120300fedcba9876543211',        -- 30-char: timestamp(14) + hex(16)
    'dvr',
    'demo_live_stream_001',
    'dvr_int_002',
    '5eed517e-ba5e-da7a-517e-ba5eda7a0001',  -- Demo tenant
    'demo-media',
    'deleted',
    1024000,
    '/var/lib/mistserver/recordings/dvr/5eedfeed-11fe-ca57-feed-11feca570001/20240101120300fedcba9876543211',
    'm3u8',
    'local',
    'pending',
    NOW() - INTERVAL '1 day',    -- Already expired (past retention)
    TRUE,
    NOW() - INTERVAL '2 days',
    NOW() - INTERVAL '1 day'
),
-- Demo DVR chapter artifact (hidden finalized VOD for the seeded recording)
(
    '34d74b7acd7ec8cf78f6cc8c9f031a8a',
    'vod',
    'demo_live_stream_001',
    '34d74b7acd7ec8cf78f6cc8c9f031a8a',
    '5eed517e-ba5e-da7a-517e-ba5eda7a0001',
    'demo-media',
    'ready',
    336471,
    NULL,
    'mp4',
    'local',
    'pending',
    NOW() + INTERVAL '7 days',
    FALSE,
    NOW() - INTERVAL '4 hours',
    NOW() - INTERVAL '4 hours'
),
-- Demo VOD asset (ready, warmed to edge)
(
    'c3d4e5f678901234567890123456abcd',      -- Must match on-disk filename
    'vod',
    NULL,                                     -- No stream association
    'vod_int_001',
    '5eed517e-ba5e-da7a-517e-ba5eda7a0001',  -- Demo tenant
    'demo-media',
    'ready',
    107553,         -- H264/AAC fixture compatible with HLS/MP4 playback
    NULL,            -- No manifest for VOD (direct file playback)
    'mp4',
    'local',         -- On disk, pending sync to S3
    'pending',
    NOW() + INTERVAL '30 days',   -- 30-day retention for VOD
    TRUE,
    NOW() - INTERVAL '1 day',
    NOW() - INTERVAL '1 day'
),
-- Demo VOD asset (processing, just uploaded)
(
    '20240101120500d4e5f6789012345a',        -- 30-char: timestamp(14) + hex(16)
    'vod',
    NULL,
    'vod_int_002',
    '5eed517e-ba5e-da7a-517e-ba5eda7a0001',  -- Demo tenant
    'demo-media',
    'processing',
    104857600,       -- 100MB
    NULL,
    'mp4',
    's3',
    'synced',
    NOW() + INTERVAL '30 days',
    FALSE,
    NOW() - INTERVAL '30 minutes',
    NOW() - INTERVAL '30 minutes'
),
-- Demo VOD asset (failed validation)
(
    '20240101120600e5f6789012345678',        -- 30-char: timestamp(14) + hex(16)
    'vod',
    NULL,
    'vod_int_003',
    '5eed517e-ba5e-da7a-517e-ba5eda7a0001',  -- Demo tenant
    'demo-media',
    'failed',
    15728640,        -- 15MB
    NULL,
    'avi',
    's3',
    'synced',
    NOW() - INTERVAL '1 day',    -- Already expired
    FALSE,
    NOW() - INTERVAL '2 days',
    NOW() - INTERVAL '2 days'
)
ON CONFLICT (artifact_hash) DO UPDATE SET
    origin_cluster_id = EXCLUDED.origin_cluster_id,
    status = EXCLUDED.status,
    size_bytes = EXCLUDED.size_bytes,
    manifest_path = EXCLUDED.manifest_path,
    format = EXCLUDED.format,
    storage_location = EXCLUDED.storage_location,
    sync_status = EXCLUDED.sync_status,
    library_visible = EXCLUDED.library_visible,
    updated_at = NOW();

UPDATE foghorn.artifacts
SET artifact_type = 'vod',
    stream_internal_name = 'demo_live_stream_001',
    internal_name = '34d74b7acd7ec8cf78f6cc8c9f031a8a',
    tenant_id = '5eed517e-ba5e-da7a-517e-ba5eda7a0001',
    origin_cluster_id = 'demo-media',
    status = 'ready',
    size_bytes = 336471,
    manifest_path = NULL,
    format = 'mp4',
    storage_location = 'local',
    sync_status = 'pending',
    origin_type = 'dvr_chapter',
    origin_id = '34d74b7acd7ec8cf78f6cc8c9f031a8a',
    library_visible = FALSE,
    updated_at = NOW()
WHERE artifact_hash = '34d74b7acd7ec8cf78f6cc8c9f031a8a';

-- ============================================================================
-- FOGHORN: DVR segment ledger (per-segment source of truth)
-- ============================================================================
-- Seed rows match the on-disk media at
-- infrastructure/demo-recordings/dvr/<tenant>/<hash>/segments/segment_*.ts
-- which is bind-mounted into MistServer at /var/lib/mistserver/recordings.
-- status='pending' is honest: the sidecar startup reconciliation will upload
-- these to S3 (in dev environments with S3 creds) and flip them to 'uploaded'.
-- Until then the segments stay as recovery-source-only durability for
-- chapter finalization; they are not a playback surface.

INSERT INTO foghorn.dvr_segments (
    artifact_hash, segment_name, sequence,
    media_start_ms, media_end_ms, duration_ms,
    size_bytes, s3_key, status, created_at
) VALUES
(
    'fedcba98765432109876543210fedcba',
    'segment_0.ts', 0,
    1779105600000, 1779105610417,
    10417,
    NULL,
    'dvr/5eed517e-ba5e-da7a-517e-ba5eda7a0001/demo_live_stream_001/fedcba98765432109876543210fedcba/segments/segment_0.ts',
    'pending', NOW() - INTERVAL '4 hours'
),
(
    'fedcba98765432109876543210fedcba',
    'segment_1.ts', 1,
    1779105610417, 1779105618000,
    7583,
    NULL,
    'dvr/5eed517e-ba5e-da7a-517e-ba5eda7a0001/demo_live_stream_001/fedcba98765432109876543210fedcba/segments/segment_1.ts',
    'pending', NOW() - INTERVAL '4 hours'
)
ON CONFLICT (artifact_hash, segment_name) DO NOTHING;

-- ============================================================================
-- FOGHORN: DVR chapter window (virtual view over the segment ledger)
-- ============================================================================
-- One fixed-interval chapter spans the seeded recording. Its playback
-- surface is the hidden VOD artifact above, matching production chapter
-- finalization rather than the retired chapter-manifest path.

-- Demo chapter row: a single fixed-interval chapter covering the
-- recorded DVR window. chapter_id is the canonical
-- BuildChapterID(artifact_hash, mode, intervalSeconds, start_ms, end_ms)
-- so chapter-sweeper / direct lookups find this row instead of
-- regenerating a sibling:
--   sha256("fedcba98765432109876543210fedcba|fixed_interval|3600|1779105600000|1779105618000")[:32]
--   = 34d74b7acd7ec8cf78f6cc8c9f031a8a
--
INSERT INTO foghorn.dvr_chapters (
    chapter_id, artifact_hash, mode, interval_seconds,
    start_ms, end_ms, is_current,
    state, playback_artifact_hash,
    playback_id, segment_count, has_gaps,
    actual_media_start_ms, actual_media_end_ms,
    created_at
) VALUES (
    '34d74b7acd7ec8cf78f6cc8c9f031a8a',
    'fedcba98765432109876543210fedcba',
    'fixed_interval', 3600,
    1779105600000, 1779105618000,
    false,
    'finalized', '34d74b7acd7ec8cf78f6cc8c9f031a8a',
    'chp_demo_recording_001', 2, false,
    1779105600000, 1779105618000,
    NOW() - INTERVAL '4 hours'
)
ON CONFLICT (chapter_id) DO UPDATE SET
    state = EXCLUDED.state,
    playback_artifact_hash = EXCLUDED.playback_artifact_hash,
    playback_id = EXCLUDED.playback_id,
    segment_count = EXCLUDED.segment_count,
    has_gaps = EXCLUDED.has_gaps,
    actual_media_start_ms = EXCLUDED.actual_media_start_ms,
    actual_media_end_ms = EXCLUDED.actual_media_end_ms;

-- ============================================================================
-- FOGHORN: VOD Metadata (User-Uploaded Video Details)
-- ============================================================================
-- VOD-specific metadata like title, description, codecs, duration

INSERT INTO foghorn.vod_metadata (
    artifact_hash, filename, title, description, content_type,
    s3_upload_id, s3_key, upload_expires_at, total_parts,
    duration_ms, resolution, video_codec, audio_codec, bitrate_kbps,
    width, height, fps, audio_channels, audio_sample_rate,
    created_at, updated_at
) VALUES
-- Demo VOD (ready) - Product demo video
(
    'c3d4e5f678901234567890123456abcd',      -- Must match foghorn.artifacts + on-disk filename
    'product_demo_2024.mp4',
    'Product Demo 2024',
    'Annual product demonstration showcasing new streaming features',
    'video/mp4',
    NULL,            -- Upload completed
    'vod/5eed517e-ba5e-da7a-517e-ba5eda7a0001/c3d4e5f678901234567890123456abcd/c3d4e5f678901234567890123456abcd.mp4',
    NULL,
    1,
    5000,            -- 5 seconds
    '640x360',
    'h264',
    'aac',
    300,             -- ~300 kbps
    640, 360, 30.0, 2, 48000,
    NOW() - INTERVAL '1 day',
    NOW() - INTERVAL '1 day'
),
-- Demo VOD (processing) - Still being validated
(
    '20240101120500d4e5f6789012345a',        -- Must match foghorn.artifacts (30-char: timestamp+hex)
    'webinar_recording.mp4',
    'Live Streaming Webinar',
    'Educational webinar about low-latency streaming',
    'video/mp4',
    'abc123multipartupload',   -- Still has upload ID (not yet cleaned)
    'vod/5eed517e-ba5e-da7a-517e-ba5eda7a0001/20240101120500d4e5f6789012345a/20240101120500d4e5f6789012345a.mp4',
    NOW() + INTERVAL '90 minutes',
    5,
    NULL,            -- Not yet validated
    NULL,
    NULL,
    NULL,
    NULL,
    NULL, NULL, NULL, NULL, NULL,
    NOW() - INTERVAL '30 minutes',
    NOW() - INTERVAL '30 minutes'
),
-- Demo VOD (failed) - Invalid format
(
    '20240101120600e5f6789012345678',        -- Must match foghorn.artifacts (30-char: timestamp+hex)
    'corrupted_file.avi',
    'Failed Upload',
    'This file failed validation due to unsupported format',
    'video/x-msvideo',
    NULL,
    'vod/5eed517e-ba5e-da7a-517e-ba5eda7a0001/20240101120600e5f6789012345678/20240101120600e5f6789012345678.avi',
    NOW() - INTERVAL '1 day',
    1,
    NULL,
    NULL,
    NULL,
    NULL,
    NULL,
    NULL, NULL, NULL, NULL, NULL,
    NOW() - INTERVAL '2 days',
    NOW() - INTERVAL '2 days'
)
ON CONFLICT (artifact_hash) DO UPDATE SET
    title = EXCLUDED.title,
    updated_at = NOW();

-- ============================================================================
-- FOGHORN: Artifact Nodes (Warm Storage Distribution)
-- ============================================================================
-- Register demo artifacts on nodes so Foghorn can resolve them for VOD playback

INSERT INTO foghorn.artifact_nodes (
    artifact_hash, node_id, file_path, size_bytes, access_count, last_accessed, last_seen_at, is_orphaned
) VALUES
-- Demo clip on edge-node-1
(
    'a1b2c3d4e5f6789012345678901234ab',
    'edge-node-1',
    '/var/lib/mistserver/recordings/clips/demo_live_stream_001/a1b2c3d4e5f6789012345678901234ab.mp4',
    107553,
    42,
    NOW() - INTERVAL '3 hours',
    NOW(),
    false
),
-- Demo DVR on edge-node-1
(
    'fedcba98765432109876543210fedcba',
    'edge-node-1',
    '/var/lib/mistserver/recordings/dvr/5eedfeed-11fe-ca57-feed-11feca570001/fedcba98765432109876543210fedcba',
    513176,
    7,
    NOW() - INTERVAL '1 day',
    NOW(),
    false
),
-- Finalized chapter VOD for the seeded DVR on edge-node-1
(
    '34d74b7acd7ec8cf78f6cc8c9f031a8a',
    'edge-node-1',
    '/var/lib/mistserver/recordings/vod/34d74b7acd7ec8cf78f6cc8c9f031a8a.mp4',
    336471,
    7,
    NOW() - INTERVAL '3 hours',
    NOW(),
    false
),
-- Demo VOD on edge-node-1 (warmed from S3)
(
    'c3d4e5f678901234567890123456abcd',
    'edge-node-1',
    '/var/lib/mistserver/recordings/vod/c3d4e5f678901234567890123456abcd.mp4',
    107553,
    128,
    NOW() - INTERVAL '2 hours',
    NOW(),
    false
)
ON CONFLICT (artifact_hash, node_id) DO UPDATE SET
    file_path = EXCLUDED.file_path,
    size_bytes = EXCLUDED.size_bytes,
    last_seen_at = NOW(),
    is_orphaned = false;
