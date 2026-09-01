-- name: SetDVRChapterPlaybackID :exec
UPDATE foghorn.dvr_chapters SET playback_id = $2 WHERE chapter_id = $1;

-- name: ClosePreviousCurrentDVRChapters :execrows
UPDATE foghorn.dvr_chapters
SET is_current = false, state = CASE WHEN state = 'open' THEN 'closed' ELSE state END
WHERE artifact_hash = $1 AND is_current = true AND chapter_id <> $2;

-- name: UpsertOpenDVRChapter :exec
INSERT INTO foghorn.dvr_chapters (
  chapter_id, artifact_hash, mode, interval_seconds, start_ms, end_ms,
  is_current, state, segment_count, has_gaps, created_at
) VALUES (
  sqlc.arg(chapter_id), sqlc.arg(artifact_hash), sqlc.arg(mode), sqlc.narg(interval_seconds),
  sqlc.arg(start_ms), sqlc.arg(end_ms), true, sqlc.arg(state), 0, false, NOW()
)
ON CONFLICT (chapter_id) DO UPDATE SET is_current = EXCLUDED.is_current;

-- name: CloseDVRChapter :execrows
UPDATE foghorn.dvr_chapters SET is_current = false, state = 'closed'
WHERE chapter_id = $1 AND state = 'open';

-- name: CloseCurrentDVRChapterForArtifact :execrows
UPDATE foghorn.dvr_chapters
SET is_current = false, state = CASE WHEN state = 'open' THEN 'closed' ELSE state END
WHERE artifact_hash = $1 AND is_current = true;

-- name: ClaimDVRChapterFinalization :one
UPDATE foghorn.dvr_chapters
SET state = 'finalizing', playback_artifact_hash = sqlc.narg(playback_artifact_hash),
    finalize_attempts = finalize_attempts + 1, finalize_started_at = NOW(),
    finalize_node_id = NULLIF(sqlc.arg(finalize_node_id)::text, ''),
    finalize_processes_json = NULLIF(sqlc.arg(finalize_processes_json)::text, '')
WHERE chapter_id = sqlc.arg(chapter_id)
  AND (state = 'closed' OR (state = 'finalizing'
    AND COALESCE(finalize_started_at, created_at) < NOW() - make_interval(secs => sqlc.arg(stale_seconds))))
RETURNING finalize_attempts;

-- name: MarkDVRChapterFinalized :execrows
UPDATE foghorn.dvr_chapters
SET state = 'finalized', segment_count = sqlc.arg(segment_count), has_gaps = sqlc.arg(has_gaps),
    actual_media_start_ms = sqlc.narg(media_start_ms),
    actual_media_end_ms = sqlc.narg(media_end_ms), finalize_node_id = NULL
WHERE chapter_id = sqlc.arg(chapter_id)
  AND state = 'finalizing'
  AND finalize_attempts = sqlc.arg(expected_attempt);

-- name: MarkDVRChapterFrozen :exec
UPDATE foghorn.dvr_chapters SET state = 'frozen', frozen_at = NOW()
WHERE chapter_id = $1 AND state = 'finalized';

-- name: MarkDVRChapterReclaimStarted :execrows
UPDATE foghorn.dvr_chapters SET reclaim_started_at = NOW()
WHERE chapter_id = $1 AND state = 'frozen'
  AND (reclaim_started_at IS NULL OR reclaim_started_at < NOW() - make_interval(secs => $2));

-- name: MarkDVRChapterReclaimed :exec
UPDATE foghorn.dvr_chapters SET state = 'reclaimed'
WHERE chapter_id = $1 AND state = 'frozen';

-- name: FailDVRChapter :one
UPDATE foghorn.dvr_chapters
SET state = sqlc.arg(state), last_failure_reason = sqlc.narg(last_failure_reason), finalize_node_id = NULL
WHERE chapter_id = sqlc.arg(chapter_id)
  AND finalize_attempts = sqlc.arg(expected_attempt)
  AND ((sqlc.arg(expected_node)::text = '' AND state IN ('closed', 'finalizing'))
    OR (sqlc.arg(expected_node)::text <> '' AND state = 'finalizing' AND finalize_node_id = sqlc.arg(expected_node)::text))
RETURNING playback_artifact_hash;

-- name: FailDVRChapterArtifact :one
UPDATE foghorn.artifacts
SET status = 'failed', error_message = $2, updated_at = NOW()
WHERE artifact_hash = $1 AND origin_type = 'dvr_chapter'
  AND federated_pointer = false
  AND status NOT IN ('ready', 'failed', 'deleted', 'expired', 'aborted')
RETURNING tenant_id::text;

-- name: RetryDVRChapterFinalize :execrows
UPDATE foghorn.dvr_chapters
SET state = 'closed', last_failure_reason = sqlc.narg(last_failure_reason), finalize_node_id = NULL
WHERE chapter_id = sqlc.arg(chapter_id) AND state = 'finalizing'
  AND finalize_attempts = sqlc.arg(expected_attempt)
  AND (sqlc.arg(expected_node)::text = '' OR finalize_node_id = sqlc.arg(expected_node)::text);

-- name: ListDVRChaptersNeedingFinalization :many
SELECT sqlc.embed(c)
FROM foghorn.dvr_chapters c
WHERE (c.state = 'closed' OR (c.state = 'finalizing'
  AND COALESCE(c.finalize_started_at, c.created_at) < NOW() - LEAST(
    GREATEST(make_interval(secs => 2 * GREATEST(c.end_ms - c.start_ms, 0) / 1000.0), make_interval(secs => $1)),
    make_interval(secs => $2))))
  AND EXISTS (SELECT 1 FROM foghorn.artifacts p
    WHERE p.artifact_hash = c.artifact_hash AND p.artifact_type = 'dvr'
      AND p.federated_pointer = false AND p.status <> 'deleted')
ORDER BY c.created_at, c.chapter_id LIMIT $3;

-- name: ListDVRChaptersNeedingReclaim :many
SELECT sqlc.embed(c)
FROM foghorn.dvr_chapters c
WHERE c.state = 'frozen'
  AND (c.reclaim_started_at IS NULL OR c.reclaim_started_at < NOW() - make_interval(secs => $1))
  AND EXISTS (SELECT 1 FROM foghorn.artifacts p
    WHERE p.artifact_hash = c.artifact_hash AND p.artifact_type = 'dvr'
      AND p.federated_pointer = false)
ORDER BY c.created_at, c.chapter_id LIMIT $2;

-- name: GetDVRChapter :one
SELECT sqlc.embed(c)
FROM foghorn.dvr_chapters c WHERE c.chapter_id = $1;

-- name: GetDVRChaptersByID :many
SELECT sqlc.embed(c)
FROM foghorn.dvr_chapters c WHERE c.chapter_id = ANY($1::text[]);

-- name: GetCurrentDVRChapter :one
SELECT sqlc.embed(c)
FROM foghorn.dvr_chapters c
WHERE c.artifact_hash = $1 AND c.is_current = true
ORDER BY c.start_ms DESC, c.chapter_id DESC LIMIT 1;

-- name: GetLatestDVRChapterBefore :one
SELECT sqlc.embed(c)
FROM foghorn.dvr_chapters c
WHERE c.artifact_hash = $1 AND c.mode = $2 AND COALESCE(c.interval_seconds, 0) = $3 AND c.start_ms < $4
ORDER BY c.start_ms DESC, c.chapter_id DESC LIMIT 1;

-- name: DeleteDVRChapter :exec
DELETE FROM foghorn.dvr_chapters WHERE chapter_id = $1;

-- name: PropagateDVRChapterRetention :execrows
UPDATE foghorn.artifacts a SET retention_until = sqlc.narg(retention_until), updated_at = NOW()
FROM foghorn.dvr_chapters c
WHERE c.artifact_hash = sqlc.arg(artifact_hash)
  AND a.artifact_hash = c.playback_artifact_hash
  AND a.tenant_id = sqlc.arg(tenant_id)::uuid
  AND a.origin_type = 'dvr_chapter'
  AND a.federated_pointer = false
  AND a.status <> 'deleted';

-- name: SoftDeleteDVRParent :execrows
UPDATE foghorn.artifacts SET status = 'deleted', updated_at = NOW()
WHERE artifact_hash = sqlc.arg(artifact_hash) AND tenant_id = sqlc.arg(tenant_id)::uuid
  AND artifact_type = 'dvr' AND federated_pointer = false AND status <> 'deleted';

-- name: ListDeletedDVRParentsWithChapters :many
SELECT p.artifact_hash, p.tenant_id::text AS tenant_id
FROM foghorn.artifacts p
WHERE p.artifact_type = 'dvr' AND p.federated_pointer = false AND p.status = 'deleted'
  AND EXISTS (SELECT 1 FROM foghorn.dvr_chapters c WHERE c.artifact_hash = p.artifact_hash)
LIMIT $1;

-- name: SoftDeleteDVRChapterArtifacts :many
UPDATE foghorn.artifacts a SET status = 'deleted', updated_at = NOW()
FROM foghorn.dvr_chapters c
WHERE c.artifact_hash = sqlc.arg(artifact_hash) AND a.artifact_hash = c.playback_artifact_hash
  AND a.origin_type = 'dvr_chapter' AND a.tenant_id = sqlc.arg(tenant_id)::uuid
  AND a.federated_pointer = false AND a.status <> 'deleted'
  AND EXISTS (
      SELECT 1 FROM foghorn.artifacts parent
      WHERE parent.artifact_hash = c.artifact_hash
        AND parent.tenant_id = sqlc.arg(tenant_id)::uuid
        AND parent.artifact_type = 'dvr'
        AND parent.federated_pointer = false
  )
RETURNING a.artifact_hash;

-- name: DeleteDVRChapterRowsForTenant :exec
DELETE FROM foghorn.dvr_chapters c USING foghorn.artifacts parent
WHERE c.artifact_hash = sqlc.arg(artifact_hash) AND parent.artifact_hash = sqlc.arg(artifact_hash)
  AND parent.tenant_id = sqlc.arg(tenant_id)::uuid AND parent.artifact_type = 'dvr'
  AND parent.federated_pointer = false;

-- name: GetDVRParentArtifactStatus :one
SELECT status FROM foghorn.artifacts
WHERE artifact_hash = $1 AND artifact_type = 'dvr' AND federated_pointer = false;

-- name: ClearCurrentChaptersForInactiveDVRs :execrows
UPDATE foghorn.dvr_chapters c
SET is_current = false, state = CASE WHEN c.state = 'open' THEN 'closed' ELSE c.state END
FROM foghorn.artifacts a
WHERE c.artifact_hash = a.artifact_hash AND c.is_current = true AND a.artifact_type = 'dvr'
  AND a.federated_pointer = false
  AND a.status IN ('completed', 'completed_partial', 'failed', 'ready', 'deleted');

-- name: LockDVRChapterMutation :exec
SELECT pg_advisory_xact_lock(sqlc.arg(lock_namespace)::integer, hashtext(sqlc.arg(artifact_hash)::text));

-- name: ListDVRChaptersForArtifact :many
SELECT sqlc.embed(c)
FROM foghorn.dvr_chapters c
WHERE c.artifact_hash = sqlc.arg(artifact_hash) AND c.start_ms >= sqlc.arg(start_ms) AND (sqlc.arg(end_ms)::bigint = 0 OR c.start_ms < sqlc.arg(end_ms)::bigint)
  AND (sqlc.arg(mode)::text = '' OR c.mode = sqlc.arg(mode)::text)
  AND (sqlc.narg(interval_seconds)::int IS NULL OR COALESCE(c.interval_seconds, 0) = sqlc.narg(interval_seconds)::int)
  AND (sqlc.arg(cursor_start_ms)::bigint = 0 OR c.start_ms > sqlc.arg(cursor_start_ms)::bigint OR (c.start_ms = sqlc.arg(cursor_start_ms)::bigint AND c.chapter_id > sqlc.arg(cursor_chapter_id)))
ORDER BY c.start_ms, c.chapter_id LIMIT sqlc.arg(page_limit);
