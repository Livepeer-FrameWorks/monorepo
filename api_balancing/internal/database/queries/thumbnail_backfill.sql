-- name: ListArtifactsMissingThumbnails :many
-- Published local clips and VODs that hold no thumbnail and have nothing that could still produce one: no live
-- or published thumbnail attempt and no active processing job. Each asset gets at most max_attempts thumbnail
-- jobs inside the processing-job retention window, spaced by retry_after_seconds.
SELECT a.artifact_hash,
       a.tenant_id::text AS tenant_id,
       a.artifact_type,
       COALESCE(a.stream_id::text, '')::text AS stream_id,
       COALESCE(a.origin_cluster_id, '')::text AS origin_cluster_id,
       EXISTS (
           SELECT 1 FROM foghorn.dvr_chapters c
           WHERE c.playback_artifact_hash = a.artifact_hash
       )::boolean AS is_chapter
FROM foghorn.artifacts a
WHERE a.status = 'ready'
  AND a.artifact_type IN ('vod', 'clip')
  AND a.has_thumbnails IS NOT TRUE
  AND a.created_at < NOW() - sqlc.arg(min_age_seconds)::bigint * INTERVAL '1 second'
  AND a.federated_pointer = false
  AND a.durable_backend_local = true
  AND a.sync_status = 'synced'
  AND NOT EXISTS (
      SELECT 1 FROM foghorn.thumbnail_task_assignment t
      WHERE t.asset_key = a.artifact_hash
        AND t.tenant_id = a.tenant_id::text
        AND t.status <> 'failed'
  )
  AND NOT EXISTS (
      SELECT 1 FROM foghorn.processing_jobs j
      WHERE j.artifact_hash = a.artifact_hash
        AND (j.status IN ('queued', 'dispatched', 'processing')
             OR (j.job_type = 'thumbnail'
                 AND j.created_at > NOW() - sqlc.arg(retry_after_seconds)::bigint * INTERVAL '1 second'))
  )
  AND (
      SELECT COUNT(*) FROM foghorn.processing_jobs j
      WHERE j.artifact_hash = a.artifact_hash
        AND j.job_type = 'thumbnail'
  ) < sqlc.arg(max_attempts)::bigint
ORDER BY a.created_at
LIMIT sqlc.arg(batch_limit);

-- name: FailOrphanedThumbnailJobs :execrows
-- A queued thumbnail job is claimable only while its asset is published; one whose asset was deleted or
-- unpublished meanwhile is failed so it neither waits forever nor holds a backfill slot.
UPDATE foghorn.processing_jobs j
SET status = 'failed', error_message = 'asset no longer published', completed_at = NOW(), updated_at = NOW()
WHERE j.job_type = 'thumbnail'
  AND j.status = 'queued'
  AND NOT EXISTS (
      SELECT 1 FROM foghorn.artifacts a
      WHERE a.artifact_hash = j.artifact_hash
        AND a.status = 'ready'
  );

-- name: CountActiveThumbnailJobs :one
SELECT COUNT(*)::bigint
FROM foghorn.processing_jobs
WHERE job_type = 'thumbnail'
  AND status IN ('queued', 'dispatched', 'processing');

-- name: RecordSkippedThumbnailJob :exec
-- A thumbnail job that cannot run (the asset's process policy has no Thumbs process) is recorded terminal, so it
-- counts as an attempt and the asset is not reconsidered every pass.
INSERT INTO foghorn.processing_jobs (tenant_id, artifact_hash, job_type, status, error_message, completed_at, updated_at)
VALUES (sqlc.arg(tenant_id)::uuid, sqlc.arg(artifact_hash), 'thumbnail', 'failed', sqlc.arg(reason)::text, NOW(), NOW());
