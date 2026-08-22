-- name: RecordManagedStreamActiveCluster :execrows
UPDATE commodore.streams
SET active_ingest_cluster_id = sqlc.arg(cluster_id),
    active_ingest_cluster_updated_at = NOW(),
    active_ingest_claim_id = sqlc.arg(claim_token),
    updated_at = NOW()
WHERE id = sqlc.arg(stream_id)::uuid
  AND tenant_id = sqlc.arg(tenant_id)::uuid
  AND deleted_at IS NULL
  AND (
      active_ingest_cluster_id IS NULL
      OR active_ingest_cluster_id = ''
      OR active_ingest_cluster_updated_at IS NULL
      OR active_ingest_cluster_updated_at < NOW() - (sqlc.arg(lease_seconds)::bigint * INTERVAL '1 second')
      OR (active_ingest_cluster_id = sqlc.arg(cluster_id) AND active_ingest_claim_id = sqlc.arg(claim_token))
  );

-- name: RegisterStreamThumbnailServingCell :execrows
UPDATE commodore.streams
SET thumbnail_serving_cluster_ids = CASE
        WHEN sqlc.arg(cluster_id)::text = ANY(thumbnail_serving_cluster_ids) THEN thumbnail_serving_cluster_ids
        ELSE array_append(thumbnail_serving_cluster_ids, sqlc.arg(cluster_id)::text)
    END,
    updated_at = NOW()
WHERE id = sqlc.arg(stream_id)::uuid
  AND tenant_id = sqlc.arg(tenant_id)::uuid
  AND deleted_at IS NULL;

-- name: ClearManagedStreamActiveCluster :execrows
UPDATE commodore.streams
SET active_ingest_cluster_id = NULL,
    active_ingest_cluster_updated_at = NOW(),
    active_ingest_claim_id = NULL,
    updated_at = NOW()
WHERE id = sqlc.arg(stream_id)::uuid
  AND tenant_id = sqlc.arg(tenant_id)::uuid
  AND active_ingest_cluster_id = sqlc.arg(expected_cluster_id)
  AND active_ingest_claim_id = sqlc.arg(claim_token);

-- name: ResolveStreamByPlaybackID :one
SELECT id, internal_name, tenant_id, requires_auth, ingest_mode, active_ingest_cluster_id
FROM commodore.streams
WHERE lower(playback_id::text) = lower($1::text) AND deleted_at IS NULL;

-- name: ResolveStreamByInternalName :one
SELECT id, tenant_id, user_id, is_recording_enabled, requires_auth, active_ingest_cluster_id
FROM commodore.streams
WHERE internal_name = $1 AND deleted_at IS NULL;

-- name: ResolvePullSourceByInternalName :one
SELECT s.id, s.tenant_id, s.ingest_mode,
       p.source_uri_enc, p.enabled, COALESCE(p.allowed_cluster_ids, '{}') AS allowed_cluster_ids
FROM commodore.streams s
JOIN commodore.stream_pull_sources p ON p.stream_id = s.id
WHERE s.internal_name = $1;

-- name: GetOwnedPullSourceState :one
SELECT p.source_uri_enc, p.enabled, COALESCE(p.allowed_cluster_ids, '{}') AS allowed_cluster_ids
FROM commodore.streams s
JOIN commodore.stream_pull_sources p ON p.stream_id = s.id
WHERE s.id = $1 AND s.user_id = $2 AND s.tenant_id = $3;
