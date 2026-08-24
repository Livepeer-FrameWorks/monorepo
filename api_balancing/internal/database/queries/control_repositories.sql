-- name: ListActiveClips :many
SELECT a.artifact_hash, ''::text AS tenant_id, COALESCE(a.stream_internal_name, '')::text AS stream_internal_name,
       COALESCE(n.node_id, '')::text AS node_id, a.status, COALESCE(n.file_path, '')::text AS file_path,
       COALESCE(a.size_bytes, 0)::bigint AS size_bytes, COALESCE(a.storage_location, 'pending')::text AS storage_location
FROM foghorn.artifacts a
LEFT JOIN foghorn.artifact_nodes n ON a.artifact_hash = n.artifact_hash AND n.is_orphaned = false
WHERE a.artifact_type = 'clip' AND a.status != 'deleted';

-- name: ResolveClipInternalNameByRequestID :one
SELECT COALESCE(stream_internal_name, '')::text
FROM foghorn.artifacts
WHERE request_id = $1 AND artifact_type = 'clip';

-- name: ClipNeedsDtshSync :one
SELECT EXISTS (
    SELECT 1 FROM foghorn.artifacts
    WHERE artifact_hash = $1
      AND artifact_type = 'clip'
      AND sync_status = 'synced'
      AND COALESCE(dtsh_synced, false) = false
);

-- name: ListAllDVR :many
SELECT a.artifact_hash, ''::text AS tenant_id, COALESCE(a.stream_internal_name, '')::text AS stream_internal_name,
       COALESCE(n.node_id, '')::text AS node_id, COALESCE(n.base_url, '')::text AS base_url, a.status,
       COALESCE(a.duration_seconds, 0)::bigint AS duration_seconds, COALESCE(a.size_bytes, 0)::bigint AS size_bytes,
       COALESCE(a.manifest_path, '')::text AS manifest_path
FROM foghorn.artifacts a
LEFT JOIN foghorn.artifact_nodes n ON a.artifact_hash = n.artifact_hash AND n.is_orphaned = false
WHERE a.artifact_type = 'dvr';

-- name: ResolveDVRInternalNameByHash :one
SELECT COALESCE(stream_internal_name, '')::text
FROM foghorn.artifacts
WHERE artifact_hash = $1 AND artifact_type = 'dvr';

-- name: LockDVRProgressArtifact :one
SELECT status,
       tenant_id::text AS tenant_id,
       COALESCE(stream_id::text, '')::text AS stream_id,
       COALESCE(stream_internal_name, '')::text AS stream_internal_name,
       COALESCE(dvr_start_dispatch->>'node_id', '')::text AS dispatch_node
FROM foghorn.artifacts
WHERE artifact_hash = $1 AND artifact_type = 'dvr'
FOR UPDATE;

-- name: RecordDVRProgress :exec
UPDATE foghorn.artifacts
SET status = CASE WHEN status IN ('requested', 'starting') THEN 'recording' ELSE status END,
    size_bytes = GREATEST(COALESCE(size_bytes, 0), sqlc.arg(size_bytes)::bigint),
    updated_at = NOW()
WHERE artifact_hash = $1 AND artifact_type = 'dvr';

-- name: RecordDVRCompletion :exec
UPDATE foghorn.artifacts
SET status = sqlc.arg(status)::text,
    ended_at = NOW(),
    duration_seconds = sqlc.arg(duration_seconds)::bigint,
    size_bytes = sqlc.arg(size_bytes)::bigint,
    manifest_path = sqlc.arg(manifest_path)::text,
    error_message = NULLIF(sqlc.arg(error_message)::text, ''),
    updated_at = NOW()
WHERE artifact_hash = sqlc.arg(artifact_hash)
  AND artifact_type = 'dvr'
  AND status IN ('requested', 'starting', 'recording', 'finalizing');

-- name: DVRNeedsDtshSync :one
SELECT EXISTS (
    SELECT 1 FROM foghorn.artifacts
    WHERE artifact_hash = $1
      AND artifact_type = 'dvr'
      AND sync_status = 'synced'
      AND COALESCE(dtsh_synced, false) = false
);

-- name: ListAllNodes :many
SELECT node_id, COALESCE(base_url, '')::text AS base_url, COALESCE(outputs, '{}'::jsonb) AS outputs
FROM foghorn.node_outputs;

-- name: ListNodeMaintenance :many
SELECT node_id, mode, set_at, COALESCE(set_by, '')::text AS set_by
FROM foghorn.node_maintenance;

-- name: UpsertNodeOutputs :exec
INSERT INTO foghorn.node_outputs (node_id, base_url, outputs, last_updated)
VALUES (sqlc.arg(node_id), NULLIF(sqlc.arg(base_url)::text, ''), COALESCE(sqlc.arg(outputs)::jsonb, '{}'::jsonb), NOW())
ON CONFLICT (node_id) DO UPDATE SET
    base_url = NULLIF(EXCLUDED.base_url, ''),
    outputs = COALESCE(EXCLUDED.outputs, '{}'::jsonb),
    last_updated = NOW();

-- name: UpsertNodeLifecycles :exec
INSERT INTO foghorn.node_lifecycle (node_id, lifecycle, last_updated)
SELECT (sqlc.arg(node_ids)::text[])[i], (sqlc.arg(lifecycles)::text[])[i]::jsonb, NOW()
FROM generate_subscripts(sqlc.arg(node_ids)::text[], 1) AS g(i)
ON CONFLICT (node_id) DO UPDATE SET
    lifecycle = EXCLUDED.lifecycle,
    last_updated = NOW();

-- name: UpsertNodeComponents :exec
INSERT INTO foghorn.node_components (node_id, component, current_version, last_reported_at)
SELECT (sqlc.arg(node_ids)::text[])[i], (sqlc.arg(components)::text[])[i],
       NULLIF((sqlc.arg(versions)::text[])[i], ''), NOW()
FROM generate_subscripts(sqlc.arg(node_ids)::text[], 1) AS g(i)
ON CONFLICT (node_id, component) DO UPDATE SET
    current_version = EXCLUDED.current_version,
    last_reported_at = NOW();

-- name: UpsertNodeMaintenance :exec
INSERT INTO foghorn.node_maintenance (node_id, mode, set_at, set_by)
VALUES (sqlc.arg(node_id), sqlc.arg(mode), NOW(), NULLIF(sqlc.arg(set_by)::text, ''))
ON CONFLICT (node_id) DO UPDATE SET
    mode = EXCLUDED.mode,
    set_at = NOW(),
    set_by = EXCLUDED.set_by;

-- name: AdvanceNodeArtifactReportWatermark :one
INSERT INTO foghorn.node_artifact_report_watermark AS w (node_id, connection_fence, seq)
VALUES ($1, $2, $3)
ON CONFLICT (node_id) DO UPDATE SET connection_fence = EXCLUDED.connection_fence, seq = EXCLUDED.seq
WHERE (w.connection_fence, w.seq) < (EXCLUDED.connection_fence, EXCLUDED.seq)
RETURNING connection_fence;

-- name: AllocateNodeControlFence :one
SELECT nextval('foghorn.node_control_fence_seq')::bigint;

-- name: UpdateArtifactReportMetadata :exec
UPDATE foghorn.artifacts SET
    stream_internal_name = COALESCE(stream_internal_name, sqlc.arg(stream_internal_name)::text),
    access_count = GREATEST(COALESCE(access_count, 0), sqlc.arg(access_count)::bigint),
    last_accessed_at = CASE
        WHEN sqlc.arg(last_accessed)::bigint = 0 THEN last_accessed_at
        WHEN last_accessed_at IS NULL THEN to_timestamp(sqlc.arg(last_accessed)::bigint)
        ELSE GREATEST(last_accessed_at, to_timestamp(sqlc.arg(last_accessed)::bigint))
    END,
    updated_at = NOW()
WHERE artifact_hash = $1;

-- name: LockArtifactPlacementParent :exec
SELECT artifact_hash FROM foghorn.artifacts WHERE artifact_hash = $1 FOR UPDATE;

-- name: LockArtifactNodeState :one
SELECT role, is_orphaned, is_complete
FROM foghorn.artifact_nodes
WHERE artifact_hash = $1 AND node_id = $2
FOR UPDATE;

-- name: UpsertReportedArtifactNode :one
INSERT INTO foghorn.artifact_nodes
    (artifact_hash, node_id, file_path, size_bytes, segment_count, segment_bytes, access_count, last_accessed, last_seen_at, is_orphaned, cached_at, role, is_complete)
SELECT sqlc.arg(artifact_hash), sqlc.arg(node_id), sqlc.arg(file_path)::text, sqlc.arg(size_bytes)::bigint,
       sqlc.arg(segment_count)::bigint, sqlc.arg(segment_bytes)::bigint, sqlc.arg(access_count)::bigint,
       CASE WHEN sqlc.arg(last_accessed)::bigint > 0 THEN to_timestamp(sqlc.arg(last_accessed)::bigint) ELSE NULL END,
       NOW(), false,
       COALESCE((SELECT cached_at FROM foghorn.artifact_nodes WHERE artifact_hash = sqlc.arg(artifact_hash)::varchar AND node_id = sqlc.arg(node_id)::varchar), NOW()),
       sqlc.arg(role), sqlc.arg(is_complete)
WHERE EXISTS (SELECT 1 FROM foghorn.artifacts WHERE artifact_hash = sqlc.arg(artifact_hash))
ON CONFLICT (artifact_hash, node_id) DO UPDATE SET
    file_path = EXCLUDED.file_path,
    size_bytes = EXCLUDED.size_bytes,
    segment_count = EXCLUDED.segment_count,
    segment_bytes = EXCLUDED.segment_bytes,
    access_count = GREATEST(COALESCE(foghorn.artifact_nodes.access_count, 0), EXCLUDED.access_count),
    last_accessed = CASE
        WHEN EXCLUDED.last_accessed IS NULL THEN foghorn.artifact_nodes.last_accessed
        WHEN foghorn.artifact_nodes.last_accessed IS NULL THEN EXCLUDED.last_accessed
        ELSE GREATEST(foghorn.artifact_nodes.last_accessed, EXCLUDED.last_accessed)
    END,
    last_seen_at = NOW(),
    is_orphaned = false,
    role = CASE WHEN foghorn.artifact_nodes.role = 'origin' THEN 'origin' ELSE EXCLUDED.role END,
    is_complete = CASE WHEN foghorn.artifact_nodes.role = 'origin' THEN foghorn.artifact_nodes.is_complete
        ELSE (foghorn.artifact_nodes.is_complete OR EXCLUDED.is_complete) END
RETURNING role, is_complete;

-- name: OrphanStaleReportedArtifactNodes :many
UPDATE foghorn.artifact_nodes
SET is_orphaned = true
WHERE node_id = $1
  AND last_seen_at < NOW() - INTERVAL '10 minutes'
  AND is_orphaned = false
RETURNING artifact_hash, role;

-- name: GetArtifactPlacementTenant :one
SELECT tenant_id::text FROM foghorn.artifacts WHERE artifact_hash = $1;

-- name: AllocateArtifactNodeCopyVersion :one
SELECT nextval('foghorn.artifact_node_copy_version_seq')::bigint;

-- name: SetArtifactNodeLastEmittedVersion :exec
UPDATE foghorn.artifact_nodes
SET last_emitted_version = $1
WHERE artifact_hash = $2 AND node_id = $3;

-- name: GetArtifactSyncInfo :one
SELECT artifact_hash, artifact_type, COALESCE(status, 'requested')::text AS status,
       COALESCE(sync_status, 'pending')::text AS sync_status, s3_url, last_sync_attempt, sync_error
FROM foghorn.artifacts
WHERE artifact_hash = $1;

-- name: ListArtifactCachedNodes :many
SELECT node_id, cached_at
FROM foghorn.artifact_nodes
WHERE artifact_hash = $1 AND is_orphaned = false;

-- name: MarkArtifactSynced :exec
UPDATE foghorn.artifacts
SET sync_status = 'synced',
    s3_url = COALESCE(NULLIF(sqlc.arg(s3_url)::text, ''), s3_url),
    last_sync_attempt = NOW(),
    sync_error = NULL
WHERE artifact_hash = $1;

-- name: SetArtifactSyncStatus :exec
UPDATE foghorn.artifacts
SET sync_status = sqlc.arg(sync_status)::text,
    s3_url = COALESCE(NULLIF(sqlc.arg(s3_url)::text, ''), s3_url),
    last_sync_attempt = NOW(),
    sync_error = NULL
WHERE artifact_hash = $1;

-- name: UpsertCachedArtifactNode :one
INSERT INTO foghorn.artifact_nodes (artifact_hash, node_id, file_path, size_bytes, last_seen_at, is_orphaned, cached_at, role, is_complete)
VALUES (sqlc.arg(artifact_hash), sqlc.arg(node_id), NULLIF(sqlc.arg(file_path)::text, ''),
        NULLIF(sqlc.arg(size_bytes)::bigint, 0), NOW(), false, NOW(), 'cache', true)
ON CONFLICT (artifact_hash, node_id) DO UPDATE SET
    file_path = COALESCE(NULLIF(EXCLUDED.file_path, ''), foghorn.artifact_nodes.file_path),
    size_bytes = COALESCE(EXCLUDED.size_bytes, foghorn.artifact_nodes.size_bytes),
    last_seen_at = NOW(),
    is_orphaned = false,
    is_complete = CASE WHEN foghorn.artifact_nodes.role = 'origin' THEN foghorn.artifact_nodes.is_complete ELSE true END,
    cached_at = COALESCE(foghorn.artifact_nodes.cached_at, NOW())
RETURNING size_bytes;

-- name: LockDVRRecordingOrigin :one
SELECT role, is_complete, is_orphaned
FROM foghorn.artifact_nodes
WHERE artifact_hash = $1 AND node_id = $2
FOR UPDATE;

-- name: UpsertDVRRecordingOrigin :one
INSERT INTO foghorn.artifact_nodes (artifact_hash, node_id, base_url, cached_at, last_seen_at, is_orphaned, role, is_complete)
VALUES (sqlc.arg(artifact_hash), sqlc.arg(node_id), sqlc.arg(base_url)::text, NOW(), NOW(), false, 'origin', false)
ON CONFLICT (artifact_hash, node_id) DO UPDATE SET
    base_url = EXCLUDED.base_url,
    last_seen_at = NOW(),
    is_orphaned = false,
    cached_at = COALESCE(foghorn.artifact_nodes.cached_at, NOW()),
    role = 'origin'
RETURNING is_complete;

-- name: UpsertOriginArtifactNode :one
INSERT INTO foghorn.artifact_nodes (artifact_hash, node_id, file_path, size_bytes, last_seen_at, is_orphaned, cached_at, role, is_complete)
VALUES (sqlc.arg(artifact_hash), sqlc.arg(node_id), NULLIF(sqlc.arg(file_path)::text, ''),
        NULLIF(sqlc.arg(size_bytes)::bigint, 0), NOW(), false, NOW(), 'origin', sqlc.arg(is_complete))
ON CONFLICT (artifact_hash, node_id) DO UPDATE SET
    file_path = COALESCE(NULLIF(EXCLUDED.file_path, ''), foghorn.artifact_nodes.file_path),
    size_bytes = COALESCE(EXCLUDED.size_bytes, foghorn.artifact_nodes.size_bytes),
    last_seen_at = NOW(),
    is_orphaned = false,
    role = 'origin',
    is_complete = CASE WHEN EXCLUDED.is_complete THEN true ELSE foghorn.artifact_nodes.is_complete END
RETURNING is_complete;

-- name: ListOriginNodes :many
SELECT node_id FROM foghorn.artifact_nodes
WHERE artifact_hash = $1
  AND role = 'origin'
  AND is_complete = true
  AND is_orphaned = false
ORDER BY last_seen_at DESC;

-- name: GetArtifactCachedAt :one
SELECT COALESCE(MIN(cached_at), to_timestamp(0))::timestamptz AS cached_at FROM foghorn.artifact_nodes
WHERE artifact_hash = $1 AND is_orphaned = false;

-- name: IsArtifactSynced :one
SELECT EXISTS (
    SELECT 1 FROM foghorn.artifacts
    WHERE artifact_hash = $1 AND sync_status = 'synced'
);

-- name: ListAllNodeArtifacts :many
SELECT an.node_id, an.artifact_hash, COALESCE(a.artifact_type, 'clip')::text AS artifact_type,
       COALESCE(a.stream_internal_name, '')::text AS stream_internal_name,
       COALESCE(an.file_path, '')::text AS file_path, COALESCE(an.size_bytes, 0)::bigint AS size_bytes,
       COALESCE(EXTRACT(EPOCH FROM a.created_at)::bigint, 0)::bigint AS created_at,
       COALESCE(an.access_count, 0)::bigint AS access_count,
       COALESCE(EXTRACT(EPOCH FROM an.last_accessed), 0)::bigint AS last_accessed
FROM foghorn.artifact_nodes an
JOIN foghorn.artifacts a ON a.artifact_hash = an.artifact_hash
WHERE an.is_orphaned = false
  AND a.status != 'deleted'
ORDER BY an.node_id;

-- name: ListUnemittedArtifactNodeKeys :many
SELECT artifact_hash, node_id FROM foghorn.artifact_nodes
WHERE is_orphaned = false AND last_emitted_version = 0
ORDER BY artifact_hash, node_id
LIMIT $1;

-- name: OrphanGloballyStaleArtifactNodes :many
WITH stale AS (
    SELECT artifact_hash, node_id FROM foghorn.artifact_nodes
    WHERE is_orphaned = false AND last_seen_at < NOW() - INTERVAL '15 minutes'
    ORDER BY last_seen_at
    LIMIT 500
    FOR UPDATE SKIP LOCKED
)
UPDATE foghorn.artifact_nodes an SET is_orphaned = true
FROM stale
WHERE an.artifact_hash = stale.artifact_hash AND an.node_id = stale.node_id
  AND an.is_orphaned = false
  AND an.last_seen_at < NOW() - INTERVAL '15 minutes'
RETURNING an.artifact_hash, an.node_id, an.role;

-- name: LockUnemittedArtifactNode :one
SELECT an.role, an.is_complete, COALESCE(an.size_bytes, 0)::bigint AS size_bytes,
       an.last_emitted_version, a.tenant_id::text AS tenant_id
FROM foghorn.artifact_nodes an
JOIN foghorn.artifacts a ON a.artifact_hash = an.artifact_hash
WHERE an.artifact_hash = $1 AND an.node_id = $2 AND an.is_orphaned = false
FOR UPDATE OF an SKIP LOCKED;

-- name: LockArtifactNodeRole :one
SELECT role FROM foghorn.artifact_nodes
WHERE artifact_hash = $1 AND node_id = $2
FOR UPDATE;

-- name: DeleteArtifactNode :exec
DELETE FROM foghorn.artifact_nodes WHERE artifact_hash = $1 AND node_id = $2;

-- name: OrphanNodeArtifacts :many
UPDATE foghorn.artifact_nodes
SET is_orphaned = true, last_seen_at = NOW()
WHERE node_id = $1 AND is_orphaned = false
  AND NOT (role = 'origin' AND is_complete = false)
RETURNING artifact_hash, role;

-- name: VODNeedsDtshSync :one
SELECT EXISTS (
    SELECT 1 FROM foghorn.artifacts
    WHERE artifact_hash = $1
      AND artifact_type = 'vod'
      AND sync_status = 'synced'
      AND COALESCE(dtsh_synced, false) = false
);
