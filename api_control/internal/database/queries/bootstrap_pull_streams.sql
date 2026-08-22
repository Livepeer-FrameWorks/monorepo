-- name: GetBootstrapPullStream :one
SELECT s.id::text AS stream_id,
       s.title,
       COALESCE(s.description, '') AS description,
       s.ingest_mode,
       p.source_uri_enc,
       p.enabled,
       COALESCE(p.allowed_cluster_ids, '{}') AS allowed_cluster_ids
FROM commodore.streams s
LEFT JOIN commodore.stream_pull_sources p ON p.stream_id = s.id
WHERE s.tenant_id = sqlc.arg(tenant_id)::uuid
  AND lower(s.playback_id::text) = lower(sqlc.arg(playback_id)::text);

-- name: UpdateBootstrapPullStream :exec
UPDATE commodore.streams
SET title = sqlc.arg(title),
    description = sqlc.arg(description)::text,
    updated_at = NOW()
WHERE id = sqlc.arg(stream_id)::uuid;

-- name: UpsertBootstrapPullSource :exec
INSERT INTO commodore.stream_pull_sources
    (stream_id, source_uri_enc, enabled, allowed_cluster_ids, created_at, updated_at)
VALUES (sqlc.arg(stream_id)::uuid, sqlc.arg(source_uri_enc), sqlc.arg(enabled),
        sqlc.arg(allowed_cluster_ids)::text[], NOW(), NOW())
ON CONFLICT (stream_id) DO UPDATE SET
    source_uri_enc = EXCLUDED.source_uri_enc,
    enabled = EXCLUDED.enabled,
    allowed_cluster_ids = EXCLUDED.allowed_cluster_ids,
    updated_at = NOW();

-- name: GetBootstrapOwnerUser :one
SELECT id::text AS id
FROM commodore.users
WHERE tenant_id = sqlc.arg(tenant_id)::uuid AND role = 'owner'
ORDER BY created_at
LIMIT 1;

-- name: CreateBootstrapPullStream :one
INSERT INTO commodore.streams
    (id, tenant_id, user_id, stream_key, playback_id, internal_name,
     title, description, ingest_mode, created_at, updated_at)
VALUES (gen_random_uuid(), sqlc.arg(tenant_id)::uuid, sqlc.arg(owner_id)::uuid,
        'pull-' || sqlc.arg(playback_id)::text, sqlc.arg(playback_id)::citext,
        replace(gen_random_uuid()::text, '-', ''),
        sqlc.arg(title), sqlc.arg(description)::text, 'pull', NOW(), NOW())
RETURNING id::text;

-- name: CreateBootstrapPullSource :exec
INSERT INTO commodore.stream_pull_sources
    (stream_id, source_uri_enc, enabled, allowed_cluster_ids, created_at, updated_at)
VALUES (sqlc.arg(stream_id)::uuid, sqlc.arg(source_uri_enc), sqlc.arg(enabled),
        sqlc.arg(allowed_cluster_ids)::text[], NOW(), NOW());
