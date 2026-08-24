-- name: GetArtifactCreationCommandStatus :one
SELECT status, catalog_revision, kind, artifact_hash
FROM foghorn.artifact_creation_commands
WHERE request_id = sqlc.arg(request_id)::uuid
  AND tenant_id = sqlc.arg(tenant_id)::uuid;

-- name: GetArtifactCreationCommandAckState :one
SELECT status, kind, artifact_hash
FROM foghorn.artifact_creation_commands
WHERE request_id = sqlc.arg(request_id)::uuid
  AND tenant_id = sqlc.arg(tenant_id)::uuid;

-- name: ConsumeTerminalArtifactCreationCommand :exec
UPDATE foghorn.artifact_creation_commands
SET consumed_at = NOW()
WHERE request_id = sqlc.arg(request_id)::uuid
  AND tenant_id = sqlc.arg(tenant_id)::uuid
  AND kind = sqlc.arg(kind)
  AND artifact_hash = sqlc.arg(artifact_hash)
  AND status IN ('committed', 'rejected');

-- name: InsertAcceptedArtifactCreationCommand :exec
INSERT INTO foghorn.artifact_creation_commands
    (request_id, tenant_id, kind, artifact_hash, status, updated_at)
VALUES (sqlc.arg(request_id)::uuid, sqlc.arg(tenant_id)::uuid, sqlc.arg(kind), sqlc.arg(artifact_hash), 'accepted', NOW())
ON CONFLICT (request_id) DO NOTHING;

-- name: ReadArtifactCreationCommandIdentity :one
SELECT COALESCE(tenant_id = sqlc.arg(tenant_id)::uuid
        AND kind = sqlc.arg(kind)
        AND artifact_hash = sqlc.arg(artifact_hash), FALSE)::boolean AS identity_ok,
       status
FROM foghorn.artifact_creation_commands
WHERE request_id = sqlc.arg(request_id)::uuid;

-- name: CommitArtifactCreationCommand :execrows
UPDATE foghorn.artifact_creation_commands
SET status = 'committed',
    catalog_revision = COALESCE((
        SELECT a.catalog_revision
        FROM foghorn.artifacts a
        WHERE a.artifact_hash = sqlc.arg(artifact_hash)
          AND a.tenant_id = sqlc.arg(tenant_id)::uuid
          AND a.artifact_type = sqlc.arg(kind)
    ), 0),
    updated_at = NOW()
WHERE request_id = sqlc.arg(request_id)::uuid
  AND tenant_id = sqlc.arg(tenant_id)::uuid
  AND kind = sqlc.arg(kind)
  AND artifact_hash = sqlc.arg(artifact_hash)
  AND status = 'accepted';

-- name: RejectArtifactCreationCommand :execrows
UPDATE foghorn.artifact_creation_commands
SET status = 'rejected', updated_at = NOW()
WHERE request_id = sqlc.arg(request_id)::uuid
  AND tenant_id = sqlc.arg(tenant_id)::uuid
  AND kind = sqlc.arg(kind)
  AND artifact_hash = sqlc.arg(artifact_hash)
  AND status = 'accepted';

-- name: GetClipFulfilledSourceParams :one
SELECT j.source_params
FROM foghorn.processing_jobs j
WHERE j.artifact_hash = sqlc.arg(artifact_hash)::text
  AND j.source_params IS NOT NULL
  AND EXISTS (
      SELECT 1
      FROM foghorn.artifacts a
      WHERE a.artifact_hash = j.artifact_hash
        AND a.tenant_id = sqlc.arg(tenant_id)::uuid
        AND a.artifact_type = 'clip'
  )
ORDER BY j.created_at DESC
LIMIT 1;
