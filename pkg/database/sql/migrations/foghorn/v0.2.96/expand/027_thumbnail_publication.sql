-- Server-minted, node-bound thumbnail publication: per-attempt staging + immutable versioned objects and an
-- active pointer (keyed by the globally-unique asset_key) switched transactionally, so a node cannot overwrite
-- the served object and a mid-publish crash cannot strand or corrupt it. Dedicated tables (not columns on
-- foghorn.artifacts) because a live stream has no artifact row and one attempt owns multiple files. All-new
-- tables, so inline CHECK/FK are safe. Schema source of truth: pkg/database/sql/schema/foghorn.sql.

-- One row per server-minted publication attempt. attempt_id is crypto-rand and ECHOED by the node at
-- completion, so a completion binds a Foghorn-ASSIGNED operation rather than a node-chosen id. tenant_id is the
-- resource-bound media tenant (never a cluster owner). A stuck attempt past `expiry` is re-driven by the
-- recovery reconciler; a durable `publishing` state blocks a newer attempt for the same asset from racing it.
-- Server-owned STRICTLY-MONOTONIC claim order. The pointer CAS advances on claim_seq, not created_at: two
-- attempts can share a created_at (same millisecond, or clock skew across HA Foghorn instances), and a `>=`
-- created_at compare would then let each replace the other (a non-total order). A sequence gives every attempt a
-- distinct, strictly-increasing rank regardless of wall clock (gaps from rolled-back txns are fine — only the
-- ordering matters, not contiguity).
CREATE SEQUENCE IF NOT EXISTS foghorn.thumbnail_attempt_seq;

CREATE TABLE IF NOT EXISTS foghorn.thumbnail_task_assignment (
    attempt_id          TEXT PRIMARY KEY,
    tenant_id           TEXT NOT NULL,
    asset_key           TEXT NOT NULL,             -- stream_id (live) or artifact_hash (VOD/clip/DVR)
    node_id             TEXT NOT NULL,             -- assigned (canonical) producing node
    destination_cluster TEXT NOT NULL,             -- official-durable destination cluster
    status              TEXT NOT NULL CHECK (status IN ('assigned','uploading','verifying','publishing','published','failed')),
    version             TEXT NOT NULL DEFAULT '',  -- immutable version segment once promoted
    claim_seq           BIGINT NOT NULL DEFAULT nextval('foghorn.thumbnail_attempt_seq'), -- monotonic pointer-CAS rank
    superseded_at       TIMESTAMPTZ,               -- set when a newer version displaces this one; GC horizon anchor
    expiry              TIMESTAMPTZ NOT NULL,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
-- Idempotent for an environment that created the table before superseded_at was added.
ALTER TABLE foghorn.thumbnail_task_assignment ADD COLUMN IF NOT EXISTS superseded_at TIMESTAMPTZ;
-- Per-resource lookup (block a newer attempt / find the in-flight attempt for an asset).
CREATE INDEX IF NOT EXISTS idx_foghorn_thumb_task_resource ON foghorn.thumbnail_task_assignment(asset_key, tenant_id);
-- Recovery scan (attempts stuck in a non-terminal state past their lease).
CREATE INDEX IF NOT EXISTS idx_foghorn_thumb_task_recovery ON foghorn.thumbnail_task_assignment(status, expiry);

-- One row per file an attempt owns (allowlisted: poster.jpg / sprite.jpg / sprite.vtt). staging_key is the
-- per-attempt upload target; version_key is the immutable published object; etag/size are provider-observed at
-- verify (provider is authoritative). Cascades with its assignment.
CREATE TABLE IF NOT EXISTS foghorn.thumbnail_task_object (
    attempt_id  TEXT NOT NULL REFERENCES foghorn.thumbnail_task_assignment(attempt_id) ON DELETE CASCADE,
    file_name   TEXT NOT NULL,
    staging_key TEXT NOT NULL,             -- thumbnails/{asset}/.staging/{attempt}/{file}
    version_key TEXT NOT NULL DEFAULT '',  -- thumbnails/{asset}/v/{version}/{file}, set on promote
    etag        TEXT NOT NULL DEFAULT '',
    size_bytes  BIGINT NOT NULL DEFAULT 0,
    verified    BOOLEAN NOT NULL DEFAULT false,
    PRIMARY KEY (attempt_id, file_name)
);

-- The atomic active pointer: which immutable version each resource currently serves. A Foghorn-owned DB row
-- (NOT a mutable S3 object) switched by a guarded monotonic CAS, so there is no fixed mutable object to race.
-- asset_key is the GLOBALLY-UNIQUE public resource identity: a stream_id UUID or an opaque, randomly-minted
-- clip/dvr/vod hash (NOT a content hash — it never recurs across tenants; Commodore enforces uniqueness and
-- foghorn.artifacts.artifact_hash is a global primary key). tenant_id is mandatory OWNERSHIP/authorization
-- attribution, never part of the identity: every mutation proves tenant ownership, but public resolution
-- (Chandler's opaque-key URL, the S3 namespace, the cache) is keyed by asset_key alone.
CREATE TABLE IF NOT EXISTS foghorn.thumbnail_active_pointer (
    asset_key      TEXT PRIMARY KEY,
    tenant_id      TEXT NOT NULL,
    -- FK to the active attempt: deleting that assignment CASCADES the pointer, so purge can never leave a
    -- dangling pointer that outlives its backing objects (the pointer and its assignment are removed together).
    active_version TEXT NOT NULL REFERENCES foghorn.thumbnail_task_assignment(attempt_id) ON DELETE CASCADE,
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
