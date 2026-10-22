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

-- Clips and VODs (DVR chapters are VODs) deleted within the window, newest
-- first. Any node may hold relay caches of them without having reported them,
-- so their deletes go to every node, not only to artifact_nodes holders.
-- name: ListRecentlyDeletedRelayCacheableArtifacts :many
SELECT artifact_hash, artifact_type
FROM foghorn.artifacts
WHERE artifact_type IN ('clip', 'vod', 'chapter')
  AND status = 'deleted'
  AND updated_at > sqlc.arg(deleted_after)::timestamptz
ORDER BY updated_at DESC
LIMIT 1000;

-- Clips and VODs (DVR chapters are VODs) deleted after the (updated_at,
-- artifact_hash) position, oldest first: one page of the deletes a node is
-- sent from its relay delete cursor onwards.
-- name: ListDeletedRelayCacheableArtifactsAfter :many
SELECT artifact_hash, artifact_type, updated_at, federated_pointer
FROM foghorn.artifacts
WHERE artifact_type IN ('clip', 'vod', 'chapter')
  AND status = 'deleted'
  AND updated_at >= sqlc.arg(after_time)::timestamptz
  AND (updated_at > sqlc.arg(after_time)::timestamptz OR artifact_hash > sqlc.arg(after_hash)::text)
ORDER BY updated_at ASC, artifact_hash ASC
LIMIT sqlc.arg(page_size)::int;

-- A federated pointer's delete is not sent to a node holding an origin copy of
-- it, as in the holder passes above.
-- name: NodeHoldsArtifactOrigin :one
SELECT EXISTS (
    SELECT 1 FROM foghorn.artifact_nodes
    WHERE artifact_hash = sqlc.arg(artifact_hash) AND node_id = sqlc.arg(node_id)
      AND is_orphaned = false AND role = 'origin'
);

-- name: GetNodeRelayDeleteCursor :one
SELECT covered_through FROM foghorn.node_relay_delete_cursor WHERE node_id = $1;

-- name: AdvanceNodeRelayDeleteCursor :exec
INSERT INTO foghorn.node_relay_delete_cursor (node_id, covered_through, updated_at)
VALUES (sqlc.arg(node_id), sqlc.arg(covered_through)::timestamptz, NOW())
ON CONFLICT (node_id) DO UPDATE SET
    covered_through = EXCLUDED.covered_through,
    updated_at = NOW()
WHERE foghorn.node_relay_delete_cursor.covered_through < EXCLUDED.covered_through;

-- Moves the cursors of nodes the fan-out pass reached up to covered_through,
-- but only those already within the pass's window: a node further behind has
-- deletes older than the window outstanding, which its re-drive sends.
-- name: AdvanceCaughtUpNodeRelayDeleteCursors :exec
UPDATE foghorn.node_relay_delete_cursor
SET covered_through = sqlc.arg(covered_through)::timestamptz, updated_at = NOW()
WHERE node_id = ANY(sqlc.arg(node_ids)::text[])
  AND covered_through >= sqlc.arg(window_start)::timestamptz
  AND covered_through < sqlc.arg(covered_through)::timestamptz;

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
