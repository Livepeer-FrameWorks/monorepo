-- name: CountSavedDVRChapters :one
SELECT COUNT(*)::integer
FROM foghorn.dvr_chapters c
JOIN foghorn.artifacts parent ON parent.artifact_hash=c.artifact_hash
WHERE parent.tenant_id=sqlc.arg(tenant_id)::uuid
 AND parent.artifact_type='dvr' AND parent.status<>'deleted'
 AND c.artifact_hash=sqlc.arg(artifact_hash)::text
 AND (sqlc.arg(range_start_ms)::bigint=0 OR c.end_ms>sqlc.arg(range_start_ms)::bigint)
 AND (sqlc.arg(range_end_ms)::bigint=0 OR c.start_ms<sqlc.arg(range_end_ms)::bigint)
;

-- name: ListSavedDVRChaptersForward :many
SELECT sqlc.embed(c)
FROM foghorn.dvr_chapters c
JOIN foghorn.artifacts parent ON parent.artifact_hash=c.artifact_hash
WHERE parent.tenant_id=sqlc.arg(tenant_id)::uuid
 AND parent.artifact_type='dvr' AND parent.status<>'deleted'
 AND c.artifact_hash=sqlc.arg(artifact_hash)::text
 AND (sqlc.arg(range_start_ms)::bigint=0 OR c.end_ms>sqlc.arg(range_start_ms)::bigint)
 AND (sqlc.arg(range_end_ms)::bigint=0 OR c.start_ms<sqlc.arg(range_end_ms)::bigint)
 AND (NOT sqlc.arg(has_cursor)::boolean OR (c.start_ms,c.chapter_id) > (sqlc.arg(cursor_start_ms)::bigint,sqlc.arg(cursor_id)::text))
ORDER BY c.start_ms ASC,c.chapter_id ASC
LIMIT sqlc.arg(page_limit)::integer;

-- name: ListSavedDVRChaptersBackward :many
SELECT sqlc.embed(c)
FROM foghorn.dvr_chapters c
JOIN foghorn.artifacts parent ON parent.artifact_hash=c.artifact_hash
WHERE parent.tenant_id=sqlc.arg(tenant_id)::uuid
 AND parent.artifact_type='dvr' AND parent.status<>'deleted'
 AND c.artifact_hash=sqlc.arg(artifact_hash)::text
 AND (sqlc.arg(range_start_ms)::bigint=0 OR c.end_ms>sqlc.arg(range_start_ms)::bigint)
 AND (sqlc.arg(range_end_ms)::bigint=0 OR c.start_ms<sqlc.arg(range_end_ms)::bigint)
 AND (NOT sqlc.arg(has_cursor)::boolean OR (c.start_ms,c.chapter_id) < (sqlc.arg(cursor_start_ms)::bigint,sqlc.arg(cursor_id)::text))
ORDER BY c.start_ms DESC,c.chapter_id DESC
LIMIT sqlc.arg(page_limit)::integer;
