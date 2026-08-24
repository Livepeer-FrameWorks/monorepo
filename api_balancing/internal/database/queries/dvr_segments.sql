-- name: LockDVRSegmentParent :one
SELECT status FROM foghorn.artifacts
WHERE artifact_hash = $1 AND artifact_type = 'dvr' AND tenant_id::text = $2
FOR UPDATE;

-- name: GetExistingDVRSegment :one
SELECT sequence, status, media_start_ms, media_end_ms, duration_ms
FROM foghorn.dvr_segments
WHERE artifact_hash = $1 AND segment_name = $2;

-- name: HealLostDVRSegment :exec
UPDATE foghorn.dvr_segments SET status = 'pending'
WHERE artifact_hash = $1 AND segment_name = $2 AND status = 'lost_local';

-- name: GetNextDVRSegmentSequence :one
SELECT (COALESCE(MAX(sequence), -1) + 1)::bigint FROM foghorn.dvr_segments WHERE artifact_hash = $1;

-- name: InsertPendingDVRSegment :exec
INSERT INTO foghorn.dvr_segments (
    artifact_hash, segment_name, sequence, media_start_ms, media_end_ms, duration_ms,
    s3_key, status, created_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, 'pending', NOW());

-- name: MarkDVRSegmentUploaded :exec
UPDATE foghorn.dvr_segments
SET status = 'uploaded', size_bytes = $3, uploaded_at = NOW()
WHERE foghorn.dvr_segments.artifact_hash = $1 AND segment_name = $2 AND status IN ('pending', 'failed_upload')
  AND EXISTS (
      SELECT 1 FROM foghorn.artifacts a
      WHERE a.artifact_hash = foghorn.dvr_segments.artifact_hash
        AND a.artifact_type = 'dvr' AND a.tenant_id = $4
  );

-- name: GetDVRSegmentProgress :one
SELECT COUNT(*)::bigint AS segment_count, COALESCE(SUM(size_bytes), 0)::bigint AS size_bytes
FROM foghorn.dvr_segments
WHERE foghorn.dvr_segments.artifact_hash = $1 AND status NOT IN ('lost_local', 'reclaimed')
  AND EXISTS (
      SELECT 1 FROM foghorn.artifacts a
      WHERE a.artifact_hash = foghorn.dvr_segments.artifact_hash
        AND a.artifact_type = 'dvr' AND a.tenant_id = $2
  );

-- name: MarkDVRSegmentDropped :execrows
UPDATE foghorn.dvr_segments
SET status = sqlc.arg(target_status)::text, drop_reason = sqlc.arg(drop_reason)::text,
    deleted_local_at = CASE WHEN sqlc.arg(was_uploaded)::boolean THEN NOW() ELSE deleted_local_at END,
    dropped_at = NOW()
WHERE foghorn.dvr_segments.artifact_hash = sqlc.arg(artifact_hash) AND segment_name = sqlc.arg(segment_name)
  AND status NOT IN ('deleted_local', 'lost_local', 'reclaimed')
  AND EXISTS (
      SELECT 1 FROM foghorn.artifacts a
      WHERE a.artifact_hash = foghorn.dvr_segments.artifact_hash
        AND a.artifact_type = 'dvr' AND a.tenant_id = sqlc.arg(tenant_id)
  );

-- name: UpsertLostDVRSegment :exec
INSERT INTO foghorn.dvr_segments (
    artifact_hash, segment_name, sequence, media_start_ms, media_end_ms, duration_ms,
    size_bytes, s3_key, status, drop_reason, created_at, dropped_at
) VALUES (sqlc.arg(artifact_hash), sqlc.arg(segment_name), sqlc.arg(sequence),
          sqlc.arg(media_start_ms), sqlc.arg(media_end_ms), sqlc.arg(duration_ms),
          sqlc.narg(size_bytes), '', 'lost_local', sqlc.arg(drop_reason), NOW(), NOW())
ON CONFLICT (artifact_hash, segment_name) DO UPDATE SET
    status = 'lost_local', drop_reason = EXCLUDED.drop_reason, dropped_at = NOW()
WHERE foghorn.dvr_segments.status NOT IN ('deleted_local', 'lost_local', 'reclaimed');

-- name: ListEvictableDVRSegments :many
SELECT s.segment_name FROM foghorn.dvr_segments s
WHERE s.artifact_hash = $1 AND s.status = 'uploaded'
  AND s.media_end_ms < $2
  AND EXISTS (
      SELECT 1 FROM foghorn.artifacts a
      WHERE a.artifact_hash = s.artifact_hash AND a.artifact_type = 'dvr'
        AND a.tenant_id = $4
  )
  AND NOT EXISTS (
      SELECT 1 FROM foghorn.dvr_chapters c
      WHERE c.artifact_hash = s.artifact_hash AND c.start_ms < s.media_end_ms
        AND c.end_ms > s.media_start_ms AND c.state NOT IN ('frozen', 'reclaimed')
  )
ORDER BY s.sequence ASC
LIMIT $3;

-- name: ListPendingDVRSegments :many
SELECT artifact_hash, segment_name, sequence, media_start_ms, media_end_ms, duration_ms,
       size_bytes, s3_key, status, drop_reason, created_at, uploaded_at, deleted_local_at, dropped_at
FROM foghorn.dvr_segments
WHERE artifact_hash = sqlc.arg(artifact_hash) AND status IN ('pending', 'failed_upload')
  AND created_at <= sqlc.arg(cutoff)
ORDER BY sequence ASC LIMIT sqlc.arg(row_limit);

-- name: ListDVRSegmentsOwnedByChapter :many
SELECT artifact_hash, segment_name, sequence, media_start_ms, media_end_ms, duration_ms,
       size_bytes, s3_key, status, drop_reason, created_at, uploaded_at, deleted_local_at, dropped_at
FROM foghorn.dvr_segments
WHERE artifact_hash = $1 AND media_start_ms >= $2 AND media_start_ms < $3
ORDER BY media_start_ms ASC, sequence ASC;

-- name: ListAllDVRSegmentsForArtifact :many
SELECT artifact_hash, segment_name, sequence, media_start_ms, media_end_ms, duration_ms,
       size_bytes, s3_key, status, drop_reason, created_at, uploaded_at, deleted_local_at, dropped_at
FROM foghorn.dvr_segments
WHERE artifact_hash = $1
ORDER BY media_start_ms ASC, sequence ASC;

-- name: ListDVRSegmentsForRange :many
SELECT artifact_hash, segment_name, sequence, media_start_ms, media_end_ms, duration_ms,
       size_bytes, s3_key, status, drop_reason, created_at, uploaded_at, deleted_local_at, dropped_at
FROM foghorn.dvr_segments
WHERE artifact_hash = $1 AND media_start_ms < $3 AND media_end_ms > $2
ORDER BY media_start_ms ASC, sequence ASC;

-- name: LookupDVRSegmentsByName :many
SELECT artifact_hash, segment_name, sequence, media_start_ms, media_end_ms, duration_ms,
       size_bytes, s3_key, status, drop_reason, created_at, uploaded_at, deleted_local_at, dropped_at
FROM foghorn.dvr_segments
WHERE foghorn.dvr_segments.artifact_hash = sqlc.arg(artifact_hash)
  AND segment_name = ANY(sqlc.arg(segment_names)::text[])
  AND EXISTS (
      SELECT 1 FROM foghorn.artifacts a
      WHERE a.artifact_hash = foghorn.dvr_segments.artifact_hash
        AND a.artifact_type = 'dvr' AND a.tenant_id = sqlc.arg(tenant_id)
  );

-- name: MarkRemainingDVRSegmentsLost :execrows
UPDATE foghorn.dvr_segments
SET status = 'lost_local', drop_reason = $2, dropped_at = NOW()
WHERE artifact_hash = $1 AND status IN ('pending', 'failed_upload');
