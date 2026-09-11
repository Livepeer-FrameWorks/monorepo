-- name: GetMediaPlacementPolicy :one
SELECT * FROM commodore.media_placement_policies
WHERE tenant_id = sqlc.arg(tenant_id)::uuid
  AND scope_kind = sqlc.arg(scope_kind)
  AND scope_id = sqlc.arg(scope_id)::uuid;

-- name: EnsureMediaPlacementPolicy :exec
INSERT INTO commodore.media_placement_policies (tenant_id, scope_kind, scope_id)
VALUES (sqlc.arg(tenant_id)::uuid, sqlc.arg(scope_kind), sqlc.arg(scope_id)::uuid)
ON CONFLICT (tenant_id, scope_kind, scope_id) DO NOTHING;

-- name: RetainDeletedStreamMediaPlacementPolicy :execrows
INSERT INTO commodore.media_placement_policies (tenant_id, scope_kind, scope_id)
SELECT tenant_id, 'stream', id FROM commodore.streams
WHERE tenant_id = sqlc.arg(tenant_id)::uuid
  AND id = sqlc.arg(stream_id)::uuid AND deleted_at IS NOT NULL
ON CONFLICT (tenant_id, scope_kind, scope_id) DO NOTHING;

-- name: LockMediaPlacementPolicy :one
SELECT * FROM commodore.media_placement_policies
WHERE tenant_id = sqlc.arg(tenant_id)::uuid
  AND scope_kind = sqlc.arg(scope_kind)
  AND scope_id = sqlc.arg(scope_id)::uuid
FOR UPDATE;

-- name: MediaPlacementStreamExists :one
SELECT EXISTS(SELECT 1 FROM commodore.streams
WHERE tenant_id = sqlc.arg(tenant_id)::uuid AND id = sqlc.arg(stream_id)::uuid
  AND deleted_at IS NULL) AS found;

-- name: LockMediaPlacementStream :one
SELECT id FROM commodore.streams
WHERE tenant_id = sqlc.arg(tenant_id)::uuid AND id = sqlc.arg(stream_id)::uuid
  AND deleted_at IS NULL
FOR SHARE;

-- name: GetMediaPlacementChange :one
SELECT * FROM commodore.media_placement_changes
WHERE tenant_id = sqlc.arg(tenant_id)::uuid
  AND scope_kind = sqlc.arg(scope_kind)
  AND scope_id = sqlc.arg(scope_id)::uuid
  AND idempotency_key = sqlc.arg(idempotency_key);

-- name: GetMediaPlacementRevision :one
SELECT * FROM commodore.media_placement_changes
WHERE tenant_id = sqlc.arg(tenant_id)::uuid
  AND scope_kind = sqlc.arg(scope_kind)
  AND scope_id = sqlc.arg(scope_id)::uuid
  AND revision = sqlc.arg(revision);

-- name: UpdateMediaPlacementPolicy :execrows
UPDATE commodore.media_placement_policies
SET revision = sqlc.arg(revision), parent_revision = sqlc.arg(parent_revision),
    policy_payload = sqlc.arg(policy_payload), updated_at = NOW()
WHERE tenant_id = sqlc.arg(tenant_id)::uuid
  AND scope_kind = sqlc.arg(scope_kind)
  AND scope_id = sqlc.arg(scope_id)::uuid
  AND revision = sqlc.arg(expected_revision);

-- name: InsertMediaPlacementChange :one
INSERT INTO commodore.media_placement_changes
    (tenant_id, scope_kind, scope_id, idempotency_key, request_sha256, revision,
     parent_revision, policy_digest, review_digest, previous_policy_payload, policy_payload, actor_id)
VALUES (sqlc.arg(tenant_id)::uuid, sqlc.arg(scope_kind), sqlc.arg(scope_id)::uuid,
    sqlc.arg(idempotency_key), sqlc.arg(request_sha256), sqlc.arg(revision),
    sqlc.arg(parent_revision), sqlc.arg(policy_digest), sqlc.arg(review_digest), sqlc.arg(previous_policy_payload),
    sqlc.arg(policy_payload), sqlc.arg(actor_id))
RETURNING *;

-- name: SupersedeMediaPlacementChanges :exec
UPDATE commodore.media_placement_changes
SET rollout_status = 'superseded', updated_at = NOW()
WHERE tenant_id = sqlc.arg(tenant_id)::uuid
  AND scope_kind = sqlc.arg(scope_kind)
  AND scope_id = sqlc.arg(scope_id)::uuid
  AND revision < sqlc.arg(revision)
  AND rollout_status IN ('pending', 'blocked');

-- name: ActivateMediaPlacementPolicy :execrows
UPDATE commodore.media_placement_policies
SET active_revision = sqlc.arg(active_revision), active_parent_revision = sqlc.arg(active_parent_revision),
    active_policy_payload = sqlc.arg(active_policy_payload), updated_at = NOW()
WHERE tenant_id = sqlc.arg(tenant_id)::uuid
  AND scope_kind = sqlc.arg(scope_kind)
  AND scope_id = sqlc.arg(scope_id)::uuid
  AND revision >= sqlc.arg(active_revision)
  AND (active_revision < sqlc.arg(active_revision)
       OR active_parent_revision <> sqlc.arg(active_parent_revision)
       OR active_policy_payload <> sqlc.arg(active_policy_payload));

-- name: MarkMediaPlacementChangesEffective :execrows
UPDATE commodore.media_placement_changes
SET rollout_status = 'effective', rollout_reason = '', updated_at = NOW()
WHERE tenant_id = sqlc.arg(tenant_id)::uuid
  AND scope_kind = sqlc.arg(scope_kind)
  AND scope_id = sqlc.arg(scope_id)::uuid
  AND revision <= sqlc.arg(revision)
  AND rollout_status IN ('pending', 'blocked');

-- name: SetMediaPlacementChangeRollout :execrows
UPDATE commodore.media_placement_changes
SET rollout_status = sqlc.arg(rollout_status), rollout_reason = sqlc.arg(rollout_reason), updated_at = NOW()
WHERE tenant_id = sqlc.arg(tenant_id)::uuid
  AND scope_kind = sqlc.arg(scope_kind)
  AND scope_id = sqlc.arg(scope_id)::uuid
  AND revision = sqlc.arg(revision)
  AND rollout_status IN ('pending', 'blocked')
  AND (rollout_status <> sqlc.arg(rollout_status) OR rollout_reason <> sqlc.arg(rollout_reason));
