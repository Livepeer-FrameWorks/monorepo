-- name: GetStreamProcessingOverrides :one
SELECT processes_live, processes_dvr, processes_clip, processes_dvr_finalize, processes_vod
FROM commodore.stream_processing_config
WHERE stream_id = $1;

-- name: GetTenantProcessingOverrides :one
SELECT processes_live, processes_dvr, processes_clip, processes_dvr_finalize, processes_vod
FROM commodore.tenant_processing_config
WHERE tenant_id = $1;

-- name: GetStreamRouteByPlaybackID :one
SELECT tenant_id, active_ingest_cluster_id
FROM commodore.streams
WHERE lower(playback_id::text) = lower($1::text) AND deleted_at IS NULL;

-- name: GetStreamRouteByInternalName :one
SELECT tenant_id, active_ingest_cluster_id
FROM commodore.streams
WHERE internal_name = $1 AND deleted_at IS NULL;

-- name: GetStreamRouteByKey :one
SELECT tenant_id, active_ingest_cluster_id,
       COALESCE(active_ingest_cluster_updated_at > NOW() - ($2::bigint * INTERVAL '1 second'), false)::boolean AS lease_fresh
FROM commodore.streams
WHERE stream_key = $1 AND deleted_at IS NULL;

-- name: GetArtifactRouteByContent :one
SELECT tenant_id, COALESCE(cluster_id::text, '')::text AS cluster_id
FROM (
    SELECT tenant_id,
           COALESCE(NULLIF(storage_cluster_id, ''), NULLIF(origin_cluster_id, '')) AS cluster_id
    FROM commodore.clips
    WHERE lower(playback_id::text) = lower($1::text) OR clip_hash = $1
    UNION ALL
    SELECT tenant_id,
           COALESCE(NULLIF(storage_cluster_id, ''), NULLIF(origin_cluster_id, '')) AS cluster_id
    FROM commodore.vod_assets
    WHERE lower(playback_id::text) = lower($1::text) OR vod_hash = $1
    UNION ALL
    SELECT tenant_id,
           COALESCE(NULLIF(storage_cluster_id, ''), NULLIF(origin_cluster_id, '')) AS cluster_id
    FROM commodore.dvr_recordings
    WHERE lower(playback_id::text) = lower($1::text) OR dvr_hash = $1
    UNION ALL
    SELECT cp.tenant_id,
           COALESCE(NULLIF(va.storage_cluster_id, ''), NULLIF(va.origin_cluster_id, '')) AS cluster_id
    FROM commodore.dvr_chapter_playback cp
    JOIN commodore.vod_assets va
      ON va.tenant_id = cp.tenant_id AND va.vod_hash = cp.artifact_hash
    WHERE lower(cp.playback_id::text) = lower($1::text)
) resolved
LIMIT 1;
