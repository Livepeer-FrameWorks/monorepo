-- Yugabyte sequence caches are session-local: two pooled sessions may commit values
-- in the opposite order from allocation. Correctness fences therefore serialize at
-- the key where their values are compared. New counters start at 2^52, while the
-- four client/session-cached legacy sequences deliberately remain in their existing
-- low namespace throughout expand. Restarting them here would let an old binary
-- allocate values inside the new namespace and defeat the rolling-upgrade fence.
-- Artifact catalog revisions are server-triggered, so their trigger moves directly
-- to the high namespace without a session-cached allocation window. Decimal
-- serialization for Redis watermarks is performed by Go, not by Lua concatenation.

CREATE TABLE IF NOT EXISTS foghorn.node_control_fence_counter (
    node_id VARCHAR(100) PRIMARY KEY,
    value BIGINT NOT NULL CHECK (value >= 4503599627370496 AND value < 9007199254740992)
);

INSERT INTO foghorn.node_control_fence_counter (node_id, value)
SELECT node_id, GREATEST(MAX(connection_fence), 4503599627370496)
FROM foghorn.node_artifact_report_watermark
GROUP BY node_id
ON CONFLICT (node_id) DO UPDATE
SET value = GREATEST(foghorn.node_control_fence_counter.value, EXCLUDED.value);

CREATE TABLE IF NOT EXISTS foghorn.source_projection_revision_counter (
    tenant_id UUID NOT NULL,
    stream_internal_name VARCHAR(255) NOT NULL,
    value BIGINT NOT NULL CHECK (value >= 4503599627370496 AND value < 9007199254740992),
    PRIMARY KEY (tenant_id, stream_internal_name)
);

WITH durable AS (
    SELECT tenant_id, stream_internal_name, source_revision
    FROM foghorn.ingest_sessions
    WHERE source_revision IS NOT NULL
    UNION ALL
    SELECT tenant_id, stream_internal_name, source_revision
    FROM foghorn.ingest_offline_effects
    UNION ALL
    SELECT tenant_id, stream_internal_name, source_revision
    FROM foghorn.ingest_admission_effects
)
INSERT INTO foghorn.source_projection_revision_counter (tenant_id, stream_internal_name, value)
SELECT tenant_id, stream_internal_name, GREATEST(MAX(source_revision), 4503599627370496)
FROM durable
GROUP BY tenant_id, stream_internal_name
ON CONFLICT (tenant_id, stream_internal_name) DO UPDATE
SET value = GREATEST(foghorn.source_projection_revision_counter.value, EXCLUDED.value);

CREATE TABLE IF NOT EXISTS foghorn.thumbnail_claim_counter (
    asset_key TEXT PRIMARY KEY,
    value BIGINT NOT NULL CHECK (value >= 4503599627370496 AND value < 9007199254740992)
);

INSERT INTO foghorn.thumbnail_claim_counter (asset_key, value)
SELECT asset_key, GREATEST(MAX(claim_seq), 4503599627370496)
FROM foghorn.thumbnail_task_assignment
GROUP BY asset_key
ON CONFLICT (asset_key) DO UPDATE
SET value = GREATEST(foghorn.thumbnail_claim_counter.value, EXCLUDED.value);

CREATE TABLE IF NOT EXISTS foghorn.artifact_node_copy_version_counter (
    artifact_hash VARCHAR(32) NOT NULL,
    node_id VARCHAR(100) NOT NULL,
    value BIGINT NOT NULL CHECK (value >= 4503599627370496 AND value < 9007199254740992),
    PRIMARY KEY (artifact_hash, node_id)
);

INSERT INTO foghorn.artifact_node_copy_version_counter (artifact_hash, node_id, value)
SELECT artifact_hash, node_id, GREATEST(last_emitted_version, 4503599627370496)
FROM foghorn.artifact_nodes
ON CONFLICT (artifact_hash, node_id) DO UPDATE
SET value = GREATEST(foghorn.artifact_node_copy_version_counter.value, EXCLUDED.value);

CREATE OR REPLACE FUNCTION foghorn.bump_artifact_catalog_revision() RETURNS trigger AS $$
BEGIN
    IF TG_OP = 'INSERT' THEN
        NEW.catalog_revision := GREATEST(COALESCE(NEW.catalog_revision, 0), 4503599627370496);
    ELSE
        NEW.catalog_revision := GREATEST(OLD.catalog_revision + 1, 4503599627370496);
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
