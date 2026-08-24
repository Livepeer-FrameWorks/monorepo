-- name: LockThumbnailParentTerminal :one
SELECT status IN ('deleted', 'failed', 'expired', 'aborted') FROM foghorn.artifacts WHERE artifact_hash = $1 FOR UPDATE;
-- name: InsertThumbnailAssignment :exec
INSERT INTO foghorn.thumbnail_task_assignment(attempt_id, tenant_id, asset_key, node_id, destination_cluster, status, version, expiry, durable_backend_local, backend_id)
VALUES($1, $2, $3, $4, $5, 'assigned', $1, $6, true, $7);
-- name: InsertThumbnailTaskObject :exec
INSERT INTO foghorn.thumbnail_task_object(attempt_id, file_name, staging_key) VALUES($1, $2, $3);
-- name: GetThumbnailAssignment :one
SELECT attempt_id, tenant_id, asset_key, node_id, destination_cluster, status, version, expiry FROM foghorn.thumbnail_task_assignment WHERE attempt_id = $1;
-- name: ListThumbnailObjects :many
SELECT file_name, staging_key, version_key, etag, size_bytes, verified FROM foghorn.thumbnail_task_object WHERE attempt_id = $1 ORDER BY file_name;
-- name: VerifyThumbnailObject :execrows
UPDATE foghorn.thumbnail_task_object o SET version_key = $3, etag = $4, size_bytes = $5, verified = true
WHERE o.attempt_id = $1 AND o.file_name = $2 AND EXISTS (SELECT 1 FROM foghorn.thumbnail_task_assignment a WHERE a.attempt_id = o.attempt_id
AND a.status IN ('assigned', 'uploading', 'verifying', 'publishing') AND a.expiry>NOW() AND a.publish_lease_token = $6);
-- name: ThumbnailParentTombstoned :one
SELECT COALESCE((SELECT status IN ('deleted', 'failed', 'expired', 'aborted') FROM foghorn.artifacts WHERE artifact_hash = $1), false)
OR EXISTS (SELECT 1 FROM foghorn.stream_cleanup_obligation WHERE asset_key = $1);
-- name: FailThumbnailAttempt :exec
UPDATE foghorn.thumbnail_task_assignment SET status = 'failed', updated_at = NOW() WHERE attempt_id = $1 AND status NOT IN ('published', 'failed');
-- name: AcquireThumbnailPublishLease :one
UPDATE foghorn.thumbnail_task_assignment SET publish_leased_until = NOW()+(sqlc.arg(lease_seconds)::bigint*INTERVAL '1 second'), publish_lease_token = gen_random_uuid()::text
WHERE attempt_id = sqlc.arg(attempt_id) AND status IN ('assigned', 'uploading', 'verifying', 'publishing') AND expiry>NOW()
AND (publish_leased_until IS NULL OR publish_leased_until<=NOW()) RETURNING publish_lease_token;
-- name: EnterThumbnailPublishing :execrows
UPDATE foghorn.thumbnail_task_assignment SET status = 'publishing', updated_at = NOW()
WHERE attempt_id = $1 AND status IN ('assigned', 'uploading', 'verifying') AND expiry>NOW() AND publish_lease_token = $2;
-- name: SettlePublishingThumbnailFailed :exec
UPDATE foghorn.thumbnail_task_assignment SET status = 'failed', updated_at = NOW() WHERE attempt_id = $1 AND status = 'publishing';
-- name: LockPublishableThumbnailAttempt :one
SELECT asset_key, tenant_id FROM foghorn.thumbnail_task_assignment
WHERE attempt_id = $1 AND status = 'publishing' AND expiry>NOW() AND publish_lease_token = $2 FOR UPDATE;
-- name: CountUnverifiedThumbnailObjects :one
SELECT COUNT(*) FROM foghorn.thumbnail_task_object WHERE attempt_id = $1 AND verified = false;
-- name: GetThumbnailActiveVersion :one
SELECT active_version FROM foghorn.thumbnail_active_pointer WHERE asset_key = $1;
-- name: ActivateThumbnailPointer :execrows
INSERT INTO foghorn.thumbnail_active_pointer(asset_key, tenant_id, active_version, active_token, updated_at) VALUES($2, $3, $1, $4, NOW())
ON CONFLICT(asset_key) DO UPDATE SET active_version = EXCLUDED.active_version, active_token = EXCLUDED.active_token, tenant_id = EXCLUDED.tenant_id, updated_at = NOW()
WHERE foghorn.thumbnail_active_pointer.tenant_id = EXCLUDED.tenant_id
AND (SELECT claim_seq FROM foghorn.thumbnail_task_assignment WHERE attempt_id = EXCLUDED.active_version)>=(SELECT claim_seq FROM foghorn.thumbnail_task_assignment WHERE attempt_id = foghorn.thumbnail_active_pointer.active_version);
-- name: MarkThumbnailSuperseded :exec
UPDATE foghorn.thumbnail_task_assignment SET superseded_at = NOW() WHERE attempt_id = $1;
-- name: MarkThumbnailPublished :execrows
UPDATE foghorn.thumbnail_task_assignment SET status = 'published', updated_at = NOW() WHERE attempt_id = $1 AND status = 'publishing';
-- name: ThumbnailProjectionEligible :one
SELECT EXISTS (SELECT 1 FROM foghorn.thumbnail_task_assignment a JOIN foghorn.thumbnail_active_pointer p ON p.asset_key = a.asset_key AND p.active_version = a.attempt_id
WHERE a.attempt_id = $1 AND a.status = 'published');
-- name: UnprojectedThumbnailEligible :one
SELECT EXISTS (SELECT 1 FROM foghorn.thumbnail_task_assignment a JOIN foghorn.thumbnail_active_pointer p ON p.asset_key = a.asset_key AND p.active_version = a.attempt_id
WHERE a.attempt_id = $1 AND a.status = 'published' AND a.deterministic_projected_at IS NULL);
-- name: MarkThumbnailProjected :execrows
UPDATE foghorn.thumbnail_task_assignment a SET deterministic_projected_at = NOW(), deterministic_reassert_at = NOW()+(sqlc.arg(reassert_seconds)::bigint*INTERVAL '1 second')
WHERE a.attempt_id = sqlc.arg(attempt_id) AND a.status = 'published' AND a.deterministic_projected_at IS NULL
AND EXISTS (SELECT 1 FROM foghorn.thumbnail_active_pointer p WHERE p.asset_key = sqlc.arg(asset_key) AND p.active_version = sqlc.arg(attempt_id));
-- name: MarkArtifactHasThumbnails :exec
UPDATE foghorn.artifacts SET has_thumbnails = true, thumbnail_serving_cluster_id = COALESCE(NULLIF(sqlc.arg(serving_cluster)::text, ''), thumbnail_serving_cluster_id), updated_at = NOW()
WHERE artifact_hash = sqlc.arg(artifact_hash) AND tenant_id::text = sqlc.arg(tenant_id) AND (has_thumbnails IS DISTINCT FROM true OR (sqlc.arg(serving_cluster)::text<>'' AND thumbnail_serving_cluster_id IS DISTINCT FROM sqlc.arg(serving_cluster)::text));
-- name: ClearThumbnailReassert :exec
UPDATE foghorn.thumbnail_task_assignment SET deterministic_reassert_at = NULL WHERE attempt_id = $1;
-- name: EnqueueThumbnailCleanup :exec
INSERT INTO foghorn.staging_cleanup_queue(object_key, backend_id) VALUES($1, $2) ON CONFLICT(object_key) DO UPDATE
SET next_attempt_at = NOW(), leased_until = NULL, lease_token = NULL, backend_id = COALESCE(foghorn.staging_cleanup_queue.backend_id, EXCLUDED.backend_id);
-- name: EnqueueThumbnailCleanupDeferred :exec
INSERT INTO foghorn.staging_cleanup_queue(object_key, next_attempt_at, backend_id) VALUES(sqlc.arg(object_key), NOW()+(sqlc.arg(delay_seconds)::bigint*INTERVAL '1 second'), sqlc.narg(backend_id))
ON CONFLICT(object_key) DO UPDATE SET next_attempt_at = GREATEST(foghorn.staging_cleanup_queue.next_attempt_at, EXCLUDED.next_attempt_at), leased_until = NULL, lease_token = NULL, backend_id = COALESCE(foghorn.staging_cleanup_queue.backend_id, EXCLUDED.backend_id);
-- name: DequeueThumbnailCleanup :exec
DELETE FROM foghorn.staging_cleanup_queue WHERE object_key = $1;
-- name: ThumbnailAttemptFailed :one
SELECT status = 'failed' FROM foghorn.thumbnail_task_assignment WHERE attempt_id = $1;
-- name: ReconstructThumbnailAttemptObjectKeys :many
SELECT a.asset_key, COALESCE(NULLIF(a.publish_lease_token, ''), a.version) AS version, o.file_name FROM foghorn.thumbnail_task_object o
JOIN foghorn.thumbnail_task_assignment a ON a.attempt_id = o.attempt_id WHERE o.attempt_id = $1;
-- name: ListThumbnailDestinations :many
SELECT destination_cluster, COALESCE(backend_id, '') AS backend_id, bool_or(durable_backend_local) AS backend_local
FROM foghorn.thumbnail_task_assignment WHERE tenant_id = $1 AND asset_key = $2 GROUP BY destination_cluster, backend_id;
-- name: DeleteThumbnailActivePointer :exec
DELETE FROM foghorn.thumbnail_active_pointer WHERE tenant_id = $1 AND asset_key = $2;
-- name: DeleteThumbnailAssignments :exec
DELETE FROM foghorn.thumbnail_task_assignment WHERE tenant_id = $1 AND asset_key = $2;
-- name: ListAssetThumbnailObjectKeys :many
SELECT a.attempt_id, COALESCE(NULLIF(a.publish_lease_token, ''), a.version) AS version, COALESCE(o.version_key, '') AS version_key, o.file_name
FROM foghorn.thumbnail_task_assignment a JOIN foghorn.thumbnail_task_object o ON o.attempt_id = a.attempt_id WHERE a.tenant_id = $1 AND a.asset_key = $2;
-- name: ListThumbnailStagingKeys :many
SELECT staging_key FROM foghorn.thumbnail_task_object WHERE attempt_id = $1;
-- name: ListThumbnailVersionKeys :many
SELECT version_key FROM foghorn.thumbnail_task_object WHERE attempt_id = $1;
-- name: ListPublishingThumbnailAttempts :many
SELECT attempt_id, COALESCE(publish_lease_token, '') AS token FROM foghorn.thumbnail_task_assignment WHERE status = 'publishing' AND expiry>NOW() LIMIT $1;
-- name: ListExpiredThumbnailAttemptIDs :many
SELECT attempt_id FROM foghorn.thumbnail_task_assignment WHERE status IN ('assigned', 'uploading', 'verifying', 'publishing') AND expiry<NOW() LIMIT $1;
-- name: ListSupersededThumbnailAttemptIDs :many
SELECT a.attempt_id FROM foghorn.thumbnail_task_assignment a JOIN foghorn.thumbnail_active_pointer p ON p.asset_key = a.asset_key
WHERE a.status = 'published' AND a.version<>p.active_version AND a.superseded_at IS NOT NULL AND a.superseded_at<$1 ORDER BY a.superseded_at LIMIT $2;
-- name: ListStuckIncompleteThumbnailAttemptIDs :many
SELECT attempt_id FROM foghorn.thumbnail_task_assignment WHERE status IN ('assigned', 'uploading', 'verifying') AND expiry>$1 AND updated_at<$2 LIMIT $3;
-- name: FailExpiredThumbnailAttempt :execrows
UPDATE foghorn.thumbnail_task_assignment SET status = 'failed', updated_at = NOW() WHERE attempt_id = $1
AND status IN ('assigned', 'uploading', 'verifying', 'publishing') AND expiry<=NOW() AND (publish_leased_until IS NULL OR publish_leased_until<=NOW());
-- name: DeleteSupersededThumbnailAttempt :execrows
DELETE FROM foghorn.thumbnail_task_assignment a USING foghorn.thumbnail_active_pointer p WHERE a.attempt_id = $1 AND p.asset_key = a.asset_key
AND a.version<>p.active_version AND a.superseded_at IS NOT NULL AND a.superseded_at<$2;
