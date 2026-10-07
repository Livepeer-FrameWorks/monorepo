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

-- The deletes a node still owes: soft-deleted clips, DVRs and VODs whose bytes
-- it still holds. Re-driven when the node registers, since a delete sent to an
-- earlier connection, or deferred in its sidecar's memory, did not survive it.
-- name: ListDeletedArtifactsOnNode :many
SELECT a.artifact_hash, a.artifact_type
FROM foghorn.artifacts a
JOIN foghorn.artifact_nodes an
  ON an.artifact_hash = a.artifact_hash AND an.is_orphaned = false
WHERE an.node_id = sqlc.arg(node_id)
  AND a.artifact_type IN ('clip', 'dvr', 'vod')
  AND a.status = 'deleted'
  AND (a.federated_pointer = false OR an.role = 'cache')
ORDER BY a.artifact_hash
LIMIT 1000;
