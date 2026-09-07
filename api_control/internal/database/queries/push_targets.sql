-- name: InsertPushTarget :exec
INSERT INTO commodore.push_targets (
    id, tenant_id, stream_id, platform, name, target_uri,
    is_enabled, status, created_at, updated_at
)
VALUES ($1, $2, $3, $4, $5, $6, true, 'idle', $7, $7);

-- name: ListPushTargets :many
SELECT id, stream_id, platform, name, target_uri, is_enabled, status,
       reason_code, last_error, last_pushed_at, created_at, updated_at
FROM commodore.push_targets pt
WHERE pt.stream_id = sqlc.arg(stream_id) AND pt.tenant_id = sqlc.arg(tenant_id)
  AND EXISTS (
      SELECT 1 FROM commodore.streams s
      WHERE s.id = pt.stream_id
        AND s.tenant_id = sqlc.arg(tenant_id)
        AND (s.user_id = sqlc.arg(user_id) OR sqlc.arg(tenant_manager)::boolean)
        AND s.deleted_at IS NULL
  )
ORDER BY created_at ASC;

-- name: ListPushTargetSiblingsForOwner :many
SELECT sibling.id, sibling.stream_id, sibling.target_uri
FROM commodore.push_targets selected
JOIN commodore.streams stream
  ON stream.id = selected.stream_id
 AND stream.tenant_id = selected.tenant_id
 AND stream.deleted_at IS NULL
JOIN commodore.push_targets sibling
  ON sibling.stream_id = selected.stream_id
 AND sibling.tenant_id = selected.tenant_id
WHERE selected.id = sqlc.arg(id)
  AND selected.tenant_id = sqlc.arg(tenant_id)
  AND (stream.user_id = sqlc.arg(user_id) OR sqlc.arg(tenant_manager)::boolean)
ORDER BY sibling.id;

-- name: UpdatePushTargetFields :one
UPDATE commodore.push_targets pt
SET name = CASE WHEN sqlc.arg(apply_name)::boolean THEN sqlc.arg(name) ELSE name END,
    target_uri = CASE WHEN sqlc.arg(apply_target_uri)::boolean THEN sqlc.arg(target_uri) ELSE target_uri END,
    is_enabled = CASE WHEN sqlc.arg(apply_enabled)::boolean THEN sqlc.arg(is_enabled)::boolean ELSE is_enabled END,
    updated_at = NOW()
WHERE pt.id = sqlc.arg(id) AND pt.tenant_id = sqlc.arg(tenant_id)
  AND EXISTS (
      SELECT 1 FROM commodore.streams s
      WHERE s.id = pt.stream_id
        AND s.tenant_id = sqlc.arg(tenant_id)
        AND (s.user_id = sqlc.arg(user_id) OR sqlc.arg(tenant_manager)::boolean)
        AND s.deleted_at IS NULL
  )
RETURNING id, stream_id, platform, name, target_uri, is_enabled, status,
          reason_code, last_error, last_pushed_at, created_at, updated_at;

-- name: DeletePushTarget :one
DELETE FROM commodore.push_targets pt
WHERE pt.id = sqlc.arg(id) AND pt.tenant_id = sqlc.arg(tenant_id)
  AND EXISTS (
      SELECT 1 FROM commodore.streams s
      WHERE s.id = pt.stream_id
        AND s.tenant_id = sqlc.arg(tenant_id)
        AND (s.user_id = sqlc.arg(user_id) OR sqlc.arg(tenant_manager)::boolean)
        AND s.deleted_at IS NULL
  )
RETURNING stream_id;

-- name: ListEnabledPushTargets :many
SELECT id, platform, name, target_uri
FROM commodore.push_targets
WHERE stream_id = $1 AND tenant_id = $2 AND is_enabled = true;

-- name: UpdatePushTargetStatus :one
UPDATE commodore.push_targets
SET status = sqlc.arg(status),
    reason_code = sqlc.arg(reason_code),
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
          reason_code, last_error, last_pushed_at, created_at, updated_at;

-- name: GetPushTargetStreamOwner :one
SELECT s.user_id
FROM commodore.streams s
WHERE s.id = sqlc.arg(stream_id) AND s.tenant_id = sqlc.arg(tenant_id)
  AND s.deleted_at IS NULL;

-- name: StreamExistsForPushTargetManager :one
SELECT EXISTS (
    SELECT 1 FROM commodore.streams s
    WHERE s.id = sqlc.arg(stream_id)
      AND s.tenant_id = sqlc.arg(tenant_id)
      AND (s.user_id = sqlc.arg(user_id) OR sqlc.arg(tenant_manager)::boolean)
      AND s.deleted_at IS NULL
) AS stream_exists;
