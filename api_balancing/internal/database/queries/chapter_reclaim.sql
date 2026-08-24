-- name: ListFinalizedChaptersMissingDTSH :many
SELECT c.playback_artifact_hash, COALESCE(an.node_id, '')::text AS node_id
FROM foghorn.dvr_chapters AS c
JOIN foghorn.artifacts AS a ON a.artifact_hash = c.playback_artifact_hash
LEFT JOIN foghorn.artifact_nodes AS an
  ON an.artifact_hash = c.playback_artifact_hash
 AND an.is_orphaned = false
WHERE c.state = 'finalized'
  AND c.finalize_started_at IS NOT NULL
  AND c.finalize_started_at < NOW() - INTERVAL '5 minutes'
  AND a.origin_type = 'dvr_chapter'
  AND COALESCE(a.dtsh_synced, false) = false
LIMIT 50;

-- name: ListReclaimableDVRSegments :many
WITH overlapping AS (
    SELECT s.segment_name, s.s3_key, s.status,
           BOOL_AND(c.state IN ('frozen', 'reclaimed')) AS all_done
    FROM foghorn.dvr_segments AS s
    JOIN foghorn.dvr_chapters AS c
      ON c.artifact_hash = s.artifact_hash
     AND c.start_ms < s.media_end_ms
     AND c.end_ms > s.media_start_ms
    WHERE s.artifact_hash = sqlc.arg(artifact_hash)
      AND s.media_start_ms < sqlc.arg(range_end_ms)
      AND s.media_end_ms > sqlc.arg(range_start_ms)
      AND s.status = ANY(sqlc.arg(statuses)::text[])
    GROUP BY s.segment_name, s.s3_key, s.status
)
SELECT segment_name, COALESCE(s3_key, '')::text AS s3_key
FROM overlapping
WHERE all_done = true;

-- name: MarkDVRSegmentReclaimed :exec
UPDATE foghorn.dvr_segments
SET status = 'reclaimed',
    deleted_local_at = COALESCE(deleted_local_at, NOW())
WHERE artifact_hash = $1 AND segment_name = $2;

-- name: MarkDVRSegmentOrphanUnreachable :exec
UPDATE foghorn.dvr_segments
SET status = 'orphan_unreachable', deleted_local_at = NOW()
WHERE artifact_hash = $1
  AND segment_name = $2
  AND status IN ('pending', 'uploaded', 'failed_upload');

-- name: CountUnreclaimedDVRSegments :one
SELECT COUNT(*)
FROM foghorn.dvr_segments
WHERE artifact_hash = $1
  AND media_start_ms < sqlc.arg(range_end_ms)
  AND media_end_ms > sqlc.arg(range_start_ms)
  AND status != 'reclaimed';

-- name: LatestRecordingNode :one
SELECT COALESCE(node_id, '')::text AS node_id
FROM foghorn.artifact_nodes
WHERE artifact_hash = $1
  AND is_orphaned = false
ORDER BY last_seen_at DESC NULLS LAST
LIMIT 1;
