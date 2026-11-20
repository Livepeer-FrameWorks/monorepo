-- name: GetStreamProcessingOverrides :one
SELECT COALESCE(processing.processes_live::text, '')::text AS processes_live,
       COALESCE(processing.processes_dvr::text, '')::text AS processes_dvr,
       COALESCE(processing.processes_clip::text, '')::text AS processes_clip,
       COALESCE(processing.processes_dvr_finalize::text, '')::text AS processes_dvr_finalize,
       COALESCE(processing.processes_vod::text, '')::text AS processes_vod
FROM commodore.stream_processing_config processing
JOIN commodore.streams stream ON stream.id = processing.stream_id
WHERE processing.stream_id = sqlc.arg(stream_id)::uuid
  AND stream.tenant_id = sqlc.arg(tenant_id)::uuid;

-- name: GetStreamLiveVideoABR :one
SELECT live_video_abr FROM commodore.streams
WHERE id = $1 AND tenant_id = $2 AND deleted_at IS NULL;

-- name: GetTenantProcessingOverrides :one
SELECT COALESCE(processes_live::text, '')::text AS processes_live,
       COALESCE(processes_dvr::text, '')::text AS processes_dvr,
       COALESCE(processes_clip::text, '')::text AS processes_clip,
       COALESCE(processes_dvr_finalize::text, '')::text AS processes_dvr_finalize,
       COALESCE(processes_vod::text, '')::text AS processes_vod
FROM commodore.tenant_processing_config
WHERE tenant_id = $1;

-- name: GetStreamRouteByPlaybackID :one
SELECT tenant_id, active_ingest_cluster_id
FROM commodore.streams
WHERE lower(playback_id::text) = lower($1::text) AND deleted_at IS NULL;

-- name: GetDVRSourceRoute :one
SELECT active_ingest_cluster_id, active_ingest_cluster_updated_at
FROM commodore.streams
WHERE id = $1 AND tenant_id = $2 AND deleted_at IS NULL;

-- name: GetStreamRouteByInternalName :one
SELECT tenant_id, active_ingest_cluster_id
FROM commodore.streams
WHERE internal_name = $1 AND deleted_at IS NULL;

-- name: GetStreamRouteByKey :one
SELECT tenant_id, active_ingest_cluster_id,
       COALESCE(active_ingest_cluster_updated_at > NOW() - ($2::bigint * INTERVAL '1 second'), false)::boolean AS lease_fresh
FROM commodore.streams
WHERE stream_key = $1 AND ingest_mode = 'push' AND deleted_at IS NULL;

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

-- name: GetStreamProcessingChoices :one
SELECT live_video_abr, transcode_ladder FROM commodore.streams
WHERE id = $1 AND tenant_id = $2 AND deleted_at IS NULL;

-- name: GetTenantTranscodeLadder :one
SELECT transcode_ladder FROM commodore.tenant_processing_config
WHERE tenant_id = $1;

-- name: SetTenantTranscodeLadder :exec
INSERT INTO commodore.tenant_processing_config (tenant_id, transcode_ladder, updated_at)
VALUES (sqlc.arg(tenant_id), sqlc.narg(transcode_ladder), NOW())
ON CONFLICT (tenant_id) DO UPDATE
SET transcode_ladder = EXCLUDED.transcode_ladder, updated_at = NOW();
