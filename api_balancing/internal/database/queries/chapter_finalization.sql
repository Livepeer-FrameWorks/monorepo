-- name: GetChapterParentDVR :one
SELECT a.tenant_id::text AS tenant_id,
       COALESCE(a.user_id::text, '')::text AS user_id,
       COALESCE(a.stream_id::text, '')::text AS stream_id,
       COALESCE(a.stream_internal_name, '')::text AS stream_internal_name,
       COALESCE(a.origin_cluster_id, '')::text AS origin_cluster_id,
       COALESCE(a.storage_cluster_id, '')::text AS storage_cluster_id,
       a.retention_until,
       COALESCE((SELECT node_id FROM foghorn.artifact_nodes
                 WHERE artifact_hash = a.artifact_hash AND is_orphaned = false
                 ORDER BY last_seen_at DESC NULLS LAST LIMIT 1), '')::text AS recording_node
FROM foghorn.artifacts a
WHERE a.artifact_hash = $1 AND a.artifact_type = 'dvr';

-- name: EnsureChapterPlaybackArtifact :exec
INSERT INTO foghorn.artifacts (
    artifact_hash, artifact_type, tenant_id, user_id, internal_name, stream_internal_name,
    origin_type, origin_id, library_visible, status, storage_location, sync_status, format,
    origin_cluster_id, retention_until, created_at, updated_at
) VALUES (
    sqlc.arg(artifact_hash), 'vod', sqlc.arg(tenant_id)::uuid, NULLIF(sqlc.arg(user_id)::text, '')::uuid,
    sqlc.arg(internal_name), sqlc.arg(stream_internal_name), 'dvr_chapter', sqlc.arg(chapter_id), false,
    'finalizing', 'pending', 'pending', 'mkv', NULLIF(sqlc.arg(origin_cluster_id)::text, ''),
    sqlc.narg(retention_until), NOW(), NOW()
)
ON CONFLICT (artifact_hash) DO NOTHING;
