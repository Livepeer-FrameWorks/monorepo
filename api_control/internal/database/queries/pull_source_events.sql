-- name: InsertPullSourceEvent :exec
INSERT INTO commodore.pull_source_events
    (tenant_id, stream_id, internal_name, event_kind, detail)
VALUES
    (sqlc.arg(tenant_id)::uuid,
     NULLIF(sqlc.arg(stream_id)::text, '')::uuid,
     sqlc.arg(internal_name),
     sqlc.arg(event_kind),
     NULLIF(sqlc.arg(detail)::text, ''));

-- name: StampResolvedPullStreamPlacement :exec
UPDATE commodore.streams
SET active_ingest_cluster_id = sqlc.arg(cluster_id),
    active_ingest_cluster_updated_at = NOW(),
    active_ingest_claim_id = sqlc.arg(claim_id)
WHERE id = sqlc.arg(stream_id)::uuid
  AND tenant_id = sqlc.arg(tenant_id)::uuid
  AND ingest_mode = 'pull'
  AND (
      active_ingest_cluster_id IS NULL
      OR active_ingest_cluster_id = ''
      OR active_ingest_cluster_updated_at IS NULL
      OR active_ingest_cluster_updated_at < NOW() - sqlc.arg(lease_seconds)::bigint * INTERVAL '1 second'
      OR (
          active_ingest_cluster_id = sqlc.arg(cluster_id)
          AND (active_ingest_claim_id IS NULL OR active_ingest_claim_id = '' OR active_ingest_claim_id = sqlc.arg(claim_id))
      )
  );

-- name: ListPullSourceEventsByStream :many
SELECT id::text,
       COALESCE(stream_id::text, ''::text)::text AS stream_id,
       internal_name,
       event_kind,
       COALESCE(detail, ''::text)::text AS detail,
       created_at
FROM commodore.pull_source_events
WHERE tenant_id = sqlc.arg(tenant_id)::uuid
  AND stream_id = sqlc.arg(stream_id)::uuid
ORDER BY created_at DESC
LIMIT sqlc.arg(row_limit);

-- name: ListPullSourceEventsByInternalName :many
SELECT id::text,
       COALESCE(stream_id::text, ''::text)::text AS stream_id,
       internal_name,
       event_kind,
       COALESCE(detail, ''::text)::text AS detail,
       created_at
FROM commodore.pull_source_events
WHERE tenant_id = sqlc.arg(tenant_id)::uuid
  AND internal_name = sqlc.arg(internal_name)
ORDER BY created_at DESC
LIMIT sqlc.arg(row_limit);
