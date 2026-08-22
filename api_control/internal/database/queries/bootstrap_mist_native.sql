-- name: ListBootstrapMistNativeStreams :many
SELECT s.id::text AS stream_id, s.playback_id::text AS playback_id
FROM commodore.streams s
WHERE s.tenant_id = sqlc.arg(tenant_id)::uuid AND s.ingest_mode = 'mist_native';

-- name: DeleteBootstrapStream :exec
DELETE FROM commodore.streams
WHERE id = sqlc.arg(stream_id)::uuid;

-- name: GetBootstrapMistNativeStream :one
SELECT s.id::text AS stream_id,
       s.title,
       COALESCE(s.description, '') AS description,
       s.ingest_mode,
       s.always_on,
       s.is_recording_enabled,
       s.monitoring_enabled,
       mn.source_spec,
       mn.source_kind,
       mn.placement_count,
       COALESCE(mn.allowed_cluster_ids, '{}') AS allowed_cluster_ids,
       CASE WHEN mn.local_asset_paths IS NULL THEN '[]'::text ELSE mn.local_asset_paths::text END AS local_asset_paths_json,
       CASE WHEN spc.processes_live IS NULL THEN ''::text ELSE spc.processes_live::text END AS processes_live_json
FROM commodore.streams s
LEFT JOIN commodore.stream_mist_sources mn ON mn.stream_id = s.id
LEFT JOIN commodore.stream_processing_config spc ON spc.stream_id = s.id
WHERE s.tenant_id = sqlc.arg(tenant_id)::uuid
  AND lower(s.playback_id::text) = lower(sqlc.arg(playback_id)::text);

-- name: UpdateBootstrapMistNativeStream :exec
UPDATE commodore.streams
SET title = sqlc.arg(title),
    description = sqlc.arg(description)::text,
    always_on = sqlc.arg(always_on),
    is_recording_enabled = sqlc.arg(is_recording_enabled),
    monitoring_enabled = sqlc.narg(monitoring_enabled)::boolean,
    updated_at = NOW()
WHERE id = sqlc.arg(stream_id)::uuid;

-- name: UpsertBootstrapMistSource :exec
INSERT INTO commodore.stream_mist_sources
    (stream_id, source_spec, source_kind, placement_count,
     allowed_cluster_ids, local_asset_paths, created_at, updated_at)
VALUES (sqlc.arg(stream_id)::uuid, sqlc.arg(source_spec), sqlc.arg(source_kind),
        sqlc.arg(placement_count), sqlc.arg(allowed_cluster_ids)::text[],
        sqlc.arg(local_asset_paths)::jsonb, NOW(), NOW())
ON CONFLICT (stream_id) DO UPDATE SET
    source_spec = EXCLUDED.source_spec,
    source_kind = EXCLUDED.source_kind,
    placement_count = EXCLUDED.placement_count,
    allowed_cluster_ids = EXCLUDED.allowed_cluster_ids,
    local_asset_paths = EXCLUDED.local_asset_paths,
    updated_at = NOW();

-- name: CreateBootstrapMistNativeStream :one
INSERT INTO commodore.streams
    (id, tenant_id, user_id, stream_key, playback_id, internal_name,
     title, description, ingest_mode, always_on, is_recording_enabled,
     monitoring_enabled, created_at, updated_at)
VALUES (gen_random_uuid(), sqlc.arg(tenant_id)::uuid, sqlc.arg(owner_id)::uuid,
        'mistnative-' || sqlc.arg(playback_id)::text, sqlc.arg(playback_id)::citext,
        replace(gen_random_uuid()::text, '-', ''),
        sqlc.arg(title), sqlc.arg(description)::text, 'mist_native',
        sqlc.arg(always_on), sqlc.arg(is_recording_enabled),
        sqlc.narg(monitoring_enabled)::boolean, NOW(), NOW())
RETURNING id::text;

-- name: CreateBootstrapMistSource :exec
INSERT INTO commodore.stream_mist_sources
    (stream_id, source_spec, source_kind, placement_count,
     allowed_cluster_ids, local_asset_paths, created_at, updated_at)
VALUES (sqlc.arg(stream_id)::uuid, sqlc.arg(source_spec), sqlc.arg(source_kind),
        sqlc.arg(placement_count), sqlc.arg(allowed_cluster_ids)::text[],
        sqlc.arg(local_asset_paths)::jsonb, NOW(), NOW());

-- name: DeleteBootstrapStreamProcessingConfig :exec
DELETE FROM commodore.stream_processing_config
WHERE stream_id = sqlc.arg(stream_id)::uuid;

-- name: UpsertBootstrapStreamProcessingConfig :exec
INSERT INTO commodore.stream_processing_config (stream_id, processes_live, updated_at)
VALUES (sqlc.arg(stream_id)::uuid, sqlc.arg(processes_live)::jsonb, NOW())
ON CONFLICT (stream_id) DO UPDATE SET
    processes_live = EXCLUDED.processes_live,
    updated_at = NOW();
