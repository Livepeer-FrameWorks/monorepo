-- name: ClaimDVRFinalization :one
UPDATE foghorn.artifacts
SET status = 'finalizing', updated_at = NOW(), ended_at = COALESCE(ended_at, NOW())
WHERE artifact_hash = sqlc.arg(artifact_hash) AND artifact_type = 'dvr'
  AND (status IN ('requested', 'starting', 'recording', 'stopping')
       OR (status = 'finalizing' AND updated_at < NOW() - (sqlc.arg(stale_seconds)::double precision * INTERVAL '1 second')))
RETURNING status, tenant_id::text AS tenant_id;

-- name: CompleteDVRFinalization :execrows
UPDATE foghorn.artifacts
SET status = sqlc.arg(final_status)::text,
    size_bytes = COALESCE(NULLIF(sqlc.arg(size_bytes)::bigint, 0), size_bytes),
    duration_seconds = COALESCE(NULLIF(sqlc.arg(duration_seconds)::bigint, 0)::int, duration_seconds),
    retention_until = sqlc.narg(retention_until), updated_at = NOW(),
    ended_at = COALESCE(ended_at, sqlc.arg(ended_at)),
    dvr_start_dispatch = CASE
        WHEN sqlc.arg(retain_stop_obligation)::boolean THEN dvr_start_dispatch
        WHEN COALESCE(dvr_start_dispatch->>'node_id', '') <> ''
            THEN jsonb_build_object('node_id', dvr_start_dispatch->>'node_id')
        ELSE NULL END
WHERE artifact_hash = sqlc.arg(artifact_hash) AND artifact_type = 'dvr'
  AND status = 'finalizing' AND tenant_id::text = sqlc.arg(tenant_id);

-- name: GetDVRLifecycleContext :one
SELECT COALESCE(tenant_id::text, '')::text AS tenant_id,
       COALESCE(user_id::text, '')::text AS user_id,
       COALESCE(stream_id::text, '')::text AS stream_id,
       stream_internal_name, retention_until, started_at
FROM foghorn.artifacts
WHERE artifact_hash = $1 AND artifact_type = 'dvr';

-- name: CountDVRFinalSegments :one
SELECT COUNT(*) FILTER (WHERE status IN ('uploaded', 'deleted_local'))::integer AS uploaded_count,
       COUNT(*) FILTER (WHERE status = 'lost_local')::integer AS lost_count
FROM foghorn.dvr_segments
WHERE artifact_hash = $1;

-- name: GetDVRRetentionDays :one
SELECT dvr_retention_days FROM foghorn.artifacts
WHERE artifact_hash = $1 AND artifact_type = 'dvr';

-- name: FailDVRFinalization :execrows
UPDATE foghorn.artifacts
SET status = 'failed', error_message = sqlc.arg(error_message)::text,
    retention_until = sqlc.narg(retention_until), updated_at = NOW(),
    ended_at = COALESCE(ended_at, sqlc.arg(ended_at)),
    dvr_start_dispatch = CASE
        WHEN sqlc.arg(retain_stop_obligation)::boolean THEN dvr_start_dispatch
        WHEN COALESCE(dvr_start_dispatch->>'node_id', '') <> ''
            THEN jsonb_build_object('node_id', dvr_start_dispatch->>'node_id')
        ELSE NULL END
WHERE artifact_hash = sqlc.arg(artifact_hash) AND artifact_type = 'dvr'
  AND status = 'finalizing' AND tenant_id::text = sqlc.arg(tenant_id);

-- name: GetDVRRetentionUntil :one
SELECT retention_until FROM foghorn.artifacts
WHERE artifact_hash = $1 AND artifact_type = 'dvr';

-- name: ClearDVRStopObligation :exec
UPDATE foghorn.artifacts
SET dvr_start_dispatch = CASE
        WHEN COALESCE(dvr_start_dispatch->>'node_id', '') <> ''
            THEN jsonb_build_object('node_id', dvr_start_dispatch->>'node_id')
        ELSE NULL END,
    updated_at = NOW()
WHERE artifact_hash = $1 AND tenant_id::text = $2 AND artifact_type = 'dvr'
  AND dvr_start_dispatch ? 'state'
  AND status NOT IN ('requested', 'starting', 'recording', 'stopping', 'finalizing');

-- name: GetDVROwnerTenant :one
SELECT tenant_id::text FROM foghorn.artifacts
WHERE artifact_hash = $1 AND artifact_type = 'dvr';

-- name: GetDVRReportAuthorization :one
SELECT status, COALESCE(dvr_start_dispatch->>'node_id', '')::text AS dispatch_node
FROM foghorn.artifacts
WHERE artifact_hash = sqlc.arg(artifact_hash) AND artifact_type = 'dvr' AND tenant_id = sqlc.arg(tenant_id)::uuid;

-- name: LockDVRDispatchOwner :one
SELECT COALESCE(dvr_start_dispatch->>'node_id', '')::text AS dispatch_node
FROM foghorn.artifacts
WHERE artifact_hash = sqlc.arg(artifact_hash) AND artifact_type = 'dvr' AND tenant_id = sqlc.arg(tenant_id)::uuid
FOR UPDATE;

-- name: GetUniqueDVRRecordingOrigin :one
SELECT CASE WHEN COUNT(*) = 1 THEN MAX(an.node_id) ELSE '' END::text
FROM foghorn.artifact_nodes an
WHERE an.artifact_hash = $1 AND an.role = 'origin' AND an.is_orphaned = false AND an.node_id <> ''
  AND EXISTS (
      SELECT 1 FROM foghorn.artifacts a
      WHERE a.artifact_hash = an.artifact_hash AND a.tenant_id::text = $2
  );

-- name: BindDVRDispatchOwner :execrows
UPDATE foghorn.artifacts
SET dvr_start_dispatch = jsonb_set(COALESCE(dvr_start_dispatch, '{}'::jsonb), '{node_id}', to_jsonb(sqlc.arg(dispatch_node)::text), true),
    updated_at = NOW()
WHERE artifact_hash = sqlc.arg(artifact_hash) AND artifact_type = 'dvr'
  AND tenant_id = sqlc.arg(tenant_id) AND COALESCE(dvr_start_dispatch->>'node_id', '') = '';

-- name: GetDVRDispatchOwner :one
SELECT COALESCE(dvr_start_dispatch->>'node_id', '')::text
FROM foghorn.artifacts
WHERE artifact_hash = $1 AND artifact_type = 'dvr' AND tenant_id::text = $2;

-- name: GetDVRArtifactStatus :one
SELECT status FROM foghorn.artifacts WHERE artifact_hash = $1 AND artifact_type = 'dvr';

-- name: GetArtifactTenantText :one
SELECT COALESCE(tenant_id::text, '')::text FROM foghorn.artifacts WHERE artifact_hash = $1;
