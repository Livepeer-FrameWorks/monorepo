-- name: ArtifactHasActiveNodes :one
SELECT EXISTS (
    SELECT 1 FROM foghorn.artifact_nodes
    WHERE artifact_hash = $1 AND NOT is_orphaned
);

-- name: MarkArtifactS3OnlyWhenUnhosted :exec
UPDATE foghorn.artifacts
SET storage_location = CASE WHEN sync_status = 'synced' THEN 's3' ELSE storage_location END,
    updated_at = NOW()
WHERE artifact_hash = $1;

-- name: ArtifactLifecycleStatus :one
SELECT status FROM foghorn.artifacts WHERE artifact_hash = $1;
