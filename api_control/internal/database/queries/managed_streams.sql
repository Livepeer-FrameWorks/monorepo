-- name: ListManagedStreams :many
SELECT s.id::text AS stream_id,
       s.playback_id,
       s.internal_name,
       s.tenant_id::text AS tenant_id,
       s.ingest_mode,
       mn.source_spec,
       mn.source_kind,
       s.always_on,
       mn.placement_count,
       COALESCE(mn.allowed_cluster_ids, '{}') AS allowed_cluster_ids
FROM commodore.streams s
JOIN commodore.stream_mist_sources mn ON mn.stream_id = s.id
WHERE s.ingest_mode = 'mist_native'
  AND s.always_on = TRUE
  AND sqlc.arg(cluster_id)::text = ANY(mn.allowed_cluster_ids)
ORDER BY s.id::text;

-- name: ListStreamMonitoring :many
SELECT id::text AS stream_id, internal_name, monitoring_enabled
FROM commodore.streams
WHERE tenant_id = $1::uuid
ORDER BY id::text;
