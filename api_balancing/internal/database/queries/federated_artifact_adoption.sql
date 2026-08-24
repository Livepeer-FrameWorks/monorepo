-- name: AdoptRemoteArtifact :exec
INSERT INTO foghorn.artifacts (
    artifact_hash, artifact_type, tenant_id, internal_name, stream_internal_name, format,
    status, storage_location, sync_status, origin_cluster_id, storage_cluster_id
) VALUES (
    sqlc.arg(artifact_hash), sqlc.arg(artifact_type), sqlc.arg(tenant_id)::uuid,
    sqlc.arg(internal_name)::text, sqlc.arg(stream_internal_name)::text, sqlc.arg(format)::text,
    'active', 's3', sqlc.arg(sync_status)::text, sqlc.arg(origin_cluster_id)::text, sqlc.narg(storage_cluster_id)
)
ON CONFLICT (artifact_hash) DO UPDATE SET
    storage_location = 's3',
    sync_status = CASE WHEN EXCLUDED.sync_status = 'synced' THEN 'synced' ELSE foghorn.artifacts.sync_status END,
    internal_name = CASE WHEN COALESCE(foghorn.artifacts.internal_name, '') = '' AND EXCLUDED.internal_name <> '' THEN EXCLUDED.internal_name ELSE foghorn.artifacts.internal_name END,
    stream_internal_name = CASE WHEN COALESCE(foghorn.artifacts.stream_internal_name, '') = '' AND EXCLUDED.stream_internal_name <> '' THEN EXCLUDED.stream_internal_name ELSE foghorn.artifacts.stream_internal_name END,
    format = CASE WHEN COALESCE(foghorn.artifacts.format, '') = '' AND EXCLUDED.format <> '' THEN EXCLUDED.format ELSE foghorn.artifacts.format END,
    origin_cluster_id = CASE WHEN COALESCE(foghorn.artifacts.origin_cluster_id, '') = '' THEN EXCLUDED.origin_cluster_id ELSE foghorn.artifacts.origin_cluster_id END,
    storage_cluster_id = CASE WHEN COALESCE(foghorn.artifacts.storage_cluster_id, '') = '' AND EXCLUDED.storage_cluster_id IS NOT NULL THEN EXCLUDED.storage_cluster_id ELSE foghorn.artifacts.storage_cluster_id END;
