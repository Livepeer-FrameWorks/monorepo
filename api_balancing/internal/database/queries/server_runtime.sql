-- name: RefreshDVRArtifactNodeProgress :exec
UPDATE foghorn.artifact_nodes SET last_seen_at = NOW(), is_orphaned = false, segment_count = GREATEST(COALESCE(segment_count, 0), $3), size_bytes = GREATEST(COALESCE(size_bytes, 0), $4)
WHERE artifact_hash = $1 AND node_id = $2;
-- name: GetPersistedNodeOutputs :one
SELECT COALESCE(base_url, '') AS base_url, COALESCE(outputs, '{}'::jsonb)::text AS outputs_json FROM foghorn.node_outputs WHERE node_id = $1;
-- name: GetNodeComponentVersion :one
SELECT COALESCE(current_version, '') FROM foghorn.node_components WHERE node_id = $1 AND component = $2;
-- name: GetFreezeArtifactMetadata :one
SELECT stream_internal_name, origin_cluster_id, storage_cluster_id, sync_status, COALESCE(format, '') AS format FROM foghorn.artifacts
WHERE artifact_hash = $1 AND artifact_type = $2 AND tenant_id::text = $3;
-- name: NodeHoldsLiveArtifactCopy :one
SELECT EXISTS (SELECT 1 FROM foghorn.artifact_nodes an JOIN foghorn.artifacts a ON a.artifact_hash = an.artifact_hash
WHERE an.artifact_hash = $1 AND an.node_id = $2 AND a.tenant_id::text = $3 AND an.is_complete = true AND an.is_orphaned = false AND a.status<>'deleted');
-- name: ClaimFreezeAttempt :execrows
UPDATE foghorn.artifacts SET storage_location = 'freezing', sync_status = 'in_progress', sync_request_id = sqlc.narg(sync_request_id), sync_node_id = sqlc.narg(sync_node_id),
storage_cluster_id = NULLIF(sqlc.arg(storage_cluster_id)::text, ''), sync_object_key = sqlc.narg(sync_object_key), durable_backend_local = true, backend_id = sqlc.narg(backend_id), last_sync_attempt = NOW(), updated_at = NOW()
WHERE foghorn.artifacts.artifact_hash = sqlc.arg(artifact_hash) AND foghorn.artifacts.tenant_id::text = sqlc.arg(tenant_id) AND foghorn.artifacts.status = 'ready' AND EXISTS (SELECT 1 FROM foghorn.artifact_nodes an WHERE an.artifact_hash = sqlc.arg(artifact_hash) AND an.node_id = sqlc.narg(sync_node_id) AND an.is_complete = true AND an.is_orphaned = false)
AND (foghorn.artifacts.sync_status IN ('pending', 'failed', 'synced') OR (foghorn.artifacts.sync_status = 'in_progress' AND foghorn.artifacts.sync_request_id = sqlc.narg(sync_request_id) AND foghorn.artifacts.sync_node_id = sqlc.narg(sync_node_id) AND foghorn.artifacts.sync_object_key = sqlc.narg(sync_object_key) AND foghorn.artifacts.storage_cluster_id IS NOT DISTINCT FROM NULLIF(sqlc.arg(storage_cluster_id)::text, '')));
-- name: ClaimDtshAttempt :one
WITH prev AS (SELECT COALESCE(sync_object_key, '') AS object_key, COALESCE(dtsh_sync_request_id, '') AS old_request FROM foghorn.artifacts WHERE artifact_hash = $1 AND tenant_id::text = $4 FOR UPDATE)
UPDATE foghorn.artifacts a SET dtsh_status = 'in_progress', dtsh_sync_request_id = $2, dtsh_sync_node_id = $3, dtsh_last_attempt = NOW(), updated_at = NOW() FROM prev
WHERE a.artifact_hash = $1 AND a.tenant_id::text = $4 AND a.artifact_type IN ('clip', 'vod') AND a.status = 'ready' AND a.sync_status = 'synced' AND a.dtsh_synced = false
AND EXISTS (SELECT 1 FROM foghorn.artifact_nodes an WHERE an.artifact_hash = $1 AND an.node_id = $3 AND an.is_complete = true AND an.is_orphaned = false)
AND (a.dtsh_status IS NULL OR (a.dtsh_sync_request_id = $2 AND a.dtsh_sync_node_id = $3) OR (a.dtsh_status = 'failed' AND a.dtsh_last_attempt<NOW()-(LEAST(a.dtsh_failure_count, 20)*INTERVAL '30 seconds')) OR (a.dtsh_status = 'in_progress' AND a.dtsh_last_attempt<NOW()-INTERVAL '10 minutes'))
RETURNING prev.object_key, prev.old_request;
-- name: ClearDtshAttempt :one
UPDATE foghorn.artifacts SET dtsh_status = 'failed', dtsh_failure_count = dtsh_failure_count+1, dtsh_sync_request_id = NULL, dtsh_sync_node_id = NULL, updated_at = NOW()
WHERE artifact_hash = $1 AND dtsh_sync_request_id = $2 AND dtsh_sync_node_id = $3 AND tenant_id::text = $4 RETURNING COALESCE(sync_object_key, '');
-- name: FailDtshAttempt :one
UPDATE foghorn.artifacts SET dtsh_status = 'failed', dtsh_failure_count = dtsh_failure_count+1, sync_error = NULLIF(sqlc.arg(error_message)::text, ''), dtsh_sync_request_id = NULL, dtsh_sync_node_id = NULL, dtsh_last_attempt = NOW(), updated_at = NOW()
WHERE artifact_hash = sqlc.arg(artifact_hash) AND dtsh_sync_request_id = sqlc.narg(dtsh_sync_request_id) AND dtsh_sync_node_id = sqlc.narg(dtsh_sync_node_id) AND status NOT IN ('deleted', 'expired', 'aborted') AND tenant_id::text = sqlc.arg(tenant_id) RETURNING COALESCE(sync_object_key, '');
-- name: UpdateProcessingJobCache :execrows
UPDATE foghorn.processing_jobs SET processes_json = $3, updated_at = NOW() WHERE job_id = $1 AND artifact_hash = $2 AND processing_node_id = $4 AND status IN ('dispatched', 'processing');
-- name: LockProcessingJobForCompletion :one
SELECT status, COALESCE(processing_node_id, '') AS processing_node_id FROM foghorn.processing_jobs WHERE job_id = $1 FOR UPDATE;
-- name: LockProcessingArtifactForCompletion :one
SELECT a.artifact_hash, COALESCE(a.artifact_type, '')::text AS artifact_type, COALESCE(a.tenant_id::text, '')::text AS tenant_id, COALESCE(a.stream_id::text, '')::text AS stream_id, COALESCE(a.stream_internal_name, '')::text AS stream_internal_name, COALESCE(a.s3_url, '')::text AS s3_url, COALESCE(a.format, '')::text AS format,
COALESCE((pj.source_params->>'source_start_unix')::bigint, 0)::bigint AS requested_start_unix, COALESCE((pj.source_params->>'source_stop_unix')::bigint, 0)::bigint AS requested_stop_unix
FROM foghorn.processing_jobs pj JOIN foghorn.artifacts a ON pj.artifact_hash = a.artifact_hash WHERE pj.job_id = $1 FOR UPDATE OF a;
-- name: MarkProcessingArtifactReady :execrows
UPDATE foghorn.artifacts SET format = sqlc.narg(format), size_bytes = sqlc.narg(size_bytes), duration_seconds = CASE WHEN sqlc.arg(duration_ms)::bigint>0 THEN (sqlc.arg(duration_ms)::bigint/1000)::int ELSE duration_seconds END,
duration_ms = CASE WHEN sqlc.arg(duration_ms)::bigint>0 THEN sqlc.arg(duration_ms)::bigint ELSE duration_ms END, tracks = CASE WHEN sqlc.arg(tracks_present)::boolean THEN sqlc.arg(tracks)::jsonb ELSE tracks END,
status = CASE WHEN artifact_type IN ('clip', 'vod') THEN 'ready' ELSE status END, sync_status = 'pending', storage_location = 'local', updated_at = NOW()
WHERE artifact_hash = sqlc.arg(artifact_hash) AND status NOT IN ('ready', 'failed', 'deleted', 'expired', 'aborted');
-- name: UpdateCompletedVODMetadata :exec
UPDATE foghorn.vod_metadata SET duration_ms = NULLIF(sqlc.narg(duration_ms)::text, '')::integer, resolution = sqlc.narg(resolution), video_codec = sqlc.narg(video_codec), audio_codec = sqlc.narg(audio_codec),
bitrate_kbps = NULLIF(sqlc.narg(bitrate_kbps)::text, '')::integer, width = NULLIF(sqlc.narg(width)::text, '')::integer, height = NULLIF(sqlc.narg(height)::text, '')::integer,
fps = NULLIF(sqlc.narg(fps)::text, '')::real, audio_channels = NULLIF(sqlc.narg(audio_channels)::text, '')::integer, audio_sample_rate = NULLIF(sqlc.narg(audio_sample_rate)::text, '')::integer,
updated_at = NOW() WHERE artifact_hash = sqlc.arg(artifact_hash);
-- name: CompleteProcessingJob :exec
UPDATE foghorn.processing_jobs SET status = 'completed', progress = 100, output_metadata = sqlc.narg(output_metadata), completed_at = NOW(), updated_at = NOW() WHERE job_id = $1;
-- name: LockProcessingJobForFailure :one
SELECT pj.status, COALESCE(pj.processing_node_id, '')::text AS processing_node_id, COALESCE(a.artifact_hash, '')::text AS artifact_hash, COALESCE(a.artifact_type, '')::text AS artifact_type, COALESCE(a.tenant_id::text, '')::text AS tenant_id, COALESCE(a.stream_id::text, '')::text AS stream_id, COALESCE(a.stream_internal_name, '')::text AS stream_internal_name
FROM foghorn.processing_jobs pj LEFT JOIN foghorn.artifacts a ON pj.artifact_hash = a.artifact_hash WHERE pj.job_id = $1 FOR UPDATE OF pj;
-- name: MarkProcessingJobFailed :exec
UPDATE foghorn.processing_jobs SET status = 'failed', error_message = $2, completed_at = NOW(), updated_at = NOW() WHERE job_id = $1;
-- name: MarkProcessingArtifactFailed :execrows
UPDATE foghorn.artifacts SET status = 'failed', error_message = $2, updated_at = NOW() WHERE artifact_hash = $1 AND tenant_id::text = $3 AND status NOT IN ('ready', 'failed', 'deleted', 'expired', 'aborted');
-- name: UpdateProcessingJobProgress :one
UPDATE foghorn.processing_jobs SET progress = GREATEST(progress, $2), updated_at = NOW() WHERE job_id = $1 AND status IN ('dispatched', 'processing') AND processing_node_id = $3 RETURNING artifact_hash, tenant_id::text, progress;
-- name: GetProcessingArtifactLifecycle :one
SELECT COALESCE(artifact_type, '')::text AS artifact_type, COALESCE(stream_id::text, '')::text AS stream_id, COALESCE(stream_internal_name, '')::text AS stream_internal_name FROM foghorn.artifacts WHERE artifact_hash = $1;
-- name: UpdateChapterFinalizeProgress :one
UPDATE foghorn.dvr_chapters c SET finalize_started_at = NOW() FROM foghorn.artifacts a
WHERE c.chapter_id = sqlc.arg(chapter_id)
  AND c.playback_artifact_hash = a.artifact_hash
  AND c.state = 'finalizing'
  AND c.finalize_node_id = sqlc.arg(finalize_node_id)
  AND c.finalize_attempts = sqlc.arg(expected_attempt)
RETURNING c.playback_artifact_hash, a.tenant_id::text;
-- name: GetArtifactSyncObjectKey :one
SELECT sync_object_key FROM foghorn.artifacts WHERE artifact_hash = $1 AND tenant_id::text = $2;
-- name: DtshSyncedForArtifact :one
SELECT COALESCE(dtsh_synced, false) FROM foghorn.artifacts WHERE artifact_hash = $1;
-- name: ThumbnailResourceProducedByNode :one
SELECT EXISTS (SELECT 1 FROM foghorn.artifact_nodes an JOIN foghorn.artifacts a ON a.artifact_hash = an.artifact_hash WHERE an.artifact_hash = $1 AND an.node_id = $2 AND an.is_complete = true AND an.is_orphaned = false AND a.tenant_id::text = $3)
OR EXISTS (SELECT 1 FROM foghorn.processing_jobs WHERE artifact_hash = $1 AND processing_node_id = $2 AND tenant_id::text = $3 AND status IN ('dispatched', 'processing'));
-- name: ResolveThumbnailVODArtifact :one
SELECT artifact_hash, tenant_id::text, COALESCE(storage_cluster_id, origin_cluster_id) AS cluster_id FROM foghorn.artifacts WHERE internal_name = $1;
-- name: ResolveThumbnailProcessingArtifact :one
SELECT tenant_id::text, COALESCE(NULLIF(storage_cluster_id, ''), NULLIF(origin_cluster_id, ''))::text AS cluster_id, artifact_type FROM foghorn.artifacts WHERE artifact_hash = $1 AND artifact_type IN ('clip', 'vod', 'dvr');
-- name: GetArtifactThumbnailMarkContext :one
SELECT tenant_id::text, artifact_type, storage_cluster_id, origin_cluster_id, COALESCE(has_thumbnails, false) FROM foghorn.artifacts WHERE artifact_hash = $1;
-- name: MarkArtifactThumbnailPresent :exec
UPDATE foghorn.artifacts SET has_thumbnails = true, updated_at = NOW() WHERE artifact_hash = $1 AND has_thumbnails IS DISTINCT FROM true;
-- name: BackfillArtifactOriginCluster :exec
UPDATE foghorn.artifacts SET origin_cluster_id = $2, updated_at = NOW() WHERE artifact_hash = $1 AND origin_cluster_id IS NULL;
-- name: GetSyncCompletionAttempt :one
SELECT COALESCE(artifact_type, '')::text AS artifact_type, COALESCE(stream_internal_name, '')::text AS stream_internal_name, COALESCE(format, '')::text AS format,
COALESCE(tenant_id::text, '')::text AS tenant_id, COALESCE(stream_id::text, '')::text AS stream_id, COALESCE(sync_object_key, '')::text AS sync_object_key,
COALESCE(sync_status, '')::text AS sync_status, COALESCE(active_object_key, '')::text AS active_object_key, COALESCE(active_dtsh_key, '')::text AS active_dtsh_key,
COALESCE(s3_url, '')::text AS s3_url, COALESCE(dtsh_sync_request_id, '')::text AS dtsh_sync_request_id
FROM foghorn.artifacts WHERE artifact_hash = $1 AND tenant_id::text = $2 AND ((sync_request_id = $3 AND sync_node_id = $4) OR (dtsh_sync_request_id = $3 AND dtsh_sync_node_id = $4));
-- name: CompleteMainArtifactSync :execrows
UPDATE foghorn.artifacts SET storage_location = 'local', sync_status = 'synced', s3_url = COALESCE(NULLIF(sqlc.arg(s3_url)::text, ''), s3_url), active_object_key = COALESCE(NULLIF(sqlc.arg(active_object_key)::text, ''), active_object_key), dtsh_synced = sqlc.narg(dtsh_synced),
active_dtsh_key = CASE WHEN sqlc.narg(dtsh_synced) THEN NULLIF(sqlc.arg(active_dtsh_key)::text, '') ELSE NULL END, dtsh_status = CASE WHEN sqlc.narg(dtsh_synced) THEN NULL ELSE dtsh_status END,
dtsh_sync_request_id = CASE WHEN sqlc.narg(dtsh_synced) THEN NULL ELSE dtsh_sync_request_id END, dtsh_sync_node_id = CASE WHEN sqlc.narg(dtsh_synced) THEN NULL ELSE dtsh_sync_node_id END,
dtsh_failure_count = CASE WHEN sqlc.narg(dtsh_synced) THEN 0 ELSE dtsh_failure_count END, size_bytes = COALESCE(NULLIF(sqlc.arg(size_bytes)::bigint, 0), size_bytes), last_sync_attempt = NOW(), sync_error = NULL,
sync_request_id = NULL, sync_node_id = NULL, updated_at = NOW() WHERE artifact_hash = sqlc.arg(artifact_hash) AND tenant_id::text = sqlc.arg(tenant_id) AND status NOT IN ('deleted', 'expired', 'aborted')
AND sync_status = 'in_progress' AND sync_request_id = sqlc.narg(sync_request_id) AND sync_node_id = sqlc.narg(sync_node_id) AND (sqlc.arg(sync_object_key)::text = '' OR sync_object_key = sqlc.arg(sync_object_key)::text);
-- name: CompleteIncrementalDtshSync :execrows
UPDATE foghorn.artifacts SET dtsh_synced = true, dtsh_status = NULL, dtsh_failure_count = 0, active_dtsh_key = COALESCE(NULLIF(sqlc.arg(active_dtsh_key)::text, ''), active_dtsh_key), dtsh_sync_request_id = NULL, dtsh_sync_node_id = NULL, updated_at = NOW()
WHERE artifact_hash = sqlc.arg(artifact_hash) AND sync_status = 'synced' AND dtsh_synced = false AND status NOT IN ('deleted', 'expired', 'aborted') AND dtsh_sync_request_id = sqlc.narg(dtsh_sync_request_id) AND dtsh_sync_node_id = sqlc.narg(dtsh_sync_node_id) AND tenant_id::text = sqlc.arg(tenant_id);
-- name: UpsertSyncedVODObjectKey :exec
INSERT INTO foghorn.vod_metadata(artifact_hash, s3_key, filename) VALUES($1, $2, $3) ON CONFLICT(artifact_hash) DO UPDATE SET s3_key = EXCLUDED.s3_key;
-- name: LockSyncFailureAttempt :one
SELECT COALESCE(tenant_id::text, '')::text AS tenant_id, COALESCE(sync_object_key, '')::text AS sync_object_key FROM foghorn.artifacts
WHERE artifact_hash = $1 AND tenant_id::text = $4 AND status NOT IN ('deleted', 'expired', 'aborted') AND sync_status = 'in_progress' AND sync_request_id = $2 AND sync_node_id = $3 FOR UPDATE;
-- name: CountOtherCompleteArtifactCopies :one
SELECT count(*) FROM foghorn.artifact_nodes WHERE artifact_hash = $1 AND node_id<>$2 AND is_orphaned = false AND is_complete = true;
-- name: FailMainArtifactSync :exec
UPDATE foghorn.artifacts SET storage_location = 'local', sync_status = sqlc.arg(sync_status)::text, status = CASE WHEN sqlc.arg(sync_status)::text = 'lost_local' THEN 'failed' ELSE status END,
sync_error = NULLIF(sqlc.arg(error_message)::text, ''), last_sync_attempt = NOW(), failure_count = CASE WHEN sqlc.arg(sync_status)::text = 'failed' THEN failure_count+1 ELSE failure_count END,
sync_request_id = NULL, sync_node_id = NULL, updated_at = NOW() WHERE artifact_hash = sqlc.arg(artifact_hash) AND tenant_id::text = sqlc.arg(tenant_id);
