-- DVR chapter playback ID registry. Hidden chapter artifacts
-- (origin_type='dvr_chapter', library_visible=false) get real public
-- playback IDs minted by Commodore so chapter playback shares the same
-- public-ID boundary as VOD. The mapping is keyed by chapter_id;
-- artifact_hash is denormalized for fast resolver paths.

CREATE TABLE IF NOT EXISTS commodore.dvr_chapter_playback (
    chapter_id    VARCHAR(32) PRIMARY KEY,
    tenant_id     UUID NOT NULL,
    playback_id   CITEXT NOT NULL,
    artifact_hash VARCHAR(32) NOT NULL,
    created_at    TIMESTAMP DEFAULT NOW(),
    updated_at    TIMESTAMP DEFAULT NOW()
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_commodore_dvr_chapter_playback_pid_ci
    ON commodore.dvr_chapter_playback((lower(playback_id::text)));

CREATE INDEX IF NOT EXISTS idx_commodore_dvr_chapter_playback_tenant
    ON commodore.dvr_chapter_playback(tenant_id);

CREATE INDEX IF NOT EXISTS idx_commodore_dvr_chapter_playback_artifact
    ON commodore.dvr_chapter_playback(artifact_hash);
