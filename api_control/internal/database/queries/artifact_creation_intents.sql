-- name: UpsertArtifactCreationIntent :one
INSERT INTO commodore.artifact_creation_intents
    (tenant_id, kind, artifact_hash, request_id, origin_cluster_id, status, payload, created_at, updated_at)
VALUES (sqlc.arg(tenant_id)::uuid, sqlc.arg(kind), sqlc.arg(artifact_hash),
        sqlc.arg(request_id)::uuid, sqlc.arg(origin_cluster_id)::text, 'pending',
        sqlc.narg(payload)::text::jsonb, NOW(), NOW())
ON CONFLICT (tenant_id, kind, artifact_hash) DO UPDATE
SET request_id = commodore.artifact_creation_intents.request_id
RETURNING request_id::text;

-- name: TerminalizeClaimedArtifactCreationIntent :execrows
UPDATE commodore.artifact_creation_intents
SET status = sqlc.arg(new_status),
    last_error = NULLIF(sqlc.arg(reason)::text, ''),
    lease_token = NULL,
    leased_until = NULL,
    command_ack_pending = sqlc.arg(ack_pending),
    command_ack_attempts = CASE WHEN sqlc.arg(ack_pending) THEN 0 ELSE command_ack_attempts END,
    command_ack_next_at = CASE WHEN sqlc.arg(ack_pending) THEN NOW() ELSE NULL END,
    updated_at = NOW()
WHERE tenant_id = sqlc.arg(tenant_id)::uuid
  AND kind = sqlc.arg(kind)
  AND artifact_hash = sqlc.arg(artifact_hash)
  AND status = 'pending'
  AND lease_token = sqlc.arg(lease_token)::uuid;

-- name: TerminalizeArtifactCreationIntent :execrows
UPDATE commodore.artifact_creation_intents
SET status = sqlc.arg(new_status),
    last_error = NULLIF(sqlc.arg(reason)::text, ''),
    lease_token = NULL,
    leased_until = NULL,
    command_ack_pending = sqlc.arg(ack_pending),
    command_ack_attempts = CASE WHEN sqlc.arg(ack_pending) THEN 0 ELSE command_ack_attempts END,
    command_ack_next_at = CASE WHEN sqlc.arg(ack_pending) THEN NOW() ELSE NULL END,
    updated_at = NOW()
WHERE tenant_id = sqlc.arg(tenant_id)::uuid
  AND kind = sqlc.arg(kind)
  AND artifact_hash = sqlc.arg(artifact_hash)
  AND status = 'pending';

-- name: DeleteCatalogOnlyVOD :exec
DELETE FROM commodore.vod_assets
WHERE tenant_id = sqlc.arg(tenant_id)::uuid AND vod_hash = sqlc.arg(artifact_hash);

-- name: DeleteCatalogOnlyDVR :exec
DELETE FROM commodore.dvr_recordings
WHERE tenant_id = sqlc.arg(tenant_id)::uuid AND dvr_hash = sqlc.arg(artifact_hash);

-- name: DeleteCatalogOnlyClip :exec
DELETE FROM commodore.clips
WHERE tenant_id = sqlc.arg(tenant_id)::uuid AND clip_hash = sqlc.arg(artifact_hash);

-- name: NoteArtifactCreationIntentAttempt :exec
UPDATE commodore.artifact_creation_intents
SET attempts = attempts + 1, last_error = sqlc.arg(reason)::text, updated_at = NOW()
WHERE tenant_id = sqlc.arg(tenant_id)::uuid
  AND kind = sqlc.arg(kind)
  AND artifact_hash = sqlc.arg(artifact_hash)
  AND status = 'pending'
  AND lease_token = sqlc.arg(lease_token)::uuid;

-- name: ClaimArtifactCreationCommandAcks :many
WITH due AS (
    SELECT tenant_id, kind, artifact_hash
    FROM commodore.artifact_creation_intents
    WHERE command_ack_pending
      AND (command_ack_next_at IS NULL OR command_ack_next_at <= NOW())
      AND (command_ack_leased_until IS NULL OR command_ack_leased_until <= NOW())
    ORDER BY command_ack_next_at NULLS FIRST
    LIMIT sqlc.arg(batch_size)
    FOR UPDATE SKIP LOCKED
)
UPDATE commodore.artifact_creation_intents i
SET command_ack_leased_until = NOW() + (sqlc.arg(lease_interval)::text)::interval,
    command_ack_lease_token = sqlc.arg(lease_token)::uuid,
    updated_at = NOW()
FROM due
WHERE i.tenant_id = due.tenant_id
  AND i.kind = due.kind
  AND i.artifact_hash = due.artifact_hash
RETURNING i.tenant_id::text AS tenant_id, i.kind, i.artifact_hash,
          i.request_id::text AS request_id,
          COALESCE(i.origin_cluster_id, ''::text)::text AS origin_cluster_id,
          i.command_ack_attempts;

-- name: ClearArtifactCreationCommandAck :exec
UPDATE commodore.artifact_creation_intents
SET command_ack_pending = FALSE,
    command_acked_at = NOW(),
    command_ack_leased_until = NULL,
    command_ack_lease_token = NULL
WHERE tenant_id = sqlc.arg(tenant_id)::uuid
  AND kind = sqlc.arg(kind)
  AND artifact_hash = sqlc.arg(artifact_hash)
  AND command_ack_pending = TRUE
  AND command_ack_lease_token = sqlc.arg(lease_token)::uuid;

-- name: BackoffArtifactCreationCommandAck :exec
UPDATE commodore.artifact_creation_intents
SET command_ack_attempts = command_ack_attempts + 1,
    command_ack_next_at = NOW() + LEAST(
        INTERVAL '30 seconds' * power(2, LEAST(command_ack_attempts, 20)),
        INTERVAL '15 minutes'
    ),
    command_ack_leased_until = NULL,
    command_ack_lease_token = NULL,
    updated_at = NOW()
WHERE tenant_id = sqlc.arg(tenant_id)::uuid
  AND kind = sqlc.arg(kind)
  AND artifact_hash = sqlc.arg(artifact_hash)
  AND command_ack_pending = TRUE
  AND command_ack_lease_token = sqlc.arg(lease_token)::uuid;

-- name: ClaimArtifactCreationIntents :many
UPDATE commodore.artifact_creation_intents AS i
SET lease_token = sqlc.arg(lease_token)::uuid,
    leased_until = NOW() + (sqlc.arg(lease_interval)::text)::interval
WHERE (i.tenant_id, i.kind, i.artifact_hash) IN (
    SELECT tenant_id, kind, artifact_hash
    FROM commodore.artifact_creation_intents
    WHERE status = 'pending'
      AND updated_at < NOW() - (sqlc.arg(grace_interval)::text)::interval
      AND (leased_until IS NULL OR leased_until < NOW())
    ORDER BY updated_at
    LIMIT sqlc.arg(batch_size)
    FOR UPDATE SKIP LOCKED
)
RETURNING i.tenant_id::text AS tenant_id, i.kind, i.artifact_hash,
          i.request_id::text AS request_id,
          COALESCE(i.origin_cluster_id, ''::text)::text AS origin_cluster_id,
          COALESCE(i.payload::text, ''::text)::text AS payload,
          i.created_at < NOW() - (sqlc.arg(missing_interval)::text)::interval AS past_missing_deadline;

-- name: LockArtifactCreationIdentity :exec
SELECT pg_advisory_xact_lock(hashtext(sqlc.arg(lock_key))::bigint);

-- name: GetArtifactDeletionMarkerForUpdate :one
SELECT deletion_revision
FROM commodore.artifact_catalog_tombstones
WHERE tenant_id = sqlc.arg(tenant_id)::uuid
  AND kind = sqlc.arg(kind)
  AND artifact_hash = sqlc.arg(artifact_hash)
FOR UPDATE;

-- name: InsertConvergedClip :exec
INSERT INTO commodore.clips (
    id, tenant_id, user_id, stream_id, clip_hash, internal_name, playback_id,
    title, description, start_time, duration, clip_mode, requested_params,
    origin_cluster_id, retention_until, requires_auth, playback_policy,
    playback_webhook_secret_enc, created_at, updated_at
) VALUES (
    sqlc.arg(clip_id)::uuid, sqlc.arg(tenant_id)::uuid, sqlc.arg(user_id)::uuid,
    NULLIF(sqlc.arg(stream_id)::text, '')::uuid, sqlc.arg(clip_hash),
    sqlc.arg(internal_name), sqlc.arg(playback_id), sqlc.arg(title),
    sqlc.arg(description), sqlc.arg(start_time), sqlc.arg(duration),
    sqlc.arg(clip_mode), sqlc.arg(requested_params)::text::jsonb,
    sqlc.arg(origin_cluster_id), sqlc.narg(retention_until),
    sqlc.arg(requires_auth), sqlc.narg(playback_policy)::text::jsonb,
    sqlc.narg(webhook_secret), NOW(), NOW()
)
ON CONFLICT (clip_hash) DO NOTHING;
