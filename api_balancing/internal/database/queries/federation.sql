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
     origin_cluster_id, storage_location, sync_status, federated_pointer, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, 'pending', 'pending', true, NOW(), NOW())
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
WHERE tenant_id = $1 AND status != 'deleted' AND federated_pointer = false
ORDER BY created_at DESC;

-- name: InsertMigratedArtifactMetadata :execrows
INSERT INTO foghorn.artifacts
    (artifact_hash, artifact_type, tenant_id, internal_name, stream_internal_name,
     format, status, storage_location, sync_status, s3_url, size_bytes, origin_cluster_id,
     federated_pointer)
SELECT sqlc.arg(artifact_hash)::varchar(32), sqlc.arg(artifact_type)::varchar(10),
       sqlc.arg(tenant_id)::uuid, sqlc.arg(internal_name), sqlc.arg(stream_internal_name),
       sqlc.arg(format), 'ready', sqlc.arg(storage_location), sqlc.arg(sync_status),
       sqlc.arg(s3_url), sqlc.arg(size_bytes), sqlc.arg(origin_cluster_id), true
WHERE NOT EXISTS (
    SELECT 1
    FROM foghorn.media_object_authority_projection AS authority
    WHERE authority.tenant_id = sqlc.arg(tenant_id)::uuid
      AND authority.artifact_hash = sqlc.arg(artifact_hash)::text
      AND authority.lifecycle = 'tombstone'
)
ON CONFLICT (artifact_hash) DO UPDATE SET
    federated_pointer = true,
    status = 'ready',
    updated_at = NOW()
WHERE foghorn.artifacts.tenant_id = EXCLUDED.tenant_id
  AND foghorn.artifacts.federated_pointer = true
  -- A terminal pointer may be owned by the out-of-transaction derivative
  -- purge saga. Metadata migration must not resurrect it mid-sweep.
  AND foghorn.artifacts.status <> 'deleted'
  AND foghorn.artifacts.federated_purge_token IS NULL
  AND NOT EXISTS (
      SELECT 1
      FROM foghorn.media_object_authority_projection AS authority
      WHERE authority.tenant_id = EXCLUDED.tenant_id
        AND authority.artifact_hash = EXCLUDED.artifact_hash
        AND authority.lifecycle = 'tombstone'
  );

-- name: FillMigratedArtifactMetadata :exec
UPDATE foghorn.artifacts AS artifact
SET internal_name = CASE WHEN COALESCE(artifact.internal_name, '') = '' AND sqlc.arg(internal_name)::text <> '' THEN sqlc.arg(internal_name)::text ELSE artifact.internal_name END,
    stream_internal_name = CASE WHEN COALESCE(artifact.stream_internal_name, '') = '' AND sqlc.arg(stream_internal_name)::text <> '' THEN sqlc.arg(stream_internal_name)::text ELSE artifact.stream_internal_name END,
    format = CASE WHEN COALESCE(artifact.format, '') = '' AND sqlc.arg(format)::text <> '' THEN sqlc.arg(format)::text ELSE artifact.format END,
    storage_location = CASE WHEN COALESCE(artifact.storage_location, '') = '' AND sqlc.arg(storage_location)::text <> '' THEN sqlc.arg(storage_location)::text ELSE artifact.storage_location END,
    sync_status = CASE WHEN COALESCE(artifact.sync_status, '') = '' AND sqlc.arg(sync_status)::text <> '' THEN sqlc.arg(sync_status)::text ELSE artifact.sync_status END,
    s3_url = CASE WHEN COALESCE(artifact.s3_url, '') = '' AND sqlc.arg(s3_url)::text <> '' THEN sqlc.arg(s3_url)::text ELSE artifact.s3_url END,
    size_bytes = CASE WHEN COALESCE(artifact.size_bytes, 0) = 0 AND sqlc.arg(size_bytes)::bigint > 0 THEN sqlc.arg(size_bytes)::bigint ELSE artifact.size_bytes END,
    origin_cluster_id = CASE WHEN COALESCE(artifact.origin_cluster_id, '') = '' THEN sqlc.arg(origin_cluster_id)::text ELSE artifact.origin_cluster_id END,
    status = 'ready',
    updated_at = NOW()
WHERE artifact.artifact_hash = sqlc.arg(artifact_hash)::varchar(32)
  AND (artifact.artifact_type = sqlc.arg(artifact_type)
       OR (artifact.artifact_type IN ('vod', 'chapter') AND sqlc.arg(artifact_type)::text IN ('vod', 'chapter')))
  AND artifact.tenant_id = sqlc.arg(tenant_id)
  AND artifact.federated_pointer = true
  AND artifact.status <> 'deleted'
  AND artifact.federated_purge_token IS NULL
  AND NOT EXISTS (
      SELECT 1
      FROM foghorn.media_object_authority_projection AS authority
      WHERE authority.tenant_id = sqlc.arg(tenant_id)
      AND authority.artifact_hash = sqlc.arg(artifact_hash)::text
        AND authority.lifecycle = 'tombstone'
  );

-- name: FederatedArtifactStreamID :one
SELECT COALESCE(stream_id::text, '')::text AS stream_id
FROM foghorn.artifacts
WHERE artifact_hash = $1
  AND (artifact_type = $2 OR (artifact_type IN ('vod', 'chapter') AND $2::text IN ('vod', 'chapter')))
  AND tenant_id = $3
  AND status != 'deleted'
LIMIT 1;
