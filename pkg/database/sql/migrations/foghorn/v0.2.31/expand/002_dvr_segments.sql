-- DVR PER-SEGMENT LEDGER
-- Durable record of every recorded segment for DVR/always-on streams.
-- Foghorn is the source of truth; sidecar reports segments via control stream.
-- Chapter manifests read bounded ranges from this ledger.
--
-- See: docs/architecture/dvr-continuous-archive.md

CREATE TABLE IF NOT EXISTS foghorn.dvr_segments (
    artifact_hash   VARCHAR(32) NOT NULL REFERENCES foghorn.artifacts(artifact_hash) ON DELETE CASCADE,
    segment_name    TEXT NOT NULL,
    sequence        BIGINT NOT NULL,
    media_start_ms  BIGINT NOT NULL,
    media_end_ms    BIGINT NOT NULL,
    duration_ms     BIGINT NOT NULL,
    size_bytes      BIGINT,                 -- set when upload acknowledged
    s3_key          TEXT NOT NULL,
    status          VARCHAR(20) NOT NULL DEFAULT 'pending',
        -- pending        : ledger row exists, S3 upload in flight or queued
        -- uploaded       : S3 confirmed; safe to evict locally once outside live window
        -- failed_upload  : upload attempt failed; retry policy applies
        -- deleted_local  : local copy gone, S3 has it; renders normal #EXTINF
        -- lost_local     : local copy gone before S3 upload; renders #EXT-X-GAP
    drop_reason     VARCHAR(32),
        -- disk_pressure | retention_expired | operator_cleanup | upload_failed
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    uploaded_at     TIMESTAMPTZ,
    deleted_local_at TIMESTAMPTZ,
    dropped_at      TIMESTAMPTZ,

    PRIMARY KEY (artifact_hash, segment_name),

    CONSTRAINT chk_foghorn_dvr_segments_status CHECK (status IN (
        'pending', 'uploaded', 'failed_upload', 'deleted_local', 'lost_local'
    ))
);

-- Sequence is monotonic per artifact; enforce uniqueness so manifest ordering is stable.
CREATE UNIQUE INDEX IF NOT EXISTS idx_foghorn_dvr_segments_sequence
    ON foghorn.dvr_segments(artifact_hash, sequence);

-- Manifest generation walks segments in media-time order.
CREATE INDEX IF NOT EXISTS idx_foghorn_dvr_segments_media_order
    ON foghorn.dvr_segments(artifact_hash, media_start_ms, sequence);

-- Eviction queries (uploaded AND outside live window).
CREATE INDEX IF NOT EXISTS idx_foghorn_dvr_segments_evictable
    ON foghorn.dvr_segments(artifact_hash, status, media_end_ms)
    WHERE status = 'uploaded';

-- Finalization retry queries (pending/failed_upload rows older than threshold).
CREATE INDEX IF NOT EXISTS idx_foghorn_dvr_segments_pending
    ON foghorn.dvr_segments(artifact_hash, status, created_at)
    WHERE status IN ('pending', 'failed_upload');

-- DVR ARTIFACT STATUS RENAME — stopped -> completed
-- New DVR state machine:
--     requested -> starting -> recording -> finalizing
--                  -> completed | completed_partial | failed
-- Edge DVR is not live in production; no real data to migrate. Any local
-- dev/test rows still on the prior stopped status can be reset by hand
-- (see api_balancing service docs) — bulk rewrites are not permitted in
-- expand migrations.
