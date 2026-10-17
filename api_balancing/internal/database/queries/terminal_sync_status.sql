-- name: BackfillTerminalSyncStatusBatch :one
-- Settles terminal artifacts that a writer left sync_status='in_progress' before
-- the terminal trigger existed; the trigger settles every later transition.
WITH batch AS (
    SELECT artifact_hash
    FROM foghorn.artifacts
    WHERE status IN ('deleted', 'expired', 'aborted')
      AND sync_status = 'in_progress'
    ORDER BY artifact_hash
    LIMIT sqlc.arg(batch_size)::integer
    FOR UPDATE SKIP LOCKED
), updated AS (
    UPDATE foghorn.artifacts AS artifact
    SET sync_status = 'failed',
        sync_error = COALESCE(NULLIF(artifact.sync_error, ''), 'artifact reached ' || artifact.status || ' before sync completed'),
        storage_location = CASE WHEN artifact.storage_location = 'freezing' THEN 'local' ELSE artifact.storage_location END,
        updated_at = NOW()
    FROM batch
    WHERE artifact.artifact_hash = batch.artifact_hash
    RETURNING artifact.artifact_hash
)
SELECT (SELECT COUNT(*) FROM batch)::bigint AS scanned_count,
       (SELECT COUNT(*) FROM updated)::bigint AS changed_count;

-- name: CountTerminalArtifactsWithSyncInProgress :one
SELECT COUNT(*)::bigint
FROM foghorn.artifacts
WHERE status IN ('deleted', 'expired', 'aborted')
  AND sync_status = 'in_progress';
