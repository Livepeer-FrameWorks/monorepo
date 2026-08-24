-- name: ResolveArtifactTenants :many
SELECT artifact_hash, tenant_id::text AS tenant_id
FROM foghorn.artifacts
WHERE artifact_hash = ANY($1::text[]) AND tenant_id IS NOT NULL;

-- name: ResolveActiveDVRNodes :many
SELECT a.stream_internal_name, an.node_id
FROM foghorn.artifacts a
JOIN foghorn.artifact_nodes an ON an.artifact_hash = a.artifact_hash
WHERE a.stream_internal_name = ANY($1::text[])
  AND a.artifact_type = 'dvr'
  AND a.status IN ('requested', 'starting', 'recording')
  AND COALESCE(an.is_orphaned, false) = false;

-- name: DVRRecordingTenant :one
SELECT COALESCE(tenant_id::text, '')::text AS tenant_id
FROM foghorn.artifacts
WHERE internal_name = $1 AND artifact_type = 'dvr' AND status = 'recording'
LIMIT 1;

-- name: GetFederatedArtifactDescriptor :one
SELECT COALESCE(a.internal_name, '')::text AS internal_name,
       COALESCE(a.stream_internal_name, '')::text AS stream_internal_name,
       a.artifact_type, COALESCE(a.format, '')::text AS format,
       COALESCE(a.storage_location, '')::text AS storage_location,
       COALESCE(a.sync_status, '')::text AS sync_status, a.size_bytes,
       COALESCE(a.storage_cluster_id, a.origin_cluster_id) AS authoritative_cluster,
       COALESCE(NULLIF(a.active_object_key, ''), NULLIF(v.s3_key, ''),
                NULLIF(a.sync_object_key, ''), '')::text AS object_key
FROM foghorn.artifacts a
LEFT JOIN foghorn.vod_metadata v ON v.artifact_hash = a.artifact_hash
WHERE a.artifact_hash = $1 AND a.tenant_id = $2 AND a.status != 'deleted';

-- name: LatestLiveOriginNode :one
SELECT an.node_id, COALESCE(NULLIF(an.base_url, ''), no.base_url, '')::text AS base_url
FROM foghorn.artifact_nodes an
LEFT JOIN foghorn.node_outputs no ON no.node_id = an.node_id
WHERE an.artifact_hash = $1
  AND an.role = 'origin' AND an.is_complete = true AND an.is_orphaned = false
  AND an.last_seen_at > NOW() - INTERVAL '90 seconds'
ORDER BY an.last_seen_at DESC
LIMIT 1;

-- name: LocalArtifactTenant :one
SELECT tenant_id FROM foghorn.artifacts WHERE artifact_hash = $1 AND tenant_id IS NOT NULL;

-- name: InsertMintArtifactShell :exec
INSERT INTO foghorn.artifacts
    (artifact_hash, artifact_type, tenant_id, stream_internal_name, internal_name,
     origin_cluster_id, storage_location, sync_status, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, 'pending', 'pending', NOW(), NOW())
ON CONFLICT (artifact_hash) DO NOTHING;

-- name: ListFederatedTenantArtifacts :many
SELECT artifact_hash, artifact_type, COALESCE(internal_name, '')::text AS internal_name,
       COALESCE(format, '')::text AS format,
       COALESCE(storage_location, '')::text AS storage_location,
       COALESCE(sync_status, '')::text AS sync_status,
       COALESCE(s3_url, '')::text AS s3_url, COALESCE(size_bytes, 0)::bigint AS size_bytes,
       COALESCE(EXTRACT(EPOCH FROM created_at)::bigint, 0)::bigint AS created_at,
       COALESCE(EXTRACT(EPOCH FROM frozen_at)::bigint, 0)::bigint AS frozen_at,
       COALESCE(stream_internal_name, '')::text AS stream_internal_name
FROM foghorn.artifacts
WHERE tenant_id = $1 AND status != 'deleted'
ORDER BY created_at DESC;

-- name: InsertMigratedArtifactMetadata :execrows
INSERT INTO foghorn.artifacts
    (artifact_hash, artifact_type, tenant_id, internal_name, stream_internal_name,
     format, status, storage_location, sync_status, s3_url, size_bytes, origin_cluster_id)
VALUES ($1, $2, $3, $4, $11, $5, 'active', $6, $7, $8, $9, $10)
ON CONFLICT (artifact_hash) DO NOTHING;

-- name: FillMigratedArtifactMetadata :exec
UPDATE foghorn.artifacts
SET internal_name = CASE WHEN COALESCE(internal_name, '') = '' AND sqlc.arg(internal_name)::text <> '' THEN sqlc.arg(internal_name)::text ELSE internal_name END,
    stream_internal_name = CASE WHEN COALESCE(stream_internal_name, '') = '' AND sqlc.arg(stream_internal_name)::text <> '' THEN sqlc.arg(stream_internal_name)::text ELSE stream_internal_name END,
    format = CASE WHEN COALESCE(format, '') = '' AND sqlc.arg(format)::text <> '' THEN sqlc.arg(format)::text ELSE format END,
    storage_location = CASE WHEN COALESCE(storage_location, '') = '' AND sqlc.arg(storage_location)::text <> '' THEN sqlc.arg(storage_location)::text ELSE storage_location END,
    sync_status = CASE WHEN COALESCE(sync_status, '') = '' AND sqlc.arg(sync_status)::text <> '' THEN sqlc.arg(sync_status)::text ELSE sync_status END,
    s3_url = CASE WHEN COALESCE(s3_url, '') = '' AND sqlc.arg(s3_url)::text <> '' THEN sqlc.arg(s3_url)::text ELSE s3_url END,
    size_bytes = CASE WHEN COALESCE(size_bytes, 0) = 0 AND sqlc.arg(size_bytes)::bigint > 0 THEN sqlc.arg(size_bytes)::bigint ELSE size_bytes END,
    origin_cluster_id = CASE WHEN COALESCE(origin_cluster_id, '') = '' THEN sqlc.arg(origin_cluster_id)::text ELSE origin_cluster_id END
WHERE artifact_hash = sqlc.arg(artifact_hash) AND artifact_type = sqlc.arg(artifact_type) AND tenant_id = sqlc.arg(tenant_id);

-- name: FederatedArtifactStreamID :one
SELECT COALESCE(stream_id::text, '')::text AS stream_id
FROM foghorn.artifacts
WHERE artifact_hash = $1 AND artifact_type = $2 AND tenant_id = $3
  AND status != 'deleted'
LIMIT 1;
