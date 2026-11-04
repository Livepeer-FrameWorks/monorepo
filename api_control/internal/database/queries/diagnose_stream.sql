-- name: ResolveDiagnosticStreamTenant :one
-- Operator-only identity resolution; subsequent diagnostic reads are tenant-filtered.
SELECT tenant_id::text FROM commodore.streams
WHERE (id = NULLIF(sqlc.arg(stream_id)::text, '')::uuid OR playback_id = sqlc.arg(playback_id)::text)
  AND deleted_at IS NULL;

-- name: GetStreamDiagnosis :one
SELECT jsonb_build_object(
  'tenant_id', s.tenant_id, 'stream_id', s.id, 'playback_id', s.playback_id,
  'internal_name', s.internal_name, 'ingest_mode', s.ingest_mode,
  'active_cluster_id', COALESCE(s.active_ingest_cluster_id, ''),
  'last_cluster_id', COALESCE(s.last_ingest_cluster_id, ''),
  'claim_id', COALESCE(s.active_ingest_claim_id, ''),
  'claim_updated_at', COALESCE(s.active_ingest_cluster_updated_at::text, ''),
  'node_id', COALESCE(s.active_ingest_node_id, ''),
  'control_cell_id', COALESCE(s.active_ingest_control_cell_id, ''),
  'authorities', COALESCE((SELECT jsonb_agg(jsonb_build_object(
    'kind', a.authority_kind, 'authority_id', a.authority_id,
    'current_version', COALESCE(c.authority_version, 0), 'cell_id', a.cell_id,
    'targeted_version', a.highest_targeted_version,
    'acknowledged_version', COALESCE(d.highest_acknowledged_version, 0),
    'acknowledged_at', COALESCE(d.last_acknowledged_at::text, ''))
    ORDER BY a.authority_kind, a.cell_id)
    FROM commodore.media_authority_targets a
    LEFT JOIN commodore.media_authority_current c USING (authority_kind, authority_id)
    LEFT JOIN commodore.media_authority_distribution d USING (authority_kind, authority_id, cell_id)
    WHERE (a.authority_kind = 'tenant' AND a.authority_id = s.tenant_id::text)
       OR (a.authority_kind = 'media_object' AND a.authority_id = 'live_stream:' || s.id::text)), '[]'::jsonb),
  'push_target_count', (SELECT count(*) FROM commodore.push_targets p WHERE p.tenant_id = s.tenant_id AND p.stream_id = s.id),
  'push_targets', COALESCE((SELECT jsonb_agg(page.item ORDER BY page.id) FROM (
    SELECT p.id, jsonb_build_object('id', p.id, 'platform', COALESCE(p.platform, ''),
      'enabled', COALESCE(p.is_enabled, false), 'status', COALESCE(p.status, ''), 'reason_code', p.reason_code) AS item
    FROM commodore.push_targets p WHERE p.tenant_id = s.tenant_id AND p.stream_id = s.id
    ORDER BY p.id LIMIT 50) page), '[]'::jsonb)
)::jsonb AS diagnosis
FROM commodore.streams s
WHERE s.tenant_id = sqlc.arg(tenant_id)::uuid
  AND (s.id = NULLIF(sqlc.arg(stream_id)::text, '')::uuid OR s.playback_id = sqlc.arg(playback_id)::text)
  AND s.deleted_at IS NULL;
