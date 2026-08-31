-- name: ListDeletedClipNodes :many
SELECT a.artifact_hash, an.node_id
FROM foghorn.artifacts a
JOIN foghorn.artifact_nodes an
  ON an.artifact_hash = a.artifact_hash AND an.is_orphaned = false
WHERE a.artifact_type = 'clip'
  AND a.status = 'deleted'
	-- A federated pointer may own a disposable cache copy, but a legacy
	-- pointer with an origin row must never turn remote authority into a hard
	-- delete of locally-originated bytes.
	AND (a.federated_pointer = false OR an.role = 'cache')
  AND a.updated_at < NOW() - CAST(sqlc.arg(max_age) AS text)::interval
LIMIT 100;

-- name: ListDeletedDVRNodes :many
SELECT a.artifact_hash, an.node_id
FROM foghorn.artifacts a
JOIN foghorn.artifact_nodes an
  ON an.artifact_hash = a.artifact_hash AND an.is_orphaned = false
WHERE a.artifact_type = 'dvr'
  AND a.status = 'deleted'
	AND (a.federated_pointer = false OR an.role = 'cache')
  AND a.updated_at < NOW() - CAST(sqlc.arg(max_age) AS text)::interval
LIMIT 100;

-- name: ListDeletedVODNodes :many
SELECT a.artifact_hash, an.node_id
FROM foghorn.artifacts a
JOIN foghorn.artifact_nodes an
  ON an.artifact_hash = a.artifact_hash AND an.is_orphaned = false
WHERE a.artifact_type = 'vod'
  AND a.status = 'deleted'
	AND (a.federated_pointer = false OR an.role = 'cache')
  AND a.updated_at < NOW() - CAST(sqlc.arg(max_age) AS text)::interval
LIMIT 100;

-- name: DeleteStaleOrphanedArtifactNodes :execrows
DELETE FROM foghorn.artifact_nodes
WHERE is_orphaned = true
  AND last_seen_at < NOW() - INTERVAL '24 hours';
