-- name: GetVodOriginal :one
SELECT a.status, a.active_object_key, a.durable_backend_local,
       COALESCE(a.backend_id, '')::text AS backend_id,
       v.original_s3_key, v.original_size_bytes, COALESCE(v.filename, 'original')::text AS filename
FROM foghorn.artifacts a
JOIN foghorn.vod_metadata v ON v.artifact_hash = a.artifact_hash
WHERE a.tenant_id = sqlc.arg(tenant_id)::uuid
  AND a.artifact_hash = sqlc.arg(artifact_hash)
  AND a.artifact_type = 'vod' AND a.federated_pointer = false
  AND a.status NOT IN ('deleted', 'failed', 'expired', 'aborted');

-- name: StampVodOriginal :execrows
UPDATE foghorn.vod_metadata v
SET original_s3_key = v.s3_key, original_size_bytes = sqlc.arg(size_bytes)::bigint,
    updated_at = NOW()
FROM foghorn.artifacts a
WHERE a.artifact_hash = v.artifact_hash
  AND a.tenant_id = sqlc.arg(tenant_id)::uuid AND a.artifact_hash = sqlc.arg(artifact_hash)
  AND a.status = 'processing' AND a.federated_pointer = false
  AND v.keep_original AND v.source_url IS NULL AND v.original_s3_key IS NULL
  AND v.s3_key = sqlc.arg(object_key)::text AND a.active_object_key = v.s3_key
  AND sqlc.arg(size_bytes)::bigint > 0
  AND EXISTS (SELECT 1 FROM foghorn.vod_original_retention_activation
              WHERE singleton = true AND activated_at IS NOT NULL);

-- name: GetVodOriginalToStamp :one
SELECT v.s3_key, COALESCE(a.size_bytes, 0)::bigint AS size_bytes FROM foghorn.vod_metadata v
JOIN foghorn.artifacts a ON a.artifact_hash = v.artifact_hash
WHERE a.artifact_hash = sqlc.arg(artifact_hash) AND a.tenant_id = sqlc.arg(tenant_id)::uuid
  AND a.status = 'processing' AND a.federated_pointer = false
  AND v.keep_original AND v.source_url IS NULL AND v.original_s3_key IS NULL;

-- name: ClearVodOriginal :execrows
UPDATE foghorn.vod_metadata v
SET original_s3_key = NULL, original_size_bytes = NULL, keep_original = false, updated_at = NOW()
FROM foghorn.artifacts a
WHERE a.artifact_hash = v.artifact_hash AND a.tenant_id = sqlc.arg(tenant_id)::uuid
  AND a.artifact_hash = sqlc.arg(artifact_hash) AND a.federated_pointer = false
  AND v.original_s3_key = sqlc.arg(object_key)::text
  AND a.active_object_key IS DISTINCT FROM v.original_s3_key;

-- name: TouchVodOriginalCatalog :exec
UPDATE foghorn.artifacts SET updated_at = NOW(), catalog_revision = catalog_revision + 1
WHERE artifact_hash = sqlc.arg(artifact_hash) AND tenant_id = sqlc.arg(tenant_id)::uuid;

-- name: GetKeptVodOriginalKey :one
SELECT COALESCE(v.original_s3_key, '')::text AS original_s3_key
FROM foghorn.vod_metadata v JOIN foghorn.artifacts a ON a.artifact_hash = v.artifact_hash
WHERE a.artifact_hash = sqlc.arg(artifact_hash) AND a.tenant_id = sqlc.arg(tenant_id)::uuid;

-- name: LockVodOriginalArtifact :one
SELECT artifact_hash FROM foghorn.artifacts
WHERE tenant_id = sqlc.arg(tenant_id)::uuid AND artifact_hash = sqlc.arg(artifact_hash)
  AND status NOT IN ('deleted', 'failed', 'expired', 'aborted')
FOR UPDATE;

-- name: IsVodOriginalRetentionActivated :one
SELECT EXISTS (
    SELECT 1 FROM foghorn.vod_original_retention_activation
    WHERE singleton = true AND activated_at IS NOT NULL
)::boolean;
