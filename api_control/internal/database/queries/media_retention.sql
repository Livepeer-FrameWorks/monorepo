-- name: GetTenantMediaRetentionPolicy :one
SELECT default_vod_retention_days,
       default_dvr_retention_days,
       default_clip_retention_days,
       COALESCE(updated_by::text, ''::text)::text AS updated_by,
       updated_at
FROM commodore.tenant_media_retention_policies
WHERE tenant_id = sqlc.arg(tenant_id)::uuid;

-- name: UpsertTenantMediaRetentionPolicy :exec
INSERT INTO commodore.tenant_media_retention_policies
    (tenant_id,
     default_vod_retention_days,
     default_dvr_retention_days,
     default_clip_retention_days,
     updated_by,
     created_at,
     updated_at)
VALUES
    (sqlc.arg(tenant_id)::uuid,
     CASE WHEN sqlc.arg(apply_vod)::boolean THEN sqlc.narg(vod_days)::integer ELSE NULL END,
     CASE WHEN sqlc.arg(apply_dvr)::boolean THEN sqlc.narg(dvr_days)::integer ELSE NULL END,
     CASE WHEN sqlc.arg(apply_clip)::boolean THEN sqlc.narg(clip_days)::integer ELSE NULL END,
     NULLIF(sqlc.arg(updated_by)::text, '')::uuid,
     NOW(),
     NOW())
ON CONFLICT (tenant_id) DO UPDATE
SET default_vod_retention_days = CASE
        WHEN sqlc.arg(apply_vod)::boolean THEN EXCLUDED.default_vod_retention_days
        ELSE tenant_media_retention_policies.default_vod_retention_days
    END,
    default_dvr_retention_days = CASE
        WHEN sqlc.arg(apply_dvr)::boolean THEN EXCLUDED.default_dvr_retention_days
        ELSE tenant_media_retention_policies.default_dvr_retention_days
    END,
    default_clip_retention_days = CASE
        WHEN sqlc.arg(apply_clip)::boolean THEN EXCLUDED.default_clip_retention_days
        ELSE tenant_media_retention_policies.default_clip_retention_days
    END,
    updated_by = EXCLUDED.updated_by,
    updated_at = NOW();

-- name: GetStreamRetentionOverrides :one
SELECT id::text AS stream_id,
       dvr_retention_days_override,
       clip_retention_days_override
FROM commodore.streams
WHERE id = sqlc.arg(stream_id)::uuid
  AND tenant_id = sqlc.arg(tenant_id)::uuid;

-- name: UpdateStreamRetentionOverrides :one
UPDATE commodore.streams
SET dvr_retention_days_override = CASE
        WHEN sqlc.arg(apply_dvr)::boolean THEN sqlc.narg(dvr_days)::integer
        ELSE dvr_retention_days_override
    END,
    clip_retention_days_override = CASE
        WHEN sqlc.arg(apply_clip)::boolean THEN sqlc.narg(clip_days)::integer
        ELSE clip_retention_days_override
    END,
    updated_at = NOW()
WHERE id = sqlc.arg(stream_id)::uuid
  AND tenant_id = sqlc.arg(tenant_id)::uuid
RETURNING dvr_retention_days_override, clip_retention_days_override;

-- name: ResolveDVRTenantArtifact :one
SELECT COALESCE(origin_cluster_id, '') AS origin_cluster_id, dvr_hash
FROM commodore.dvr_recordings
WHERE (dvr_hash = sqlc.arg(identifier)
       OR id::text = sqlc.arg(identifier)
       OR lower(playback_id::text) = lower(sqlc.arg(identifier)::text))
  AND tenant_id = sqlc.arg(tenant_id)::uuid;

-- name: ResolveClipTenantArtifact :one
SELECT COALESCE(origin_cluster_id, '') AS origin_cluster_id, clip_hash
FROM commodore.clips
WHERE (clip_hash = sqlc.arg(identifier) OR id::text = sqlc.arg(identifier))
  AND tenant_id = sqlc.arg(tenant_id)::uuid;

-- name: ResolveVODTenantArtifact :one
SELECT COALESCE(origin_cluster_id, '') AS origin_cluster_id,
       vod_hash,
       COALESCE(origin_type, '') AS origin_type
FROM commodore.vod_assets
WHERE (vod_hash = sqlc.arg(identifier) OR id::text = sqlc.arg(identifier))
  AND tenant_id = sqlc.arg(tenant_id)::uuid;

-- name: GetDVRRetentionStreamID :one
SELECT COALESCE(stream_id::text, ''::text)::text AS stream_id
FROM commodore.dvr_recordings
WHERE dvr_hash = sqlc.arg(artifact_hash)
  AND tenant_id = sqlc.arg(tenant_id)::uuid;

-- name: GetClipRetentionStreamID :one
SELECT COALESCE(stream_id::text, ''::text)::text AS stream_id
FROM commodore.clips
WHERE clip_hash = sqlc.arg(artifact_hash)
  AND tenant_id = sqlc.arg(tenant_id)::uuid;

-- name: GetDVRRetentionUntil :one
SELECT retention_until
FROM commodore.dvr_recordings
WHERE dvr_hash = sqlc.arg(artifact_hash)
  AND tenant_id = sqlc.arg(tenant_id)::uuid;

-- name: GetClipRetentionUntil :one
SELECT retention_until
FROM commodore.clips
WHERE clip_hash = sqlc.arg(artifact_hash)
  AND tenant_id = sqlc.arg(tenant_id)::uuid;

-- name: GetVODRetentionUntil :one
SELECT retention_until
FROM commodore.vod_assets
WHERE vod_hash = sqlc.arg(artifact_hash)
  AND tenant_id = sqlc.arg(tenant_id)::uuid;

-- name: ApplyDVRRetentionState :exec
UPDATE commodore.dvr_recordings
SET retention_override_days = sqlc.narg(override_days)::integer,
    retention_override_until = sqlc.narg(override_until)::timestamptz,
    retention_source = sqlc.arg(retention_source),
    retention_until = sqlc.narg(retention_until)::timestamptz,
    updated_at = NOW()
WHERE dvr_hash = sqlc.arg(artifact_hash)
  AND tenant_id = sqlc.arg(tenant_id)::uuid;

-- name: ApplyClipRetentionState :exec
UPDATE commodore.clips
SET retention_override_days = sqlc.narg(override_days)::integer,
    retention_override_until = sqlc.narg(override_until)::timestamptz,
    retention_source = sqlc.arg(retention_source),
    retention_until = sqlc.narg(retention_until)::timestamptz,
    updated_at = NOW()
WHERE clip_hash = sqlc.arg(artifact_hash)
  AND tenant_id = sqlc.arg(tenant_id)::uuid;

-- name: ApplyVODRetentionState :exec
UPDATE commodore.vod_assets
SET retention_override_days = sqlc.narg(override_days)::integer,
    retention_override_until = sqlc.narg(override_until)::timestamptz,
    retention_source = sqlc.arg(retention_source),
    retention_until = sqlc.narg(retention_until)::timestamptz,
    updated_at = NOW()
WHERE vod_hash = sqlc.arg(artifact_hash)
  AND tenant_id = sqlc.arg(tenant_id)::uuid;
