-- name: ArtifactIdentifierExists :one
SELECT EXISTS (
    SELECT 1 FROM commodore.streams s
    WHERE s.internal_name = $1 OR lower(s.playback_id::text) = lower($1::text)
    UNION ALL
    SELECT 1 FROM commodore.clips c
    WHERE c.internal_name = $1 OR lower(c.playback_id::text) = lower($1::text) OR c.clip_hash = $1
    UNION ALL
    SELECT 1 FROM commodore.dvr_recordings d
    WHERE d.internal_name = $1 OR lower(d.playback_id::text) = lower($1::text) OR d.dvr_hash = $1
    UNION ALL
    SELECT 1 FROM commodore.vod_assets v
    WHERE v.internal_name = $1 OR lower(v.playback_id::text) = lower($1::text) OR v.vod_hash = $1
);

-- name: GetClipSourceStream :one
SELECT internal_name, active_ingest_cluster_id, active_ingest_cluster_updated_at,
       requires_auth, COALESCE(playback_policy::text, '')::text AS playback_policy,
       playback_webhook_secret_enc
FROM commodore.streams
WHERE id = $1 AND tenant_id = $2 AND deleted_at IS NULL;

-- name: GetClipRegistry :one
SELECT c.id, c.clip_hash, c.playback_id, c.stream_id::text, c.title, c.description,
       c.start_time, c.duration, c.clip_mode,
       COALESCE(c.requested_params::text, '')::text AS requested_params,
       c.size_bytes, c.retention_until, COALESCE(c.retention_source, '')::text AS retention_source,
       c.created_at, c.updated_at,
       COALESCE(c.thumbnail_serving_cluster_id, c.storage_cluster_id, c.origin_cluster_id, '')::text AS thumbnail_cluster,
       c.has_thumbnails
FROM commodore.clips c
WHERE c.tenant_id = $1 AND c.clip_hash = $2;

-- name: GetClipDeletionRoute :one
SELECT stream_id::text, origin_cluster_id
FROM commodore.clips
WHERE clip_hash = $1 AND tenant_id = $2;

-- name: GetDVRDeletionRoute :one
SELECT stream_id::text, origin_cluster_id
FROM commodore.dvr_recordings
WHERE dvr_hash = $1 AND tenant_id = $2;

-- name: GetStreamDisplayMetadata :one
SELECT title, description
FROM commodore.streams
WHERE id = $1 AND tenant_id = $2;

-- name: NormalizeArtifactPlaybackID :one
SELECT playback_id::text
FROM (
    SELECT playback_id FROM commodore.clips WHERE clip_hash = $1
    UNION ALL
    SELECT playback_id FROM commodore.vod_assets WHERE vod_hash = $1
    UNION ALL
    SELECT playback_id FROM commodore.dvr_recordings WHERE dvr_hash = $1
) resolved
WHERE playback_id IS NOT NULL AND playback_id != ''
LIMIT 1;
