-- Global playback routing aliases for DVR chapter views.
--
-- Foghorn owns chapter materialization and the segment ledger. Commodore owns
-- public playback routing, so it records each materialized chapter ID with the
-- DVR artifact origin cluster before returning dvr+{chapter_id} to callers.

CREATE TABLE IF NOT EXISTS commodore.dvr_chapter_aliases (
    chapter_id VARCHAR(64) PRIMARY KEY,
    dvr_hash VARCHAR(32) NOT NULL,
    tenant_id UUID NOT NULL,
    stream_id UUID,
    origin_cluster_id VARCHAR(100) NOT NULL,
    mode VARCHAR(32) NOT NULL,
    interval_seconds INTEGER,
    start_ms BIGINT NOT NULL,
    end_ms BIGINT NOT NULL,
    created_at TIMESTAMP DEFAULT NOW(),
    updated_at TIMESTAMP DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_commodore_dvr_chapter_alias_dvr
    ON commodore.dvr_chapter_aliases(dvr_hash, start_ms, end_ms);
CREATE INDEX IF NOT EXISTS idx_commodore_dvr_chapter_alias_tenant
    ON commodore.dvr_chapter_aliases(tenant_id, created_at DESC);
