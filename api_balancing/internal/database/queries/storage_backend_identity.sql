-- name: EstablishCellStorageIdentity :exec
INSERT INTO foghorn.cell_storage_identity (id, backend_id, bucket, endpoint, region, prefix)
VALUES (true, sqlc.arg(backend_id), sqlc.arg(bucket), sqlc.arg(endpoint), sqlc.arg(region), sqlc.arg(prefix))
ON CONFLICT (id) DO NOTHING;

-- name: GetCellStorageIdentity :one
SELECT bucket, endpoint, region, prefix
FROM foghorn.cell_storage_identity
WHERE id = true;

-- name: CellStorageIdentityCommitted :one
SELECT EXISTS (SELECT 1 FROM foghorn.cell_storage_identity WHERE id = true);
