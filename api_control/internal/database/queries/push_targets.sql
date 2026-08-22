-- name: InsertPushTarget :exec
INSERT INTO commodore.push_targets (
    id, tenant_id, stream_id, platform, name, target_uri,
    is_enabled, status, created_at, updated_at
)
VALUES ($1, $2, $3, $4, $5, $6, true, 'idle', $7, $7);

-- name: ListPushTargets :many
SELECT id, stream_id, platform, name, target_uri, is_enabled, status,
       last_error, last_pushed_at, created_at, updated_at
FROM commodore.push_targets
WHERE stream_id = $1 AND tenant_id = $2
ORDER BY created_at ASC;

-- name: UpdatePushTargetFields :one
UPDATE commodore.push_targets
SET name = CASE WHEN sqlc.arg(apply_name)::boolean THEN sqlc.arg(name) ELSE name END,
    target_uri = CASE WHEN sqlc.arg(apply_target_uri)::boolean THEN sqlc.arg(target_uri) ELSE target_uri END,
    is_enabled = CASE WHEN sqlc.arg(apply_enabled)::boolean THEN sqlc.arg(is_enabled)::boolean ELSE is_enabled END,
    updated_at = NOW()
WHERE id = sqlc.arg(id) AND tenant_id = sqlc.arg(tenant_id)
RETURNING id, stream_id, platform, name, target_uri, is_enabled, status,
          last_error, last_pushed_at, created_at, updated_at;

-- name: DeletePushTarget :one
DELETE FROM commodore.push_targets
WHERE id = $1 AND tenant_id = $2
RETURNING stream_id;

-- name: ListEnabledPushTargets :many
SELECT id, platform, name, target_uri
FROM commodore.push_targets
WHERE stream_id = $1 AND tenant_id = $2 AND is_enabled = true;

-- name: UpdatePushTargetStatus :one
UPDATE commodore.push_targets
SET status = sqlc.arg(status),
    last_error = CASE
        WHEN sqlc.arg(apply_last_error)::boolean THEN sqlc.narg(last_error)
        ELSE last_error
    END,
    last_pushed_at = CASE
        WHEN sqlc.arg(mark_pushed)::boolean THEN NOW()
        ELSE last_pushed_at
    END,
    updated_at = NOW()
WHERE id = sqlc.arg(id) AND tenant_id = sqlc.arg(tenant_id)
RETURNING id, stream_id, platform, name, target_uri, is_enabled, status,
          last_error, last_pushed_at, created_at, updated_at;
