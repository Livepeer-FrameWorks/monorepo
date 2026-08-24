-- name: GetExistingClipStatus :one
SELECT status
FROM foghorn.artifacts
WHERE artifact_hash = sqlc.arg(artifact_hash)
  AND artifact_type = 'clip'
  AND tenant_id = sqlc.arg(tenant_id)::uuid;

-- name: InsertQueuedClipArtifact :exec
INSERT INTO foghorn.artifacts
    (artifact_hash, artifact_type, stream_internal_name, internal_name, stream_id,
     tenant_id, user_id, status, request_id, manifest_path, format,
     origin_cluster_id, retention_until, created_at, updated_at)
VALUES (sqlc.arg(artifact_hash), 'clip', sqlc.arg(stream_internal_name), sqlc.arg(internal_name),
        NULLIF(sqlc.arg(stream_id)::text, '')::uuid, NULLIF(sqlc.arg(tenant_id)::text, '')::uuid,
        NULLIF(sqlc.arg(user_id)::text, '')::uuid, 'queued', sqlc.arg(request_id),
        sqlc.arg(manifest_path), sqlc.arg(format), sqlc.arg(origin_cluster_id),
        sqlc.arg(retention_until), NOW(), NOW());

-- name: GetClipForDeletion :one
SELECT status, size_bytes, retention_until, stream_internal_name, tenant_id, user_id,
       format, storage_cluster_id, origin_cluster_id, active_object_key,
       active_dtsh_key, sync_object_key, durable_backend_local, backend_id
FROM foghorn.artifacts
WHERE artifact_hash = sqlc.arg(artifact_hash)
  AND artifact_type = 'clip'
  AND tenant_id = sqlc.arg(tenant_id)::uuid;

-- name: LatestArtifactNodeID :one
SELECT node_id
FROM foghorn.artifact_nodes
WHERE artifact_hash = sqlc.arg(artifact_hash)
  AND NOT is_orphaned
ORDER BY last_seen_at DESC
LIMIT 1;

-- name: DeleteClipCatalog :execrows
UPDATE foghorn.artifacts
SET status = 'deleted', updated_at = NOW()
WHERE artifact_hash = sqlc.arg(artifact_hash)
  AND artifact_type = 'clip'
  AND tenant_id = sqlc.arg(tenant_id)::uuid
  AND status != 'deleted';

-- name: CancelClipProcessingJobs :exec
UPDATE foghorn.processing_jobs
SET status = 'failed', error_message = 'clip deleted', updated_at = NOW()
WHERE artifact_hash = sqlc.arg(artifact_hash)
  AND status IN ('queued', 'dispatched', 'processing');

-- name: GetDVRStatus :one
SELECT status
FROM foghorn.artifacts
WHERE artifact_hash = sqlc.arg(artifact_hash)
  AND artifact_type = 'dvr'
  AND tenant_id = sqlc.arg(tenant_id)::uuid;

-- name: GetDVRStartDispatch :one
SELECT dvr_start_dispatch::text AS dvr_start_dispatch
FROM foghorn.artifacts
WHERE artifact_hash = sqlc.arg(artifact_hash)
  AND artifact_type = 'dvr'
  AND tenant_id::text = sqlc.arg(tenant_id);

-- name: FindActiveDVRForStream :one
SELECT artifact_hash, status, dvr_start_dispatch::text AS dvr_start_dispatch
FROM foghorn.artifacts
WHERE stream_internal_name = sqlc.arg(stream_internal_name)
  AND artifact_type = 'dvr'
  AND status IN ('requested', 'starting', 'recording')
  AND tenant_id = sqlc.arg(tenant_id)::uuid
ORDER BY created_at DESC
LIMIT 1;

-- name: AcquireDVRStartLock :exec
SELECT pg_advisory_xact_lock(hashtext(sqlc.arg(lock_key))::bigint);

-- name: FindDVRStartRaceWinner :one
SELECT artifact_hash, status
FROM foghorn.artifacts
WHERE stream_internal_name = sqlc.arg(stream_internal_name)
  AND artifact_type = 'dvr'
  AND status IN ('requested', 'starting', 'recording')
  AND tenant_id = sqlc.arg(tenant_id)::uuid
  AND (
      (sqlc.arg(ingest_generation)::text <> '' AND ingest_generation = sqlc.arg(ingest_generation)::uuid)
      OR (sqlc.arg(ingest_generation)::text = '' AND dvr_start_dispatch->>'source_node_id' = sqlc.arg(source_node_id)::text)
  )
ORDER BY created_at DESC
LIMIT 1;

-- name: InsertRequestedDVRArtifact :exec
INSERT INTO foghorn.artifacts (
    artifact_hash, artifact_type, stream_internal_name, internal_name,
    stream_id, tenant_id, user_id, status, request_id, format, origin_cluster_id,
    dvr_window_seconds, dvr_chapter_mode, dvr_chapter_interval, dvr_retention_days,
    dvr_processes_json, dvr_start_dispatch, ingest_generation, created_at, updated_at
)
VALUES (
    sqlc.arg(artifact_hash), 'dvr', sqlc.arg(stream_internal_name), sqlc.arg(internal_name),
    NULLIF(sqlc.arg(stream_id)::text, '')::uuid, NULLIF(sqlc.arg(tenant_id)::text, '')::uuid,
    NULLIF(sqlc.arg(user_id)::text, '')::uuid, 'requested', sqlc.arg(request_id), 'm3u8',
    sqlc.arg(origin_cluster_id), sqlc.arg(dvr_window_seconds)::int,
    NULLIF(sqlc.arg(dvr_chapter_mode)::text, '')::text,
    NULLIF(sqlc.arg(dvr_chapter_interval)::int, 0)::int,
    NULLIF(sqlc.arg(dvr_retention_days)::int, 0)::int,
    NULLIF(sqlc.arg(dvr_processes_json)::text, '')::text,
    sqlc.arg(dispatch_json)::text::jsonb,
    NULLIF(sqlc.arg(ingest_generation)::text, '')::uuid,
    NOW(), NOW()
);

-- name: MarkDVRStarting :execrows
UPDATE foghorn.artifacts
SET status = 'starting', dvr_start_dispatch = sqlc.arg(dispatch_json)::text::jsonb, updated_at = NOW()
WHERE artifact_hash = sqlc.arg(artifact_hash)
  AND artifact_type = 'dvr'
  AND tenant_id = sqlc.arg(tenant_id)::uuid
  AND status = 'requested'
  AND NOT EXISTS (
      SELECT 1
      FROM foghorn.ingest_sessions s
      WHERE s.id = foghorn.artifacts.ingest_generation
        AND s.ended_at IS NOT NULL
  );

-- name: GetArtifactStatusForTenant :one
SELECT status
FROM foghorn.artifacts
WHERE artifact_hash = sqlc.arg(artifact_hash)
  AND tenant_id = sqlc.arg(tenant_id)::uuid;

-- name: TouchStartedDVR :execrows
UPDATE foghorn.artifacts
SET updated_at = NOW()
WHERE artifact_hash = sqlc.arg(artifact_hash)
  AND artifact_type = 'dvr'
  AND tenant_id = sqlc.arg(tenant_id)::uuid
  AND status IN ('starting', 'recording');

-- name: GetDVRForStop :one
SELECT status, COALESCE(stream_internal_name, '')::text AS stream_internal_name,
       size_bytes, retention_until, started_at, ended_at, tenant_id, user_id
FROM foghorn.artifacts
WHERE artifact_hash = sqlc.arg(artifact_hash)
  AND artifact_type = 'dvr'
  AND tenant_id = sqlc.arg(tenant_id)::uuid;

-- name: LatestArtifactNodeWithSize :one
SELECT node_id, size_bytes
FROM foghorn.artifact_nodes
WHERE artifact_hash = sqlc.arg(artifact_hash)
  AND NOT is_orphaned
ORDER BY last_seen_at DESC
LIMIT 1;

-- name: GetDVRForDeletion :one
SELECT status, COALESCE(stream_internal_name, '')::text AS stream_internal_name,
       size_bytes, retention_until, started_at, ended_at, tenant_id, user_id,
       storage_cluster_id, origin_cluster_id, active_object_key, active_dtsh_key,
       sync_object_key, durable_backend_local, backend_id
FROM foghorn.artifacts
WHERE artifact_hash = sqlc.arg(artifact_hash)
  AND artifact_type = 'dvr'
  AND tenant_id = sqlc.arg(tenant_id)::uuid;

-- name: LatestPlayableDVRChapterID :one
SELECT playback_id
FROM foghorn.dvr_chapters
WHERE artifact_hash = sqlc.arg(artifact_hash)
  AND state IN ('finalized', 'frozen', 'reclaimed')
  AND playback_id IS NOT NULL
ORDER BY end_ms DESC
LIMIT 1;

-- name: GetExistingVodUpload :one
SELECT m.s3_upload_id, m.s3_key, m.total_parts, m.upload_expires_at,
       COALESCE(a.backend_id, '')::text AS backend_id
FROM foghorn.artifacts a
JOIN foghorn.vod_metadata m ON m.artifact_hash = a.artifact_hash
WHERE a.artifact_hash = sqlc.arg(artifact_hash)
  AND a.artifact_type = 'vod'
  AND a.tenant_id = sqlc.arg(tenant_id)::uuid;

-- name: InsertUploadingVodArtifact :exec
INSERT INTO foghorn.artifacts (
    artifact_hash, artifact_type, internal_name, tenant_id, user_id, status,
    sync_status, size_bytes, s3_url, format, origin_cluster_id, storage_cluster_id,
    retention_until, backend_id, created_at, updated_at
)
VALUES (
    sqlc.arg(artifact_hash), 'vod', sqlc.arg(internal_name),
    NULLIF(sqlc.arg(tenant_id)::text, '')::uuid,
    NULLIF(sqlc.arg(user_id)::text, '')::uuid,
    'uploading', 'in_progress', sqlc.arg(size_bytes), sqlc.arg(s3_url),
    sqlc.arg(format), sqlc.arg(origin_cluster_id), sqlc.arg(storage_cluster_id),
    sqlc.arg(retention_until), sqlc.arg(backend_id), NOW(), NOW()
);

-- name: InsertVodMultipartMetadata :exec
INSERT INTO foghorn.vod_metadata (
    artifact_hash, filename, title, description, content_type,
    s3_upload_id, s3_key, upload_expires_at, total_parts, created_at, updated_at
)
VALUES (
    sqlc.arg(artifact_hash), sqlc.arg(filename), sqlc.arg(title), sqlc.arg(description),
    sqlc.arg(content_type), sqlc.arg(s3_upload_id), sqlc.arg(s3_key),
    sqlc.arg(upload_expires_at), sqlc.arg(total_parts), NOW(), NOW()
);

-- name: GetVodUploadStatusRow :one
SELECT v.artifact_hash, COALESCE(v.s3_key, '')::text AS s3_key, a.status,
       a.error_message, a.retention_until, v.upload_expires_at, v.total_parts,
       COALESCE(a.backend_id, '')::text AS backend_id
FROM foghorn.vod_metadata v
JOIN foghorn.artifacts a ON v.artifact_hash = a.artifact_hash
WHERE v.s3_upload_id = sqlc.arg(s3_upload_id)
  AND a.tenant_id = sqlc.arg(tenant_id)::uuid;

-- name: GetCompletableVodUpload :one
SELECT v.artifact_hash, v.s3_key, a.size_bytes, a.user_id, a.status
FROM foghorn.vod_metadata v
JOIN foghorn.artifacts a ON v.artifact_hash = a.artifact_hash
WHERE v.s3_upload_id = sqlc.arg(s3_upload_id)
  AND a.tenant_id = sqlc.arg(tenant_id)::uuid
  AND a.status IN ('uploading', 'completing', 'processing');

-- name: ClaimVodCompletion :execrows
UPDATE foghorn.artifacts AS a
SET status = 'completing', last_sync_attempt = NOW(), updated_at = NOW()
WHERE a.artifact_hash = sqlc.arg(artifact_hash)
  AND a.tenant_id = sqlc.arg(tenant_id)::uuid
  AND a.status = 'uploading'
  AND a.artifact_hash IN (
      SELECT vm.artifact_hash
      FROM foghorn.vod_metadata vm
      WHERE vm.s3_upload_id = sqlc.arg(s3_upload_id)
  );

-- name: PersistVodCompletionContract :exec
UPDATE foghorn.vod_metadata
SET processes_json = COALESCE(NULLIF(sqlc.arg(processes_json)::text, ''), processes_json),
    vod_completion_descriptor = sqlc.arg(completion_descriptor)::text::jsonb,
    updated_at = NOW()
WHERE artifact_hash = sqlc.arg(artifact_hash)
  AND s3_upload_id = sqlc.arg(s3_upload_id);

-- name: GetVodCompletionContract :one
SELECT COALESCE(v.vod_completion_descriptor::text, '')::text AS completion_descriptor,
       COALESCE(v.processes_json, '')::text AS processes_json,
       COALESCE(a.backend_id, '')::text AS backend_id
FROM foghorn.vod_metadata v
JOIN foghorn.artifacts a ON a.artifact_hash = v.artifact_hash
WHERE v.artifact_hash = sqlc.arg(artifact_hash);

-- name: FailVodCompletion :execrows
UPDATE foghorn.artifacts AS a
SET status = 'failed', sync_status = 'failed', sync_error = sqlc.arg(error_message),
    error_message = sqlc.arg(error_message), last_sync_attempt = NOW(), updated_at = NOW()
WHERE artifact_hash = sqlc.arg(artifact_hash)
  AND tenant_id = sqlc.arg(tenant_id)::uuid
  AND status NOT IN ('deleted', 'ready', 'failed', 'processing');

-- name: AdvanceVodToProcessing :execrows
UPDATE foghorn.artifacts AS a
SET status = 'processing',
    storage_location = 's3',
    sync_status = 'synced',
    sync_error = NULL,
    last_sync_attempt = NOW(),
    frozen_at = COALESCE(frozen_at, NOW()),
    s3_url = COALESCE(s3_url, sqlc.arg(s3_url)),
    durable_backend_local = true,
    active_object_key = COALESCE(active_object_key, (
        SELECT s3_key
        FROM foghorn.vod_metadata
        WHERE artifact_hash = a.artifact_hash
    )),
    updated_at = NOW()
WHERE a.artifact_hash = sqlc.arg(artifact_hash)
  AND a.tenant_id = sqlc.arg(tenant_id)::uuid
  AND a.status = 'completing'
  AND a.artifact_hash IN (
      SELECT vm.artifact_hash
      FROM foghorn.vod_metadata vm
      WHERE vm.s3_upload_id = sqlc.arg(s3_upload_id)
  );

-- name: GetAbortableVodUpload :one
SELECT v.artifact_hash, v.s3_key, a.user_id, COALESCE(a.backend_id, '')::text AS backend_id
FROM foghorn.vod_metadata v
JOIN foghorn.artifacts a ON v.artifact_hash = a.artifact_hash
WHERE v.s3_upload_id = sqlc.arg(s3_upload_id)
  AND a.status = 'uploading'
  AND a.tenant_id = sqlc.arg(tenant_id)::uuid;

-- name: ClaimVodAbort :execrows
UPDATE foghorn.artifacts
SET status = 'aborting', updated_at = NOW()
WHERE artifact_hash = sqlc.arg(artifact_hash)
  AND tenant_id = sqlc.arg(tenant_id)::uuid
  AND status = 'uploading';

-- name: FinalizeVodAbort :execrows
UPDATE foghorn.artifacts
SET status = 'deleted', updated_at = NOW()
WHERE artifact_hash = sqlc.arg(artifact_hash)
  AND tenant_id = sqlc.arg(tenant_id)::uuid
  AND status = 'aborting';

-- name: DeleteVodMetadata :exec
DELETE FROM foghorn.vod_metadata
WHERE artifact_hash = sqlc.arg(artifact_hash);

-- name: GetVodForDeletion :one
SELECT a.status, COALESCE(v.s3_key, '')::text AS s3_key, a.s3_url, a.format,
       a.size_bytes, a.retention_until, a.user_id, a.storage_cluster_id,
       a.origin_cluster_id, COALESCE(a.origin_type, '')::text AS origin_type,
       a.active_object_key, a.active_dtsh_key, a.sync_object_key,
       a.durable_backend_local, a.backend_id
FROM foghorn.artifacts a
LEFT JOIN foghorn.vod_metadata v ON a.artifact_hash = v.artifact_hash
WHERE a.artifact_hash = sqlc.arg(artifact_hash)
  AND a.artifact_type = 'vod'
  AND a.tenant_id = sqlc.arg(tenant_id)::uuid;

-- name: SoftDeleteVodArtifact :execrows
UPDATE foghorn.artifacts
SET status = 'deleted', sync_request_id = NULL, sync_node_id = NULL, updated_at = NOW()
WHERE artifact_hash = sqlc.arg(artifact_hash)
  AND artifact_type = 'vod'
  AND tenant_id = sqlc.arg(tenant_id)::uuid
  AND status != 'deleted';

-- name: ListArtifactNodeIDs :many
SELECT node_id
FROM foghorn.artifact_nodes
WHERE artifact_hash = sqlc.arg(artifact_hash)
  AND NOT is_orphaned;

-- name: GetVodAsset :one
SELECT a.artifact_hash AS id, a.artifact_hash, a.status, a.size_bytes,
       COALESCE(a.storage_location, 'pending')::text AS storage_location,
       COALESCE(a.s3_url, '')::text AS s3_url,
       a.error_message, a.created_at, a.updated_at, a.retention_until,
       COALESCE(v.filename, '')::text AS filename,
       COALESCE(v.title, '')::text AS title,
       COALESCE(v.description, '')::text AS description,
       v.duration_ms, v.resolution, v.video_codec, v.audio_codec, v.bitrate_kbps,
       COALESCE(v.s3_upload_id, '')::text AS s3_upload_id,
       COALESCE(v.s3_key, '')::text AS s3_key
FROM foghorn.artifacts a
LEFT JOIN foghorn.vod_metadata v ON a.artifact_hash = v.artifact_hash
WHERE a.artifact_hash = sqlc.arg(artifact_hash)
  AND a.artifact_type = 'vod'
  AND a.status != 'deleted';

-- name: ListTenantArtifactSessionNames :many
SELECT internal_name
FROM foghorn.artifacts
WHERE tenant_id = sqlc.arg(tenant_id)::uuid
  AND status != 'deleted'
  AND COALESCE(internal_name, '') != '';

-- name: ListTenantArtifactNodes :many
SELECT DISTINCT an.node_id
FROM foghorn.artifacts a
JOIN foghorn.artifact_nodes an ON an.artifact_hash = a.artifact_hash
WHERE a.artifact_hash = sqlc.arg(artifact_hash)
  AND a.tenant_id = sqlc.arg(tenant_id)::uuid
  AND a.status != 'deleted'
  AND COALESCE(an.is_orphaned, false) = false;

-- name: FindArtifactHashForSession :one
SELECT artifact_hash
FROM foghorn.artifacts
WHERE internal_name = sqlc.arg(internal_name)
  AND tenant_id = sqlc.arg(tenant_id)::uuid
  AND status != 'deleted'
LIMIT 1;

-- name: ListNodeComponentVersions :many
SELECT component, COALESCE(current_version, '')::text AS current_version
FROM foghorn.node_components
WHERE node_id = sqlc.arg(node_id)
ORDER BY component;

-- name: SumTenantActiveArtifactBytes :one
SELECT COALESCE(SUM(size_bytes), 0)::bigint AS total_bytes
FROM foghorn.artifacts
WHERE tenant_id = sqlc.arg(tenant_id)::uuid
  AND status NOT IN ('failed', 'expired', 'deleted', 'aborted');
