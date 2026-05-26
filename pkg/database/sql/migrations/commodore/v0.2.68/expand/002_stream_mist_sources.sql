-- Mist-native source config sidecar for ingest_mode='mist_native' streams.
-- Mirrors stream_pull_sources in shape: one row per stream, FK cascade-delete.
-- Schema source of truth: pkg/database/sql/schema/commodore.sql
--
-- source_kind discriminates safe input forms. Bootstrap render restricts
-- mist_native streams of every source_kind to the operator/system tenant.
--
-- placement_count is the number of eligible healthy edge nodes Foghorn pins
-- this stream to on each reconcile (deterministic-hash placement).
--
-- allowed_cluster_ids currently names exactly one source cluster. The array
-- shape is kept for pull-stream symmetry.
--
-- local_asset_paths is informational: ansible places the actual files, this
-- column documents what bootstrap declared so operators can audit expected
-- on-disk state.

CREATE TABLE IF NOT EXISTS commodore.stream_mist_sources (
    stream_id UUID PRIMARY KEY REFERENCES commodore.streams(id) ON DELETE CASCADE,

    source_spec TEXT NOT NULL,
    source_kind TEXT NOT NULL
        CONSTRAINT stream_mist_sources_kind_chk CHECK (source_kind IN ('file', 'playlist', 'exec')),

    placement_count INTEGER NOT NULL DEFAULT 1
        CONSTRAINT stream_mist_sources_placement_chk CHECK (placement_count >= 1),
    allowed_cluster_ids TEXT[] NOT NULL
        CONSTRAINT stream_mist_sources_allowed_clusters_chk
            CHECK (cardinality(allowed_cluster_ids) = 1),

    local_asset_paths JSONB NOT NULL DEFAULT '[]'::JSONB,

    created_at TIMESTAMP DEFAULT NOW(),
    updated_at TIMESTAMP DEFAULT NOW()
);
