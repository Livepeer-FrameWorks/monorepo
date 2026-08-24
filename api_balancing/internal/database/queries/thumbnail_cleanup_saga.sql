-- name: GetThumbnailAssetBackendSnapshot :one
SELECT COUNT(DISTINCT backend_id)::integer AS distinct_backends, COALESCE(MIN(backend_id), '')::text AS backend_id
FROM foghorn.thumbnail_task_assignment
WHERE tenant_id = $1 AND asset_key = $2 AND backend_id IS NOT NULL AND backend_id <> '';

-- name: InsertStreamCleanupObligation :execrows
INSERT INTO foghorn.stream_cleanup_obligation (asset_key, tenant_id, backend_id)
VALUES ($1, $2, $3)
ON CONFLICT (asset_key) DO NOTHING;

-- name: AssetTombstoned :one
SELECT EXISTS (SELECT 1 FROM foghorn.stream_cleanup_obligation WHERE asset_key = $1);

-- name: GetAssetTombstone :one
SELECT 1::integer FROM foghorn.stream_cleanup_obligation WHERE asset_key = $1;

-- name: LockThumbnailAsset :exec
SELECT pg_advisory_xact_lock(sqlc.arg(lock_namespace)::integer, hashtext(sqlc.arg(asset_key)::text));
